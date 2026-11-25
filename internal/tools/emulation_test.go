package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	cdpcss "github.com/chromedp/cdproto/css"
	"github.com/chromedp/chromedp"
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
	if !strings.Contains(errText, "type") || !strings.Contains(errText, "invalid_type") {
		t.Fatalf("expected vision deficiency validation error, got: %s", errText)
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

	// navigator.onLine should reflect offline state.
	type boolEval struct {
		Result bool `json:"result"`
	}
	onlineRes := callTool[boolEval](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `navigator.onLine`,
	})
	if onlineRes.Result {
		t.Fatalf("expected navigator.onLine to be false when offline")
	}

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

	// navigator.onLine should return to true when back online.
	onlineRes = callTool[boolEval](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `navigator.onLine`,
	})
	if !onlineRes.Result {
		t.Fatalf("expected navigator.onLine to be true after returning online")
	}

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

func TestNormalizeBlockPattern(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"*", "*://*:*/*"},
		{"https://example.com/api/*", "https://example.com/api/*"},
		{"*://*:*/*.css", "*://*:*/*.css"},
		{"*/api/*", "*://*:*/api/*"},
		{"/api/secret", "*://*:*/api/secret"},
		{"*.png", "*://*:*/*.png"},
		{"*tracking*", "*://*:*/*tracking*"},
		{"*api/v1*", "*://*:*/*api/v1*"},
		{"*.example.com", "*://*.example.com:*/*"},
		{"*.example.com/*", "*://*.example.com:*/*"},
		{"*.example.com:8443/*", "*://*.example.com:8443/*"},
		{"ads.example.com/pixel.gif", "*://ads.example.com:*/pixel.gif"},
		{"ads.example.com", "*://ads.example.com:*/*"},
		{"127.0.0.1/api/*", "*://127.0.0.1:*/api/*"},
		{"localhost:3000/api/*", "*://localhost:3000/api/*"},
		{"[::1]/api/*", "*://[::1]:*/api/*"},
		{"[::1]:8080/api/*", "*://[::1]:8080/api/*"},
	}
	for _, tc := range tests {
		if got := normalizeBlockPattern(tc.in); got != tc.want {
			t.Errorf("normalizeBlockPattern(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBlockURLsHostWithoutPortAndEmptyPattern(t *testing.T) {
	tabID := navigateToFixture(t, "emulation.html")
	defer closeTab(t, tabID)

	// Empty pattern is rejected explicitly before calling Chrome.
	errText := callToolExpectErr(t, "block_urls", map[string]any{
		"tab":      tabID,
		"patterns": []string{""},
	})
	if !strings.Contains(errText, "must not be empty") {
		t.Fatalf("expected empty-pattern validation error, got: %s", errText)
	}

	// Host pattern without explicit port must still block the local test server's ephemeral port.
	callTool[struct{}](t, "block_urls", map[string]any{
		"tab":      tabID,
		"patterns": []string{"127.0.0.1/api/*"},
	})
	type evalResult struct {
		Result string `json:"result"`
	}
	res := callTool[evalResult](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `fetch('/api/data').then(() => 'ok').catch(e => 'blocked: ' + e.message)`,
	})
	if !strings.Contains(res.Result, "blocked") {
		t.Fatalf("expected 127.0.0.1/api/* to block ephemeral-port fetch, got: %q", res.Result)
	}

	// Clear blocks.
	callTool[struct{}](t, "block_urls", map[string]any{
		"tab":      tabID,
		"patterns": []string{},
	})
}

// --- force_pseudo_state ---

func TestForcePseudoStateHoverFocusClearAndCoverageCoexistence(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/style.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		_, _ = w.Write([]byte(`
			#btn { color: rgb(0, 0, 0); background-color: rgb(255, 255, 255); }
			#btn:hover { color: rgb(255, 0, 0); }
			#btn:focus { background-color: rgb(0, 0, 255); }
			#other { color: rgb(0, 0, 0); }
			#other:hover { color: rgb(0, 128, 0); }
		`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><html><head><link rel="stylesheet" href="/style.css"></head><body><button id="btn">Btn</button><button id="other">Other</button><button id="temp">Temp</button></body></html>`))
	})
	mux.HandleFunc("/page2", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><html><body><p id="p2">Page 2</p></body></html>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)
	callTool[NavigateOutput](t, "navigate", map[string]any{"tab": tabID, "url": srv.URL + "/"})

	// Track CSS.styleSheetAdded events to verify CSS.disable actually ran when
	// all forced states clear, when stale forced nodes are pruned, or on navigation.
	tb, err := harness.mgr.ResolveTab("", tabID)
	if err != nil {
		t.Fatalf("ResolveTab: %v", err)
	}
	var sheetAdded atomic.Int64
	lctx, lcancel := context.WithCancel(tb.Context())
	defer lcancel()
	if err := chromedp.Listen(lctx, func(ev any) {
		if _, ok := ev.(*cdpcss.EventStyleSheetAdded); ok {
			sheetAdded.Add(1)
		}
	}); err != nil {
		t.Fatalf("chromedp.Listen: %v", err)
	}
	injectStyleProbe := func() int64 {
		t.Helper()
		before := sheetAdded.Load()
		callTool[EvaluateOutput](t, "evaluate", map[string]any{
			"tab": tabID,
			"expression": `(function() {
				const s = document.createElement('style');
				s.textContent = '.probe-' + Math.random().toString(36).slice(2) + ' { color: pink; }';
				document.head.appendChild(s);
				// Force synchronous Blink style recalculation so CSS.styleSheetAdded
				// is emitted before evaluate returns when the CSS domain is enabled.
				return getComputedStyle(document.body).color;
			})()`,
		})
		return sheetAdded.Load() - before
	}
	assertCSSDisabled := func(label string) {
		t.Helper()
		if delta := injectStyleProbe(); delta != 0 {
			t.Errorf("%s: CSS domain remained enabled (EventStyleSheetAdded incremented by %d)", label, delta)
		}
	}

	getStyle := func(sel string) map[string]string {
		t.Helper()
		q := callTool[QueryOutput](t, "query", map[string]any{
			"tab":            tabID,
			"selector":       sel,
			"computed_style": []string{"color", "background-color"},
		})
		if len(q.Elements) != 1 {
			t.Fatalf("query(%s) returned %d elements", sel, len(q.Elements))
		}
		return q.Elements[0].ComputedStyle
	}

	// Baseline computed style.
	if st := getStyle("#btn"); st["color"] != "rgb(0, 0, 0)" {
		t.Fatalf("baseline #btn color = %q, want rgb(0, 0, 0)", st["color"])
	}

	// Force :hover and :focus (including duplicate entry to verify deduplication).
	forced := callTool[ForcePseudoStateOutput](t, "force_pseudo_state", map[string]any{
		"tab":      tabID,
		"selector": "#btn",
		"states":   []string{"hover", "focus", "hover"},
	})
	if len(forced.States) != 2 || forced.States[0] != "hover" || forced.States[1] != "focus" {
		t.Fatalf("force_pseudo_state returned %v, want [hover focus]", forced.States)
	}
	if st := getStyle("#btn"); st["color"] != "rgb(255, 0, 0)" || st["background-color"] != "rgb(0, 0, 255)" {
		t.Fatalf("forced #btn styles = %+v, want color=rgb(255, 0, 0) background-color=rgb(0, 0, 255)", st)
	}
	// Positive oracle check: while forced pseudo-state is active, CSS domain is enabled.
	if delta := injectStyleProbe(); delta == 0 {
		t.Fatal("expected EventStyleSheetAdded while CSS domain is enabled for forced pseudo-state")
	}

	// Coexistence with get_coverage(type="css"): CSS coverage must still see
	// /style.css AND must preserve active forced pseudo-states afterward.
	cov := callTool[GetCoverageOutput](t, "get_coverage", map[string]any{
		"tab":  tabID,
		"type": "css",
	})
	foundCSS := false
	for _, e := range cov.Entries {
		if strings.HasSuffix(e.URL, "/style.css") && e.TotalBytes > 0 {
			foundCSS = true
			break
		}
	}
	if !foundCSS {
		t.Fatalf("get_coverage(css) while pseudo-state forced missed /style.css: %+v", cov.Entries)
	}
	if st := getStyle("#btn"); st["color"] != "rgb(255, 0, 0)" || st["background-color"] != "rgb(0, 0, 255)" {
		t.Fatalf("after get_coverage(css), #btn forced styles were lost: %+v", st)
	}

	// Force :hover on a second element; clearing #btn must NOT disturb #other.
	callTool[ForcePseudoStateOutput](t, "force_pseudo_state", map[string]any{
		"tab":      tabID,
		"selector": "#other",
		"states":   []string{"hover"},
	})
	rawClearedBtn := callToolRaw(t, "force_pseudo_state", map[string]any{
		"tab":      tabID,
		"selector": "#btn",
		"states":   []string{},
	})
	if rawClearedBtn.IsError || !strings.Contains(contentText(rawClearedBtn), `"states":[]`) {
		t.Fatalf("clearing #btn returned %s (err=%v), want \"states\":[]", contentText(rawClearedBtn), rawClearedBtn.IsError)
	}
	if st := getStyle("#btn"); st["color"] != "rgb(0, 0, 0)" || st["background-color"] != "rgb(255, 255, 255)" {
		t.Fatalf("cleared #btn styles = %+v, want baseline colors", st)
	}
	if st := getStyle("#other"); st["color"] != "rgb(0, 128, 0)" {
		t.Fatalf("#other lost forced :hover when #btn was cleared: %+v", st)
	}

	// Clear #other (omitting states); returns to baseline AND disables CSS domain.
	callTool[ForcePseudoStateOutput](t, "force_pseudo_state", map[string]any{
		"tab":      tabID,
		"selector": "#other",
	})
	if st := getStyle("#other"); st["color"] != "rgb(0, 0, 0)" {
		t.Fatalf("cleared #other styles = %+v, want rgb(0, 0, 0)", st)
	}
	assertCSSDisabled("after clearing last forced element")

	// Stale-node pruning: force :hover on #temp, remove #temp from DOM, then
	// run get_coverage(css); stale node must be pruned and CSS disabled cleanly.
	callTool[ForcePseudoStateOutput](t, "force_pseudo_state", map[string]any{
		"tab":      tabID,
		"selector": "#temp",
		"states":   []string{"hover"},
	})
	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `document.getElementById('temp').remove(); true`,
	})
	callTool[GetCoverageOutput](t, "get_coverage", map[string]any{"tab": tabID, "type": "css"})
	assertCSSDisabled("after get_coverage pruned removed forced element")

	// Cross-document navigation resets forced states and disables CSS asynchronously.
	callTool[ForcePseudoStateOutput](t, "force_pseudo_state", map[string]any{
		"tab":      tabID,
		"selector": "#btn",
		"states":   []string{"hover"},
	})
	callTool[NavigateOutput](t, "navigate", map[string]any{"tab": tabID, "url": srv.URL + "/page2"})
	// Poll briefly for async disableCSSIfIdle to finish after EventFrameNavigated.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if injectStyleProbe() == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("CSS domain stayed enabled after cross-document navigation")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Validation & missing selector errors.
	if errText := callToolExpectErr(t, "force_pseudo_state", map[string]any{"tab": tabID, "selector": "#p2", "states": []string{"bogus"}}); !strings.Contains(errText, "bogus") {
		t.Errorf("invalid pseudo state err = %q, want bogus", errText)
	}
	if errText := callToolExpectErr(t, "force_pseudo_state", map[string]any{"tab": tabID, "selector": "#missing", "states": []string{"hover"}, "timeout": 300}); errText == "" {
		t.Error("expected error for missing selector")
	}
}

// --- emulate_device ---

func TestEmulateDevicePortraitLandscapeAndReset(t *testing.T) {
	// Serve a responsive page with a viewport meta tag so Mobile=true reports
	// the exact CSS viewport dimensions rather than Chromium's 980px fallback.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><html><head><meta name="viewport" content="width=device-width, initial-scale=1"></head><body>Responsive</body></html>`))
	}))
	defer srv.Close()

	tabID := navigateToFixture(t, "emulation.html")
	defer closeTab(t, tabID)
	callTool[NavigateOutput](t, "navigate", map[string]any{"tab": tabID, "url": srv.URL + "/"})

	readEnv := func() struct {
		Width       int     `json:"width"`
		Height      int     `json:"height"`
		DPR         float64 `json:"dpr"`
		UA          string  `json:"ua"`
		TouchPoints int     `json:"touch_points"`
	} {
		t.Helper()
		ev := callTool[EvaluateOutput](t, "evaluate", map[string]any{
			"tab": tabID,
			"expression": `({
				width: window.innerWidth,
				height: window.innerHeight,
				dpr: window.devicePixelRatio,
				ua: navigator.userAgent,
				touch_points: navigator.maxTouchPoints,
			})`,
		})
		var got struct {
			Width       int     `json:"width"`
			Height      int     `json:"height"`
			DPR         float64 `json:"dpr"`
			UA          string  `json:"ua"`
			TouchPoints int     `json:"touch_points"`
		}
		if err := json.Unmarshal(ev.Result, &got); err != nil {
			t.Fatalf("unmarshal env: %v (raw=%s)", err, ev.Result)
		}
		return got
	}

	baseline := readEnv()

	// Portrait iPhone 15 Pro (Safari viewport is 393x659 @3x).
	out := callTool[EmulateDeviceOutput](t, "emulate_device", map[string]any{
		"tab":    tabID,
		"device": "iPhone 15 Pro",
	})
	if out.Device != "iPhone 15 Pro" || out.Width != 393 || out.Height != 659 || out.DeviceScaleFactor != 3 || !out.Mobile || !out.Touch || out.Landscape || !strings.Contains(out.UserAgent, "iPhone") {
		t.Fatalf("emulate_device portrait output = %+v", out)
	}
	env := readEnv()
	if env.Width != 393 || env.Height != 659 || env.DPR != 3 || !strings.Contains(env.UA, "iPhone") || env.TouchPoints == 0 {
		t.Fatalf("portrait page env = %+v, want 393x659 @3x iPhone touch>0", env)
	}

	// Landscape iPhone 15 Pro (Safari landscape viewport is 734x343 @3x).
	outLand := callTool[EmulateDeviceOutput](t, "emulate_device", map[string]any{
		"tab":       tabID,
		"device":    "iPhone 15 Pro",
		"landscape": true,
	})
	if !outLand.Landscape || outLand.Width != 734 || outLand.Height != 343 {
		t.Fatalf("emulate_device landscape output = %+v", outLand)
	}
	envLand := readEnv()
	if envLand.Width != 734 || envLand.Height != 343 {
		t.Fatalf("landscape page env = %+v, want 734x343", envLand)
	}

	// Reset restores desktop UA, touch, DPR, and viewport.
	resetOut := callTool[EmulateDeviceOutput](t, "emulate_device", map[string]any{
		"tab":    tabID,
		"device": "reset",
	})
	if resetOut.Device != "reset" {
		t.Fatalf("reset output = %+v", resetOut)
	}
	envReset := readEnv()
	if envReset.UA != baseline.UA || envReset.TouchPoints != 0 || envReset.Width != baseline.Width || envReset.Height != baseline.Height || envReset.DPR != baseline.DPR {
		t.Fatalf("reset page env = %+v, want baseline %+v", envReset, baseline)
	}

	// Validation errors.
	if errText := callToolExpectErr(t, "emulate_device", map[string]any{"tab": tabID, "device": "Nokia 3310"}); !strings.Contains(errText, "Nokia 3310") {
		t.Errorf("unknown device err = %q, want Nokia 3310", errText)
	}
	if errText := callToolExpectErr(t, "emulate_device", map[string]any{"tab": tabID, "device": "reset", "landscape": true}); !strings.Contains(errText, "landscape") {
		t.Errorf("reset+landscape err = %q, want landscape", errText)
	}
}
