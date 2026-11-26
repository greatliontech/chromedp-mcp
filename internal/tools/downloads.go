package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/browser"
	"github.com/greatliontech/chromedp-mcp/internal/collector"
)

// getDownloadsInput is the input for get_downloads.
type getDownloadsInput struct {
	Browser string `json:"browser,omitempty" jsonschema:"Browser ID. Defaults to active browser."`
	Mode    string `json:"mode" jsonschema:"Read mode for completed downloads: 'peek' to keep entries in the buffer, 'drain' to consume them. Required. The in-progress list is always returned by snapshot."`
	Limit   int    `json:"limit,omitempty" jsonschema:"Max entries to return (default all)"`
}

// getDownloadsOutput is the output for get_downloads.
type getDownloadsOutput struct {
	Downloads  []collector.DownloadEntry `json:"downloads"`
	InProgress []collector.DownloadEntry `json:"in_progress"`
}

func registerDownloadTools(s *mcp.Server, mgr *browser.Manager) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_downloads",
		Description: "Get tracked file downloads with their status, progress, and file paths. Shows both completed and in-progress downloads. 'mode' must be 'peek' or 'drain'.",
		InputSchema: modeSchemaFor[getDownloadsInput](modePeek, modeDrain),
		Annotations: mutatingAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input getDownloadsInput) (*mcp.CallToolResult, getDownloadsOutput, error) {
		if err := validateMode(input.Mode, modePeek, modeDrain); err != nil {
			return nil, getDownloadsOutput{}, err
		}
		if err := validateLimit(input.Limit); err != nil {
			return nil, getDownloadsOutput{}, err
		}
		b, err := mgr.Resolve(input.Browser)
		if err != nil {
			return nil, getDownloadsOutput{}, err
		}

		var downloads []collector.DownloadEntry
		if input.Mode == modePeek {
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

		return nil, getDownloadsOutput{
			Downloads:  downloads,
			InProgress: inProgress,
		}, nil
	})
}
