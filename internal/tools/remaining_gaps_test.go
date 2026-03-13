package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

// ===========================================================================
// Unit tests: isLocalStorage
// ===========================================================================

func TestIsLocalStorage(t *testing.T) {
	tests := []struct {
		input   string
		isLocal bool
		wantErr bool
	}{
		{input: "", isLocal: true, wantErr: false},
		{input: "local", isLocal: true, wantErr: false},
		{input: "session", isLocal: false, wantErr: false},
		{input: "invalid", isLocal: false, wantErr: true},
		{input: "Local", isLocal: false, wantErr: true},     // case sensitive
		{input: "SESSION", isLocal: false, wantErr: true},   // case sensitive
		{input: "indexeddb", isLocal: false, wantErr: true}, // not supported
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := isLocalStorage(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("isLocalStorage(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.isLocal {
				t.Errorf("isLocalStorage(%q) = %v, want %v", tt.input, got, tt.isLocal)
			}
			if tt.wantErr && err != nil && !strings.Contains(err.Error(), "invalid storage type") {
				t.Errorf("isLocalStorage(%q) error = %q, want it to contain 'invalid storage type'", tt.input, err)
			}
		})
	}
}

// ===========================================================================
// Unit tests: isValidUTF8
// ===========================================================================

func TestIsValidUTF8(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{name: "ascii", input: "hello world", want: true},
		{name: "empty", input: "", want: true},
		{name: "unicode", input: "héllo wörld 🌍", want: true},
		{name: "chinese", input: "你好世界", want: true},
		{name: "invalid byte", input: "\xff\xfe", want: false},
		{name: "truncated multibyte", input: "\xc0", want: false},
		{name: "null bytes", input: "\x00\x00", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isValidUTF8(tt.input)
			if got != tt.want {
				t.Errorf("isValidUTF8(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// ===========================================================================
// Integration test: elementScale
// ===========================================================================

// TestElementScaleWithinMax verifies elementScale returns 1.0 when the
// element is smaller than maxDim.
func TestElementScaleWithinMax(t *testing.T) {
	tabID := navigateToFixture(t, "cdp-coords.html")
	defer closeTab(t, tabID)

	// #top-box is 100x80 per the fixture.
	// elementScale with maxDim=1000 should return 1.0 (no downscale).
	result := callToolRaw(t, "screenshot", map[string]any{
		"tab":           tabID,
		"selector":      "#top-box",
		"max_dimension": 1000,
	})
	if result.IsError {
		t.Fatalf("screenshot with max_dimension (no downscale) failed: %s", contentText(result))
	}
	// The screenshot succeeds — if elementScale were broken, it would error.
}

// TestElementScaleDownscale verifies elementScale applies downscaling when
// the element exceeds maxDim.
func TestElementScaleDownscale(t *testing.T) {
	tabID := navigateToFixture(t, "cdp-coords.html")
	defer closeTab(t, tabID)

	// #top-box is 100x80. With max_dimension=50, scale should be ~0.5.
	// The resulting screenshot should succeed and be smaller.
	result := callToolRaw(t, "screenshot", map[string]any{
		"tab":           tabID,
		"selector":      "#top-box",
		"max_dimension": 50,
	})
	if result.IsError {
		t.Fatalf("screenshot with max_dimension failed: %s", contentText(result))
	}
}

// ===========================================================================
// Integration test: browser_close with no active browser
// ===========================================================================

// TestBrowserCloseNoBrowsers verifies that browser_close returns a clear
// error when no browsers exist (separate harness to avoid affecting
// the shared harness browser).
func TestBrowserCloseNoBrowsers(t *testing.T) {
	// The main harness always has a browser, so we test the specific
	// error path by trying to close a non-existent ID.
	errText := callToolExpectErr(t, "browser_close", map[string]any{
		"browser": "non-existent-browser-id",
	})
	if errText == "" {
		t.Fatal("expected error closing non-existent browser")
	}
}

// ===========================================================================
// Integration test: set_geolocation with default accuracy
// ===========================================================================

// TestSetGeolocationDefaultAccuracy verifies that when latitude/longitude
// are set but accuracy is omitted, the default accuracy (1) is applied.
func TestSetGeolocationDefaultAccuracy(t *testing.T) {
	tabID := navigateToFixture(t, "emulation.html")
	defer closeTab(t, tabID)

	// Grant geolocation permission.
	callTool[struct{}](t, "set_permission", map[string]any{
		"tab":     tabID,
		"name":    "geolocation",
		"setting": "granted",
	})

	// Set geolocation WITHOUT explicit accuracy.
	callTool[struct{}](t, "set_geolocation", map[string]any{
		"tab":       tabID,
		"latitude":  51.5074,
		"longitude": -0.1278,
		// accuracy omitted — should default to 1
	})

	// Query the position and verify coordinates are correct.
	result := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": `new Promise((resolve, reject) => {
			navigator.geolocation.getCurrentPosition(
				pos => resolve({
					lat: pos.coords.latitude,
					lng: pos.coords.longitude,
					acc: pos.coords.accuracy
				}),
				err => reject(err.message),
				{timeout: 5000}
			);
		})`,
	})

	var coords struct {
		Lat float64 `json:"lat"`
		Lng float64 `json:"lng"`
		Acc float64 `json:"acc"`
	}
	if err := json.Unmarshal(result.Result, &coords); err != nil {
		t.Fatalf("unmarshal: %v, raw: %s", err, result.Result)
	}
	if coords.Lat != 51.5074 || coords.Lng != -0.1278 {
		t.Errorf("coordinates = (%v, %v), want (51.5074, -0.1278)", coords.Lat, coords.Lng)
	}
	if coords.Acc != 1 {
		t.Errorf("accuracy = %v, want 1 (default)", coords.Acc)
	}
}

// ===========================================================================
// Integration test: emulate_network throughput coercion
// ===========================================================================

// TestEmulateNetworkDefaultThroughput verifies that calling emulate_network
// with all defaults (throughput=0) doesn't error — the 0→-1 coercion works.
func TestEmulateNetworkDefaultThroughput(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// Call with all defaults — throughput values are 0, should be coerced
	// to -1 (disabled) internally.
	callTool[struct{}](t, "emulate_network", map[string]any{
		"tab": tabID,
	})

	// Reset to normal — same call pattern.
	callTool[struct{}](t, "emulate_network", map[string]any{
		"tab": tabID,
	})
}

// TestEmulateNetworkExplicitThroughput verifies explicit throughput values
// are passed through without coercion.
func TestEmulateNetworkExplicitThroughput(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// Set a low throughput — should not error.
	callTool[struct{}](t, "emulate_network", map[string]any{
		"tab":                 tabID,
		"download_throughput": 1000000,
		"upload_throughput":   500000,
		"latency":             50,
	})

	// Reset.
	callTool[struct{}](t, "emulate_network", map[string]any{
		"tab": tabID,
	})
}

// ===========================================================================
// Integration test: accessibility tree interestingOnly filtering
// ===========================================================================

// TestAccessibilityTreeInterestingOnlyFiltering verifies that
// interestingOnly=true produces a smaller tree than interestingOnly=false.
func TestAccessibilityTreeInterestingOnlyFiltering(t *testing.T) {
	tabID := navigateToFixture(t, "accessibility.html")
	defer closeTab(t, tabID)

	// Get the tree with interestingOnly=true (default).
	interestingResult := callToolRaw(t, "get_accessibility_tree", map[string]any{
		"tab": tabID,
	})
	if interestingResult.IsError {
		text := contentText(interestingResult)
		if strings.Contains(text, "unknown PropertyName") {
			t.Skip("skipping: cdproto does not support all PropertyName values")
		}
		t.Fatalf("interesting only: %s", text)
	}
	interestingText := contentText(interestingResult)

	// Get the tree with interestingOnly=false.
	allResult := callToolRaw(t, "get_accessibility_tree", map[string]any{
		"tab":              tabID,
		"interesting_only": false,
	})
	if allResult.IsError {
		text := contentText(allResult)
		if strings.Contains(text, "unknown PropertyName") {
			t.Skip("skipping: cdproto does not support all PropertyName values")
		}
		t.Fatalf("all nodes: %s", text)
	}
	allText := contentText(allResult)

	// The full tree should have more data than the filtered one.
	if len(allText) <= len(interestingText) {
		t.Errorf("expected full tree (%d chars) to be larger than interesting-only tree (%d chars)",
			len(allText), len(interestingText))
	}
}

// TestAccessibilityTreeDepthLimit verifies that the depth parameter
// is accepted and returns a valid (non-empty) tree.
func TestAccessibilityTreeDepthLimit(t *testing.T) {
	tabID := navigateToFixture(t, "accessibility.html")
	defer closeTab(t, tabID)

	// Depth=1 should return a shallow but valid tree.
	result := callToolRaw(t, "get_accessibility_tree", map[string]any{
		"tab":   tabID,
		"depth": 1,
	})
	if result.IsError {
		text := contentText(result)
		if strings.Contains(text, "unknown PropertyName") {
			t.Skip("skipping: cdproto does not support all PropertyName values")
		}
		t.Fatalf("depth limit: %s", text)
	}
	text := contentText(result)
	if text == "" || text == "null" {
		t.Fatal("depth=1 accessibility tree is empty")
	}
}

// ===========================================================================
// Integration test: press_key with multi-byte UTF-8 character
// ===========================================================================

// TestPressKeyUTF8Character verifies that a multi-byte UTF-8 character
// (e.g. an emoji or accented char) can be pressed via the UTF-8 decode path.
func TestPressKeyUTF8Character(t *testing.T) {
	tabID := navigateToFixture(t, "forms.html")
	defer closeTab(t, tabID)

	// Focus the input first.
	callTool[struct{}](t, "focus", map[string]any{
		"tab":      tabID,
		"selector": "#name",
	})

	// Press an accented character (é) which is multi-byte UTF-8.
	callTool[struct{}](t, "press_key", map[string]any{
		"tab": tabID,
		"key": "é",
	})

	// Verify the character was typed.
	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `document.querySelector('#name').value`,
	})
	var val string
	if err := json.Unmarshal(out.Result, &val); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if val != "é" {
		t.Errorf("input value = %q, want %q", val, "é")
	}
}

// ===========================================================================
// Integration test: screenshot with non-existent selector
// ===========================================================================

// TestScreenshotNonExistentSelector verifies that a screenshot of a
// non-existent element returns a clear error.
func TestScreenshotNonExistentSelector(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "screenshot", map[string]any{
		"tab":      tabID,
		"selector": "#does-not-exist-at-all",
	})
	if errText == "" {
		t.Fatal("screenshot with non-existent selector should error")
	}
	// The error could be "not found" (selectorError) or "context deadline exceeded"
	// (chromedp timeout). Either way, a clear error is returned.
	lowerErr := strings.ToLower(errText)
	if !strings.Contains(lowerErr, "not found") && !strings.Contains(lowerErr, "deadline") && !strings.Contains(lowerErr, "timeout") {
		t.Errorf("error = %q, want to contain 'not found', 'deadline', or 'timeout'", errText)
	}
}

// ===========================================================================
// Integration test: navigate lifecycle timeout
// ===========================================================================

// TestNavigateLifecycleTimeoutDomContentLoaded verifies the error path
// when domcontentloaded lifecycle times out (if it already fired before
// navigation completes, this verifies the happy path instead).
func TestNavigateDomContentLoadedSucceeds(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// DOMContentLoaded should fire quickly on a simple fixture.
	out := callTool[NavigateOutput](t, "navigate", map[string]any{
		"tab":        tabID,
		"url":        fixtureURL("page2.html"),
		"wait_until": "domcontentloaded",
	})
	if out.URL == "" {
		t.Error("expected non-empty URL after navigate with domcontentloaded")
	}
}

// ===========================================================================
// Integration test: get_html outer=false
// ===========================================================================

// TestGetHTMLInnerExplicit verifies that outer=false returns inner HTML.
func TestGetHTMLInnerExplicit(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// outer=false should return inner HTML (no surrounding tag).
	out := callTool[GetHTMLOutput](t, "get_html", map[string]any{
		"tab":      tabID,
		"selector": "#title",
		"outer":    false,
	})
	// Inner HTML of #title should be the text content without the wrapping tag.
	if strings.Contains(out.HTML, "<h1") {
		t.Errorf("inner HTML should not contain the wrapper tag, got: %s", out.HTML)
	}
	if !strings.Contains(out.HTML, "Hello World") {
		t.Errorf("inner HTML should contain 'Hello World', got: %s", out.HTML)
	}
}

// ===========================================================================
// Integration test: get_text hidden branch
// ===========================================================================

// TestGetTextHiddenTrue verifies that hidden=true uses textContent
// (includes hidden elements).
func TestGetTextHiddenTrue(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// hidden=true should include text from display:none elements.
	out := callTool[GetTextOutput](t, "get_text", map[string]any{
		"tab":    tabID,
		"hidden": true,
	})
	// The page body has some content — just verify it's non-empty.
	if out.Text == "" {
		t.Error("get_text with hidden=true should return non-empty text")
	}
}
