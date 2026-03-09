package tools

import (
	"context"

	"github.com/chromedp/chromedp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/thegrumpylion/chromedp-mcp/internal/browser"
	"github.com/thegrumpylion/chromedp-mcp/internal/tab"
)

// TabNewInput is the input for tab_new.
type TabNewInput struct {
	URL     string `json:"url,omitempty" jsonschema:"URL to navigate to in the new tab"`
	Browser string `json:"browser,omitempty" jsonschema:"Browser ID. Defaults to active browser."`
}

// TabNewOutput is the output for tab_new.
type TabNewOutput struct {
	TabID string `json:"tab_id"`
	URL   string `json:"url,omitempty"`
}

// TabListInput is the input for tab_list.
type TabListInput struct {
	Browser string `json:"browser,omitempty" jsonschema:"Browser ID. Defaults to active browser."`
}

// TabListOutput is the output for tab_list.
type TabListOutput struct {
	Tabs []tab.TabInfo `json:"tabs"`
}

// TabActivateInput is the input for tab_activate.
type TabActivateInput struct {
	Tab string `json:"tab" jsonschema:"Tab ID to activate"`
}

// TabCloseInput is the input for tab_close.
type TabCloseInput struct {
	Tab string `json:"tab" jsonschema:"Tab ID to close"`
}

func registerTabTools(s *mcp.Server, mgr *browser.Manager) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "tab_new",
		Description: "Create a new browser tab, optionally navigating to a URL. The new tab becomes the active tab.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input TabNewInput) (*mcp.CallToolResult, TabNewOutput, error) {
		b, err := resolveBrowser(mgr, input.Browser)
		if err != nil {
			return nil, TabNewOutput{}, err
		}
		t, err := b.Tabs.NewTab()
		if err != nil {
			return nil, TabNewOutput{}, err
		}
		out := TabNewOutput{TabID: t.ID}
		if input.URL != "" {
			if err := chromedp.Run(t.Context(), chromedp.Navigate(input.URL)); err != nil {
				return nil, TabNewOutput{}, err
			}
			out.URL = input.URL
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "tab_list",
		Description: "List all open tabs with their IDs, URLs, and titles.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:   true,
			IdempotentHint: true,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input TabListInput) (*mcp.CallToolResult, TabListOutput, error) {
		b, err := resolveBrowser(mgr, input.Browser)
		if err != nil {
			return nil, TabListOutput{}, err
		}
		return nil, TabListOutput{Tabs: b.Tabs.List()}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "tab_activate",
		Description: "Set a tab as the active tab by ID.",
		Annotations: &mcp.ToolAnnotations{
			IdempotentHint: true,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input TabActivateInput) (*mcp.CallToolResult, struct{}, error) {
		// We need to find which browser owns this tab. Try active browser first.
		b := mgr.Active()
		if b == nil {
			return nil, struct{}{}, errNoBrowser
		}
		if err := b.Tabs.Activate(input.Tab); err != nil {
			return nil, struct{}{}, err
		}
		return nil, struct{}{}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "tab_close",
		Description: "Close a tab by ID. If the active tab is closed, the most recently used remaining tab becomes active.",
		Annotations: &mcp.ToolAnnotations{
			DestructiveHint: ptrBool(true),
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input TabCloseInput) (*mcp.CallToolResult, struct{}, error) {
		b := mgr.Active()
		if b == nil {
			return nil, struct{}{}, errNoBrowser
		}
		if err := b.Tabs.Close(input.Tab); err != nil {
			return nil, struct{}{}, err
		}
		return nil, struct{}{}, nil
	})
}

// resolveBrowser returns the browser for the given ID, or the active browser
// (auto-launching if needed) when id is empty.
func resolveBrowser(mgr *browser.Manager, id string) (*browser.Browser, error) {
	if id != "" {
		return mgr.Get(id)
	}
	return mgr.EnsureBrowser()
}
