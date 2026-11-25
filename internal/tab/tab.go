// Package tab manages browser tabs and their associated event collectors.
package tab

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/css"
	"github.com/chromedp/cdproto/dom"
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

// defaultTabInfoTimeout bounds Target.getTargetInfo calls in Tab.Info/URL/Title
// so a wedged browser target cannot block tab_list indefinitely.
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
	// Intercept stores ordered network request interception rules for this tab.
	Intercept *collector.Intercept

	// interceptOpMu serializes lazy Fetch.enable/Fetch.disable domain toggles
	// with rule mutations (outer lock; never acquired by EventRequestPaused).
	interceptOpMu sync.Mutex
	fetchEnabled  bool

	// cssOpMu serializes CSS domain enable/disable transitions between
	// ForcePseudoState and WithCSSCoverage (outer lock; never acquired in
	// WithListener).
	cssOpMu    sync.Mutex
	cssEnabled bool

	// cssStateMu guards active forced pseudo-states across documents
	// (leaf lock; never held across CDP I/O).
	cssStateMu sync.Mutex
	cssDocGen  uint64
	cssForced  map[cdp.BackendNodeID][]string

	// dragOpMu serializes multi-step drag_and_drop sequences (SetInterceptDrags
	// and mouse/drag event dispatch) on this tab.
	dragOpMu sync.Mutex
}

// New creates a new tab from a parent browser context. It creates a new
// Chrome target, starts event listeners, and initializes collectors.
// startupCtx bounds initialization (e.g. MCP request cancellation) without
// tying the tab's post-init lifetime to it; nil defaults to context.Background().
func New(parentCtx, startupCtx context.Context, id string) (*Tab, error) {
	return newTab(parentCtx, startupCtx, id, "", false)
}

// Attach attaches to an existing Chrome page target (such as Chrome's initial
// default tab on browser_launch or an already-open tab on browser_connect)
// without creating a new target. Uses WithDetachOnCancel so failed attaches,
// concurrent dedup, and browser disconnect never destroy pre-existing targets,
// while explicit tab_close closes the target via CloseTarget.
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
		Intercept:    collector.NewIntercept(),
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
				if ev.Frame != nil && ev.Frame.ParentID == "" {
					t.resetForcedPseudoStates()
				}
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
				t.resetForcedPseudoStates()
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

// linkContext derives a child context from t.ctx (carrying the tab's chromedp
// target) that also cancels when caller ctx finishes, optionally bounded by
// timeout when timeout > 0.
func (t *Tab) linkContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	var (
		runCtx context.Context
		cancel context.CancelFunc
	)
	if timeout > 0 {
		runCtx, cancel = context.WithTimeout(t.ctx, timeout)
	} else {
		runCtx, cancel = context.WithCancel(t.ctx)
	}
	stop := context.AfterFunc(ctx, cancel)
	return runCtx, func() {
		stop()
		cancel()
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
	runCtx, cancel := t.linkContext(ctx, defaultTabInfoTimeout)
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

// Title returns the current title of the tab.
func (t *Tab) Title(ctx context.Context) (string, error) {
	_, title, err := t.Info(ctx)
	return title, err
}

// AddInterceptRule enables CDP Fetch interception lazily on the first rule and
// registers rule on this tab. Returns the assigned rule ID.
func (t *Tab) AddInterceptRule(ctx context.Context, rule collector.InterceptRule) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	t.interceptOpMu.Lock()
	defer t.interceptOpMu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}

	if !t.fetchEnabled {
		callCtx, cancel := t.linkContext(ctx, 0)
		_, err := chromedp.Call(callCtx, fetch.Enable, fetch.EnableParams{
			Patterns: []*fetch.RequestPattern{{URLPattern: "*"}},
		})
		cancel()
		if err != nil {
			// If caller ctx cancelled while Fetch.enable was in flight, Chrome
			// may have already enabled Fetch before our reply arrived; disable
			// best-effort so Fetch cannot stay stuck enabled with zero rules.
			if t.ctx.Err() == nil && ctx.Err() != nil {
				t.disableFetchLocked()
			}
			return "", fmt.Errorf("enable fetch interception: %w", err)
		}
		t.fetchEnabled = true
	}

	return t.Intercept.Add(rule), nil
}

// RemoveInterceptRule deletes a rule by ID and disables CDP Fetch interception
// when the last rule is removed.
func (t *Tab) RemoveInterceptRule(ctx context.Context, ruleID string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	t.interceptOpMu.Lock()
	defer t.interceptOpMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}

	remaining, err := t.Intercept.Remove(ruleID)
	if err != nil {
		return err
	}
	if remaining == 0 && t.fetchEnabled {
		t.disableFetchLocked()
	}
	return nil
}

// ClearInterceptRules removes all rules on this tab and disables CDP Fetch
// interception if enabled. Returns the number of rules removed.
func (t *Tab) ClearInterceptRules(ctx context.Context) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	t.interceptOpMu.Lock()
	defer t.interceptOpMu.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	removed := t.Intercept.Clear()
	if t.fetchEnabled {
		t.disableFetchLocked()
	}
	return removed, nil
}

// ListInterceptRules returns summaries of active interception rules in order.
func (t *Tab) ListInterceptRules() []collector.InterceptRuleInfo {
	return t.Intercept.List()
}

// disableFetchLocked calls Fetch.disable under a short bounded context derived
// from t.ctx so caller cancellation cannot leave Fetch stuck enabled with zero
// active rules. Caller must hold t.interceptOpMu.
func (t *Tab) disableFetchLocked() {
	if t.ctx.Err() != nil {
		t.fetchEnabled = false
		return
	}
	disableCtx, cancel := context.WithTimeout(t.ctx, defaultTabInfoTimeout)
	defer cancel()
	if _, err := chromedp.Call(disableCtx, fetch.Disable, cdp.Empty{}); err == nil || t.ctx.Err() != nil {
		t.fetchEnabled = false
	}
}

func (t *Tab) handleRequestPaused(ev *fetch.EventRequestPaused, rule collector.InterceptRule, matched bool) {
	callCtx, cancel := context.WithTimeout(t.ctx, defaultInterceptDispatchTimeout)
	defer cancel()

	if !matched {
		_, _ = chromedp.Call(callCtx, fetch.ContinueRequest, fetch.ContinueRequestParams{
			RequestID: ev.RequestID,
		})
		return
	}

	var err error
	switch rule.Action {
	case collector.InterceptActionFulfill:
		status := rule.Status
		if status == 0 {
			status = 200
		}
		_, err = chromedp.Call(callCtx, fetch.FulfillRequest, fetch.FulfillRequestParams{
			RequestID:       ev.RequestID,
			ResponseCode:    int64(status),
			ResponseHeaders: collector.HeaderEntriesFromMap(rule.Headers),
			Body:            rule.Body,
		})
	case collector.InterceptActionModify:
		params := fetch.ContinueRequestParams{
			RequestID: ev.RequestID,
			URL:       rule.URL,
			Method:    rule.OverrideMethod,
			PostData:  rule.PostData,
		}
		if len(rule.Headers) > 0 {
			if ev.Request != nil {
				params.Headers = collector.MergeRequestHeaders(ev.Request.Headers, rule.Headers)
			} else {
				params.Headers = collector.HeaderEntriesFromMap(rule.Headers)
			}
		}
		_, err = chromedp.Call(callCtx, fetch.ContinueRequest, params)
	case collector.InterceptActionFail:
		reason := rule.ErrorReason
		if reason == "" {
			reason = network.ErrorReasonFailed
		}
		_, err = chromedp.Call(callCtx, fetch.FailRequest, fetch.FailRequestParams{
			RequestID:   ev.RequestID,
			ErrorReason: reason,
		})
	default:
		_, _ = chromedp.Call(callCtx, fetch.ContinueRequest, fetch.ContinueRequestParams{
			RequestID: ev.RequestID,
		})
		return
	}

	// Best-effort fallback continue if the matched action failed while the tab
	// is still alive so a paused request never stalls the browser indefinitely.
	if err != nil && callCtx.Err() == nil {
		_, _ = chromedp.Call(callCtx, fetch.ContinueRequest, fetch.ContinueRequestParams{
			RequestID: ev.RequestID,
		})
	}
}

func (t *Tab) resetForcedPseudoStates() {
	t.cssStateMu.Lock()
	t.cssDocGen++
	hadForced := len(t.cssForced) > 0
	clear(t.cssForced)
	t.cssStateMu.Unlock()
	if hadForced {
		// Disable CSS asynchronously off WithListener's event-reader goroutine
		// so cross-document navigations never leave CSS enabled with zero forced nodes.
		go t.disableCSSIfIdle()
	}
}

func (t *Tab) disableCSSIfIdle() {
	t.cssOpMu.Lock()
	defer t.cssOpMu.Unlock()
	if t.cssEnabled && t.forcedPseudoCount() == 0 {
		t.disableCSSLocked()
	}
}

func (t *Tab) snapshotForcedPseudoStates() (map[cdp.BackendNodeID][]string, uint64) {
	t.cssStateMu.Lock()
	defer t.cssStateMu.Unlock()
	if len(t.cssForced) == 0 {
		return nil, t.cssDocGen
	}
	cp := make(map[cdp.BackendNodeID][]string, len(t.cssForced))
	for id, st := range t.cssForced {
		cp[id] = slices.Clone(st)
	}
	return cp, t.cssDocGen
}

func (t *Tab) forcedPseudoCount() int {
	t.cssStateMu.Lock()
	defer t.cssStateMu.Unlock()
	return len(t.cssForced)
}

// ForcePseudoState applies (or clears when states is empty) forced CSS
// pseudo-classes on the element identified by backendNodeID (resolved by the
// caller via chromedp.Nodes so chromedp's internal DOM node cache stays intact).
// Keeps the CSS domain enabled while any element has forced states and
// disables it automatically once all forced states on the tab are cleared.
func (t *Tab) ForcePseudoState(ctx context.Context, backendNodeID cdp.BackendNodeID, states []string) ([]string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if backendNodeID == 0 {
		return nil, fmt.Errorf("invalid backend node ID")
	}

	t.cssOpMu.Lock()
	defer t.cssOpMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	t.cssStateMu.Lock()
	gen := t.cssDocGen
	t.cssStateMu.Unlock()

	forced := make([]string, len(states))
	copy(forced, states)

	callCtx, cancel := t.linkContext(ctx, 0)
	defer cancel()

	wasEnabled := t.cssEnabled
	err := chromedp.Do(callCtx, chromedp.Func(func(ctx context.Context, tgt *chromedp.Target) error {
		if len(forced) > 0 && !t.cssEnabled {
			if _, err := cdp.Call(ctx, tgt, dom.Enable, dom.EnableParams{}); err != nil {
				return fmt.Errorf("enable DOM domain: %w", err)
			}
			if _, err := cdp.Call(ctx, tgt, css.Enable, cdp.Empty{}); err != nil {
				return fmt.Errorf("enable CSS domain: %w", err)
			}
			t.cssEnabled = true
		}

		if t.cssEnabled {
			pushRes, err := cdp.Call(ctx, tgt, dom.PushNodesByBackendIDsToFrontend, dom.PushNodesByBackendIDsToFrontendParams{
				BackendNodeIDs: []cdp.BackendNodeID{backendNodeID},
			})
			if err != nil {
				return fmt.Errorf("resolve DOM node: %w", err)
			}
			if len(pushRes.NodeIDs) == 0 || pushRes.NodeIDs[0] == 0 {
				return fmt.Errorf("element is no longer attached to the document")
			}
			if _, err := cdp.Call(ctx, tgt, css.ForcePseudoState, css.ForcePseudoStateParams{
				NodeID:              pushRes.NodeIDs[0],
				ForcedPseudoClasses: forced,
			}); err != nil {
				return fmt.Errorf("forcePseudoState: %w", err)
			}
		}
		return nil
	}))
	if err != nil {
		if (!wasEnabled && (t.cssEnabled || (t.ctx.Err() == nil && ctx.Err() != nil))) && t.forcedPseudoCount() == 0 {
			t.disableCSSLocked()
		}
		return nil, err
	}

	t.cssStateMu.Lock()
	if t.cssDocGen == gen {
		if len(forced) == 0 {
			delete(t.cssForced, backendNodeID)
		} else {
			if t.cssForced == nil {
				t.cssForced = make(map[cdp.BackendNodeID][]string)
			}
			t.cssForced[backendNodeID] = slices.Clone(forced)
		}
	}
	remaining := len(t.cssForced)
	t.cssStateMu.Unlock()

	if remaining == 0 && t.cssEnabled {
		t.disableCSSLocked()
	}
	return forced, nil
}

// WithCSSCoverage runs a CSS coverage collection callback while coordinating
// CSS.enable/CSS.disable with any active forced pseudo-states. If CSS was
// already enabled for forced pseudo-states, it is disabled first so the
// callback's CSS.enable receives fresh styleSheetAdded events, and any still-
// live forced pseudo-states are restored afterward.
func (t *Tab) WithCSSCoverage(ctx context.Context, fn func(ctx context.Context, tgt *chromedp.Target) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	t.cssOpMu.Lock()
	defer t.cssOpMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}

	if t.cssEnabled {
		t.disableCSSLocked()
	}

	callCtx, cancel := t.linkContext(ctx, 0)
	defer cancel()
	err := chromedp.Do(callCtx, chromedp.Func(fn))
	if err != nil && t.ctx.Err() == nil && ctx.Err() != nil {
		// Caller cancelled mid-call (potentially mid-CSS.enable before the
		// callback's defer CSS.disable registered); disable best-effort so CSS
		// cannot stay stuck enabled and break subsequent styleSheetAdded events.
		t.disableCSSLocked()
	} else {
		t.cssEnabled = false
	}

	t.restoreForcedPseudoStatesLocked()
	return err
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
	callCtx, cancel := t.linkContext(ctx, 0)
	defer cancel()
	return chromedp.Do(callCtx, chromedp.Func(fn))
}

// disableCSSLocked disables the CSS domain under a short bounded context
// derived from t.ctx. Caller must hold t.cssOpMu.
func (t *Tab) disableCSSLocked() {
	if t.ctx.Err() != nil {
		t.cssEnabled = false
		return
	}
	disableCtx, cancel := context.WithTimeout(t.ctx, defaultTabInfoTimeout)
	defer cancel()
	if _, err := chromedp.Call(disableCtx, css.Disable, cdp.Empty{}); err == nil || t.ctx.Err() != nil {
		t.cssEnabled = false
	}
}

// restoreForcedPseudoStatesLocked best-effort re-enables CSS and reapplies
// tracked forced pseudo-states after CSS coverage finishes. Caller must hold
// t.cssOpMu.
func (t *Tab) restoreForcedPseudoStatesLocked() {
	if t.ctx.Err() != nil {
		return
	}
	forced, snapGen := t.snapshotForcedPseudoStates()
	if len(forced) == 0 {
		return
	}

	restoreCtx, cancel := context.WithTimeout(t.ctx, defaultTabInfoTimeout)
	defer cancel()

	ids := make([]cdp.BackendNodeID, 0, len(forced))
	for id := range forced {
		ids = append(ids, id)
	}

	applied := 0
	var stale []cdp.BackendNodeID
	err := chromedp.Do(restoreCtx, chromedp.Func(func(ctx context.Context, tgt *chromedp.Target) error {
		if _, err := cdp.Call(ctx, tgt, dom.Enable, dom.EnableParams{}); err != nil {
			return err
		}
		if _, err := cdp.Call(ctx, tgt, css.Enable, cdp.Empty{}); err != nil {
			return err
		}
		t.cssEnabled = true
		pushRes, err := cdp.Call(ctx, tgt, dom.PushNodesByBackendIDsToFrontend, dom.PushNodesByBackendIDsToFrontendParams{
			BackendNodeIDs: ids,
		})
		if err != nil {
			return err
		}
		dialogOpen := t.Dialog != nil && t.Dialog.Open()
		for i, bid := range ids {
			if i >= len(pushRes.NodeIDs) || pushRes.NodeIDs[i] == 0 {
				stale = append(stale, bid)
				continue
			}
			if !dialogOpen {
				connected, connErr := isBackendNodeConnected(ctx, tgt, bid)
				if connErr != nil {
					// Transient timeout/cancel while probing connectedness:
					// do not falsely prune live forced pseudo-states.
					return connErr
				}
				if !connected {
					stale = append(stale, bid)
					continue
				}
			}
			if _, err := cdp.Call(ctx, tgt, css.ForcePseudoState, css.ForcePseudoStateParams{
				NodeID:              pushRes.NodeIDs[i],
				ForcedPseudoClasses: forced[bid],
			}); err == nil {
				applied++
			}
		}
		return nil
	}))
	if len(stale) > 0 {
		t.cssStateMu.Lock()
		if t.cssDocGen == snapGen {
			for _, bid := range stale {
				delete(t.cssForced, bid)
			}
		}
		t.cssStateMu.Unlock()
	}
	if err != nil || applied == 0 {
		if t.cssEnabled {
			t.disableCSSLocked()
		}
	}
}

func isBackendNodeConnected(ctx context.Context, tgt *chromedp.Target, bid cdp.BackendNodeID) (bool, error) {
	res, err := cdp.Call(ctx, tgt, dom.ResolveNode, dom.ResolveNodeParams{
		BackendNodeID: bid,
	})
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, nil
	}
	if res.Object == nil || res.Object.ObjectID == "" {
		return false, nil
	}
	oid := res.Object.ObjectID
	defer func() {
		cleanCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), defaultTabInfoTimeout)
		defer cancel()
		_, _ = cdp.Call(cleanCtx, tgt, runtime.ReleaseObject, runtime.ReleaseObjectParams{
			ObjectID: oid,
		})
	}()
	callRes, err := cdp.Call(ctx, tgt, runtime.CallFunctionOn, runtime.CallFunctionOnParams{
		ObjectID:            oid,
		FunctionDeclaration: `function() { return Boolean(this && this.isConnected); }`,
		ReturnByValue:       new(true),
		Silent:              new(true),
	})
	if err != nil {
		return false, err
	}
	if callRes.ExceptionDetails != nil || callRes.Result == nil {
		return false, nil
	}
	return string(callRes.Result.Value) == "true", nil
}
