package tools

import (
	"context"
	"errors"
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
		Description: "List all frames in a tab, including the main frame, same-origin in-process iframes (reachable=true), and known out-of-process cross-origin iframes (reachable=false). Pass a reachable frame's id as the optional 'frame' parameter on evaluate, query, get_html, or get_text.",
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

// frameElementJS wraps a JS body that reads/operates on the first element
// matching selector, throwing a clear error if the element disappeared after
// waiting.
func frameElementJS(selector, body string) string {
	qSel := jsQuote(selector)
	return fmt.Sprintf("(function() {\nconst el = document.querySelector(%s);\nif (!el) throw new Error('selector ' + %s + ' matched no elements');\n%s\n})()", qSel, qSel, body)
}

// waitForSelector waits for selector to exist in the top frame (frameID == "")
// or inside frameID's execution context. When frameID is non-empty, it returns
// the live UniqueContextID (re-resolving across same-origin subframe
// navigations while failing fast on CSS syntax errors, frame detachment, or
// parent context cancellation).
func waitForSelector(sctx, tctx context.Context, t *tab.Tab, uniqueCtxID, frameID, selector string) (string, error) {
	if frameID == "" {
		if err := chromedp.Do(sctx, chromedp.WaitReady(chromedp.CSS(selector))); err != nil {
			return "", selectorError(tctx, selector, err)
		}
		return "", nil
	}

	checkJS := fmt.Sprintf(`document.querySelector(%s) !== null`, jsQuote(selector))
	curCtxID := uniqueCtxID
	deadCtxIDs := make(map[string]bool)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		if curCtxID != "" {
			found, err := chromedp.Run(sctx, chromedp.Evaluate[bool](checkJS, evalInUniqueContext(curCtxID)))
			if err == nil {
				if found {
					return curCtxID, nil
				}
			} else {
				if tctx.Err() != nil {
					return "", err
				}
				if errors.Is(sctx.Err(), context.DeadlineExceeded) {
					return "", fmt.Errorf("selector %q not found in frame %q: %w", selector, frameID, sctx.Err())
				}
				var exc *chromedp.ExceptionError
				if errors.As(err, &exc) {
					return "", fmt.Errorf("selector %q in frame %q: %w", selector, frameID, err)
				}
				// Re-check frame state when a context vanishes mid-wait (e.g.
				// iframe navigation): record curCtxID as dead so we never
				// re-select it before executionContextDestroyed drains, fail
				// fast if detached or swapped to an OOPIF, switch to a new
				// context immediately if ready, or retry while known.
				if isStaleContextError(err) {
					deadCtxIDs[curCtxID] = true
					ec, reachable, known, swapped := t.ExecContexts.ContextForFrame(frameID)
					if !known {
						return "", fmt.Errorf("frame %q detached while waiting for selector %q", frameID, selector)
					}
					if swapped {
						return "", fmt.Errorf("frame %q became out-of-process (cross-origin) while waiting for selector %q", frameID, selector)
					}
					if reachable && !deadCtxIDs[ec.UniqueID] {
						curCtxID = ec.UniqueID
						continue
					}
					curCtxID = ""
				} else {
					return "", err
				}
			}
		} else {
			ec, reachable, known, swapped := t.ExecContexts.ContextForFrame(frameID)
			if !known {
				return "", fmt.Errorf("frame %q detached while waiting for selector %q", frameID, selector)
			}
			if swapped {
				return "", fmt.Errorf("frame %q became out-of-process (cross-origin) while waiting for selector %q", frameID, selector)
			}
			if reachable && !deadCtxIDs[ec.UniqueID] {
				curCtxID = ec.UniqueID
				continue
			}
		}

		select {
		case <-tctx.Done():
			return "", tctx.Err()
		case <-sctx.Done():
			if curCtxID == "" {
				if _, reachable, known, _ := t.ExecContexts.ContextForFrame(frameID); known && !reachable {
					return "", fmt.Errorf("frame %q has no reachable execution context while waiting for selector %q: %w", frameID, selector, sctx.Err())
				}
			}
			return "", fmt.Errorf("selector %q not found in frame %q: %w", selector, frameID, sctx.Err())
		case <-ticker.C:
		}
	}
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
