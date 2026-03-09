// Package tools registers all MCP tools on the server.
package tools

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/thegrumpylion/chromedp-mcp/internal/browser"
)

var (
	errNoBrowser = errors.New("no browser is active; use browser_launch or browser_connect first")
	errNoTab     = errors.New("no tab is active; use tab_new first")
)

// Register registers all tools on the given MCP server.
func Register(s *mcp.Server, mgr *browser.Manager) {
	registerBrowserTools(s, mgr)
	registerTabTools(s, mgr)
	registerNavigationTools(s, mgr)
	registerVisualTools(s, mgr)
	registerDOMTools(s, mgr)
	registerConsoleTools(s, mgr)
	registerNetworkTools(s, mgr)
	registerJSTools(s, mgr)
	registerInteractionTools(s, mgr)
	registerCookieTools(s, mgr)
	registerPerformanceTools(s, mgr)
}

// ptrBool returns a pointer to a bool value.
func ptrBool(v bool) *bool {
	return &v
}

// TabInput is embedded by tool inputs that operate on a tab.
type TabInput struct {
	Tab string `json:"tab,omitempty" jsonschema:"Tab ID. If omitted uses the active tab."`
}

// selectorTimeout is a safety-net timeout for chromedp selector-based actions.
// It caps the maximum time spent polling for an element to avoid hanging
// indefinitely on missing selectors. The JS pre-check in checkSelector
// should catch most missing-element cases instantly; this timeout is a
// fallback for races where an element disappears between check and action.
const selectorTimeout = 3 * time.Second

// checkSelector verifies that a CSS selector matches at least one element
// in the page. It uses a single JS querySelector call which returns
// immediately, avoiding chromedp's retry/poll loop that would otherwise
// wait until the context deadline on missing elements.
func checkSelector(ctx context.Context, selector string) error {
	var exists bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		fmt.Sprintf(`document.querySelector(%q) !== null`, selector), &exists,
	)); err != nil {
		return fmt.Errorf("selector check failed: %w", err)
	}
	if !exists {
		return fmt.Errorf("element %q not found", selector)
	}
	return nil
}

// withSelectorCheck runs a pre-check for the selector and then executes the
// given function with a timeout-bounded context. This provides both instant
// failure for missing elements and a safety-net timeout for edge cases.
func withSelectorCheck(ctx context.Context, selector string, fn func(ctx context.Context) error) error {
	if err := checkSelector(ctx, selector); err != nil {
		return err
	}
	sctx, cancel := context.WithTimeout(ctx, selectorTimeout)
	defer cancel()
	return fn(sctx)
}
