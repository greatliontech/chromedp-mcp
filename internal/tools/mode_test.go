package tools

import (
	"strings"
	"testing"
)

// TestValidateModeAccepted verifies the two valid modes pass.
func TestValidateModeAccepted(t *testing.T) {
	for _, m := range []string{ModePeek, ModeDrain} {
		if err := validateMode(m); err != nil {
			t.Errorf("validateMode(%q) = %v, want nil", m, err)
		}
	}
}

// TestValidateModeEmpty verifies an empty mode is rejected (no silent
// default into destructive behavior).
func TestValidateModeEmpty(t *testing.T) {
	err := validateMode("")
	if err == nil {
		t.Fatal("validateMode(\"\") returned nil; want error")
	}
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("error %q should mention 'required'", err)
	}
}

// TestValidateModeInvalid verifies unknown values are rejected with a
// message that surfaces the offending value.
func TestValidateModeInvalid(t *testing.T) {
	err := validateMode("clear")
	if err == nil {
		t.Fatal("validateMode(\"clear\") returned nil; want error")
	}
	if !strings.Contains(err.Error(), `"clear"`) {
		t.Errorf("error %q should quote the invalid mode", err)
	}
}

// TestModeRejectedAtToolBoundary verifies the schema-level required check
// fires for every tool that consumes buffers when 'mode' is omitted. The
// error must specifically reference "mode" (quoted) so the LLM gets a
// clear signal — generic "missing required property" wouldn't disambiguate
// when the call also omits other required fields.
func TestModeRejectedAtToolBoundary(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	cases := []struct {
		name string
		args map[string]any
	}{
		{"get_console_logs", map[string]any{"tab": tabID}},
		{"get_js_errors", map[string]any{"tab": tabID}},
		{"get_network_requests", map[string]any{"tab": tabID}},
		{"get_layout_shifts", map[string]any{"tab": tabID}},
		// get_websocket_frames also requires request_id; the schema lists
		// every missing required prop, so the error still names 'mode'.
		{"get_websocket_frames", map[string]any{"tab": tabID}},
		// get_downloads is browser-scoped, not tab-scoped.
		{"get_downloads", map[string]any{}},
	}
	for _, tc := range cases {
		errText := callToolExpectErr(t, tc.name, tc.args)
		if !strings.Contains(errText, `"mode"`) {
			t.Errorf("%s without mode: error %q should reference \"mode\"", tc.name, errText)
		}
	}
}

// TestModeRejectedInvalidValueAtSchema verifies the schema-level enum
// constraint rejects an unknown mode value before the handler sees it.
// This protects callers from typos like "wipe" landing as a destructive
// no-op or worse.
func TestModeRejectedInvalidValueAtSchema(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "get_console_logs", map[string]any{
		"tab":  tabID,
		"mode": "wipe",
	})
	if !strings.Contains(errText, "wipe") && !strings.Contains(errText, "enum") {
		t.Errorf("error %q should mention the invalid mode value or the enum constraint", errText)
	}
}
