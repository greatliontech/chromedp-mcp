package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/browser"
	"github.com/greatliontech/chromedp-mcp/internal/collector"
)

// GetDownloadsInput is the input for get_downloads.
type GetDownloadsInput struct {
	Browser string `json:"browser,omitempty" jsonschema:"Browser ID. Defaults to active browser."`
	Mode    string `json:"mode" jsonschema:"Read mode for completed downloads: 'peek' to keep entries in the buffer, 'drain' to consume them. Required. The in-progress list is always returned by snapshot."`
	Limit   int    `json:"limit,omitempty" jsonschema:"Max entries to return (default all)"`
}

// GetDownloadsOutput is the output for get_downloads.
type GetDownloadsOutput struct {
	Downloads  []collector.DownloadEntry `json:"downloads"`
	InProgress []collector.DownloadEntry `json:"in_progress"`
}

func registerDownloadTools(s *mcp.Server, mgr *browser.Manager) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_downloads",
		Description: "Get tracked file downloads with their status, progress, and file paths. Shows both completed and in-progress downloads. Requires --download-dir to be configured. 'mode' must be 'peek' or 'drain'.",
		InputSchema: modeSchemaFor[GetDownloadsInput](ModePeek, ModeDrain),
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: true,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GetDownloadsInput) (*mcp.CallToolResult, GetDownloadsOutput, error) {
		if err := validateMode(input.Mode, ModePeek, ModeDrain); err != nil {
			return nil, GetDownloadsOutput{}, err
		}
		var b *browser.Browser
		var err error
		if input.Browser != "" {
			b, err = mgr.Get(input.Browser)
		} else {
			b, err = mgr.Active()
		}
		if err != nil {
			return nil, GetDownloadsOutput{}, err
		}

		var downloads []collector.DownloadEntry
		if input.Mode == ModePeek {
			downloads = b.Downloads.Peek(input.Limit)
		} else {
			downloads = b.Downloads.Drain(input.Limit)
		}
		if downloads == nil {
			downloads = []collector.DownloadEntry{}
		}

		inProgress := b.Downloads.InProgress()
		if inProgress == nil {
			inProgress = []collector.DownloadEntry{}
		}

		return nil, GetDownloadsOutput{
			Downloads:  downloads,
			InProgress: inProgress,
		}, nil
	})
}
