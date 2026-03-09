package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/thegrumpylion/chromedp-mcp/internal/browser"
)

// BrowserLaunchInput is the input for browser_launch.
type BrowserLaunchInput struct {
	Headless *bool `json:"headless,omitempty" jsonschema:"Run in headless mode (default true)"`
	Width    int   `json:"width,omitempty" jsonschema:"Initial viewport width in pixels (default 1920)"`
	Height   int   `json:"height,omitempty" jsonschema:"Initial viewport height in pixels (default 1080)"`
}

// BrowserLaunchOutput is the output for browser_launch.
type BrowserLaunchOutput struct {
	BrowserID string `json:"browser_id"`
}

// BrowserConnectInput is the input for browser_connect.
type BrowserConnectInput struct {
	URL string `json:"url" jsonschema:"Chrome remote debugging URL (ws:// or http://)"`
}

// BrowserConnectOutput is the output for browser_connect.
type BrowserConnectOutput struct {
	BrowserID string `json:"browser_id"`
}

// BrowserCloseInput is the input for browser_close.
type BrowserCloseInput struct {
	Browser string `json:"browser,omitempty" jsonschema:"Browser ID. If omitted closes the active browser."`
}

// BrowserCloseOutput is the output for browser_close.
type BrowserCloseOutput struct {
	Closed string `json:"closed"`
}

// BrowserListOutput is the output for browser_list.
type BrowserListOutput struct {
	Browsers []browser.BrowserInfo `json:"browsers"`
}

func registerBrowserTools(s *mcp.Server, mgr *browser.Manager, opts *Options) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "browser_launch",
		Description: "Launch a new Chrome browser instance managed by the server.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: false,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input BrowserLaunchInput) (*mcp.CallToolResult, BrowserLaunchOutput, error) {
		launchOpts := browser.DefaultLaunchOptions()
		if input.Headless != nil {
			launchOpts.Headless = *input.Headless
		}
		if input.Width > 0 {
			launchOpts.Width = input.Width
		}
		if input.Height > 0 {
			launchOpts.Height = input.Height
		}
		launchOpts.DownloadDir = opts.DownloadDir
		b, err := mgr.Launch(launchOpts)
		if err != nil {
			return nil, BrowserLaunchOutput{}, err
		}
		return nil, BrowserLaunchOutput{BrowserID: b.ID}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "browser_connect",
		Description: "Connect to an already-running Chrome instance via its remote debugging URL.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: false,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input BrowserConnectInput) (*mcp.CallToolResult, BrowserConnectOutput, error) {
		b, err := mgr.Connect(input.URL, browser.ConnectOptions{
			DownloadDir: opts.DownloadDir,
		})
		if err != nil {
			return nil, BrowserConnectOutput{}, err
		}
		return nil, BrowserConnectOutput{BrowserID: b.ID}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "browser_close",
		Description: "Close a browser. In launch mode kills Chrome. In connect mode disconnects.",
		Annotations: &mcp.ToolAnnotations{
			DestructiveHint: ptrBool(true),
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input BrowserCloseInput) (*mcp.CallToolResult, BrowserCloseOutput, error) {
		id := input.Browser
		if id == "" {
			b, err := mgr.Active()
			if err != nil {
				return nil, BrowserCloseOutput{}, err
			}
			id = b.ID
		}
		if err := mgr.Close(id); err != nil {
			return nil, BrowserCloseOutput{}, err
		}
		return nil, BrowserCloseOutput{Closed: id}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "browser_list",
		Description: "List all managed browsers with their IDs, modes, and connection status.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:   true,
			IdempotentHint: true,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input struct{}) (*mcp.CallToolResult, BrowserListOutput, error) {
		return nil, BrowserListOutput{Browsers: mgr.List()}, nil
	})
}
