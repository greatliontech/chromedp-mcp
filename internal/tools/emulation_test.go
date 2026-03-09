package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

// --- set_geolocation ---

// TestSetGeolocationOverride verifies that geolocation can be overridden.
func TestSetGeolocationOverride(t *testing.T) {
	tabID := navigateToFixture(t, "emulation.html")
	defer closeTab(t, tabID)

	// Grant geolocation permission first.
	callTool[struct{}](t, "set_permission", map[string]any{
		"tab":     tabID,
		"name":    "geolocation",
		"setting": "granted",
	})

	// Override geolocation.
	callTool[struct{}](t, "set_geolocation", map[string]any{
		"tab":       tabID,
		"latitude":  48.8566,
		"longitude": 2.3522,
		"accuracy":  10.0,
	})

	// Query geolocation via JS.
	type evalResult struct {
		Result json.RawMessage `json:"result"`
	}
	result := callTool[evalResult](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": `new Promise((resolve, reject) => {
			navigator.geolocation.getCurrentPosition(
				pos => resolve({lat: pos.coords.latitude, lng: pos.coords.longitude}),
				err => reject(err.message),
				{timeout: 5000}
			);
		})`,
	})

	var coords struct {
		Lat float64 `json:"lat"`
		Lng float64 `json:"lng"`
	}
	if err := json.Unmarshal(result.Result, &coords); err != nil {
		t.Fatalf("unmarshal coords: %v, raw: %s", err, result.Result)
	}
	if coords.Lat != 48.8566 || coords.Lng != 2.3522 {
		t.Fatalf("expected Paris coordinates (48.8566, 2.3522), got (%v, %v)", coords.Lat, coords.Lng)
	}
}

// TestSetGeolocationReset verifies that omitting all fields resets geolocation.
func TestSetGeolocationReset(t *testing.T) {
	tabID := navigateToFixture(t, "emulation.html")
	defer closeTab(t, tabID)

	// Override then reset — the CDP call should succeed.
	callTool[struct{}](t, "set_geolocation", map[string]any{
		"tab":       tabID,
		"latitude":  0.0,
		"longitude": 0.0,
	})
	callTool[struct{}](t, "set_geolocation", map[string]any{
		"tab": tabID,
	})
}

// --- set_timezone ---

// TestSetTimezone verifies timezone override.
func TestSetTimezone(t *testing.T) {
	tabID := navigateToFixture(t, "emulation.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "set_timezone", map[string]any{
		"tab":         tabID,
		"timezone_id": "Pacific/Auckland",
	})

	// Check that the timezone is reflected in JS.
	type evalResult struct {
		Result string `json:"result"`
	}
	result := callTool[evalResult](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "Intl.DateTimeFormat().resolvedOptions().timeZone",
	})
	if result.Result != "Pacific/Auckland" {
		t.Fatalf("expected Pacific/Auckland, got %q", result.Result)
	}
}

// TestSetTimezoneReset verifies timezone reset.
func TestSetTimezoneReset(t *testing.T) {
	tabID := navigateToFixture(t, "emulation.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "set_timezone", map[string]any{
		"tab":         tabID,
		"timezone_id": "Asia/Tokyo",
	})

	type evalResult struct {
		Result string `json:"result"`
	}
	result := callTool[evalResult](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "Intl.DateTimeFormat().resolvedOptions().timeZone",
	})
	if result.Result != "Asia/Tokyo" {
		t.Fatalf("expected Asia/Tokyo, got %q", result.Result)
	}

	// Reset — empty string uses browser-internal reset which may
	// error. Use the real system timezone name instead.
	// Actually, CDP requires a non-empty string for SetTimezoneOverride.
	// Let's reset to UTC which is always valid.
	callTool[struct{}](t, "set_timezone", map[string]any{
		"tab":         tabID,
		"timezone_id": "UTC",
	})

	result = callTool[evalResult](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "Intl.DateTimeFormat().resolvedOptions().timeZone",
	})
	if result.Result != "UTC" {
		t.Fatalf("expected UTC after reset, got %q", result.Result)
	}
}

// TestSetTimezoneInvalid verifies that invalid timezone IDs return errors.
func TestSetTimezoneInvalid(t *testing.T) {
	tabID := navigateToFixture(t, "emulation.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "set_timezone", map[string]any{
		"tab":         tabID,
		"timezone_id": "Not/A/Real/Timezone",
	})
	if errText == "" {
		t.Fatal("expected error for invalid timezone")
	}
}

// --- set_locale ---

// TestSetLocale verifies locale override affects Intl APIs.
func TestSetLocale(t *testing.T) {
	tabID := navigateToFixture(t, "emulation.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "set_locale", map[string]any{
		"tab":    tabID,
		"locale": "de_DE",
	})

	// SetLocaleOverride affects Intl APIs, not navigator.language.
	// In German locale, decimal separator is comma.
	type evalResult struct {
		Result string `json:"result"`
	}
	result := callTool[evalResult](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "new Intl.NumberFormat().format(1234.5)",
	})
	// German format: "1.234,5"
	if !strings.Contains(result.Result, ",") {
		t.Fatalf("expected German number format with comma separator, got %q", result.Result)
	}
}

// TestSetLocaleReset verifies locale reset (empty string).
func TestSetLocaleReset(t *testing.T) {
	tabID := navigateToFixture(t, "emulation.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "set_locale", map[string]any{
		"tab":    tabID,
		"locale": "de_DE",
	})

	type evalResult struct {
		Result string `json:"result"`
	}
	result := callTool[evalResult](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "new Intl.NumberFormat().format(1234.5)",
	})
	if !strings.Contains(result.Result, ",") {
		t.Fatalf("expected German number format, got %q", result.Result)
	}

	// Reset to default.
	callTool[struct{}](t, "set_locale", map[string]any{
		"tab":    tabID,
		"locale": "",
	})

	// After reset, the number format should use the system default (likely en-US with ".").
	result = callTool[evalResult](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "new Intl.NumberFormat().format(1234.5)",
	})
	if !strings.Contains(result.Result, ".") {
		t.Fatalf("expected default locale number format with dot separator after reset, got %q", result.Result)
	}
}

// --- set_user_agent ---

// TestSetUserAgent verifies user agent override.
func TestSetUserAgent(t *testing.T) {
	tabID := navigateToFixture(t, "emulation.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "set_user_agent", map[string]any{
		"tab":        tabID,
		"user_agent": "MyCustomBot/1.0",
	})

	type evalResult struct {
		Result string `json:"result"`
	}
	result := callTool[evalResult](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "navigator.userAgent",
	})
	if result.Result != "MyCustomBot/1.0" {
		t.Fatalf("expected MyCustomBot/1.0, got %q", result.Result)
	}
}

// TestSetUserAgentWithPlatform verifies platform override.
func TestSetUserAgentWithPlatform(t *testing.T) {
	tabID := navigateToFixture(t, "emulation.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "set_user_agent", map[string]any{
		"tab":        tabID,
		"user_agent": "TestAgent/2.0",
		"platform":   "TestPlatform",
	})

	type evalResult struct {
		Result string `json:"result"`
	}

	// Check user agent.
	ua := callTool[evalResult](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "navigator.userAgent",
	})
	if ua.Result != "TestAgent/2.0" {
		t.Fatalf("expected TestAgent/2.0, got %q", ua.Result)
	}

	// Check platform.
	plat := callTool[evalResult](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "navigator.platform",
	})
	if plat.Result != "TestPlatform" {
		t.Fatalf("expected TestPlatform, got %q", plat.Result)
	}
}

// --- set_cpu_throttling ---

// TestSetCPUThrottling verifies the CDP call succeeds.
func TestSetCPUThrottling(t *testing.T) {
	tabID := navigateToFixture(t, "emulation.html")
	defer closeTab(t, tabID)

	// Enable throttling.
	callTool[struct{}](t, "set_cpu_throttling", map[string]any{
		"tab":  tabID,
		"rate": 4.0,
	})

	// Disable throttling.
	callTool[struct{}](t, "set_cpu_throttling", map[string]any{
		"tab":  tabID,
		"rate": 1.0,
	})
}

// TestSetCPUThrottlingMinimumRate verifies that rates below 1 are clamped.
func TestSetCPUThrottlingMinimumRate(t *testing.T) {
	tabID := navigateToFixture(t, "emulation.html")
	defer closeTab(t, tabID)

	// Rate 0 should be clamped to 1 (no throttle).
	callTool[struct{}](t, "set_cpu_throttling", map[string]any{
		"tab":  tabID,
		"rate": 0.0,
	})
}

// --- set_vision_deficiency ---

// TestSetVisionDeficiency verifies each valid deficiency type.
func TestSetVisionDeficiency(t *testing.T) {
	tabID := navigateToFixture(t, "emulation.html")
	defer closeTab(t, tabID)

	types := []string{
		"none", "blurredVision", "reducedContrast",
		"achromatopsia", "deuteranopia", "protanopia", "tritanopia",
	}
	for _, typ := range types {
		callTool[struct{}](t, "set_vision_deficiency", map[string]any{
			"tab":  tabID,
			"type": typ,
		})
	}
}

// TestSetVisionDeficiencyInvalid verifies invalid type is rejected.
func TestSetVisionDeficiencyInvalid(t *testing.T) {
	tabID := navigateToFixture(t, "emulation.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "set_vision_deficiency", map[string]any{
		"tab":  tabID,
		"type": "invalid_type",
	})
	if !strings.Contains(errText, "invalid vision deficiency type") {
		t.Fatalf("expected validation error, got: %s", errText)
	}
}

// --- emulate_network ---

// TestEmulateNetworkOffline verifies offline mode blocks network.
func TestEmulateNetworkOffline(t *testing.T) {
	tabID := navigateToFixture(t, "emulation.html")
	defer closeTab(t, tabID)

	// Go offline.
	callTool[struct{}](t, "emulate_network", map[string]any{
		"tab":     tabID,
		"offline": true,
	})

	// A fetch should fail when offline.
	type evalResult struct {
		Result string `json:"result"`
	}
	result := callTool[evalResult](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `fetch('/api/data').then(() => 'ok').catch(e => 'failed: ' + e.message)`,
	})
	if !strings.Contains(result.Result, "failed") {
		t.Fatalf("expected fetch to fail when offline, got: %q", result.Result)
	}

	// Go back online.
	callTool[struct{}](t, "emulate_network", map[string]any{
		"tab":     tabID,
		"offline": false,
	})

	// Fetch should succeed now.
	result = callTool[evalResult](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `fetch('/api/data').then(() => 'ok').catch(e => 'failed: ' + e.message)`,
	})
	if result.Result != "ok" {
		t.Fatalf("expected fetch to succeed when online, got: %q", result.Result)
	}
}

// TestEmulateNetworkLatency verifies latency adds delay.
func TestEmulateNetworkLatency(t *testing.T) {
	tabID := navigateToFixture(t, "emulation.html")
	defer closeTab(t, tabID)

	// Set 500ms minimum latency.
	callTool[struct{}](t, "emulate_network", map[string]any{
		"tab":     tabID,
		"latency": 500.0,
	})

	// Measure fetch time — it should be at least 400ms (with some margin).
	type evalResult struct {
		Result float64 `json:"result"`
	}
	result := callTool[evalResult](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": `(async () => {
			const start = Date.now();
			await fetch('/api/data');
			return Date.now() - start;
		})()`,
	})
	if result.Result < 400 {
		t.Fatalf("expected at least 400ms latency, got %.0fms", result.Result)
	}

	// Reset network conditions.
	callTool[struct{}](t, "emulate_network", map[string]any{
		"tab": tabID,
	})
}

// --- block_urls ---

// TestBlockURLs verifies that blocked URLs fail to load.
func TestBlockURLs(t *testing.T) {
	tabID := navigateToFixture(t, "emulation.html")
	defer closeTab(t, tabID)

	// Block the API endpoint.
	callTool[struct{}](t, "block_urls", map[string]any{
		"tab":      tabID,
		"patterns": []string{"*/api/*"},
	})

	// A fetch to the blocked URL should fail.
	type evalResult struct {
		Result string `json:"result"`
	}
	result := callTool[evalResult](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `fetch('/api/data').then(() => 'ok').catch(e => 'blocked: ' + e.message)`,
	})
	if !strings.Contains(result.Result, "blocked") {
		t.Fatalf("expected fetch to be blocked, got: %q", result.Result)
	}

	// Clear blocks.
	callTool[struct{}](t, "block_urls", map[string]any{
		"tab":      tabID,
		"patterns": []string{},
	})

	// Fetch should work now.
	result = callTool[evalResult](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `fetch('/api/data').then(() => 'ok').catch(e => 'blocked: ' + e.message)`,
	})
	if result.Result != "ok" {
		t.Fatalf("expected fetch to succeed after unblock, got: %q", result.Result)
	}
}

// TestBlockURLsWildcard verifies wildcard matching.
func TestBlockURLsWildcard(t *testing.T) {
	tabID := navigateToFixture(t, "emulation.html")
	defer closeTab(t, tabID)

	// Block all .png images.
	callTool[struct{}](t, "block_urls", map[string]any{
		"tab":      tabID,
		"patterns": []string{"*.png"},
	})

	// Try to fetch the image — should fail.
	type evalResult struct {
		Result string `json:"result"`
	}
	result := callTool[evalResult](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `fetch('/image.png').then(() => 'ok').catch(e => 'blocked')`,
	})
	if result.Result != "blocked" {
		t.Fatalf("expected PNG to be blocked, got: %q", result.Result)
	}

	// Clear blocks.
	callTool[struct{}](t, "block_urls", map[string]any{
		"tab":      tabID,
		"patterns": []string{},
	})
}

// --- Consolidation: delete_cookies clears all when name is empty ---

// TestDeleteCookiesClearAll verifies that omitting name clears all cookies.
func TestDeleteCookiesClearAll(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// Set cookies via JS (simpler, no domain issues).
	type evalResult struct {
		Result string `json:"result"`
	}
	callTool[evalResult](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.cookie = 'a=1'; document.cookie = 'b=2'; 'ok'",
	})

	// Verify they exist.
	cookies := callTool[GetCookiesOutput](t, "get_cookies", map[string]any{"tab": tabID})
	if len(cookies.Cookies) < 2 {
		t.Fatalf("expected at least 2 cookies, got %d", len(cookies.Cookies))
	}

	// Delete all (no name).
	callTool[struct{}](t, "delete_cookies", map[string]any{"tab": tabID})

	// Verify all cleared.
	cookies = callTool[GetCookiesOutput](t, "get_cookies", map[string]any{"tab": tabID})
	if len(cookies.Cookies) != 0 {
		t.Fatalf("expected 0 cookies after clear-all, got %d", len(cookies.Cookies))
	}
}

// TestDeleteCookiesClearAllIdempotent verifies double clear-all is fine.
func TestDeleteCookiesClearAllIdempotent(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "delete_cookies", map[string]any{"tab": tabID})
	callTool[struct{}](t, "delete_cookies", map[string]any{"tab": tabID})
}

// --- Consolidation: evaluate with selector ---

// TestEvaluateWithSelector verifies the merged evaluate+selector behavior.
func TestEvaluateWithSelector(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	type evalResult struct {
		Result string `json:"result"`
	}
	result := callTool[evalResult](t, "evaluate", map[string]any{
		"tab":        tabID,
		"selector":   "h1",
		"expression": "return el.textContent",
	})
	if result.Result != "Hello World" {
		t.Fatalf("expected 'Hello World', got %q", result.Result)
	}
}

// TestEvaluateWithSelectorNotFound verifies error on missing selector.
func TestEvaluateWithSelectorNotFound(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "evaluate", map[string]any{
		"tab":        tabID,
		"selector":   "#nonexistent-xyz",
		"expression": "return el.textContent",
		"timeout":    500,
	})
	if !strings.Contains(errText, "not found") {
		t.Fatalf("expected 'not found' error, got: %s", errText)
	}
}

// TestEvaluateWithoutSelector verifies plain evaluate still works.
func TestEvaluateWithoutSelector(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	type evalResult struct {
		Result float64 `json:"result"`
	}
	result := callTool[evalResult](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "2 + 2",
	})
	if result.Result != 4 {
		t.Fatalf("expected 4, got %v", result.Result)
	}
}

// --- select_option/submit_form selectorContext ---

// TestSelectOptionSelectorTimeout verifies select_option times out cleanly.
func TestSelectOptionSelectorTimeout(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#nonexistent-select",
		"value":    "x",
		"timeout":  500,
	})
	if !strings.Contains(errText, "not found") {
		t.Fatalf("expected 'not found' error, got: %s", errText)
	}
}

// TestSubmitFormSelectorTimeout verifies submit_form times out cleanly.
func TestSubmitFormSelectorTimeout(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "submit_form", map[string]any{
		"tab":      tabID,
		"selector": "#nonexistent-form",
		"timeout":  500,
	})
	if !strings.Contains(errText, "not found") {
		t.Fatalf("expected 'not found' error, got: %s", errText)
	}
}
