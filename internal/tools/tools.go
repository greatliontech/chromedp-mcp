// Package tools registers all MCP tools on the server.
package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/thegrumpylion/chromedp-mcp/internal/browser"
)

// Options configures optional behavior for the MCP tools.
type Options struct {
	// DownloadDir is the directory for saving screenshots, PDFs, and
	// downloads. When empty, save-to-disk is unavailable and the tools
	// return binary data inline only.
	DownloadDir string
}

// Register registers all tools on the given MCP server.
func Register(s *mcp.Server, mgr *browser.Manager, opts *Options) {
	if opts == nil {
		opts = &Options{}
	}
	registerBrowserTools(s, mgr, opts)
	registerTabTools(s, mgr)
	registerNavigationTools(s, mgr)
	registerVisualTools(s, mgr, opts)
	registerDOMTools(s, mgr)
	registerConsoleTools(s, mgr)
	registerNetworkTools(s, mgr)
	registerJSTools(s, mgr)
	registerInteractionTools(s, mgr)
	registerCookieTools(s, mgr)
	registerPerformanceTools(s, mgr)
	registerDownloadTools(s, mgr)
	registerConfigTools(s, mgr)
	registerEmulationTools(s, mgr)
}

// ptrBool returns a pointer to a bool value.
func ptrBool(v bool) *bool {
	return &v
}

// TabInput is embedded by tool inputs that operate on a tab but do not
// involve CSS selector polling.
type TabInput struct {
	Tab string `json:"tab,omitempty" jsonschema:"Tab ID. If omitted uses the active tab."`
}

// SelectorInput is embedded by tool inputs that involve CSS selector polling
// with a configurable timeout. It extends TabInput with a Timeout field.
type SelectorInput struct {
	TabInput
	Timeout int `json:"timeout,omitempty" jsonschema:"Max time in milliseconds to wait for selectors (default 5000). Set lower for elements known to be present."`
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

// selectorError wraps a selector timeout error with a more descriptive
// message. When the error is a context deadline exceeded, it checks the
// DOM to distinguish "element not found" from "element exists but not
// visible". parentCtx must be the tab's context (not the timed-out one).
func selectorError(parentCtx context.Context, selector string, err error) error {
	if err == nil {
		return nil
	}
	if ctx_err := context.Cause(parentCtx); ctx_err != nil {
		// Parent context is dead (browser killed, etc.) — return as-is.
		return err
	}
	if err != context.DeadlineExceeded && err.Error() != "context deadline exceeded" {
		return err
	}
	// The selector timed out. Check if the element exists in the DOM.
	var exists bool
	checkCtx, cancel := context.WithTimeout(parentCtx, 500*time.Millisecond)
	defer cancel()
	js := fmt.Sprintf("document.querySelector(%q) !== null", selector)
	if evalErr := chromedp.Run(checkCtx, chromedp.Evaluate(js, &exists)); evalErr != nil {
		return err // Can't check, return original error.
	}
	if exists {
		return fmt.Errorf("element %q exists but is not visible (timed out waiting for it to become visible)", selector)
	}
	return fmt.Errorf("element %q not found in the DOM (timed out waiting for it to appear)", selector)
}
