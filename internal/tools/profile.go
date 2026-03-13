package tools

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/browser"
	"github.com/greatliontech/chromedp-mcp/internal/profile"
)

// ProfileInfo is a single profile entry returned by browser_list_profiles.
type ProfileInfo struct {
	Name string `json:"name"`
	Dir  string `json:"dir"`
}

// BrowserListProfilesOutput is the output for browser_list_profiles.
type BrowserListProfilesOutput struct {
	Profiles []ProfileInfo `json:"profiles"`
}

func registerProfileTools(s *mcp.Server, mgr *browser.Manager, opts *Options) {
	if len(opts.AllowedProfiles) == 0 {
		return
	}

	// Auto-detect the user data dir once at registration time.
	userDataDir := profile.DefaultUserDataDir()

	// Build the allowed set for fast lookup.
	allowed := make(map[string]bool, len(opts.AllowedProfiles))
	for _, name := range opts.AllowedProfiles {
		allowed[name] = true
	}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "browser_list_profiles",
		Description: "List available Chrome/Chromium user profiles that can be used with browser_launch.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:   true,
			IdempotentHint: true,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input struct{}) (*mcp.CallToolResult, BrowserListProfilesOutput, error) {
		if userDataDir == "" {
			return nil, BrowserListProfilesOutput{}, fmt.Errorf("no Chrome/Chromium user data directory found")
		}
		all, err := profile.Discover(userDataDir)
		if err != nil {
			return nil, BrowserListProfilesOutput{}, err
		}
		var infos []ProfileInfo
		for _, p := range all {
			if allowed[p.Name] {
				infos = append(infos, ProfileInfo{
					Name: p.Name,
					Dir:  p.Dir,
				})
			}
		}
		return nil, BrowserListProfilesOutput{Profiles: infos}, nil
	})

	// Store resolved profile config on opts so browser_launch can use it.
	opts.userDataDir = userDataDir
	opts.allowedSet = allowed
}
