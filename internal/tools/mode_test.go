package tools

import (
	"strings"
	"testing"
)

// TestValidateModeAccepted verifies that values in the allowed set pass.
func TestValidateModeAccepted(t *testing.T) {
	cases := []struct {
		mode    string
		allowed []string
	}{
		{ModePeek, []string{ModePeek, ModeDrain}},
		{ModeDrain, []string{ModePeek, ModeDrain}},
		{TypeModeReplace, []string{TypeModeReplace, TypeModeAppend}},
		{TypeModeAppend, []string{TypeModeReplace, TypeModeAppend}},
	}
	for _, tc := range cases {
		if err := validateMode(tc.mode, tc.allowed...); err != nil {
			t.Errorf("validateMode(%q, %v) = %v, want nil", tc.mode, tc.allowed, err)
		}
	}
}

// TestValidateModeEmpty verifies an empty mode is rejected (no silent
// default into destructive behavior).
func TestValidateModeEmpty(t *testing.T) {
	err := validateMode("", ModePeek, ModeDrain)
	if err == nil {
		t.Fatal("validateMode(\"\", ...) returned nil; want error")
	}
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("error %q should mention 'required'", err)
	}
}

// TestValidateModeInvalid verifies unknown values are rejected with a
// message that surfaces the offending value.
func TestValidateModeInvalid(t *testing.T) {
	err := validateMode("clear", ModePeek, ModeDrain)
	if err == nil {
		t.Fatal("validateMode(\"clear\", ...) returned nil; want error")
	}
	if !strings.Contains(err.Error(), `"clear"`) {
		t.Errorf("error %q should quote the invalid mode", err)
	}
}

// TestValidateModeCrossModeRejected verifies that a value from one
// mode-set is rejected when validated against the other set — a buffer
// "drain" must not accidentally be accepted by the type tool.
func TestValidateModeCrossModeRejected(t *testing.T) {
	if err := validateMode(ModePeek, TypeModeReplace, TypeModeAppend); err == nil {
		t.Error("validateMode(ModePeek, type-modes) returned nil; want error")
	}
	if err := validateMode(TypeModeReplace, ModePeek, ModeDrain); err == nil {
		t.Error("validateMode(TypeModeReplace, buffer-modes) returned nil; want error")
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
		// type uses replace/append modes (not peek/drain) but the same
		// schema-level required check applies.
		{"type", map[string]any{"tab": tabID, "selector": "#x", "text": "hi"}},
	}
	for _, tc := range cases {
		errText := callToolExpectErr(t, tc.name, tc.args)
		if !strings.Contains(errText, `"mode"`) {
			t.Errorf("%s without mode: error %q should reference \"mode\"", tc.name, errText)
		}
	}
}

// TestModeRejectedInvalidValueAtSchema verifies the schema-level enum
// constraint rejects an unknown mode value before the handler sees it,
// for both mode-set families: buffer (peek/drain) and type (replace/append).
// A buffer-mode value passed to the type tool — and vice versa — must
// be rejected by the schema, not silently accepted then mis-handled.
func TestModeRejectedInvalidValueAtSchema(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	cases := []struct {
		name string
		args map[string]any
	}{
		// Buffer-mode tool rejects garbage and rejects a type-mode value.
		{"get_console_logs", map[string]any{"tab": tabID, "mode": "wipe"}},
		{"get_console_logs", map[string]any{"tab": tabID, "mode": "replace"}},
		// Type-mode tool rejects garbage and rejects a buffer-mode value.
		{"type", map[string]any{"tab": tabID, "selector": "#x", "text": "hi", "mode": "wipe"}},
		{"type", map[string]any{"tab": tabID, "selector": "#x", "text": "hi", "mode": "drain"}},
	}
	for _, tc := range cases {
		errText := callToolExpectErr(t, tc.name, tc.args)
		mode, _ := tc.args["mode"].(string)
		if !strings.Contains(errText, mode) && !strings.Contains(errText, "enum") {
			t.Errorf("%s with mode=%q: error %q should mention the value or 'enum'", tc.name, mode, errText)
		}
	}
}
