package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/browser"
	"github.com/greatliontech/chromedp-mcp/internal/collector"
)

// GetConsoleLogsInput is the input for get_console_logs.
type GetConsoleLogsInput struct {
	TabInput
	Level string `json:"level,omitempty" jsonschema:"Filter by level: log warn error info debug. If omitted returns all."`
	Mode  string `json:"mode" jsonschema:"Read mode: 'peek' to keep entries in the buffer, 'drain' to consume them. Required."`
	Limit int    `json:"limit,omitempty" jsonschema:"Max entries to return (default all)"`
}

// GetConsoleLogsOutput is the output for get_console_logs.
type GetConsoleLogsOutput struct {
	Logs []collector.ConsoleEntry `json:"logs"`
}

// GetJSErrorsInput is the input for get_js_errors.
type GetJSErrorsInput struct {
	TabInput
	Mode  string `json:"mode" jsonschema:"Read mode: 'peek' to keep entries in the buffer, 'drain' to consume them. Required."`
	Limit int    `json:"limit,omitempty" jsonschema:"Max entries to return (default all)"`
}

// GetJSErrorsOutput is the output for get_js_errors.
type GetJSErrorsOutput struct {
	Errors []collector.JSErrorEntry `json:"errors"`
}

// ClearConsoleInput is the input for clear_console.
type ClearConsoleInput struct {
	TabInput
}

func registerConsoleTools(s *mcp.Server, mgr *browser.Manager) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_console_logs",
		Description: "Get captured console messages (log, warn, error, info, debug). 'mode' must be 'peek' (keep entries) or 'drain' (consume entries that match level).",
		InputSchema: modeSchemaFor[GetConsoleLogsInput](ModePeek, ModeDrain),
		Annotations: &mcp.ToolAnnotations{},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GetConsoleLogsInput) (*mcp.CallToolResult, GetConsoleLogsOutput, error) {
		if err := validateMode(input.Mode, ModePeek, ModeDrain); err != nil {
			return nil, GetConsoleLogsOutput{}, err
		}
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, GetConsoleLogsOutput{}, err
		}

		var logs []collector.ConsoleEntry
		if input.Mode == ModePeek {
			logs = t.Console.Peek(input.Level, input.Limit)
		} else {
			logs = t.Console.Drain(input.Level, input.Limit)
		}
		if logs == nil {
			logs = []collector.ConsoleEntry{}
		}
		return nil, GetConsoleLogsOutput{Logs: logs}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_js_errors",
		Description: "Get captured JavaScript exceptions and promise rejections. 'mode' must be 'peek' (keep entries) or 'drain' (consume entries).",
		InputSchema: modeSchemaFor[GetJSErrorsInput](ModePeek, ModeDrain),
		Annotations: &mcp.ToolAnnotations{},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GetJSErrorsInput) (*mcp.CallToolResult, GetJSErrorsOutput, error) {
		if err := validateMode(input.Mode, ModePeek, ModeDrain); err != nil {
			return nil, GetJSErrorsOutput{}, err
		}
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, GetJSErrorsOutput{}, err
		}

		var errors []collector.JSErrorEntry
		if input.Mode == ModePeek {
			errors = t.JSErrors.Peek(input.Limit)
		} else {
			errors = t.JSErrors.Drain(input.Limit)
		}
		if errors == nil {
			errors = []collector.JSErrorEntry{}
		}
		return nil, GetJSErrorsOutput{Errors: errors}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "clear_console",
		Description: "Clear the console and JS error buffers for a tab.",
		Annotations: &mcp.ToolAnnotations{
			IdempotentHint: true,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ClearConsoleInput) (*mcp.CallToolResult, struct{}, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}
		t.Console.Clear()
		t.JSErrors.Clear()
		return nil, struct{}{}, nil
	})
}
