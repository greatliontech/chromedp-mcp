// Package tools registers all MCP tools on the server.
package tools

import (
	"errors"

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
