package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/browser"
	"github.com/greatliontech/chromedp-mcp/internal/profile"
	"github.com/greatliontech/chromedp-mcp/internal/tab"
)

// browserLaunchInput is the input for browser_launch.
type browserLaunchInput struct {
	Headless *bool  `json:"headless,omitempty" jsonschema:"Run in headless mode (default true)"`
	Width    int    `json:"width,omitempty" jsonschema:"Initial viewport width in pixels (default 1920)"`
	Height   int    `json:"height,omitempty" jsonschema:"Initial viewport height in pixels (default 1080)"`
	Profile  string `json:"profile,omitempty" jsonschema:"Chrome profile display name to use (e.g. Work). Must be in the allowed profiles list. Use browser_list_profiles to see available profiles."`
}

// browserLaunchOutput is the output for browser_launch.
type browserLaunchOutput struct {
	BrowserID string        `json:"browser_id"`
	Tabs      []tab.TabInfo `json:"tabs"`
}

// browserConnectInput is the input for browser_connect.
type browserConnectInput struct {
	URL string `json:"url" jsonschema:"Chrome remote debugging URL (ws://, wss://, http://, or https://)"`
}

// browserConnectOutput is the output for browser_connect.
type browserConnectOutput struct {
	BrowserID string        `json:"browser_id"`
	Tabs      []tab.TabInfo `json:"tabs"`
}

// browserActivateInput is the input for browser_activate.
type browserActivateInput struct {
	Browser string `json:"browser" jsonschema:"Browser ID to set as the active browser"`
}

// browserCloseInput is the input for browser_close.
type browserCloseInput struct {
	Browser string `json:"browser,omitempty" jsonschema:"Browser ID. If omitted closes the active browser."`
}

// browserCloseOutput is the output for browser_close.
type browserCloseOutput struct {
	Closed string `json:"closed"`
}

// browserListOutput is the output for browser_list.
type browserListOutput struct {
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

func registerBrowserTools(s *mcp.Server, mgr *browser.Manager, opts resolvedOptions) {
	launchSchema, err := jsonschema.For[browserLaunchInput](nil)
	if err != nil {
		panic(fmt.Sprintf("browser_launch schema: %v", err))
	}
	if len(opts.allowedSet) == 0 {
		delete(launchSchema.Properties, "profile")
	}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "browser_launch",
		Description: "Launch a new Chrome browser instance managed by the server. Automatically attaches Chrome's initial default tab as the active tab so navigate and tab tools work immediately without calling tab_new first.",
		InputSchema: launchSchema,
		Annotations: mutatingAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input browserLaunchInput) (*mcp.CallToolResult, browserLaunchOutput, error) {
		if input.Width < 0 || input.Height < 0 {
			return nil, browserLaunchOutput{}, fmt.Errorf("width and height must be >= 0, got %dx%d", input.Width, input.Height)
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
		launchOpts.DownloadDir = opts.downloadDir

		if input.Profile != "" {
			if !opts.allowedSet[input.Profile] {
				return nil, browserLaunchOutput{}, fmt.Errorf("profile %q is not in the allowed profiles list", input.Profile)
			}
			all, err := profile.Discover(opts.userDataDir)
			if err != nil {
				return nil, browserLaunchOutput{}, fmt.Errorf("discover profiles: %w", err)
			}
			p, ok := profile.ResolveByName(all, input.Profile)
			if !ok {
				return nil, browserLaunchOutput{}, fmt.Errorf("profile %q not found in %s", input.Profile, opts.userDataDir)
			}
			launchOpts.UserDataDir = opts.userDataDir
			launchOpts.ProfileDir = p.Dir
		}

		b, err := mgr.Launch(ctx, launchOpts)
		if err != nil {
			return nil, browserLaunchOutput{}, err
		}
		return nil, browserLaunchOutput{BrowserID: b.ID, Tabs: listBrowserTabs(ctx, b)}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "browser_connect",
		Description: "Connect to an already-running Chrome instance via its remote debugging URL. Automatically adopts existing open tabs (leaving them open on disconnect) without creating a blank tab.",
		Annotations: mutatingAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input browserConnectInput) (*mcp.CallToolResult, browserConnectOutput, error) {
		urlStr := strings.TrimSpace(input.URL)
		if urlStr == "" {
			return nil, browserConnectOutput{}, fmt.Errorf("url is required")
		}
		b, err := mgr.Connect(ctx, urlStr, browser.ConnectOptions{
			DownloadDir: opts.downloadDir,
		})
		if err != nil {
			return nil, browserConnectOutput{}, err
		}
		return nil, browserConnectOutput{BrowserID: b.ID, Tabs: listBrowserTabs(ctx, b)}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "browser_activate",
		Description: "Set a managed browser as the active browser by ID.",
		Annotations: idempotentWriteAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input browserActivateInput) (*mcp.CallToolResult, struct{}, error) {
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
	}, func(ctx context.Context, req *mcp.CallToolRequest, input browserCloseInput) (*mcp.CallToolResult, browserCloseOutput, error) {
		id := input.Browser
		if id == "" {
			b, err := mgr.Active()
			if err != nil {
				return nil, browserCloseOutput{}, err
			}
			id = b.ID
		}
		if err := mgr.Close(id); err != nil {
			return nil, browserCloseOutput{}, err
		}
		return nil, browserCloseOutput{Closed: id}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "browser_list",
		Description: "List all live managed browsers with their IDs, modes, active status, and open tab counts (pruning dead browsers).",
		Annotations: readOnlyAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input struct{}) (*mcp.CallToolResult, browserListOutput, error) {
		return nil, browserListOutput{Browsers: mgr.List()}, nil
	})
}
