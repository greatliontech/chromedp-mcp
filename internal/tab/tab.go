// Package tab manages browser tabs and their associated event collectors.
package tab

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/fetch"
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

// defaultTabInfoTimeout bounds short browser-level CDP calls (Target.getTargetInfo,
// Page.bringToFront, domain disable) so a wedged target cannot block indefinitely.
const defaultTabInfoTimeout = 2 * time.Second

// defaultInterceptDispatchTimeout bounds asynchronous Fetch.continueRequest /
// fulfillRequest / failRequest calls dispatched for paused requests.
const defaultInterceptDispatchTimeout = 5 * time.Second

// DefaultConsoleBuffer is the default console log buffer size.
const DefaultConsoleBuffer = 1000

// DefaultErrorBuffer is the default JS error buffer size.
const DefaultErrorBuffer = 500

// DefaultNetworkBuffer is the default network request buffer size.
const DefaultNetworkBuffer = 1000

// DefaultPerformanceBuffer is the default performance timeline buffer size.
const DefaultPerformanceBuffer = 200

// DefaultURLChangesBuffer is the default URL change buffer size.
const DefaultURLChangesBuffer = 100

// domMutationObserverScript is installed via Page.addScriptToEvaluateOnNewDocument
// so every new document immediately attaches a MutationObserver that pushes
// page-relative performance.now() timestamps into a bounded JS-side ring buffer.
//
// The script runs in every document, iframes included, so observation evaluates
// in each frame's own execution context (see collector.ExecContexts). Because
// performance.now() is document-relative, observation pins execution contexts
// by unique ID so a navigation destroys the old context instead of letting a
// stale threshold be applied to the new document.
//
// Only childList mutations are observed: attribute and characterData mutations
// fire constantly on quiescent real-world pages, whereas childList maps to
// structural changes triggered by an action. The cap (10000) bounds memory.
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
	// Intercept stores ordered network request interception rules for this tab.
	Intercept *collector.Intercept

	// interceptOpMu serializes lazy Fetch.enable/Fetch.disable domain toggles
	// with rule mutations (outer lock; never acquired by EventRequestPaused).
	interceptOpMu sync.Mutex
	fetchEnabled  bool

	// cssOpMu serializes CSS domain enable/disable transitions (outer lock;
	// never acquired in WithListener).
	cssOpMu    sync.Mutex
	cssEnabled bool

	// cssStateMu guards active forced pseudo-states across documents
	// (leaf lock; never held across CDP I/O).
	cssStateMu sync.Mutex
	cssDocGen  uint64
	cssForced  map[cdp.BackendNodeID][]string

	// snapshotMu guards the latest accessibility snapshot ref map (leaf lock;
	// never held across CDP I/O).
	snapshotMu          sync.RWMutex
	snapshotGen         uint64
	snapshotRefs        map[string]SnapshotRef
	snapshotHasEverSet  bool
	snapshotStaleReason string

	// dragOpMu serializes multi-step drag_and_drop sequences (SetInterceptDrags
	// and mouse/drag event dispatch) on this tab.
	dragOpMu sync.Mutex
}

// New creates a new tab from a parent browser context. It creates a new
// Chrome target, starts event listeners, and initializes collectors.
// startupCtx bounds initialization (e.g. MCP request cancellation) without
// tying the tab's post-init lifetime to it; nil defaults to context.Background().
func New(parentCtx, startupCtx context.Context) (*Tab, error) {
	return newTab(parentCtx, startupCtx, "", false)
}

// Attach attaches to an existing Chrome page target (such as Chrome's initial
// default tab on browser_launch, an already-open tab on browser_connect, or an
// auto-adopted popup) without creating a new target. Uses WithDetachOnCancel so
// failed attaches, concurrent dedup, and browser disconnect preserve the target,
// while explicit tab_close closes the target via CloseTarget.
func Attach(parentCtx, startupCtx context.Context, targetID target.ID) (*Tab, error) {
	if targetID == "" {
		return nil, fmt.Errorf("tab attach: target ID is required")
	}
	return newTab(parentCtx, startupCtx, targetID, true)
}

func newTab(parentCtx, startupCtx context.Context, targetID target.ID, adopted bool) (*Tab, error) {
	if startupCtx == nil {
		startupCtx = context.Background()
	}
	if err := startupCtx.Err(); err != nil {
		return nil, fmt.Errorf("tab init: %w", err)
	}
	t := &Tab{
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
		Intercept:    collector.NewIntercept(),
	}

	// Register CDP event listeners via WithListener so they are attached
	// before the target processes its first message. Listeners run synchronously
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
				if ev.Frame != nil {
					if ev.Frame.ParentID == "" {
						t.resetForcedPseudoStates()
						t.InvalidateSnapshotRefs("page navigated")
					} else {
						t.InvalidateFrameSnapshotRefs(ev.Frame.ID)
					}
				}
			case *page.EventFrameAttached:
				t.ExecContexts.HandleFrameAttached(ev)
			case *page.EventFrameDetached:
				t.ExecContexts.HandleFrameDetached(ev)
				t.Dialog.HandleFrameDetached(ev)
				if ev != nil && ev.FrameID != "" {
					t.InvalidateFrameSnapshotRefs(ev.FrameID)
				}
			case *page.EventNavigatedWithinDocument:
				t.URLChanges.HandleNavigatedWithinDocument(ev)
			case *page.EventJavascriptDialogOpening:
				t.Dialog.HandleOpening(ev)
			case *page.EventJavascriptDialogClosed:
				t.Dialog.HandleClosed(ev)
			case *inspector.EventTargetCrashed:
				t.Dialog.Clear()
				t.resetForcedPseudoStates()
				t.InvalidateSnapshotRefs("target crashed")
			case *runtime.EventExecutionContextCreated:
				t.ExecContexts.HandleExecutionContextCreated(ev)
			case *runtime.EventExecutionContextDestroyed:
				t.ExecContexts.HandleExecutionContextDestroyed(ev)
			case *runtime.EventExecutionContextsCleared:
				t.ExecContexts.HandleExecutionContextsCleared()
			case *performancetimeline.EventTimelineEventAdded:
				t.Performance.HandleTimelineEvent(ev)
			case *fetch.EventRequestPaused:
				if ev == nil || ev.RequestID == "" {
					return
				}
				rule, matched := t.Intercept.Match(ev)
				// Dispatch CDP Fetch responses asynchronously: WithListener
				// runs synchronously on chromedp's event reader goroutine and
				// calling chromedp.Call inline would deadlock.
				go t.handleRequestPaused(ev, rule, matched)
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

	// Trigger lazy target allocation/attach and configure PerformanceTimeline,
	// Network (MaxPostDataSize), DOM mutation observation, and initial frame seed.
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
		if _, err := cdp.Call(ctx, tgt, network.Enable, network.EnableParams{
			MaxPostDataSize: new(int64(collector.MaxInlineRequestBody)),
		}); err != nil {
			return err
		}
		if _, err := cdp.Call(ctx, tgt, page.AddScriptToEvaluateOnNewDocument, page.AddScriptToEvaluateOnNewDocumentParams{
			Source: domMutationObserverScript,
		}); err != nil {
			return err
		}
		if adopted {
			evalCtx, evalCancel := context.WithTimeout(ctx, time.Second)
			_, _ = chromedp.Evaluate[chromedp.Void](domMutationObserverScript)(evalCtx, tgt)
			evalCancel()
		} else if _, err := chromedp.Evaluate[chromedp.Void](domMutationObserverScript)(ctx, tgt); err != nil {
			return err
		}
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

// LinkContext derives a child of tabCtx (carrying a chromedp target) that also
// cancels when reqCtx finishes, optionally bounded by timeout when timeout > 0.
// The returned cancel function must always be called.
func LinkContext(reqCtx, tabCtx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if reqCtx == nil {
		reqCtx = context.Background()
	}
	child, cancelCause := context.WithCancelCause(tabCtx)
	stop := context.AfterFunc(reqCtx, func() {
		cancelCause(context.Cause(reqCtx))
	})
	if timeout <= 0 {
		return child, func() {
			stop()
			cancelCause(nil)
		}
	}
	timed, cancelTimeout := context.WithTimeout(child, timeout)
	return timed, func() {
		cancelTimeout()
		stop()
		cancelCause(nil)
	}
}

// LinkContext derives a child context from t.ctx linked to reqCtx and
// optionally bounded by timeout when timeout > 0.
func (t *Tab) LinkContext(reqCtx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return LinkContext(reqCtx, t.ctx, timeout)
}

// Close cancels the tab's context. For server-created tabs this closes the
// Chrome target; for adopted tabs (WithDetachOnCancel) it detaches and leaves
// the user's tab open in Chrome.
func (t *Tab) Close() {
	t.cancel()
}

// closeTargetWaitTimeout bounds the post-CloseTarget poll waiting for Chrome
// to drop an explicitly closed adopted target.
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

// BringToFront focuses this tab in Chrome via CDP Page.bringToFront so it
// becomes the foreground tab in headed mode and unthrottles background timers
// and visibilityState.
func (t *Tab) BringToFront(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	runCtx, cancel := t.LinkContext(ctx, defaultTabInfoTimeout)
	defer cancel()
	_, err := chromedp.Call(runCtx, page.BringToFront, cdp.Empty{})
	return err
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
	runCtx, cancel := t.LinkContext(ctx, defaultTabInfoTimeout)
	defer cancel()

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

// WithDragLock serializes drag_and_drop sequences on this tab so concurrent
// drags cannot race on Input.setInterceptDrags or interleaved mouse events.
func (t *Tab) WithDragLock(ctx context.Context, fn func(ctx context.Context, tgt *chromedp.Target) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	t.dragOpMu.Lock()
	defer t.dragOpMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	callCtx, cancel := t.LinkContext(ctx, 0)
	defer cancel()
	return chromedp.Do(callCtx, chromedp.Func(fn))
}
