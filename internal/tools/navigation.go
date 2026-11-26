package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/browser"
	"github.com/greatliontech/chromedp-mcp/internal/tab"
)

const (
	navActionGoto    = "goto"
	navActionReload  = "reload"
	navActionBack    = "back"
	navActionForward = "forward"

	waitStateVisible  = "visible"
	waitStateHidden   = "hidden"
	waitStateAttached = "attached"
	waitStateDetached = "detached"
)

var (
	navigateActionValues    = []string{navActionGoto, navActionReload, navActionBack, navActionForward}
	navigateWaitUntilValues = []string{"load", "domcontentloaded", "networkidle"}
	waitForStateValues      = []string{waitStateVisible, waitStateHidden, waitStateAttached, waitStateDetached}
)

// NavigateInput is the input for navigate.
type NavigateInput struct {
	TabInput
	Action      string `json:"action,omitempty" jsonschema:"Navigation action: 'goto' (default), 'reload', 'back', or 'forward'."`
	URL         string `json:"url,omitempty" jsonschema:"URL to navigate to (required for action='goto')."`
	WaitUntil   string `json:"wait_until,omitempty" jsonschema:"For action='goto': wait condition 'load' (default), 'domcontentloaded', or 'networkidle'."`
	BypassCache bool   `json:"bypass_cache,omitempty" jsonschema:"For action='reload': bypass browser cache (default false)."`
}

// NavigateOutput is the output for navigate.
type NavigateOutput struct {
	URL    string `json:"url"`
	Title  string `json:"title"`
	Status int64  `json:"status,omitempty"`
}

// WaitForInput is the input for wait_for.
type WaitForInput struct {
	TabInput
	Selector   string `json:"selector,omitempty" jsonschema:"CSS/shadow ('>>>') selector to wait for."`
	Frame      string `json:"frame,omitempty" jsonschema:"Optional same-origin frame ID for selector, text, or expression."`
	State      string `json:"state,omitempty" jsonschema:"For selector: 'visible' (default), 'hidden', 'attached', or 'detached'."`
	Text       string `json:"text,omitempty" jsonschema:"Wait until page/frame visible text contains this substring."`
	URL        string `json:"url,omitempty" jsonschema:"Wait until tab URL contains this substring."`
	Expression string `json:"expression,omitempty" jsonschema:"JS expression to poll until truthy (supports Promises)."`
	Timeout    int    `json:"timeout,omitempty" jsonschema:"Timeout in milliseconds (default 30000)."`
}

func registerNavigationTools(s *mcp.Server, mgr *browser.Manager) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "navigate",
		Description: "Navigate a tab: go to a URL ('goto', default), reload ('reload'), or move in history ('back'/'forward'). Returns final URL, page title, and HTTP status (for goto/reload when available).",
		InputSchema: schemaWithEnums[NavigateInput](map[string][]string{
			"action":     navigateActionValues,
			"wait_until": navigateWaitUntilValues,
		}),
		Annotations: mutatingAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input NavigateInput) (*mcp.CallToolResult, NavigateOutput, error) {
		if err := validateEnum("action", input.Action, true, navigateActionValues); err != nil {
			return nil, NavigateOutput{}, err
		}
		if err := validateEnum("wait_until", input.WaitUntil, true, navigateWaitUntilValues); err != nil {
			return nil, NavigateOutput{}, err
		}
		action := input.Action
		if action == "" {
			action = navActionGoto
		}
		if action == navActionGoto && input.URL == "" {
			return nil, NavigateOutput{}, fmt.Errorf("url is required for action='goto'")
		}
		if action != navActionGoto && input.URL != "" {
			return nil, NavigateOutput{}, fmt.Errorf("url is only valid when action is 'goto'")
		}
		if action != navActionGoto && input.WaitUntil != "" {
			return nil, NavigateOutput{}, fmt.Errorf("wait_until is only valid when action is 'goto'")
		}
		if action != navActionReload && input.BypassCache {
			return nil, NavigateOutput{}, fmt.Errorf("bypass_cache is only valid when action is 'reload'")
		}

		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, NavigateOutput{}, err
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		navCtx, navCancel := context.WithTimeout(tctx, defaultLifecycleTimeout)
		defer navCancel()

		var (
			resp  *network.Response
			navOp string
		)
		switch action {
		case navActionGoto:
			waitEvent := input.WaitUntil
			if waitEvent == "" {
				waitEvent = "load"
			}
			navOp = fmt.Sprintf("navigate to %q (wait_until=%s)", input.URL, waitEvent)
			switch waitEvent {
			case "domcontentloaded", "networkidle":
				resp, err = navigateUntilLifecycle(navCtx, tctx, input.URL, waitEvent)
			default:
				resp, err = chromedp.Run(navCtx, chromedp.NavigateResponse(input.URL))
			}
		case navActionReload:
			navOp = "reload"
			reloadParams := page.ReloadParams{}
			if input.BypassCache {
				reloadParams.IgnoreCache = new(true)
			}
			resp, err = chromedp.RunResponse(navCtx, chromedp.Func(func(ctx context.Context, tgt *chromedp.Target) error {
				_, err := cdp.Call(ctx, tgt, page.Reload, reloadParams)
				return err
			}))
		case navActionBack:
			navOp = "navigate back"
			err = chromedp.Do(navCtx, navigateHistory(-1))
		case navActionForward:
			navOp = "navigate forward"
			err = chromedp.Do(navCtx, navigateHistory(+1))
		}
		if err != nil {
			return nil, NavigateOutput{}, wrapNavTimeout(err, tctx.Err(), navOp)
		}

		url, title, err := tabURLAndTitle(navCtx, t)
		if err != nil {
			return nil, NavigateOutput{}, fmt.Errorf("reading tab info after %s: %w", navOp, err)
		}
		out := NavigateOutput{URL: url, Title: title}
		if resp != nil {
			out.Status = resp.Status
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "wait_for",
		Description: "Wait for a selector state ('visible'|'hidden'|'attached'|'detached'), visible page text, tab URL substring, or truthy JS expression (supports Promises). Provide exactly one condition.",
		InputSchema: schemaWithEnums[WaitForInput](map[string][]string{
			"state": waitForStateValues,
		}),
		Annotations: readOnlyAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input WaitForInput) (*mcp.CallToolResult, struct{}, error) {
		if err := validateEnum("state", input.State, true, waitForStateValues); err != nil {
			return nil, struct{}{}, err
		}
		if err := validateSelectorTimeout(input.Timeout); err != nil {
			return nil, struct{}{}, err
		}
		conditions := 0
		if input.Selector != "" {
			conditions++
		}
		if input.Text != "" {
			conditions++
		}
		if input.URL != "" {
			conditions++
		}
		if input.Expression != "" {
			conditions++
		}
		if conditions != 1 {
			return nil, struct{}{}, fmt.Errorf("exactly one of selector, text, url, or expression must be provided")
		}
		if input.State != "" && input.Selector == "" {
			return nil, struct{}{}, fmt.Errorf("state is only valid with selector")
		}
		if input.Frame != "" && input.URL != "" {
			return nil, struct{}{}, fmt.Errorf("frame cannot be combined with url")
		}
		if input.Selector != "" {
			if _, _, err := parseShadowSelector(input.Selector); err != nil {
				return nil, struct{}{}, err
			}
		}

		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()

		timeout := 30 * time.Second
		if input.Timeout > 0 {
			timeout = time.Duration(input.Timeout) * time.Millisecond
		}
		wctx, cancel := context.WithTimeout(tctx, timeout)
		defer cancel()

		switch {
		case input.URL != "":
			var lastURL string
			ticker := time.NewTicker(25 * time.Millisecond)
			defer ticker.Stop()
			for {
				curURL, _, err := t.Info(wctx)
				if err == nil {
					lastURL = curURL
					if strings.Contains(curURL, input.URL) {
						return nil, struct{}{}, nil
					}
				} else {
					if tctx.Err() != nil {
						return nil, struct{}{}, tctx.Err()
					}
					if wctx.Err() != nil {
						return nil, struct{}{}, fmt.Errorf("timed out waiting for URL containing %q (last URL: %q)", input.URL, lastURL)
					}
					// Non-context error (e.g. target detached): fail fast.
					return nil, struct{}{}, err
				}
				select {
				case <-tctx.Done():
					return nil, struct{}{}, tctx.Err()
				case <-wctx.Done():
					return nil, struct{}{}, fmt.Errorf("timed out waiting for URL containing %q (last URL: %q)", input.URL, lastURL)
				case <-ticker.C:
				}
			}

		case input.Selector != "":
			state := input.State
			if state == "" {
				state = waitStateVisible
			}
			uniqueCtxID, err := resolveFrameContext(t, input.Frame, true)
			if err != nil {
				return nil, struct{}{}, err
			}
			findExpr, err := queryOneElementJS(input.Selector)
			if err != nil {
				return nil, struct{}{}, err
			}
			probeJS := fmt.Sprintf(`(function() {
				const el = %s;
				if (!el) return "detached";
				const win = (el.ownerDocument && el.ownerDocument.defaultView) || window;
				const cs = win.getComputedStyle(el);
				const hasBox = Boolean(el.offsetWidth || el.offsetHeight ||
					(typeof el.getClientRects === "function" && el.getClientRects().length > 0));
				if (!hasBox || cs.visibility === "hidden" || cs.visibility === "collapse") return "hidden";
				return "visible";
			})()`, findExpr)

			matchState := func(cur string) bool {
				switch state {
				case waitStateAttached:
					return cur == "visible" || cur == "hidden"
				case waitStateDetached:
					return cur == "detached"
				case waitStateHidden:
					return cur == "hidden" || cur == "detached"
				default:
					return cur == "visible"
				}
			}
			_, err = pollFrameEval(wctx, tctx, t, uniqueCtxID, input.Frame, probeJS, false, 25*time.Millisecond,
				matchState,
				func(lastState string, hasVal bool) error {
					if !hasVal {
						return fmt.Errorf("timed out waiting for selector %q (state=%s): evaluation did not complete", input.Selector, state)
					}
					switch state {
					case waitStateVisible:
						if lastState == "hidden" {
							return fmt.Errorf("timed out waiting for selector %q (state=visible): element exists but is not visible", input.Selector)
						}
						return fmt.Errorf("timed out waiting for selector %q (state=visible): not found in the DOM", input.Selector)
					case waitStateAttached:
						return fmt.Errorf("timed out waiting for selector %q (state=attached): not found in the DOM", input.Selector)
					case waitStateDetached:
						return fmt.Errorf("timed out waiting for selector %q (state=detached): element is still attached to the DOM", input.Selector)
					default:
						return fmt.Errorf("timed out waiting for selector %q (state=hidden): element is still visible", input.Selector)
					}
				},
				fmt.Sprintf("selector %q", input.Selector),
			)
			if err != nil {
				return nil, struct{}{}, err
			}
			return nil, struct{}{}, nil

		case input.Text != "":
			uniqueCtxID, err := resolveFrameContext(t, input.Frame, true)
			if err != nil {
				return nil, struct{}{}, err
			}
			textJS := fmt.Sprintf(`(function() {
				const root = document.body || document.documentElement;
				if (!root) return false;
				const t = typeof root.innerText === "string" ? root.innerText : (root.textContent || "");
				return t.includes(%s);
			})()`, jsQuote(input.Text))
			_, err = pollFrameEval(wctx, tctx, t, uniqueCtxID, input.Frame, textJS, false, 25*time.Millisecond,
				func(found bool) bool { return found },
				func(_ bool, _ bool) error {
					return fmt.Errorf("timed out waiting for text %q", input.Text)
				},
				fmt.Sprintf("text %q", input.Text),
			)
			if err != nil {
				return nil, struct{}{}, err
			}
			return nil, struct{}{}, nil

		default:
			uniqueCtxID, err := resolveFrameContext(t, input.Frame, true)
			if err != nil {
				return nil, struct{}{}, err
			}
			// Coerce the predicate to Promise<boolean> so non-primitives (e.g.
			// DOM nodes) never fail serialization and Promises are awaited.
			// Newlines defend against trailing // line comments in Expression.
			coerced := "Promise.resolve(\n" + input.Expression + "\n).then(Boolean)"
			_, err = pollFrameEval(wctx, tctx, t, uniqueCtxID, input.Frame, coerced, true, 25*time.Millisecond,
				func(ok bool) bool { return ok },
				func(_ bool, _ bool) error {
					return fmt.Errorf("timed out waiting for expression to become truthy")
				},
				"expression",
			)
			if err != nil {
				return nil, struct{}{}, err
			}
			return nil, struct{}{}, nil
		}
	})
}

// tabURLAndTitle returns the tab's current URL and document.title in one CDP
// round trip, falling back to non-blocking browser-process Tab.Info whenever
// a JavaScript modal dialog blocks the renderer.
func tabURLAndTitle(ctx context.Context, t *tab.Tab) (url, title string, err error) {
	if t.Dialog != nil && t.Dialog.Open() {
		return t.Info(ctx)
	}
	type pageMeta struct {
		URL   string `json:"url"`
		Title string `json:"title"`
	}
	var (
		meta    pageMeta
		gotMeta bool
	)
	evalErr := runDialogInterruptible(ctx, t.Dialog, func(dctx context.Context) error {
		m, e := chromedp.Run(dctx, chromedp.Evaluate[pageMeta](`({url: window.location.href, title: document.title})`))
		if e == nil {
			meta = m
			gotMeta = true
		}
		return e
	})
	if evalErr == nil && gotMeta {
		return meta.URL, meta.Title, nil
	}
	if (t.Dialog != nil && t.Dialog.Open()) || (evalErr == nil && !gotMeta) || isStaleContextError(evalErr) {
		return t.Info(ctx)
	}
	return "", "", evalErr
}

// wrapNavTimeout annotates a lifecycle timeout (navCtx expired while parent
// tctx is still live) with the operation description and timeout duration.
func wrapNavTimeout(err, parentErr error, op string) error {
	if err != nil && parentErr == nil && errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("timed out after %s during %s: %w", defaultLifecycleTimeout, op, err)
	}
	return err
}

// navigateUntilLifecycle navigates via Page.navigate and waits until the top
// frame emits the requested lifecycle event ("domcontentloaded" or
// "networkidle") for the newly initiated (FrameID, LoaderID), ignoring stale
// events from previous documents or subframes. If the newly committed page
// triggers a client-side redirect (e.g. location.replace or meta refresh)
// before reaching the target lifecycle event, the redirect's LoaderID is
// adopted so navigation tracks the final destination instead of timing out.
func navigateUntilLifecycle(navCtx, parentCtx context.Context, targetURL, waitEvent string) (*network.Response, error) {
	var outResp *network.Response
	err := chromedp.Do(navCtx, chromedp.Func(func(ctx context.Context, tgt *chromedp.Target) error {
		var (
			topFrameID    cdp.FrameID
			priorLoaderID cdp.LoaderID
		)
		if treeRes, err := cdp.Call(ctx, tgt, page.GetFrameTree, cdp.Empty{}); err == nil && treeRes.FrameTree != nil && treeRes.FrameTree.Frame != nil {
			topFrameID = treeRes.FrameTree.Frame.ID
			priorLoaderID = treeRes.FrameTree.Frame.LoaderID
		}

		var (
			mu           sync.Mutex
			loaderID     cdp.LoaderID
			seenInitial  bool
			superseded   = make(map[cdp.LoaderID]bool)
			loaderByReq  = make(map[network.RequestID]cdp.LoaderID)
			respByLoader = make(map[cdp.LoaderID]*network.Response)
			errByLoader  = make(map[cdp.LoaderID]error)
			finished     bool
			early        []any
		)
		if priorLoaderID != "" {
			superseded[priorLoaderID] = true
		}

		doneCh := make(chan struct{})
		markDone := sync.OnceFunc(func() {
			close(doneCh)
		})

		matchesEvent := func(name string) bool {
			if waitEvent == "networkidle" {
				return name == "networkIdle"
			}
			return name == "DOMContentLoaded" || name == "load"
		}

		adoptLoader := func(newID cdp.LoaderID) {
			if newID == "" {
				return
			}
			if newID == loaderID {
				seenInitial = true
				return
			}
			if seenInitial && !superseded[newID] {
				// Client-side redirect superseded the initial loader.
				superseded[loaderID] = true
				loaderID = newID
			}
		}

		processLocked := func(ev any) {
			switch e := ev.(type) {
			case *network.EventRequestWillBeSent:
				if e.Type == network.ResourceTypeDocument && e.LoaderID != "" && !superseded[e.LoaderID] {
					loaderByReq[e.RequestID] = e.LoaderID
					if e.LoaderID == loaderID {
						seenInitial = true
					}
				}
			case *network.EventResponseReceived:
				if lid, ok := loaderByReq[e.RequestID]; ok {
					respByLoader[lid] = e.Response
				}
			case *network.EventLoadingFailed:
				if lid, ok := loaderByReq[e.RequestID]; ok {
					errByLoader[lid] = &chromedp.LoadError{ErrorText: e.ErrorText}
					if lid == loaderID && e.Canceled {
						finished = true
						markDone()
					}
				}
			case *page.EventFrameNavigated:
				if e.Frame == nil || e.Frame.ParentID != "" {
					return
				}
				if topFrameID == "" {
					topFrameID = e.Frame.ID
				}
				if e.Frame.ID != topFrameID {
					return
				}
				adoptLoader(e.Frame.LoaderID)
			case *page.EventLifecycleEvent:
				if e.FrameID != topFrameID || e.LoaderID == "" {
					return
				}
				if e.LoaderID == loaderID {
					seenInitial = true
				} else if e.Name == "init" {
					adoptLoader(e.LoaderID)
				}
				if e.LoaderID == loaderID && matchesEvent(e.Name) {
					finished = true
					markDone()
				}
			case *page.EventFrameStoppedLoading:
				if e.FrameID != topFrameID {
					return
				}
				if errByLoader[loaderID] != nil {
					finished = true
					markDone()
					return
				}
				// HTTP 204/205 responses never commit a document or emit
				// DOMContentLoaded/networkIdle; complete once the frame stops.
				if r := respByLoader[loaderID]; r != nil && (r.Status == 204 || r.Status == 205) {
					finished = true
					markDone()
				}
			}
		}

		lctx, lcancel := context.WithCancel(ctx)
		defer lcancel()
		if err := chromedp.Listen(lctx, func(ev any) {
			mu.Lock()
			defer mu.Unlock()
			if finished {
				return
			}
			if loaderID == "" {
				early = append(early, ev)
				return
			}
			processLocked(ev)
		}); err != nil {
			return err
		}

		navRes, err := cdp.Call(ctx, tgt, page.Navigate, page.NavigateParams{URL: targetURL})
		if err != nil {
			mu.Lock()
			finished = true
			early = nil
			mu.Unlock()
			return err
		}
		if navRes.ErrorText != "" {
			mu.Lock()
			finished = true
			early = nil
			mu.Unlock()
			return &chromedp.LoadError{ErrorText: navRes.ErrorText}
		}
		// Empty LoaderID indicates a same-document fragment navigation; no new load occurs.
		if navRes.LoaderID == "" {
			mu.Lock()
			finished = true
			early = nil
			mu.Unlock()
			return nil
		}

		mu.Lock()
		if navRes.FrameID != "" {
			topFrameID = navRes.FrameID
		}
		// Clear priorLoaderID if Chrome happened to reuse it (defensive).
		delete(superseded, navRes.LoaderID)
		loaderID = navRes.LoaderID
		for _, ev := range early {
			if finished {
				break
			}
			processLocked(ev)
		}
		early = nil
		mu.Unlock()

		select {
		case <-doneCh:
			mu.Lock()
			finalErr := errByLoader[loaderID]
			outResp = respByLoader[loaderID]
			mu.Unlock()
			if finalErr != nil {
				return finalErr
			}
			return nil
		case <-ctx.Done():
			mu.Lock()
			finished = true
			mu.Unlock()
			if parentCtx.Err() != nil {
				return parentCtx.Err()
			}
			return ctx.Err()
		}
	}))
	return outResp, err
}

// historyNavResult describes how the browser navigated after a history entry change.
type historyNavResult int

const (
	historyNavCrossDocument historyNavResult = iota // Real navigation (new document loaded)
	historyNavBFCache                               // Back-forward cache restore
	historyNavSameDocument                          // SPA pushState / history API
)

// navigateHistory navigates the browser history by the given delta (-1 for
// back, +1 for forward). It uses page.NavigateToHistoryEntry and listens
// for CDP events to determine the navigation type:
//
//   - Same-document (SPA/pushState): fires EventNavigatedWithinDocument.
//     No reload needed — the DOM is already live and correct.
//
//   - Back-forward cache restore: fires EventFrameNavigated with
//     Type == BackForwardCacheRestore. Requires a reload because Chrome
//     doesn't emit dom.EventDocumentUpdated, leaving chromedp's internal
//     DOM node cache stale (chromedp/chromedp#1346).
//
//   - Normal cross-document navigation: fires EventFrameNavigated with
//     Type == Navigation. No reload needed — the full page load triggers
//     proper DOM events that resync chromedp's cache.
func navigateHistory(delta int) chromedp.Action[chromedp.Void] {
	return chromedp.Func(func(ctx context.Context, tgt *chromedp.Target) error {
		hist, err := cdp.Call(ctx, tgt, page.GetNavigationHistory, cdp.Empty{})
		if err != nil {
			return err
		}
		targetIdx := hist.CurrentIndex + int64(delta)
		if targetIdx < 0 || targetIdx > int64(len(hist.Entries)-1) {
			if delta < 0 {
				return fmt.Errorf("no previous history entry")
			}
			return fmt.Errorf("no forward history entry")
		}

		var topFrameID cdp.FrameID
		if treeRes, err := cdp.Call(ctx, tgt, page.GetFrameTree, cdp.Empty{}); err == nil && treeRes.FrameTree != nil && treeRes.FrameTree.Frame != nil {
			topFrameID = treeRes.FrameTree.Frame.ID
		}

		// Listen for navigation events before triggering the history
		// entry change so we don't miss them. We need to distinguish
		// three cases: same-document (SPA), bfcache restore, and
		// normal cross-document navigation.
		type navEvent struct {
			result historyNavResult
		}
		ch := make(chan navEvent, 1)
		loadDoneCh := make(chan struct{})
		markLoadDone := sync.OnceFunc(func() { close(loadDoneCh) })
		var (
			mu            sync.Mutex
			crossLoaderID cdp.LoaderID
			loadedLoaders = make(map[cdp.LoaderID]bool)
		)
		lctx, lcancel := context.WithCancel(ctx)
		defer lcancel()

		if err := chromedp.Listen(lctx, func(ev any) {
			switch e := ev.(type) {
			case *page.EventNavigatedWithinDocument:
				if topFrameID != "" && e.FrameID != topFrameID {
					return // Ignore subframe same-document navigations
				}
				select {
				case ch <- navEvent{historyNavSameDocument}:
				default:
				}
			case *page.EventFrameNavigated:
				if e.Frame == nil || e.Frame.ParentID != "" {
					return // Ignore subframe navigations
				}
				result := historyNavCrossDocument
				if e.Type == page.NavigationTypeBackForwardCacheRestore {
					result = historyNavBFCache
				} else {
					mu.Lock()
					crossLoaderID = e.Frame.LoaderID
					if crossLoaderID != "" && loadedLoaders[crossLoaderID] {
						markLoadDone()
					}
					mu.Unlock()
				}
				select {
				case ch <- navEvent{result}:
				default:
				}
			case *page.EventLifecycleEvent:
				if (topFrameID == "" || e.FrameID == topFrameID) && e.Name == "load" && e.LoaderID != "" {
					mu.Lock()
					loadedLoaders[e.LoaderID] = true
					if crossLoaderID != "" && e.LoaderID == crossLoaderID {
						markLoadDone()
					}
					mu.Unlock()
				}
			case *page.EventLoadEventFired:
				mu.Lock()
				if crossLoaderID != "" {
					markLoadDone()
				}
				mu.Unlock()
			case *page.EventFrameStoppedLoading:
				if topFrameID == "" || e.FrameID == topFrameID {
					mu.Lock()
					if crossLoaderID != "" {
						markLoadDone()
					}
					mu.Unlock()
				}
			}
		}); err != nil {
			return err
		}

		if _, err := cdp.Call(ctx, tgt, page.NavigateToHistoryEntry, page.NavigateToHistoryEntryParams{
			EntryID: hist.Entries[targetIdx].ID,
		}); err != nil {
			return err
		}

		var nav navEvent
		select {
		case nav = <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}

		switch nav.result {
		case historyNavBFCache:
			// Reload to force a fresh document load. This triggers
			// dom.EventDocumentUpdated, which resyncs chromedp's
			// DOM cache. Without this, bfcache restores leave the
			// cache stale and all selector-based queries time out.
			_, err := chromedp.Reload()(ctx, tgt)
			return err
		case historyNavCrossDocument:
			// Wait for the committed cross-document history entry to finish
			// loading so document.title and DOM queries see the parsed document.
			select {
			case <-loadDoneCh:
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		return nil
	})
}
