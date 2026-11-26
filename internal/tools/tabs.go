package tools

import (
	"context"

	"github.com/chromedp/chromedp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/browser"
	"github.com/greatliontech/chromedp-mcp/internal/tab"
)

// tabNewInput is the input for tab_new.
type tabNewInput struct {
	URL     string `json:"url,omitempty" jsonschema:"URL to navigate to in the new tab"`
	Browser string `json:"browser,omitempty" jsonschema:"Browser ID. Defaults to active browser."`
}

// tabNewOutput is the output for tab_new.
type tabNewOutput struct {
	TabID string `json:"tab_id"`
	URL   string `json:"url,omitempty"`
}

// tabListInput is the input for tab_list.
type tabListInput struct {
	Browser string `json:"browser,omitempty" jsonschema:"Browser ID. Defaults to active browser."`
}

// tabListOutput is the output for tab_list.
type tabListOutput struct {
	Tabs []tab.TabInfo `json:"tabs"`
}

// tabActivateInput is the input for tab_activate.
type tabActivateInput struct {
	Tab string `json:"tab" jsonschema:"Tab ID to activate"`
}

// tabCloseInput is the input for tab_close.
type tabCloseInput struct {
	Tab string `json:"tab" jsonschema:"Tab ID to close"`
}

func registerTabTools(s *mcp.Server, mgr *browser.Manager) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "tab_new",
		Description: "Create a new browser tab, optionally navigating to a URL. The new tab becomes the active tab.",
		Annotations: mutatingAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input tabNewInput) (*mcp.CallToolResult, tabNewOutput, error) {
		b, err := mgr.Resolve(input.Browser)
		if err != nil {
			return nil, tabNewOutput{}, err
		}
		t, err := b.Tabs.NewTab(ctx)
		if err != nil {
			return nil, tabNewOutput{}, err
		}
		out := tabNewOutput{TabID: t.ID}
		if input.URL != "" {
			tctx, tcancel := tabContext(ctx, t.Context())
			defer tcancel()
			if err := chromedp.Do(tctx, chromedp.Navigate(input.URL)); err != nil {
				// Roll back the freshly created tab so a failed tab_new does not
				// leak an orphaned blank tab or hijack the active tab.
				_ = b.Tabs.Close(t.ID)
				return nil, tabNewOutput{}, err
			}
			if finalURL, err := t.URL(ctx); err == nil && finalURL != "" {
				out.URL = finalURL
			} else {
				out.URL = input.URL
			}
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "tab_list",
		Description: "List all open tabs with their IDs, URLs, and titles.",
		Annotations: readOnlyAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input tabListInput) (*mcp.CallToolResult, tabListOutput, error) {
		b, err := mgr.Resolve(input.Browser)
		if err != nil {
			return nil, tabListOutput{}, err
		}
		tabs, err := b.Tabs.List(ctx)
		if err != nil {
			return nil, tabListOutput{}, err
		}
		return nil, tabListOutput{Tabs: tabs}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "tab_activate",
		Description: "Set a tab as the active tab by ID and bring it to the front.",
		Annotations: idempotentWriteAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input tabActivateInput) (*mcp.CallToolResult, struct{}, error) {
		if err := mgr.ActivateTab(ctx, input.Tab); err != nil {
			return nil, struct{}{}, err
		}
		return nil, struct{}{}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "tab_close",
		Description: "Close a tab by ID. If the active tab is closed, the most recently used remaining tab becomes active.",
		Annotations: destructiveAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input tabCloseInput) (*mcp.CallToolResult, struct{}, error) {
		if err := mgr.CloseTab(input.Tab); err != nil {
			return nil, struct{}{}, err
		}
		return nil, struct{}{}, nil
	})
}
