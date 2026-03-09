// Package tools registers all MCP tools on the server.
package tools

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/thegrumpylion/chromedp-mcp/internal/browser"
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
	Tab     string `json:"tab,omitempty" jsonschema:"Tab ID. If omitted uses the active tab."`
	Timeout int    `json:"timeout,omitempty" jsonschema:"Max time in milliseconds to wait for selectors (default 5000). Set lower for elements known to be present."`
}

// defaultSelectorTimeout is the default timeout for selector-based chromedp
// actions. chromedp polls with a 5ms retry loop until the element appears or
// the context times out. This default gives dynamic elements a reasonable
// window to appear while keeping failures bounded.
const defaultSelectorTimeout = 5 * time.Second

// selectorContext returns a context bounded by the user-specified timeout
// (in milliseconds) or the default selector timeout.
func selectorContext(ctx context.Context, timeoutMs int) (context.Context, context.CancelFunc) {
	d := defaultSelectorTimeout
	if timeoutMs > 0 {
		d = time.Duration(timeoutMs) * time.Millisecond
	}
	return context.WithTimeout(ctx, d)
}
