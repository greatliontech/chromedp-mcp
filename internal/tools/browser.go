package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/browser"
	"github.com/greatliontech/chromedp-mcp/internal/profile"
	"github.com/greatliontech/chromedp-mcp/internal/tab"
)

// BrowserLaunchInput is the input for browser_launch.
type BrowserLaunchInput struct {
	Headless *bool  `json:"headless,omitempty" jsonschema:"Run in headless mode (default true)"`
	Width    int    `json:"width,omitempty" jsonschema:"Initial viewport width in pixels (default 1920)"`
	Height   int    `json:"height,omitempty" jsonschema:"Initial viewport height in pixels (default 1080)"`
	Profile  string `json:"profile,omitempty" jsonschema:"Chrome profile display name to use (e.g. Work). Must be in the allowed profiles list. Use browser_list_profiles to see available profiles."`
}

// BrowserLaunchOutput is the output for browser_launch.
type BrowserLaunchOutput struct {
	BrowserID string        `json:"browser_id"`
	Tabs      []tab.TabInfo `json:"tabs"`
}

// BrowserConnectInput is the input for browser_connect.
type BrowserConnectInput struct {
	URL string `json:"url" jsonschema:"Chrome remote debugging URL (ws://, wss://, http://, or https://)"`
}

// BrowserConnectOutput is the output for browser_connect.
type BrowserConnectOutput struct {
	BrowserID string        `json:"browser_id"`
	Tabs      []tab.TabInfo `json:"tabs"`
}

// BrowserActivateInput is the input for browser_activate.
type BrowserActivateInput struct {
	Browser string `json:"browser" jsonschema:"Browser ID to set as the active browser"`
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

func listBrowserTabs(ctx context.Context, b *browser.Browser) []tab.TabInfo {
	// Fetch tab metadata under a short detached deadline once the browser is
	// registered so a late caller cancel does not mask open tabs as empty.
	listCtx, listCancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	tabs, err := b.Tabs.List(listCtx)
	listCancel()
	if err != nil || tabs == nil {
		return []tab.TabInfo{}
	}
	return tabs
}

func registerBrowserTools(s *mcp.Server, mgr *browser.Manager, opts *Options) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "browser_launch",
		Description: "Launch a new Chrome browser instance managed by the server. Automatically attaches Chrome's initial default tab as the active tab so navigate and tab tools work immediately without calling tab_new first.",
		Annotations: mutatingAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input BrowserLaunchInput) (*mcp.CallToolResult, BrowserLaunchOutput, error) {
		if input.Width < 0 || input.Height < 0 {
			return nil, BrowserLaunchOutput{}, fmt.Errorf("width and height must be >= 0, got %dx%d", input.Width, input.Height)
		}
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

		if input.Profile != "" {
			if opts.allowedSet == nil || !opts.allowedSet[input.Profile] {
				return nil, BrowserLaunchOutput{}, fmt.Errorf("profile %q is not in the allowed profiles list", input.Profile)
			}
			all, err := profile.Discover(opts.userDataDir)
			if err != nil {
				return nil, BrowserLaunchOutput{}, fmt.Errorf("discover profiles: %w", err)
			}
			p, ok := profile.ResolveByName(all, input.Profile)
			if !ok {
				return nil, BrowserLaunchOutput{}, fmt.Errorf("profile %q not found in %s", input.Profile, opts.userDataDir)
			}
			launchOpts.UserDataDir = opts.userDataDir
			launchOpts.ProfileDir = p.Dir
		}

		b, err := mgr.Launch(ctx, launchOpts)
		if err != nil {
			return nil, BrowserLaunchOutput{}, err
		}
		return nil, BrowserLaunchOutput{BrowserID: b.ID, Tabs: listBrowserTabs(ctx, b)}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "browser_connect",
		Description: "Connect to an already-running Chrome instance via its remote debugging URL. Automatically adopts existing open tabs (leaving them open on disconnect) without creating a blank tab.",
		Annotations: mutatingAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input BrowserConnectInput) (*mcp.CallToolResult, BrowserConnectOutput, error) {
		urlStr := strings.TrimSpace(input.URL)
		if urlStr == "" {
			return nil, BrowserConnectOutput{}, fmt.Errorf("url is required")
		}
		b, err := mgr.Connect(ctx, urlStr, browser.ConnectOptions{
			DownloadDir: opts.DownloadDir,
		})
		if err != nil {
			return nil, BrowserConnectOutput{}, err
		}
		return nil, BrowserConnectOutput{BrowserID: b.ID, Tabs: listBrowserTabs(ctx, b)}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "browser_activate",
		Description: "Set a managed browser as the active browser by ID.",
		Annotations: idempotentWriteAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input BrowserActivateInput) (*mcp.CallToolResult, struct{}, error) {
		if input.Browser == "" {
			return nil, struct{}{}, fmt.Errorf("browser ID is required")
		}
		if err := mgr.Activate(input.Browser); err != nil {
			return nil, struct{}{}, err
		}
		return nil, struct{}{}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "browser_close",
		Description: "Close a browser. In launch mode kills Chrome. In connect mode disconnects.",
		Annotations: destructiveAnnotations(),
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
		Description: "List all live managed browsers with their IDs, modes, active status, and open tab counts (pruning dead browsers).",
		Annotations: readOnlyAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input struct{}) (*mcp.CallToolResult, BrowserListOutput, error) {
		return nil, BrowserListOutput{Browsers: mgr.List()}, nil
	})
}
