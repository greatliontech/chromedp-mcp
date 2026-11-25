// Package tab manages browser tabs and their associated event collectors.
package tab

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/inspector"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/performancetimeline"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"

	"github.com/greatliontech/chromedp-mcp/internal/collector"
)

// ErrNotFound is returned when a tab ID does not exist in a Manager.
var ErrNotFound = errors.New("tab not found")

// defaultTabInfoTimeout bounds Target.getTargetInfo calls in Tab.Info/URL/Title
// so a wedged browser target cannot block tab_list indefinitely.
const defaultTabInfoTimeout = 2 * time.Second

// DefaultConsoleBuffer is the default console log buffer size.
const DefaultConsoleBuffer = 1000

// DefaultErrorBuffer is the default JS error buffer size.
const DefaultErrorBuffer = 500

// DefaultNetworkBuffer is the default network request buffer size.
const DefaultNetworkBuffer = 1000

// DefaultPerformanceBuffer is the default performance timeline buffer size.
const DefaultPerformanceBuffer = 200

// DefaultURLChangesBuffer is the default URL change buffer size. Each
// entry is a small {from, to, kind, timestamp} record so 100 covers a
// long session without bloating memory.
const DefaultURLChangesBuffer = 100

// domMutationObserverScript is installed via Page.addScriptToEvaluateOnNewDocument
// so every new document immediately attaches a MutationObserver that
// pushes timestamps (page-relative via performance.now()) into a bounded
// JS-side ring buffer. Action tools called with observe_window_ms > 0
// read the buffer to count mutations inside their observation window.
//
// The script runs in EVERY document, iframes included, so each frame
// accumulates its own buffer in its own `window`. Reading only the top
// frame's buffer would silently ignore all iframe mutations; observation
// therefore evaluates in each frame's own execution context (see
// collector.ExecContexts).
//
// The buffer holds raw performance.now() values, which are
// document-relative and reset to zero on navigation. That is safe only
// because an observation window pins the frame's execution context by its
// system-unique ID: a navigation destroys that context, so a threshold
// from the old document can never be applied to the new one — the read
// fails instead of silently counting the wrong document's mutations.
//
// Only childList mutations are observed. Attribute and characterData
// mutations are noisy on real pages — focus state changes,
// CSS-driven attribute toggles, lazy-loaded resources, framework
// re-renders all fire dozens of irrelevant attribute/text mutations
// even on quiescent pages. childList (added/removed nodes) is the
// signal that maps to "the click changed what's on the page".
//
// The cap (10000) bounds memory; older entries are dropped on
// overflow, which under-counts but doesn't break the "zero vs nonzero"
// signal the observation window actually cares about.
const domMutationObserverScript = `
(function() {
  if (window.__mcpMutationObserverInstalled) return;
  window.__mcpMutationObserverInstalled = true;
  window.__mcpMutationCap = 10000;
  window.__mcpMutationTimestamps = [];
  var obs = new MutationObserver(function(mutations) {
    var now = performance.now();
    for (var i = 0; i < mutations.length; i++) {
      window.__mcpMutationTimestamps.push(now);
    }
    var over = window.__mcpMutationTimestamps.length - window.__mcpMutationCap;
    if (over > 0) {
      window.__mcpMutationTimestamps.splice(0, over);
    }
  });
  obs.observe(document, {
    subtree: true,
    childList: true,
  });
})();
`

// Tab represents a single browser tab with its event collectors.
type Tab struct {
	ID          string
	ctx         context.Context
	cancel      context.CancelFunc
	targetID    target.ID
	adopted     bool
	Console     *collector.Console
	JSErrors    *collector.JSErrors
	Network     *collector.Network
	WebSocket   *collector.WebSocket
	URLChanges  *collector.URLChanges
	Performance *collector.Performance
	// ExecContexts tracks each frame's main-world JS execution context, so
	// activity observation can read the per-frame DOM mutation buffers.
	ExecContexts *collector.ExecContexts
	// Dialog tracks the active JavaScript modal dialog (alert, confirm,
	// prompt, beforeunload) on this tab.
	Dialog *collector.Dialog
}

// New creates a new tab from a parent browser context. It creates a new
// Chrome target, starts event listeners, and initializes collectors.
// startupCtx bounds initialization (e.g. MCP request cancellation) without
// tying the tab's post-init lifetime to it; nil defaults to context.Background().
func New(parentCtx, startupCtx context.Context, id string) (*Tab, error) {
	return newTab(parentCtx, startupCtx, id, "", false)
}

// Attach attaches to an existing Chrome page target (e.g., an already-open tab
// in a browser connected via browser_connect) without creating a new target.
// Adopted tabs use WithDetachOnCancel so disconnecting the browser leaves the
// user's tab open in Chrome.
func Attach(parentCtx, startupCtx context.Context, id string, targetID target.ID) (*Tab, error) {
	if targetID == "" {
		return nil, fmt.Errorf("tab attach: target ID is required")
	}
	return newTab(parentCtx, startupCtx, id, targetID, true)
}

func newTab(parentCtx, startupCtx context.Context, id string, targetID target.ID, adopted bool) (*Tab, error) {
	if startupCtx == nil {
		startupCtx = context.Background()
	}
	if err := startupCtx.Err(); err != nil {
		return nil, fmt.Errorf("tab init: %w", err)
	}
	t := &Tab{
		ID:       id,
		targetID: targetID,
		adopted:  adopted,
		Console:  collector.NewConsole(DefaultConsoleBuffer),
		JSErrors: collector.NewJSErrors(DefaultErrorBuffer),
		Network:  collector.NewNetwork(DefaultNetworkBuffer),
		WebSocket: collector.NewWebSocket(
			collector.DefaultWSConnections,
			collector.DefaultWSFramesPerDirection,
			collector.MaxInlineFramePayload,
		),
		URLChanges:   collector.NewURLChanges(DefaultURLChangesBuffer),
		Performance:  collector.NewPerformance(DefaultPerformanceBuffer, 50),
		ExecContexts: collector.NewExecContexts(),
		Dialog:       collector.NewDialog(),
	}

	// Register CDP event listeners via WithListener so they are attached
	// before the target processes its first message (catching initial
	// Runtime.executionContextCreated events too). Listeners run synchronously
	// in chromedp's event dispatcher and must not block.
	ctxOpts := []chromedp.ContextOption{
		chromedp.WithListener(func(ev any) {
			switch ev := ev.(type) {
			case *runtime.EventConsoleAPICalled:
				t.Console.Handle(ev)
			case *runtime.EventExceptionThrown:
				t.JSErrors.Handle(ev)
			case *network.EventRequestWillBeSent:
				t.Network.HandleRequestWillBeSent(ev)
			case *network.EventResponseReceived:
				t.Network.HandleResponseReceived(ev)
			case *network.EventLoadingFinished:
				t.Network.HandleLoadingFinished(ev)
			case *network.EventLoadingFailed:
				t.Network.HandleLoadingFailed(ev)
			case *network.EventWebSocketCreated:
				t.WebSocket.HandleCreated(ev)
			case *network.EventWebSocketWillSendHandshakeRequest:
				t.WebSocket.HandleHandshakeRequest(ev)
			case *network.EventWebSocketHandshakeResponseReceived:
				t.WebSocket.HandleHandshakeResponse(ev)
			case *network.EventWebSocketFrameSent:
				t.WebSocket.HandleFrameSent(ev)
			case *network.EventWebSocketFrameReceived:
				t.WebSocket.HandleFrameReceived(ev)
			case *network.EventWebSocketFrameError:
				t.WebSocket.HandleFrameError(ev)
			case *network.EventWebSocketClosed:
				t.WebSocket.HandleClosed(ev)
			case *page.EventFrameNavigated:
				t.URLChanges.HandleFrameNavigated(ev)
				t.ExecContexts.HandleFrameNavigated(ev)
				t.Dialog.HandleFrameNavigated(ev)
			case *page.EventFrameAttached:
				t.ExecContexts.HandleFrameAttached(ev)
			case *page.EventFrameDetached:
				t.ExecContexts.HandleFrameDetached(ev)
				t.Dialog.HandleFrameDetached(ev)
			case *page.EventNavigatedWithinDocument:
				t.URLChanges.HandleNavigatedWithinDocument(ev)
			case *page.EventJavascriptDialogOpening:
				t.Dialog.HandleOpening(ev)
			case *page.EventJavascriptDialogClosed:
				t.Dialog.HandleClosed(ev)
			case *inspector.EventTargetCrashed:
				t.Dialog.Clear()
			case *runtime.EventExecutionContextCreated:
				t.ExecContexts.HandleExecutionContextCreated(ev)
			case *runtime.EventExecutionContextDestroyed:
				t.ExecContexts.HandleExecutionContextDestroyed(ev)
			case *runtime.EventExecutionContextsCleared:
				t.ExecContexts.HandleExecutionContextsCleared()
			case *performancetimeline.EventTimelineEventAdded:
				t.Performance.HandleTimelineEvent(ev)
			}
		}),
	}
	if targetID != "" {
		ctxOpts = append(ctxOpts, chromedp.WithTargetID(targetID))
	}
	if adopted {
		ctxOpts = append(ctxOpts, chromedp.WithDetachOnCancel())
	}

	ctx, cancel := chromedp.NewContext(parentCtx, ctxOpts...)
	t.ctx = ctx
	t.cancel = cancel

	// Initialize the tab: enable performance timeline and configure Network
	// and DOM mutation observation. We need to run a chromedp action to
	// trigger the target allocation/attach (chromedp attaches lazily on the
	// first Do call). chromedp already enables Page and Runtime domains on
	// target attach.
	cancelOnce := sync.OnceFunc(cancel)
	stopStartup := context.AfterFunc(startupCtx, cancelOnce)
	err := chromedp.Do(ctx, chromedp.Func(func(ctx context.Context, tgt *chromedp.Target) error {
		if t.targetID == "" {
			t.targetID = tgt.TargetID
		}
		if _, err := cdp.Call(ctx, tgt, performancetimeline.Enable, performancetimeline.EnableParams{
			EventTypes: []string{
				"largest-contentful-paint",
				"layout-shift",
			},
		}); err != nil && !adopted {
			return err
		}
		// Re-enable Network with MaxPostDataSize so requestWillBeSent
		// carries POST bodies up to the inline cap. chromedp also calls
		// Network.enable internally; the latest call wins, and the
		// parameter does not affect already-fired events.
		if _, err := cdp.Call(ctx, tgt, network.Enable, network.EnableParams{
			MaxPostDataSize: new(int64(collector.MaxInlineRequestBody)),
		}); err != nil {
			return err
		}
		// Register the DOM mutation observer for every new document.
		// Activity observation reads the timestamps the observer pushes
		// into a per-document JS-side ring buffer.
		if _, err := cdp.Call(ctx, tgt, page.AddScriptToEvaluateOnNewDocument, page.AddScriptToEvaluateOnNewDocumentParams{
			Source: domMutationObserverScript,
		}); err != nil {
			return err
		}
		// Also evaluate on the current document so observation works on tabs
		// that already have a document loaded (e.g., adopted browser_connect
		// tabs). On adopted tabs, bound and make this best-effort so a tab on
		// a restricted URL (e.g. chrome://) where evaluation fails still attaches.
		if adopted {
			evalCtx, evalCancel := context.WithTimeout(ctx, time.Second)
			_, _ = chromedp.Evaluate[chromedp.Void](domMutationObserverScript)(evalCtx, tgt)
			evalCancel()
		} else if _, err := chromedp.Evaluate[chromedp.Void](domMutationObserverScript)(ctx, tgt); err != nil {
			return err
		}
		// Seed the frame and URL registries so the main frame and initial URL
		// are known before any navigation. Without it a tab left on about:blank
		// (or an existing loaded tab) has no main frame for DOM mutation counts,
		// and a same-document history.pushState before any cross-document
		// navigation would lack a mainFrameID and baseline URL.
		//
		// A seed only: Page.getFrameTree does NOT list out-of-process
		// (cross-origin) iframes. Those are learned solely from the
		// frameAttached event stream, which is why ExecContexts keeps a
		// frame that merely swapped away rather than dropping it.
		if treeRes, err := cdp.Call(ctx, tgt, page.GetFrameTree, cdp.Empty{}); err == nil && treeRes.FrameTree != nil && treeRes.FrameTree.Frame != nil {
			frames := make(map[string]string)
			var walk func(*page.FrameTree)
			walk = func(n *page.FrameTree) {
				if n == nil || n.Frame == nil {
					return
				}
				frames[string(n.Frame.ID)] = string(n.Frame.ParentID)
				for _, c := range n.ChildFrames {
					walk(c)
				}
			}
			walk(treeRes.FrameTree)
			top := treeRes.FrameTree.Frame
			t.ExecContexts.SeedFrameTree(string(top.ID), string(top.LoaderID), frames)
			t.URLChanges.Seed(top.ID, top.URL+top.URLFragment)
		}
		return nil
	}))
	stopped := stopStartup()
	if err != nil || !stopped {
		cancelOnce()
		if err == nil {
			err = context.Cause(startupCtx)
		}
		return nil, fmt.Errorf("tab init: %w", err)
	}

	return t, nil
}

// Context returns the tab's chromedp context. Tool handlers use this to
// run chromedp actions against this tab.
func (t *Tab) Context() context.Context {
	return t.ctx
}

// Close cancels the tab's context. For server-created tabs this closes the
// Chrome target; for adopted tabs (WithDetachOnCancel) it detaches and leaves
// the user's tab open in Chrome.
func (t *Tab) Close() {
	t.cancel()
}

// closeTargetWaitTimeout bounds the post-CloseTarget poll waiting for Chrome
// to drop an explicitly closed adopted target (matching chromedp's own wait
// for server-created targets).
const closeTargetWaitTimeout = 500 * time.Millisecond

// CloseTarget explicitly closes the underlying Chrome target, including for
// adopted tabs where Close() alone only detaches. Used by explicit tab_close.
func (t *Tab) CloseTarget(browserCtx context.Context) {
	t.cancel()
	if !t.adopted || t.targetID == "" || browserCtx == nil || browserCtx.Err() != nil {
		return
	}
	closeCtx, cancel := context.WithTimeout(browserCtx, 2*time.Second)
	defer cancel()
	if _, err := chromedp.CallBrowser(closeCtx, target.CloseTarget, target.CloseTargetParams{
		TargetID: t.targetID,
	}); err != nil {
		return
	}
	waitCtx, waitCancel := context.WithTimeout(closeCtx, closeTargetWaitTimeout)
	defer waitCancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		targets, err := chromedp.Targets(waitCtx)
		if err != nil {
			return
		}
		found := false
		for _, info := range targets {
			if info != nil && info.TargetID == t.targetID {
				found = true
				break
			}
		}
		if !found {
			return
		}
		select {
		case <-ticker.C:
		case <-waitCtx.Done():
			return
		}
	}
}

// Info returns the tab's current URL and title via CDP Target.getTargetInfo.
// Unlike Runtime.evaluate (chromedp.Location/Title), Target.getTargetInfo runs
// in Chrome's browser process and does not block when the renderer is paused by
// a JS alert/confirm/prompt dialog or infinite loop.
func (t *Tab) Info(ctx context.Context) (url, title string, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	runCtx, cancel := context.WithTimeout(t.ctx, defaultTabInfoTimeout)
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()

	res, err := chromedp.Call(runCtx, target.GetTargetInfo, target.GetTargetInfoParams{})
	if err != nil {
		return "", "", err
	}
	info := res.TargetInfo
	if info == nil {
		return "", "", nil
	}
	title = info.Title
	// Chrome reports "about:blank" as the TargetInfo title for an untitled
	// blank tab; normalize to empty string to match document.title.
	if info.URL == "about:blank" && title == "about:blank" {
		title = ""
	}
	return info.URL, title, nil
}

// URL returns the current URL of the tab.
func (t *Tab) URL(ctx context.Context) (string, error) {
	url, _, err := t.Info(ctx)
	return url, err
}

// Title returns the current title of the tab.
func (t *Tab) Title(ctx context.Context) (string, error) {
	_, title, err := t.Info(ctx)
	return title, err
}
