package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/browser"
	"github.com/greatliontech/chromedp-mcp/internal/collector"
)

// getConsoleLogsInput is the input for get_console_logs.
type getConsoleLogsInput struct {
	tabInput
	Level string `json:"level,omitempty" jsonschema:"Filter by console level (e.g. log, warn, error, info, debug, assert, trace; CDP warning is normalized to warn). If omitted returns all."`
	Mode  string `json:"mode" jsonschema:"Read mode: 'peek' to keep entries in the buffer, 'drain' to consume them. Required."`
	Limit int    `json:"limit,omitempty" jsonschema:"Max entries to return (default all)"`
}

// getConsoleLogsOutput is the output for get_console_logs.
type getConsoleLogsOutput struct {
	Logs []collector.ConsoleEntry `json:"logs"`
}

// getJSErrorsInput is the input for get_js_errors.
type getJSErrorsInput struct {
	tabInput
	Mode  string `json:"mode" jsonschema:"Read mode: 'peek' to keep entries in the buffer, 'drain' to consume them. Required."`
	Limit int    `json:"limit,omitempty" jsonschema:"Max entries to return (default all)"`
}

// getJSErrorsOutput is the output for get_js_errors.
type getJSErrorsOutput struct {
	Errors []collector.JSErrorEntry `json:"errors"`
}

func registerConsoleTools(s *mcp.Server, mgr *browser.Manager) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_console_logs",
		Description: "Get captured console messages (e.g. log, warn, error, info, debug, assert, trace). 'mode' must be 'peek' (keep entries) or 'drain' (consume entries that match level).",
		InputSchema: modeSchemaFor[getConsoleLogsInput](modePeek, modeDrain),
		Annotations: mutatingAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input getConsoleLogsInput) (*mcp.CallToolResult, getConsoleLogsOutput, error) {
		if err := validateMode(input.Mode, modePeek, modeDrain); err != nil {
			return nil, getConsoleLogsOutput{}, err
		}
		if err := validateLimit(input.Limit); err != nil {
			return nil, getConsoleLogsOutput{}, err
		}
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, getConsoleLogsOutput{}, err
		}

		var logs []collector.ConsoleEntry
		if input.Mode == modePeek {
			logs = t.Console.Peek(input.Level, input.Limit)
		} else {
			logs = t.Console.Drain(input.Level, input.Limit)
		}
		if logs == nil {
			logs = []collector.ConsoleEntry{}
		}
		return nil, getConsoleLogsOutput{Logs: logs}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_js_errors",
		Description: "Get captured JavaScript exceptions and promise rejections. 'mode' must be 'peek' (keep entries) or 'drain' (consume entries).",
		InputSchema: modeSchemaFor[getJSErrorsInput](modePeek, modeDrain),
		Annotations: mutatingAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input getJSErrorsInput) (*mcp.CallToolResult, getJSErrorsOutput, error) {
		if err := validateMode(input.Mode, modePeek, modeDrain); err != nil {
			return nil, getJSErrorsOutput{}, err
		}
		if err := validateLimit(input.Limit); err != nil {
			return nil, getJSErrorsOutput{}, err
		}
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, getJSErrorsOutput{}, err
		}

		var errors []collector.JSErrorEntry
		if input.Mode == modePeek {
			errors = t.JSErrors.Peek(input.Limit)
		} else {
			errors = t.JSErrors.Drain(input.Limit)
		}
		if errors == nil {
			errors = []collector.JSErrorEntry{}
		}
		return nil, getJSErrorsOutput{Errors: errors}, nil
	})
}
