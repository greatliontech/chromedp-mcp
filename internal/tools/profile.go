package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/profile"
)

// profileInfo is a single profile entry returned by browser_list_profiles.
type profileInfo struct {
	Name string `json:"name"`
	Dir  string `json:"dir"`
}

// browserListProfilesOutput is the output for browser_list_profiles.
type browserListProfilesOutput struct {
	Profiles []profileInfo `json:"profiles"`
}

func registerProfileTools(s *mcp.Server, opts resolvedOptions) {
	if len(opts.allowedSet) == 0 {
		return
	}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "browser_list_profiles",
		Description: "List available Chrome/Chromium user profiles that can be used with browser_launch.",
		Annotations: readOnlyAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input struct{}) (*mcp.CallToolResult, browserListProfilesOutput, error) {
		all, err := profile.Discover(opts.userDataDir)
		if err != nil {
			return nil, browserListProfilesOutput{}, err
		}
		infos := make([]profileInfo, 0, len(all))
		for _, p := range all {
			if opts.allowedSet[p.Name] {
				infos = append(infos, profileInfo{
					Name: p.Name,
					Dir:  p.Dir,
				})
			}
		}
		return nil, browserListProfilesOutput{Profiles: infos}, nil
	})
}
