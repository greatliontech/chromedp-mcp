package tools

import (
	"strings"
	"testing"
)

// TestAddAndRemoveScript verifies that add_script injects JS that runs on
// subsequent navigations and remove_script stops it.
func TestAddAndRemoveScript(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// Add a script that sets a global variable.
	out := callTool[AddScriptOutput](t, "add_script", map[string]any{
		"tab":    tabID,
		"source": "window.__injected = 'hello from injected script';",
	})
	if out.Identifier == "" {
		t.Fatal("expected non-empty script identifier")
	}

	// Navigate to trigger the script.
	callTool[struct{}](t, "navigate", map[string]any{
		"tab": tabID,
		"url": fixtureURL("config.html"),
	})

	// Check the injected variable exists.
	type evalResult struct {
		Result string `json:"result"`
	}
	result := callTool[evalResult](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "window.__injected || 'not set'",
	})
	if result.Result != "hello from injected script" {
		t.Fatalf("expected injected value, got %q", result.Result)
	}

	// Remove the script.
	callTool[struct{}](t, "remove_script", map[string]any{
		"tab":        tabID,
		"identifier": out.Identifier,
	})

	// Navigate again — the script should no longer run.
	callTool[struct{}](t, "navigate", map[string]any{
		"tab": tabID,
		"url": fixtureURL("index.html"),
	})

	result = callTool[evalResult](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "window.__injected || 'not set'",
	})
	if result.Result != "not set" {
		t.Fatalf("expected script removed, but got %q", result.Result)
	}
}

// TestSetExtraHeaders verifies that custom headers are injected into requests.
func TestSetExtraHeaders(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// Set custom headers.
	callTool[struct{}](t, "set_extra_headers", map[string]any{
		"tab": tabID,
		"headers": map[string]string{
			"X-Custom-Auth":  "Bearer test-token-123",
			"X-Feature-Flag": "dark-mode-enabled",
		},
	})

	// Fetch the headers endpoint via JS and check the response.
	type evalResult struct {
		Result string `json:"result"`
	}
	result := callTool[evalResult](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "fetch('/api/headers').then(r => r.text()).then(t => t)",
	})

	if !strings.Contains(result.Result, "Bearer test-token-123") {
		t.Fatalf("expected custom auth header in response, got: %s", result.Result)
	}
	if !strings.Contains(result.Result, "dark-mode-enabled") {
		t.Fatalf("expected feature flag header in response, got: %s", result.Result)
	}

	// Clear headers.
	callTool[struct{}](t, "set_extra_headers", map[string]any{
		"tab":     tabID,
		"headers": map[string]string{},
	})

	// Verify headers are cleared.
	result = callTool[evalResult](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "fetch('/api/headers').then(r => r.text()).then(t => t)",
	})
	if strings.Contains(result.Result, "Bearer test-token-123") {
		t.Fatal("expected custom header to be cleared")
	}
}

// TestSetPermission verifies that permissions can be granted and denied.
func TestSetPermission(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// Grant geolocation permission.
	callTool[struct{}](t, "set_permission", map[string]any{
		"tab":     tabID,
		"name":    "geolocation",
		"setting": "granted",
	})

	// Query the permission state via JS.
	type evalResult struct {
		Result string `json:"result"`
	}
	result := callTool[evalResult](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "navigator.permissions.query({name:'geolocation'}).then(p => p.state)",
	})
	if result.Result != "granted" {
		t.Fatalf("expected geolocation granted, got %q", result.Result)
	}

	// Deny it.
	callTool[struct{}](t, "set_permission", map[string]any{
		"tab":     tabID,
		"name":    "geolocation",
		"setting": "denied",
	})

	result = callTool[evalResult](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "navigator.permissions.query({name:'geolocation'}).then(p => p.state)",
	})
	if result.Result != "denied" {
		t.Fatalf("expected geolocation denied, got %q", result.Result)
	}
}

// TestSetPermissionInvalidSetting verifies that invalid settings are rejected.
func TestSetPermissionInvalidSetting(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "set_permission", map[string]any{
		"tab":     tabID,
		"name":    "geolocation",
		"setting": "invalid",
	})
	if !strings.Contains(errText, "invalid permission setting") {
		t.Fatalf("expected validation error, got: %s", errText)
	}
}

// TestSetEmulatedMediaDarkMode verifies dark mode emulation.
func TestSetEmulatedMediaDarkMode(t *testing.T) {
	tabID := navigateToFixture(t, "config.html")
	defer closeTab(t, tabID)

	// Default should be light.
	type textResult struct {
		Text string `json:"text"`
	}
	result := callTool[textResult](t, "get_text", map[string]any{
		"tab":      tabID,
		"selector": "#color-scheme",
	})
	if result.Text != "light" {
		t.Fatalf("expected default light, got %q", result.Text)
	}

	// Switch to dark mode.
	callTool[struct{}](t, "set_emulated_media", map[string]any{
		"tab": tabID,
		"features": []map[string]string{
			{"name": "prefers-color-scheme", "value": "dark"},
		},
	})

	// Re-run the media query check via JS.
	type evalResult struct {
		Result string `json:"result"`
	}
	eval := callTool[evalResult](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'",
	})
	if eval.Result != "dark" {
		t.Fatalf("expected dark after emulation, got %q", eval.Result)
	}

	// Reset by calling with empty features.
	callTool[struct{}](t, "set_emulated_media", map[string]any{
		"tab":      tabID,
		"features": []map[string]string{},
	})
}

// TestSetEmulatedMediaReducedMotion verifies reduced motion emulation.
func TestSetEmulatedMediaReducedMotion(t *testing.T) {
	tabID := navigateToFixture(t, "config.html")
	defer closeTab(t, tabID)

	// Default should be no-preference.
	type evalResult struct {
		Result string `json:"result"`
	}
	result := callTool[evalResult](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'reduce' : 'no-preference'",
	})
	if result.Result != "no-preference" {
		t.Fatalf("expected default no-preference, got %q", result.Result)
	}

	// Enable reduced motion.
	callTool[struct{}](t, "set_emulated_media", map[string]any{
		"tab": tabID,
		"features": []map[string]string{
			{"name": "prefers-reduced-motion", "value": "reduce"},
		},
	})

	result = callTool[evalResult](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'reduce' : 'no-preference'",
	})
	if result.Result != "reduce" {
		t.Fatalf("expected reduce after emulation, got %q", result.Result)
	}
}

// TestSetEmulatedMediaPrint verifies print media type emulation.
func TestSetEmulatedMediaPrint(t *testing.T) {
	tabID := navigateToFixture(t, "config.html")
	defer closeTab(t, tabID)

	// Set media type to print.
	callTool[struct{}](t, "set_emulated_media", map[string]any{
		"tab":   tabID,
		"media": "print",
	})

	// Check that the print media query matches.
	type evalResult struct {
		Result string `json:"result"`
	}
	result := callTool[evalResult](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "window.matchMedia('print').matches ? 'print' : 'screen'",
	})
	if result.Result != "print" {
		t.Fatalf("expected print media, got %q", result.Result)
	}
}

// TestSetIgnoreCertificateErrors verifies the tool accepts the call.
// We can't fully test with a real self-signed cert in this harness,
// but we verify the CDP call succeeds without error.
func TestSetIgnoreCertificateErrors(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// Enable.
	callTool[struct{}](t, "set_ignore_certificate_errors", map[string]any{
		"tab":    tabID,
		"ignore": true,
	})

	// Disable.
	callTool[struct{}](t, "set_ignore_certificate_errors", map[string]any{
		"tab":    tabID,
		"ignore": false,
	})
}

// TestSetMultipleMediaFeatures verifies setting multiple features at once.
func TestSetMultipleMediaFeatures(t *testing.T) {
	tabID := navigateToFixture(t, "config.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "set_emulated_media", map[string]any{
		"tab": tabID,
		"features": []map[string]string{
			{"name": "prefers-color-scheme", "value": "dark"},
			{"name": "prefers-reduced-motion", "value": "reduce"},
		},
	})

	type evalResult struct {
		Result string `json:"result"`
	}
	dark := callTool[evalResult](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'",
	})
	if dark.Result != "dark" {
		t.Fatalf("expected dark, got %q", dark.Result)
	}

	motion := callTool[evalResult](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'reduce' : 'no-preference'",
	})
	if motion.Result != "reduce" {
		t.Fatalf("expected reduce, got %q", motion.Result)
	}
}
