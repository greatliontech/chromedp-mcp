package tools

import (
	"strings"
	"testing"
)

// TestAddAndRemoveScript verifies that add_script injects JS that runs on
// subsequent navigations and remove_script stops it. evaluate_now=false
// is the canonical "future loads only" mode.
func TestAddAndRemoveScript(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// Add a script that sets a global variable. evaluate_now=false so we
	// can verify the new-document registration works in isolation —
	// before navigation, the variable should be unset; after, set.
	out := callTool[AddScriptOutput](t, "add_script", map[string]any{
		"tab":          tabID,
		"source":       "window.__injected = 'hello from injected script';",
		"evaluate_now": false,
	})
	if out.Identifier == "" {
		t.Fatal("expected non-empty script identifier")
	}
	if out.Warning != "" {
		t.Errorf("Warning = %q, want empty for evaluate_now=false", out.Warning)
	}

	// Pre-navigation: window.__injected must not exist on the current
	// document. Confirms evaluate_now=false really did skip the
	// current-doc evaluation.
	type evalResult struct {
		Result string `json:"result"`
	}
	pre := callTool[evalResult](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "window.__injected || 'not set'",
	})
	if pre.Result != "not set" {
		t.Fatalf("evaluate_now=false should not run on current doc; got %q", pre.Result)
	}

	// Navigate to trigger the script.
	callTool[struct{}](t, "navigate", map[string]any{
		"tab": tabID,
		"url": fixtureURL("config.html"),
	})

	// Check the injected variable exists.
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

// TestAddScriptEvaluateNowRunsOnCurrentDoc verifies evaluate_now=true
// executes the script once on the document already loaded in the tab —
// the original issue 006 footgun (registration happens but current doc
// is left alone) must be gone.
func TestAddScriptEvaluateNowRunsOnCurrentDoc(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	out := callTool[AddScriptOutput](t, "add_script", map[string]any{
		"tab":          tabID,
		"source":       "window.__pingedAt = Date.now();",
		"evaluate_now": true,
	})
	if out.Identifier == "" {
		t.Fatal("expected non-empty script identifier")
	}
	if out.Warning != "" {
		t.Errorf("Warning = %q, want empty for clean evaluate_now success", out.Warning)
	}

	// Without navigating, the current document should now have the
	// global variable.
	type evalResult struct {
		Result string `json:"result"`
	}
	res := callTool[evalResult](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "typeof window.__pingedAt === 'number' ? 'set' : 'not set'",
	})
	if res.Result != "set" {
		t.Errorf("evaluate_now=true should run on current doc; got %q", res.Result)
	}

	// Cleanup so subsequent tests aren't affected by the registration.
	callTool[struct{}](t, "remove_script", map[string]any{
		"tab":        tabID,
		"identifier": out.Identifier,
	})
}

// TestAddScriptEvaluateNowFailureKeepsRegistration verifies that a
// runtime error in the current-document evaluation does not roll back
// the new-document registration. The script throws on the current page
// (where document.title differs from what it expects) but completes
// cleanly on the navigation target. After navigating, the global it
// sets must be present — proving the registration survived the
// current-doc failure.
func TestAddScriptEvaluateNowFailureKeepsRegistration(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// Script throws on any document whose pathname doesn't contain
	// "page2". location.pathname is set to the navigation target before
	// the registered script runs (unlike document.title, which is only
	// populated once HTML parsing reaches the <title> element). On the
	// current document (index.html, pathname="/"), throws → warning.
	// On Page 2 (after navigation), the registration runs the script
	// against pathname="/page2.html" and the global is set cleanly.
	src := `
		if (location.pathname.indexOf('page2') === -1) {
			throw new Error('not page2: ' + location.pathname);
		}
		window.__landedOnPage2 = true;
	`
	out := callTool[AddScriptOutput](t, "add_script", map[string]any{
		"tab":          tabID,
		"source":       src,
		"evaluate_now": true,
	})
	if out.Identifier == "" {
		t.Fatal("registration should not be rolled back on current-doc eval failure")
	}
	if out.Warning == "" {
		t.Error("Warning must be set when current-doc evaluation fails")
	}

	// Navigate to Page 2. The new-doc registration must fire there; the
	// script's gate condition is now satisfied so the global is set.
	callTool[struct{}](t, "navigate", map[string]any{
		"tab": tabID,
		"url": fixtureURL("page2.html"),
	})

	type evalResult struct {
		Result string `json:"result"`
	}
	res := callTool[evalResult](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "window.__landedOnPage2 === true ? 'set' : 'not set'",
	})
	if res.Result != "set" {
		t.Errorf("after navigation, registration should have run on Page 2; got %q", res.Result)
	}

	callTool[struct{}](t, "remove_script", map[string]any{
		"tab":        tabID,
		"identifier": out.Identifier,
	})
}

// TestAddScriptEvaluateNowAwaitsPromiseRejection verifies that an async
// failure (rejected Promise) in the current-document script body is
// surfaced as a warning, not silently swallowed. Without WithAwaitPromise,
// Runtime.evaluate would return the Promise object unevaluated and
// chromedp would report no error.
func TestAddScriptEvaluateNowAwaitsPromiseRejection(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// IIFE returning a Promise.reject. Without awaitPromise, chromedp
	// sees this as a successful evaluation that produced a Promise.
	out := callTool[AddScriptOutput](t, "add_script", map[string]any{
		"tab":          tabID,
		"source":       "(async () => { throw new Error('async-failure'); })()",
		"evaluate_now": true,
	})
	if out.Identifier == "" {
		t.Fatal("registration should not be rolled back on async failure")
	}
	if out.Warning == "" {
		t.Fatal("async Promise rejection in current-doc script must surface as a warning")
	}
	if !strings.Contains(out.Warning, "async-failure") {
		t.Errorf("Warning %q should reference the rejection message", out.Warning)
	}

	callTool[struct{}](t, "remove_script", map[string]any{
		"tab":        tabID,
		"identifier": out.Identifier,
	})
}

// TestAddScriptParseErrorRejectsEarly verifies a syntactically invalid
// script is rejected before the new-document registration is committed.
// Without the up-front compileScript check, the registration would be
// silently created and every subsequent navigation would throw — the
// LLM would see a clean identifier and (for evaluate_now=true) a
// runtime warning that misrepresents the future-doc behaviour.
func TestAddScriptParseErrorRejectsEarly(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// Unclosed string literal — guaranteed parse error in V8.
	for _, evalNow := range []bool{false, true} {
		errText := callToolExpectErr(t, "add_script", map[string]any{
			"tab":          tabID,
			"source":       "var x = 'unterminated;",
			"evaluate_now": evalNow,
		})
		if !strings.Contains(strings.ToLower(errText), "parse") &&
			!strings.Contains(errText, "SyntaxError") {
			t.Errorf("evaluate_now=%v: error %q should mention 'parse' or 'SyntaxError'", evalNow, errText)
		}
	}

	// And no orphan registration was created — if a future navigation
	// fires no script, this assertion holds (any throw on the new doc
	// would be visible via window.onerror or console). We rely on the
	// up-front compileScript check; if a regression skipped the check
	// the registration would exist and surface as a JS error post-nav.
	callTool[struct{}](t, "navigate", map[string]any{
		"tab": tabID,
		"url": fixtureURL("page2.html"),
	})
	logs := callTool[GetJSErrorsOutput](t, "get_js_errors", map[string]any{
		"tab":  tabID,
		"mode": "drain",
	})
	for _, e := range logs.Errors {
		if strings.Contains(e.Message, "unterminated") || strings.Contains(e.Message, "SyntaxError") {
			t.Errorf("orphan registration ran on new doc: %s", e.Message)
		}
	}
}

// TestAddScriptMissingEvaluateNow verifies the schema rejects calls that
// omit the required evaluate_now field. Established by issues 003/004 as
// the boundary contract for required parameters.
func TestAddScriptMissingEvaluateNow(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "add_script", map[string]any{
		"tab":    tabID,
		"source": "window.__noop = 1;",
	})
	if !strings.Contains(errText, "evaluate_now") {
		t.Errorf("error %q should reference 'evaluate_now'", errText)
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
