package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/thegrumpylion/chromedp-mcp/internal/browser"
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

func registerNavigationTools(s *mcp.Server, mgr *browser.Manager) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "navigate",
		Description: "Navigate a tab to a URL. Returns the final URL (after redirects), page title, and HTTP status code.",
		Annotations: &mcp.ToolAnnotations{},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input NavigateInput) (*mcp.CallToolResult, NavigateOutput, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, NavigateOutput{}, err
		}

		tctx := t.Context()
		waitEvent := "load"
		if input.WaitUntil != "" {
			waitEvent = input.WaitUntil
		}

		// For non-default wait events, set up the lifecycle listener
		// BEFORE navigating so we don't miss the event (e.g.,
		// DOMContentLoaded fires before load, and RunResponse waits
		// for load — by the time RunResponse returns, DOMContentLoaded
		// has already been dispatched).
		var lifecycleCh chan struct{}
		if waitEvent == "domcontentloaded" || waitEvent == "networkidle" {
			cdpName := waitEvent
			switch waitEvent {
			case "domcontentloaded":
				cdpName = "DOMContentLoaded"
			case "networkidle":
				cdpName = "networkIdle"
			}
			lifecycleCh = make(chan struct{}, 1)
			lctx, lcancel := context.WithCancel(tctx)
			defer lcancel()
			chromedp.ListenTarget(lctx, func(ev interface{}) {
				if le, ok := ev.(*page.EventLifecycleEvent); ok {
					if le.Name == cdpName {
						select {
						case lifecycleCh <- struct{}{}:
						default:
						}
					}
				}
			})
		}

		// Navigate and capture the HTTP response to get the status code.
		resp, err := chromedp.RunResponse(tctx, chromedp.Navigate(input.URL))
		if err != nil {
			return nil, NavigateOutput{}, err
		}

		// Wait for the lifecycle event if needed.
		if lifecycleCh != nil {
			select {
			case <-lifecycleCh:
				// Event received — proceed.
			case <-tctx.Done():
				return nil, NavigateOutput{}, tctx.Err()
			}
		}

		var url, title string
		if err := chromedp.Run(tctx, chromedp.Location(&url), chromedp.Title(&title)); err != nil {
			return nil, NavigateOutput{}, err
		}

		out := NavigateOutput{URL: url, Title: title}
		if resp != nil {
			out.Status = resp.Status
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "reload",
		Description: "Reload the current page.",
		Annotations: &mcp.ToolAnnotations{
			IdempotentHint: true,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ReloadInput) (*mcp.CallToolResult, struct{}, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}
		if input.BypassCache {
			// Use the CDP command directly so we can pass IgnoreCache.
			// chromedp.Reload() doesn't expose this option.
			if err := chromedp.Run(t.Context(), chromedp.ActionFunc(func(ctx context.Context) error {
				return page.Reload().WithIgnoreCache(true).Do(ctx)
			})); err != nil {
				return nil, struct{}{}, err
			}
		} else {
			if err := chromedp.Run(t.Context(), chromedp.Reload()); err != nil {
				return nil, struct{}{}, err
			}
		}
		return nil, struct{}{}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "go_back",
		Description: "Navigate back in browser history.",
		Annotations: &mcp.ToolAnnotations{},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GoBackInput) (*mcp.CallToolResult, struct{}, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}
		if err := chromedp.Run(t.Context(), navigateHistory(-1)); err != nil {
			return nil, struct{}{}, err
		}
		return nil, struct{}{}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "go_forward",
		Description: "Navigate forward in browser history.",
		Annotations: &mcp.ToolAnnotations{},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GoForwardInput) (*mcp.CallToolResult, struct{}, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}
		if err := chromedp.Run(t.Context(), navigateHistory(+1)); err != nil {
			return nil, struct{}{}, err
		}
		return nil, struct{}{}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "wait_for",
		Description: "Wait for a CSS selector to become visible or a JS expression to become truthy. Exactly one of selector or expression must be provided.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:   true,
			IdempotentHint: true,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input WaitForInput) (*mcp.CallToolResult, struct{}, error) {
		if input.Selector == "" && input.Expression == "" {
			return nil, struct{}{}, fmt.Errorf("exactly one of selector or expression must be provided")
		}
		if input.Selector != "" && input.Expression != "" {
			return nil, struct{}{}, fmt.Errorf("exactly one of selector or expression must be provided, not both")
		}

		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}

		timeout := 30 * time.Second
		if input.Timeout > 0 {
			timeout = time.Duration(input.Timeout) * time.Millisecond
		}

		tctx, cancel := context.WithTimeout(t.Context(), timeout)
		defer cancel()

		if input.Selector != "" {
			err = chromedp.Run(tctx, chromedp.WaitVisible(input.Selector, chromedp.ByQuery))
		} else {
			var result interface{}
			err = chromedp.Run(tctx, chromedp.Poll(input.Expression, &result))
		}
		if err != nil {
			return nil, struct{}{}, err
		}
		return nil, struct{}{}, nil
	})
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
func navigateHistory(delta int) chromedp.ActionFunc {
	return func(ctx context.Context) error {
		cur, entries, err := page.GetNavigationHistory().Do(ctx)
		if err != nil {
			return err
		}
		target := cur + int64(delta)
		if target < 0 || target > int64(len(entries)-1) {
			if delta < 0 {
				return fmt.Errorf("no previous history entry")
			}
			return fmt.Errorf("no forward history entry")
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

		chromedp.ListenTarget(lctx, func(ev interface{}) {
			switch e := ev.(type) {
			case *page.EventNavigatedWithinDocument:
				// SPA pushState / history API — same document,
				// DOM is already correct.
				select {
				case ch <- navEvent{historyNavSameDocument}:
				default:
				}
			case *page.EventFrameNavigated:
				if e.Frame.ParentID != "" {
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
		})

		if err := page.NavigateToHistoryEntry(entries[target].ID).Do(ctx); err != nil {
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
			return chromedp.Reload().Do(ctx)
		}

		// Same-document and normal cross-document navigations don't
		// need a reload — the DOM is already in a consistent state.
		return nil
	}
}
