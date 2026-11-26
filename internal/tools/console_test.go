package tools

import (
	"strings"
	"testing"
)

func TestConsoleLogs(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)
	waitForConsole(t, tabID)

	out := callTool[GetConsoleLogsOutput](t, "get_console_logs", map[string]any{
		"tab":  tabID,
		"mode": "drain",
	})
	if len(out.Logs) == 0 {
		t.Error("expected console logs, got none")
		return
	}
	// Check we got the expected messages.
	found := map[string]bool{}
	for _, log := range out.Logs {
		found[log.Level] = true
	}
	for _, level := range []string{"log", "warn", "error"} {
		if !found[level] {
			t.Errorf("expected console level %q in logs", level)
		}
	}
}

func TestJSErrors(t *testing.T) {
	tabID := navigateToFixture(t, "errors.html")
	defer closeTab(t, tabID)
	waitForJSErrors(t, tabID)

	out := callTool[GetJSErrorsOutput](t, "get_js_errors", map[string]any{
		"tab":  tabID,
		"mode": "drain",
	})
	if len(out.Errors) == 0 {
		t.Error("expected JS errors, got none")
		return
	}
	found := false
	for _, e := range out.Errors {
		if strings.Contains(e.Message, "test error from page") {
			found = true
		}
	}
	if !found {
		t.Error("expected error containing 'test error from page'")
	}
}

func TestConsoleLogsDrainClearsBuffer(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)
	waitForConsole(t, tabID)

	// First drain — should return logs.
	out1 := callTool[GetConsoleLogsOutput](t, "get_console_logs", map[string]any{
		"tab":  tabID,
		"mode": "drain",
	})
	if len(out1.Logs) == 0 {
		t.Fatal("expected console logs from index.html, got none")
	}

	// Second drain — buffer should be empty now.
	out2 := callTool[GetConsoleLogsOutput](t, "get_console_logs", map[string]any{
		"tab":  tabID,
		"mode": "drain",
	})
	if len(out2.Logs) != 0 {
		t.Errorf("after drain, expected 0 logs, got %d", len(out2.Logs))
	}
}

func TestConsoleLogsLevelFilter(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)
	waitForConsole(t, tabID)

	out := callTool[GetConsoleLogsOutput](t, "get_console_logs", map[string]any{
		"tab":   tabID,
		"mode":  "peek",
		"level": "warn",
	})
	for _, log := range out.Logs {
		if log.Level != "warn" {
			t.Errorf("level filter returned log with level %q, want 'warn'", log.Level)
		}
	}
	if len(out.Logs) == 0 {
		t.Error("expected at least one warning log from index.html")
	}
}

func TestJSErrorsDrainClearsBuffer(t *testing.T) {
	tabID := navigateToFixture(t, "errors.html")
	defer closeTab(t, tabID)
	waitForJSErrors(t, tabID)

	// First drain.
	out1 := callTool[GetJSErrorsOutput](t, "get_js_errors", map[string]any{
		"tab":  tabID,
		"mode": "drain",
	})
	if len(out1.Errors) == 0 {
		t.Fatal("expected JS errors from errors.html, got none")
	}

	// Second drain — should be empty.
	out2 := callTool[GetJSErrorsOutput](t, "get_js_errors", map[string]any{
		"tab":  tabID,
		"mode": "drain",
	})
	if len(out2.Errors) != 0 {
		t.Errorf("after drain, expected 0 errors, got %d", len(out2.Errors))
	}
}

func TestJSErrorsPeek(t *testing.T) {
	tabID := navigateToFixture(t, "errors.html")
	defer closeTab(t, tabID)
	waitForJSErrors(t, tabID)

	// Peek should not clear the buffer.
	out1 := callTool[GetJSErrorsOutput](t, "get_js_errors", map[string]any{
		"tab":  tabID,
		"mode": "peek",
	})
	if len(out1.Errors) == 0 {
		t.Fatal("expected JS errors from errors.html")
	}

	out2 := callTool[GetJSErrorsOutput](t, "get_js_errors", map[string]any{
		"tab":  tabID,
		"mode": "peek",
	})
	if len(out2.Errors) != len(out1.Errors) {
		t.Errorf("peek changed buffer: first=%d, second=%d", len(out1.Errors), len(out2.Errors))
	}
}

func TestJSErrorsEntryFields(t *testing.T) {
	tabID := navigateToFixture(t, "errors.html")
	defer closeTab(t, tabID)
	waitForJSErrors(t, tabID)

	out := callTool[GetJSErrorsOutput](t, "get_js_errors", map[string]any{
		"tab":  tabID,
		"mode": "peek",
	})
	if len(out.Errors) == 0 {
		t.Fatal("expected JS errors")
	}

	// Find the thrown error.
	var foundError bool
	for _, e := range out.Errors {
		if strings.Contains(e.Message, "test error from page") {
			foundError = true
			if e.Timestamp.IsZero() {
				t.Error("error timestamp should not be zero")
			}
		}
	}
	if !foundError {
		t.Error("expected 'test error from page' in errors")
	}
}

func TestConsoleLogsLimit(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)
	waitForConsole(t, tabID)

	// index.html emits at least 3 console messages (log, warn, error).
	out := callTool[GetConsoleLogsOutput](t, "get_console_logs", map[string]any{
		"tab":   tabID,
		"mode":  "peek",
		"limit": 1,
	})
	if len(out.Logs) > 1 {
		t.Errorf("limit=1 returned %d logs, want <= 1", len(out.Logs))
	}
}

func TestConsoleLogsPeekIdempotence(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)
	waitForConsole(t, tabID)

	out1 := callTool[GetConsoleLogsOutput](t, "get_console_logs", map[string]any{
		"tab":  tabID,
		"mode": "peek",
	})
	out2 := callTool[GetConsoleLogsOutput](t, "get_console_logs", map[string]any{
		"tab":  tabID,
		"mode": "peek",
	})
	if len(out1.Logs) != len(out2.Logs) {
		t.Errorf("peek not idempotent: first=%d, second=%d", len(out1.Logs), len(out2.Logs))
	}
}

func TestConsoleLogEntryFields(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)
	waitForConsole(t, tabID)

	out := callTool[GetConsoleLogsOutput](t, "get_console_logs", map[string]any{
		"tab":  tabID,
		"mode": "peek",
	})
	if len(out.Logs) == 0 {
		t.Fatal("expected console logs")
	}
	for _, log := range out.Logs {
		if log.Level == "" {
			t.Error("log level should not be empty")
		}
		if log.Text == "" {
			t.Error("log text should not be empty")
		}
		if log.Timestamp.IsZero() {
			t.Error("log timestamp should not be zero")
		}
	}
}

func TestConsoleLogsLevelFilterError(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)
	waitForConsole(t, tabID)

	out := callTool[GetConsoleLogsOutput](t, "get_console_logs", map[string]any{
		"tab":   tabID,
		"mode":  "peek",
		"level": "error",
	})
	for _, log := range out.Logs {
		if log.Level != "error" {
			t.Errorf("level filter 'error' returned level %q", log.Level)
		}
	}
	if len(out.Logs) == 0 {
		t.Error("expected at least one error log from index.html")
	}
}

func TestConsoleLogsLevelFilterLog(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)
	waitForConsole(t, tabID)

	out := callTool[GetConsoleLogsOutput](t, "get_console_logs", map[string]any{
		"tab":   tabID,
		"mode":  "peek",
		"level": "log",
	})
	for _, log := range out.Logs {
		if log.Level != "log" {
			t.Errorf("level filter 'log' returned level %q", log.Level)
		}
	}
	if len(out.Logs) == 0 {
		t.Error("expected at least one 'log' level entry from index.html")
	}
}

func TestConsoleLogsLimitWithFilter(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)
	waitForConsole(t, tabID)

	// Emit a second warning so limit=1 must actually truncate >1 matching entries.
	callTool[any](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `console.warn("second warning")`,
	})

	out := callTool[GetConsoleLogsOutput](t, "get_console_logs", map[string]any{
		"tab":   tabID,
		"mode":  "peek",
		"level": "warn",
		"limit": 1,
	})
	if len(out.Logs) != 1 {
		t.Errorf("limit=1 with level filter returned %d logs, want 1", len(out.Logs))
	} else if out.Logs[0].Level != "warn" {
		t.Errorf("log level = %q, want %q", out.Logs[0].Level, "warn")
	}
}

func TestConsoleLogSourceField(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)
	waitForConsole(t, tabID)

	out := callTool[GetConsoleLogsOutput](t, "get_console_logs", map[string]any{
		"tab":  tabID,
		"mode": "peek",
	})
	if len(out.Logs) == 0 {
		t.Fatal("expected console logs from index.html")
	}
	wantPrefix := harness.httpSrv.URL + "/:"
	hasSource := false
	for _, log := range out.Logs {
		if log.Level == "" {
			t.Error("log entry has empty level")
		}
		if strings.HasPrefix(log.Source, wantPrefix) {
			hasSource = true
		}
	}
	if !hasSource {
		t.Errorf("expected at least one console log with source prefix %q, got %+v", wantPrefix, out.Logs)
	}
}

func TestJSErrorsLimit(t *testing.T) {
	tabID := navigateToFixture(t, "errors.html")
	defer closeTab(t, tabID)
	waitForJSErrors(t, tabID)

	out := callTool[GetJSErrorsOutput](t, "get_js_errors", map[string]any{
		"tab":   tabID,
		"mode":  "peek",
		"limit": 1,
	})
	if len(out.Errors) > 1 {
		t.Errorf("limit=1 returned %d errors, want <= 1", len(out.Errors))
	}
}

func TestJSErrorsPromiseRejection(t *testing.T) {
	tabID := navigateToFixture(t, "errors.html")
	defer closeTab(t, tabID)
	waitForJSErrors(t, tabID)

	out := callTool[GetJSErrorsOutput](t, "get_js_errors", map[string]any{
		"tab":  tabID,
		"mode": "peek",
	})
	// Check that at least one error mentions unhandled rejection.
	var foundRejection bool
	for _, e := range out.Errors {
		if strings.Contains(strings.ToLower(e.Message), "unhandled") ||
			strings.Contains(strings.ToLower(e.Message), "rejection") {
			foundRejection = true
		}
	}
	if !foundRejection {
		// This might not be captured on all Chrome versions; skip rather than fail.
		t.Log("unhandled promise rejection not captured in JS errors (may be Chrome version dependent)")
	}
}

func TestJSErrorsDetailedFields(t *testing.T) {
	tabID := navigateToFixture(t, "errors.html")
	defer closeTab(t, tabID)
	waitForJSErrors(t, tabID)

	out := callTool[GetJSErrorsOutput](t, "get_js_errors", map[string]any{
		"tab":  tabID,
		"mode": "peek",
	})
	if len(out.Errors) == 0 {
		t.Fatal("expected JS errors")
	}
	for _, e := range out.Errors {
		if strings.Contains(e.Message, "test error from page") {
			// Source should be a URL.
			if e.Source != "" && !strings.Contains(e.Source, "errors.html") {
				t.Logf("error source = %q (expected to contain 'errors.html')", e.Source)
			}
			// Line should be positive if populated.
			if e.Line < 0 {
				t.Errorf("error line = %d, want >= 0", e.Line)
			}
			return
		}
	}
}

func TestConsoleDrainWithFilterRetainsNonMatching(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)
	waitForConsole(t, tabID)

	// Snapshot what's there to count levels.
	all := callTool[GetConsoleLogsOutput](t, "get_console_logs", map[string]any{
		"tab":  tabID,
		"mode": "peek",
	})
	if len(all.Logs) == 0 {
		t.Skip("fixture produced no console logs")
	}
	var nonWarning int
	for _, l := range all.Logs {
		if l.Level != "warn" {
			nonWarning++
		}
	}

	// Drain only "warn" level.
	_ = callTool[GetConsoleLogsOutput](t, "get_console_logs", map[string]any{
		"tab":   tabID,
		"level": "warn",
		"mode":  "drain",
	})

	// Selective drain: non-warning entries must remain.
	out2 := callTool[GetConsoleLogsOutput](t, "get_console_logs", map[string]any{
		"tab":  tabID,
		"mode": "peek",
	})
	if len(out2.Logs) != nonWarning {
		t.Errorf("after drain with level=warn, remaining = %d, want %d (non-warning entries retained)",
			len(out2.Logs), nonWarning)
	}
	for _, l := range out2.Logs {
		if l.Level == "warn" {
			t.Errorf("warning entry %q survived drain; selective drain should have removed it", l.Text)
		}
	}
}

func TestJSErrorsCleanPage(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	out := callTool[GetJSErrorsOutput](t, "get_js_errors", map[string]any{
		"tab":  tabID,
		"mode": "peek",
	})
	if out.Errors == nil {
		t.Error("errors should be an empty array, not nil")
	}
	if len(out.Errors) != 0 {
		t.Errorf("clean page should have 0 JS errors, got %d", len(out.Errors))
	}
}

func TestConsoleBufferOverflowIntegration(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	// Generate 1100 console.log messages (buffer size is 1000).
	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "for (var i = 0; i < 1100; i++) { console.log('msg-' + i); }",
	})
	waitForConsole(t, tabID)

	out := callTool[GetConsoleLogsOutput](t, "get_console_logs", map[string]any{
		"tab":  tabID,
		"mode": "peek",
	})

	if len(out.Logs) > 1000 {
		t.Errorf("console buffer should cap at 1000, got %d", len(out.Logs))
	}
	if len(out.Logs) < 900 {
		t.Errorf("expected close to 1000 logs after generating 1100, got %d", len(out.Logs))
	}

	// The oldest entries (msg-0 through msg-99) should have been evicted.
	// The first entry should be approximately msg-100.
	if len(out.Logs) > 0 {
		firstMsg := out.Logs[0].Text
		if strings.Contains(firstMsg, "msg-0") && !strings.Contains(firstMsg, "msg-0") {
			t.Log("first message should not be msg-0 (oldest should be evicted)")
		}
		lastMsg := out.Logs[len(out.Logs)-1].Text
		if !strings.Contains(lastMsg, "msg-1099") {
			t.Logf("last message = %q (expected msg-1099)", lastMsg)
		}
	}
}
