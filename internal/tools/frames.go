package tools

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/browser"
	"github.com/greatliontech/chromedp-mcp/internal/tab"
)

// listFramesTimeout bounds Page.getFrameTree so a wedged renderer or a dialog
// opening mid-call cannot stall list_frames indefinitely.
const listFramesTimeout = 5 * time.Second

// ListFramesInput is the input for list_frames.
type ListFramesInput struct {
	TabInput
}

// FrameInfo describes a single frame in a tab.
type FrameInfo struct {
	ID             string `json:"id"`
	ParentID       string `json:"parent_id,omitempty"`
	Name           string `json:"name,omitempty"`
	URL            string `json:"url,omitempty"`
	SecurityOrigin string `json:"security_origin,omitempty"`
	IsMain         bool   `json:"is_main"`
	Reachable      bool   `json:"reachable"`
}

// ListFramesOutput is the output for list_frames.
type ListFramesOutput struct {
	Frames []FrameInfo `json:"frames"`
}

func registerFrameTools(s *mcp.Server, mgr *browser.Manager) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_frames",
		Description: "List all frames in a tab, including the main frame, same-origin in-process iframes (reachable=true), and known out-of-process cross-origin iframes (reachable=false). Pass a reachable frame's id as the optional 'frame' parameter on snapshot, query, get_dom, or evaluate.",
		Annotations: readOnlyAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ListFramesInput) (*mcp.CallToolResult, ListFramesOutput, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, ListFramesOutput{}, err
		}
		// Page.getFrameTree runs in Blink's renderer and stalls while a JS
		// modal dialog is open; fail fast so callers can inspect/dismiss it.
		if t.Dialog != nil && t.Dialog.Open() {
			return nil, ListFramesOutput{}, fmt.Errorf("cannot list frames while a JavaScript dialog is open — use get_dialog and handle_dialog first")
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		treeCtx, treeCancel := context.WithTimeout(tctx, listFramesTimeout)
		defer treeCancel()

		treeRes, err := chromedp.Call(treeCtx, page.GetFrameTree, cdp.Empty{})
		if err != nil {
			return nil, ListFramesOutput{}, err
		}

		frames := make([]FrameInfo, 0)
		seen := make(map[string]struct{})
		var walk func(*page.FrameTree)
		walk = func(node *page.FrameTree) {
			if node == nil || node.Frame == nil {
				return
			}
			f := node.Frame
			id := string(f.ID)
			seen[id] = struct{}{}
			_, reachable, _, _ := t.ExecContexts.ContextForFrame(id)
			frames = append(frames, FrameInfo{
				ID:             id,
				ParentID:       string(f.ParentID),
				Name:           f.Name,
				URL:            f.URL + f.URLFragment,
				SecurityOrigin: f.SecurityOrigin,
				IsMain:         f.ParentID == "",
				Reachable:      reachable,
			})
			for _, child := range node.ChildFrames {
				walk(child)
			}
		}
		walk(treeRes.FrameTree)

		// Page.getFrameTree omits out-of-process (cross-origin) iframes. Surface
		// known OOPIFs tracked by ExecContexts that were absent from the tree
		// (skipping stale departing in-process or parentless main frames).
		knownFrames := t.ExecContexts.KnownFrames()
		var extra []string
		for id, parentID := range knownFrames {
			if _, ok := seen[id]; ok || parentID == "" {
				continue
			}
			if _, reachable, _, _ := t.ExecContexts.ContextForFrame(id); reachable {
				continue
			}
			extra = append(extra, id)
		}
		slices.Sort(extra)
		for _, id := range extra {
			frames = append(frames, FrameInfo{
				ID:        id,
				ParentID:  knownFrames[id],
				IsMain:    false,
				Reachable: false,
			})
		}

		return nil, ListFramesOutput{Frames: frames}, nil
	})
}

// resolveFrameContext resolves an optional frame ID to the frame's current
// main-world CDP UniqueContextID. Returns ("", nil) when frameID is empty.
// When allowTransientWait is true (selector-waiting tools), a known in-process
// frame that is momentarily between execution contexts during a same-origin
// navigation returns ("", nil) so waitForSelector can wait for its new context,
// while swapped cross-origin OOPIFs and unknown frames still fail fast.
func resolveFrameContext(t *tab.Tab, frameID string, allowTransientWait bool) (string, error) {
	if frameID == "" {
		return "", nil
	}
	ec, reachable, known, swapped := t.ExecContexts.ContextForFrame(frameID)
	if reachable {
		return ec.UniqueID, nil
	}
	if known && swapped {
		return "", fmt.Errorf("frame %q is out-of-process (cross-origin) and has no reachable execution context in this session; only same-origin in-process frames can be targeted", frameID)
	}
	if known {
		if allowTransientWait {
			return "", nil
		}
		return "", fmt.Errorf("frame %q has no reachable execution context in this session (it may be navigating or out-of-process); only same-origin in-process frames can be targeted", frameID)
	}
	return "", fmt.Errorf("frame %q not found — call list_frames to inspect available frames", frameID)
}

// evalInUniqueContext pins a chromedp.Evaluate call to a specific frame's
// system-unique execution context ID.
func evalInUniqueContext(uniqueContextID string) chromedp.EvaluateOption {
	return func(p *runtime.EvaluateParams) {
		p.UniqueContextID = uniqueContextID
	}
}

// evalOptsForFrame prepends evalInUniqueContext(uniqueCtxID) when targeting a
// specific frame, preserving any additional EvaluateOptions.
func evalOptsForFrame(uniqueCtxID string, extra ...chromedp.EvaluateOption) []chromedp.EvaluateOption {
	if uniqueCtxID == "" {
		return extra
	}
	opts := make([]chromedp.EvaluateOption, 0, 1+len(extra))
	opts = append(opts, evalInUniqueContext(uniqueCtxID))
	opts = append(opts, extra...)
	return opts
}

// isStaleContextError reports whether err is CDP's execution-context-gone
// error for either UniqueContextID ("uniqueContextId not found"), numeric
// contextId ("Cannot find context"), or an in-flight navigation destroying the
// active context ("Execution context was destroyed").
func isStaleContextError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "uniqueContextId not found") ||
		strings.Contains(msg, "Cannot find context") ||
		strings.Contains(msg, "Execution context was destroyed")
}
