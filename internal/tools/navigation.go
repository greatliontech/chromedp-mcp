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

		// Navigate and wait for the specified lifecycle event.
		var actions []chromedp.Action
		actions = append(actions, chromedp.Navigate(input.URL))

		if waitEvent == "networkidle" || waitEvent == "domcontentloaded" {
			// Wait for the lifecycle event after navigation.
			actions = append(actions, chromedp.ActionFunc(func(ctx context.Context) error {
				return waitForLifecycle(ctx, waitEvent)
			}))
		}

		if err := chromedp.Run(tctx, actions...); err != nil {
			return nil, NavigateOutput{}, err
		}

		var url, title string
		if err := chromedp.Run(tctx, chromedp.Location(&url), chromedp.Title(&title)); err != nil {
			return nil, NavigateOutput{}, err
		}

		return nil, NavigateOutput{URL: url, Title: title}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "reload",
		Description: "Reload the current page.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ReloadInput) (*mcp.CallToolResult, struct{}, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}
		if err := chromedp.Run(t.Context(), chromedp.Reload()); err != nil {
			return nil, struct{}{}, err
		}
		return nil, struct{}{}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "go_back",
		Description: "Navigate back in browser history.",
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

// navigateHistory navigates the browser history by the given delta (-1 for
// back, +1 for forward). It uses page.NavigateToHistoryEntry and waits for
// the EventFrameNavigated event to confirm the navigation completed, which
// works reliably even when Chrome restores pages from bfcache.
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

		// Listen for the frame navigation event before triggering the
		// history entry change so we don't miss it.
		ch := make(chan struct{}, 1)
		lctx, lcancel := context.WithCancel(ctx)
		defer lcancel()
		chromedp.ListenTarget(lctx, func(ev interface{}) {
			if fn, ok := ev.(*page.EventFrameNavigated); ok {
				// Only match the root frame (no parent).
				if fn.Frame.ParentID == "" {
					select {
					case ch <- struct{}{}:
					default:
					}
				}
			}
		})

		if err := page.NavigateToHistoryEntry(entries[target].ID).Do(ctx); err != nil {
			return err
		}

		select {
		case <-ch:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// waitForLifecycle waits for a specific page lifecycle event.
func waitForLifecycle(ctx context.Context, name string) error {
	cdpName := name
	if name == "networkidle" {
		cdpName = "networkIdle"
	}

	ch := make(chan struct{}, 1)
	lctx, lcancel := context.WithCancel(ctx)
	defer lcancel()

	chromedp.ListenTarget(lctx, func(ev interface{}) {
		if le, ok := ev.(*page.EventLifecycleEvent); ok {
			if le.Name == cdpName {
				select {
				case ch <- struct{}{}:
				default:
				}
			}
		}
	})

	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
