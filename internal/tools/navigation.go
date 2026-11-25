package tools

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/browser"
)

// NavigateInput is the input for navigate.
type NavigateInput struct {
	TabInput
	URL       string `json:"url" jsonschema:"The URL to navigate to"`
	WaitUntil string `json:"wait_until,omitempty" jsonschema:"Wait condition: load (default) domcontentloaded or networkidle"`
}

// NavigateOutput is the output for navigate.
type NavigateOutput struct {
	URL    string `json:"url"`
	Title  string `json:"title"`
	Status int64  `json:"status,omitempty"`
}

// ReloadInput is the input for reload.
type ReloadInput struct {
	TabInput
	BypassCache bool `json:"bypass_cache,omitempty" jsonschema:"Bypass browser cache (default false)"`
}

// ReloadOutput is the output for reload.
type ReloadOutput struct {
	URL   string `json:"url"`
	Title string `json:"title"`
}

// GoBackInput is the input for go_back.
type GoBackInput struct {
	TabInput
}

// GoForwardInput is the input for go_forward.
type GoForwardInput struct {
	TabInput
}

// WaitForInput is the input for wait_for.
type WaitForInput struct {
	TabInput
	Selector   string `json:"selector,omitempty" jsonschema:"CSS selector to wait for (waits until visible)"`
	Expression string `json:"expression,omitempty" jsonschema:"JS expression to poll until truthy"`
	Timeout    int    `json:"timeout,omitempty" jsonschema:"Timeout in milliseconds (default 30000)"`
}

var navigateWaitUntilValues = []string{"load", "domcontentloaded", "networkidle"}

func registerNavigationTools(s *mcp.Server, mgr *browser.Manager) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "navigate",
		Description: "Navigate a tab to a URL. Returns the final URL (after redirects), page title, and HTTP status code.",
		InputSchema: schemaWithEnums[NavigateInput](map[string][]string{
			"wait_until": navigateWaitUntilValues,
		}),
		Annotations: mutatingAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input NavigateInput) (*mcp.CallToolResult, NavigateOutput, error) {
		if input.URL == "" {
			return nil, NavigateOutput{}, fmt.Errorf("url is required")
		}
		if err := validateEnum("wait_until", input.WaitUntil, true, navigateWaitUntilValues); err != nil {
			return nil, NavigateOutput{}, err
		}
		waitEvent := input.WaitUntil
		if waitEvent == "" {
			waitEvent = "load"
		}

		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, NavigateOutput{}, err
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		navCtx, navCancel := context.WithTimeout(tctx, defaultLifecycleTimeout)
		defer navCancel()

		navOp := fmt.Sprintf("navigate to %q (wait_until=%s)", input.URL, waitEvent)
		var resp *network.Response
		switch waitEvent {
		case "domcontentloaded", "networkidle":
			resp, err = navigateUntilLifecycle(navCtx, tctx, input.URL, waitEvent)
		default:
			resp, err = chromedp.Run(navCtx, chromedp.NavigateResponse(input.URL))
		}
		if err != nil {
			return nil, NavigateOutput{}, wrapNavTimeout(err, tctx.Err(), navOp)
		}

		url, err := chromedp.Run(navCtx, chromedp.Location())
		if err != nil {
			return nil, NavigateOutput{}, wrapNavTimeout(err, tctx.Err(), navOp)
		}
		title, err := chromedp.Run(navCtx, chromedp.Title())
		if err != nil {
			return nil, NavigateOutput{}, wrapNavTimeout(err, tctx.Err(), navOp)
		}

		out := NavigateOutput{URL: url, Title: title}
		if resp != nil {
			out.Status = resp.Status
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "reload",
		Description: "Reload the current page. Returns the final URL and page title after reload.",
		Annotations: mutatingAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ReloadInput) (*mcp.CallToolResult, ReloadOutput, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, ReloadOutput{}, err
		}
		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		navCtx, navCancel := context.WithTimeout(tctx, defaultLifecycleTimeout)
		defer navCancel()
		if input.BypassCache {
			// chromedp.Reload() doesn't expose the IgnoreCache
			// option, but it wraps page.Reload in responseAction
			// which waits for the page load event. We need the
			// same wait behaviour here, so we use RunResponse with
			// an action that sends the bypass-cache variant.
			if _, err := chromedp.RunResponse(navCtx, chromedp.Func(func(ctx context.Context, tgt *chromedp.Target) error {
				_, err := cdp.Call(ctx, tgt, page.Reload, page.ReloadParams{IgnoreCache: new(true)})
				return err
			})); err != nil {
				return nil, ReloadOutput{}, wrapNavTimeout(err, tctx.Err(), "reload")
			}
		} else {
			if err := chromedp.Do(navCtx, chromedp.Reload()); err != nil {
				return nil, ReloadOutput{}, wrapNavTimeout(err, tctx.Err(), "reload")
			}
		}

		url, err := chromedp.Run(navCtx, chromedp.Location())
		if err != nil {
			return nil, ReloadOutput{}, wrapNavTimeout(err, tctx.Err(), "reload")
		}
		title, err := chromedp.Run(navCtx, chromedp.Title())
		if err != nil {
			return nil, ReloadOutput{}, wrapNavTimeout(err, tctx.Err(), "reload")
		}
		return nil, ReloadOutput{URL: url, Title: title}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "go_back",
		Description: "Navigate back in browser history.",
		Annotations: mutatingAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GoBackInput) (*mcp.CallToolResult, struct{}, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}
		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		navCtx, navCancel := context.WithTimeout(tctx, defaultLifecycleTimeout)
		defer navCancel()
		if err := chromedp.Do(navCtx, navigateHistory(-1)); err != nil {
			return nil, struct{}{}, wrapNavTimeout(err, tctx.Err(), "go_back")
		}
		return nil, struct{}{}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "go_forward",
		Description: "Navigate forward in browser history.",
		Annotations: mutatingAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GoForwardInput) (*mcp.CallToolResult, struct{}, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}
		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		navCtx, navCancel := context.WithTimeout(tctx, defaultLifecycleTimeout)
		defer navCancel()
		if err := chromedp.Do(navCtx, navigateHistory(+1)); err != nil {
			return nil, struct{}{}, wrapNavTimeout(err, tctx.Err(), "go_forward")
		}
		return nil, struct{}{}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "wait_for",
		Description: "Wait for a CSS selector to become visible or a JS expression to become truthy. Exactly one of selector or expression must be provided.",
		Annotations: readOnlyAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input WaitForInput) (*mcp.CallToolResult, struct{}, error) {
		if input.Selector == "" && input.Expression == "" {
			return nil, struct{}{}, fmt.Errorf("exactly one of selector or expression must be provided")
		}
		if input.Selector != "" && input.Expression != "" {
			return nil, struct{}{}, fmt.Errorf("exactly one of selector or expression must be provided, not both")
		}
		if input.Timeout < 0 {
			return nil, struct{}{}, fmt.Errorf("timeout must be >= 0, got %d", input.Timeout)
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

		if input.Selector != "" {
			err = chromedp.Do(wctx, chromedp.WaitVisible(chromedp.CSS(input.Selector)))
			if err != nil {
				return nil, struct{}{}, selectorError(tctx, input.Selector, err)
			}
		} else {
			// Coerce the predicate to a Promise<boolean> before sending
			// to CDP. Without this, expressions that evaluate to a
			// non-primitive (e.g. an optional-chained DOM query whose
			// intermediate returns a complex object) fail with CDP
			// error "Object reference chain is too long" because Poll
			// tries to serialize the result via returnByValue.
			//
			// The wrap is `Promise.resolve(EXPR).then(Boolean)`, not a
			// bare `Boolean(EXPR)`, because chromedp.Poll's predicate
			// JS does `await predicate(...)` — a bare `Boolean(somePromise)`
			// returns true synchronously without awaiting the Promise,
			// silently breaking async predicates like
			// `fetch('/x').then(r => r.ok)`. `Promise.resolve` is
			// idempotent on Promises and lifts plain values, so both
			// sync and async expressions are handled uniformly.
			//
			// EXPR is wrapped between newlines so a trailing line
			// comment in the user's expression doesn't swallow the
			// closing `).then(Boolean)`.
			coerced := "Promise.resolve(\n" + input.Expression + "\n).then(Boolean)"
			_, err = chromedp.Run(wctx, chromedp.Poll[any](coerced, chromedp.WithPollingTimeout(timeout)))
			if err != nil {
				return nil, struct{}{}, err
			}
		}
		return nil, struct{}{}, nil
	})
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
		lctx, lcancel := context.WithCancel(ctx)
		defer lcancel()

		if err := chromedp.Listen(lctx, func(ev any) {
			switch e := ev.(type) {
			case *page.EventNavigatedWithinDocument:
				if topFrameID != "" && e.FrameID != topFrameID {
					return // Ignore subframe same-document navigations
				}
				// SPA pushState / history API — same document,
				// DOM is already correct.
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
				}
				select {
				case ch <- navEvent{result}:
				default:
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

		if nav.result == historyNavBFCache {
			// Reload to force a fresh document load. This triggers
			// dom.EventDocumentUpdated, which resyncs chromedp's
			// DOM cache. Without this, bfcache restores leave the
			// cache stale and all selector-based queries time out.
			_, err := chromedp.Reload()(ctx, tgt)
			return err
		}

		// Same-document and normal cross-document navigations don't
		// need a reload — the DOM is already in a consistent state.
		return nil
	})
}
