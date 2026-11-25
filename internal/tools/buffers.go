package tools

import (
	"context"
	"fmt"
	"slices"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/browser"
	"github.com/greatliontech/chromedp-mcp/internal/tab"
)

// ClearBuffersInput is the input for clear_buffers.
type ClearBuffersInput struct {
	TabInput
	Browser string   `json:"browser,omitempty" jsonschema:"Browser ID. Defaults to the active browser, or to the specified tab's owning browser when tab is provided without browser. When provided without tab, tab-scoped targets clear that browser's active tab."`
	Targets []string `json:"targets,omitempty" jsonschema:"Buffers to clear: console, errors, network, websocket, performance, url_changes, downloads, or all. Omit or pass ['all'] to clear all buffers; an explicit empty array [] is rejected."`
}

// ClearBuffersOutput is the output for clear_buffers.
type ClearBuffersOutput struct {
	Cleared []string `json:"cleared"`
}

var (
	clearBufferOrder        = []string{"console", "errors", "network", "websocket", "performance", "url_changes", "downloads"}
	clearBufferTargetValues = append(slices.Clone(clearBufferOrder), "all")
)

func registerBufferTools(s *mcp.Server, mgr *browser.Manager) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "clear_buffers",
		Description: "Clear one or more event collector buffers (console, errors, network, websocket, performance, url_changes, downloads, or all). Omit targets or pass ['all'] to clear all buffers; explicit [] is rejected. Clears completed/historical entries while preserving live state (open WebSockets stay tracked with cleared frame rings and counts, in-progress downloads/requests keep tracking, URL-change baseline is preserved, and open modal dialogs are untouched). Clearing only ['downloads'] without tab does not require an open tab.",
		InputSchema: schemaWithEnums[ClearBuffersInput](map[string][]string{
			"targets": clearBufferTargetValues,
		}),
		Annotations: idempotentWriteAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ClearBuffersInput) (*mcp.CallToolResult, ClearBuffersOutput, error) {
		if input.Targets != nil && len(input.Targets) == 0 {
			return nil, ClearBuffersOutput{}, fmt.Errorf("targets must not be empty; omit targets or pass [\"all\"] to clear all buffers")
		}
		want := make(map[string]bool, len(clearBufferOrder))
		selectAll := input.Targets == nil
		for _, tgt := range input.Targets {
			if err := validateEnum("buffer target", tgt, false, clearBufferTargetValues); err != nil {
				return nil, ClearBuffersOutput{}, err
			}
			if tgt == "all" {
				selectAll = true
			} else {
				want[tgt] = true
			}
		}
		if selectAll {
			for _, name := range clearBufferOrder {
				want[name] = true
			}
		}

		// Clearing only browser-scoped downloads does not require an open tab
		// unless the caller explicitly provided a tab ID to validate.
		needsTab := input.Tab != ""
		if !needsTab {
			for name := range want {
				if name != "downloads" {
					needsTab = true
					break
				}
			}
		}

		var (
			b   *browser.Browser
			t   *tab.Tab
			err error
		)
		if needsTab {
			b, t, err = mgr.ResolveTabWithBrowser(input.Browser, input.Tab)
		} else {
			b, err = mgr.Resolve(input.Browser)
		}
		if err != nil {
			return nil, ClearBuffersOutput{}, err
		}

		cleared := make([]string, 0, len(want))
		for _, name := range clearBufferOrder {
			if !want[name] {
				continue
			}
			switch name {
			case "console":
				t.Console.Clear()
			case "errors":
				t.JSErrors.Clear()
			case "network":
				t.Network.Clear()
			case "websocket":
				t.WebSocket.Clear()
			case "performance":
				t.Performance.Clear()
			case "url_changes":
				t.URLChanges.Clear()
			case "downloads":
				b.Downloads.Clear()
			}
			cleared = append(cleared, name)
		}
		return nil, ClearBuffersOutput{Cleared: cleared}, nil
	})
}
