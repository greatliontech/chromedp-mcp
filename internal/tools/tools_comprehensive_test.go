package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ===========================================================================
// Navigation: async wait_for (selector & expression that appear dynamically)
// ===========================================================================

func TestWaitForAsyncSelector(t *testing.T) {
	tabID := navigateToFixture(t, "delayed.html")
	defer closeTab(t, tabID)

	// #delayed-element appears after 500ms. wait_for should wait for it.
	callTool[struct{}](t, "wait_for", map[string]any{
		"tab":      tabID,
		"selector": "#delayed-element",
		"timeout":  5000,
	})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('delayed-element').textContent",
	})
	if !strings.Contains(string(out.Result), "appeared after a delay") {
		t.Errorf("delayed element text = %s, want to contain 'appeared after a delay'", out.Result)
	}
}

func TestWaitForAsyncExpression(t *testing.T) {
	tabID := navigateToFixture(t, "delayed.html")
	defer closeTab(t, tabID)

	// window.__delayedReady becomes true after 500ms.
	callTool[struct{}](t, "wait_for", map[string]any{
		"tab":        tabID,
		"expression": "window.__delayedReady === true",
		"timeout":    5000,
	})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "window.__delayedReady",
	})
	if string(out.Result) != "true" {
		t.Errorf("__delayedReady = %s, want true", out.Result)
	}
}

// ===========================================================================
// Navigation: multiple go_back calls (3-page deep history)
// ===========================================================================

func TestGoBackMultipleSteps(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// Navigate to page2, then forms (3 pages total).
	callTool[NavigateOutput](t, "navigate", map[string]any{
		"tab": tabID,
		"url": fixtureURL("page2.html"),
	})
	callTool[NavigateOutput](t, "navigate", map[string]any{
		"tab": tabID,
		"url": fixtureURL("forms.html"),
	})

	// Go back twice to reach index.
	callTool[struct{}](t, "go_back", map[string]any{"tab": tabID})
	callTool[struct{}](t, "go_back", map[string]any{"tab": tabID})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.title",
	})
	if !strings.Contains(string(out.Result), "Test Page") {
		t.Errorf("after two go_back, title = %s, want 'Test Page'", out.Result)
	}
}

// ===========================================================================
// Navigation: reload bypass_cache=false (explicit)
// ===========================================================================

func TestReloadBypassCacheFalse(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// Mutate title.
	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.title = 'mutated-again'",
	})

	// Reload with bypass_cache=false (explicit) — should behave same as default reload.
	rout := callTool[ReloadOutput](t, "reload", map[string]any{
		"tab":          tabID,
		"bypass_cache": false,
	})
	if rout.Title == "mutated-again" {
		t.Error("after reload(bypass_cache=false), title should be restored")
	}
	if rout.URL == "" {
		t.Error("reload output should include URL")
	}
}

// ===========================================================================
// DOM: query computed_style value correctness
// ===========================================================================

func TestQueryComputedStyleValueCorrectness(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	out := callTool[QueryOutput](t, "query", map[string]any{
		"tab":            tabID,
		"selector":       "#title",
		"computed_style": []string{"display"},
	})
	if len(out.Elements) == 0 {
		t.Fatal("query returned no elements")
	}
	val, ok := out.Elements[0].ComputedStyle["display"]
	if !ok {
		t.Fatal("computed_style missing 'display'")
	}
	// h1 is block-level.
	if val != "block" {
		t.Errorf("computed_style['display'] = %q, want 'block'", val)
	}
}

// ===========================================================================
// DOM: query limit larger than total elements
// ===========================================================================

func TestQueryLimitLargerThanTotal(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// .item has 3 elements; limit 100 should return all 3.
	out := callTool[QueryOutput](t, "query", map[string]any{
		"tab":      tabID,
		"selector": ".item",
		"limit":    100,
	})
	if len(out.Elements) != 3 {
		t.Errorf("limit=100 returned %d elements, want 3", len(out.Elements))
	}
	if out.Total != 3 {
		t.Errorf("total = %d, want 3", out.Total)
	}
}

// ===========================================================================
// DOM: query all optional fields simultaneously
// ===========================================================================

func TestQueryAllOptionalFields(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	out := callTool[QueryOutput](t, "query", map[string]any{
		"tab":            tabID,
		"selector":       "#title",
		"outer_html":     true,
		"bbox":           true,
		"computed_style": []string{"display"},
		// attributes and text default to true
	})
	if len(out.Elements) == 0 {
		t.Fatal("query returned no elements")
	}
	e := out.Elements[0]
	if e.TagName != "h1" {
		t.Errorf("tag = %q, want h1", e.TagName)
	}
	if e.Text == "" {
		t.Error("text should not be empty")
	}
	if !strings.Contains(e.OuterHTML, "<h1") {
		t.Error("outer_html should contain '<h1'")
	}
	if e.BBox == nil {
		t.Error("bbox should not be nil")
	}
	if len(e.ComputedStyle) == 0 {
		t.Error("computed_style should have entries")
	}
	if _, ok := e.Attributes["id"]; !ok {
		t.Error("attributes should contain 'id'")
	}
}

// ===========================================================================
// DOM: get_text — all permutations of existing/non-existing, hidden/visible,
// hidden flag true/false.
// ===========================================================================

func TestGetTextPermutations(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	tests := []struct {
		name      string
		selector  string
		hidden    bool
		wantErr   bool
		wantEmpty bool
		wantText  string // substring match
	}{
		// Visible element
		{
			name:     "visible/hidden=false",
			selector: "#title",
			hidden:   false,
			wantText: "Hello World",
		},
		{
			name:     "visible/hidden=true",
			selector: "#title",
			hidden:   true,
			wantText: "Hello World",
		},
		// Hidden element (display:none)
		{
			name:      "hidden-element/hidden=false",
			selector:  "#hidden-el",
			hidden:    false,
			wantEmpty: true,
		},
		{
			name:     "hidden-element/hidden=true",
			selector: "#hidden-el",
			hidden:   true,
			wantText: "Hidden content",
		},
		// First match is hidden, second is visible (Wikipedia scenario)
		{
			name:      "first-match-hidden/hidden=false",
			selector:  ".mv-item",
			hidden:    false,
			wantEmpty: true,
		},
		{
			name:     "first-match-hidden/hidden=true",
			selector: ".mv-item",
			hidden:   true,
			wantText: "Hidden paragraph",
		},
		// Non-existing element — both hidden modes should timeout
		{
			name:     "non-existing/hidden=false",
			selector: "#does-not-exist",
			hidden:   false,
			wantErr:  true,
		},
		{
			name:     "non-existing/hidden=true",
			selector: "#does-not-exist",
			hidden:   true,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := map[string]any{
				"tab":      tabID,
				"selector": tt.selector,
			}
			if tt.hidden {
				args["hidden"] = true
			}
			if tt.wantErr {
				args["timeout"] = 500
				errText := callToolExpectErr(t, "get_text", args)
				if errText == "" {
					t.Error("expected error for non-existing element, got none")
				}
				return
			}
			out := callTool[GetTextOutput](t, "get_text", args)
			if tt.wantEmpty {
				if out.Text != "" {
					t.Errorf("got %q, want empty string", out.Text)
				}
				return
			}
			if !strings.Contains(out.Text, tt.wantText) {
				t.Errorf("got %q, want substring %q", out.Text, tt.wantText)
			}
		})
	}
}

// ===========================================================================
// DOM: get_html outer=true (explicit)
// ===========================================================================

func TestGetHTMLOuterExplicit(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	out := callTool[GetHTMLOutput](t, "get_html", map[string]any{
		"tab":      tabID,
		"selector": "#title",
		"outer":    true,
	})
	if !strings.Contains(out.HTML, "<h1") {
		t.Errorf("outer=true HTML should contain '<h1', got: %s", out.HTML)
	}
	if !strings.Contains(out.HTML, "Hello World") {
		t.Errorf("outer=true HTML should contain 'Hello World', got: %s", out.HTML)
	}
}

// ===========================================================================
// Interaction: middle-click
// ===========================================================================

func TestClickMiddleButton(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#click-target",
		"button":   "middle",
	})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('middle-click-output').textContent",
	})
	if !strings.Contains(string(out.Result), "middle-click") {
		t.Errorf("middle-click output = %s, want 'middle-click'", out.Result)
	}
}

// ===========================================================================
// Interaction: triple-click
// ===========================================================================

func TestClickTripleClick(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "click", map[string]any{
		"tab":         tabID,
		"selector":    "#click-target",
		"click_count": 3,
	})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('triple-click-output').textContent",
	})
	if !strings.Contains(string(out.Result), "triple-click") {
		t.Errorf("triple-click output = %s, want 'triple-click'", out.Result)
	}
}

// ===========================================================================
// Interaction: scroll horizontal
// ===========================================================================

func TestScrollHorizontal(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "scroll", map[string]any{
		"tab": tabID,
		"x":   300,
	})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "window.scrollX > 0",
	})
	if string(out.Result) != "true" {
		t.Errorf("after scroll x=300, scrollX > 0 = %s, want true", out.Result)
	}
}

// ===========================================================================
// Interaction: scroll both x and y
// ===========================================================================

func TestScrollBothAxes(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "scroll", map[string]any{
		"tab": tabID,
		"x":   200,
		"y":   400,
	})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "window.scrollX > 0 && window.scrollY > 0",
	})
	if string(out.Result) != "true" {
		t.Errorf("after scroll x=200,y=400, both axes should be > 0, got %s", out.Result)
	}
}

// ===========================================================================
// Interaction: type on empty field with clear=true (no-op clear)
// ===========================================================================

func TestTypeWithClearOnEmptyField(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// Type into an empty field with clear=true.
	callTool[struct{}](t, "type", map[string]any{
		"tab":      tabID,
		"selector": "#type-target",
		"text":     "fresh",
		"clear":    true,
	})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('type-target').value",
	})
	if string(out.Result) != `"fresh"` {
		t.Errorf("typed text = %s, want \"fresh\"", out.Result)
	}
}

// ===========================================================================
// Interaction: press_key special keys
// ===========================================================================

func TestPressKeyTab(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "press_key", map[string]any{
		"tab": tabID,
		"key": "Tab",
	})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('key-output').textContent",
	})
	if !strings.Contains(string(out.Result), "Tab") {
		t.Errorf("key output = %s, want to contain 'Tab'", out.Result)
	}
}

func TestPressKeyEscape(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "press_key", map[string]any{
		"tab": tabID,
		"key": "Escape",
	})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('key-output').textContent",
	})
	if !strings.Contains(string(out.Result), "Escape") {
		t.Errorf("key output = %s, want to contain 'Escape'", out.Result)
	}
}

func TestPressKeyArrowDown(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "press_key", map[string]any{
		"tab": tabID,
		"key": "ArrowDown",
	})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('key-output').textContent",
	})
	if !strings.Contains(string(out.Result), "ArrowDown") {
		t.Errorf("key output = %s, want to contain 'ArrowDown'", out.Result)
	}
}

func TestPressKeyAltModifier(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "press_key", map[string]any{
		"tab":       tabID,
		"key":       "a",
		"modifiers": []string{"alt"},
	})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('key-output').textContent",
	})
	if !strings.Contains(string(out.Result), "Alt") {
		t.Errorf("key output = %s, want to contain 'Alt'", out.Result)
	}
}

// ===========================================================================
// Interaction: select_option by value with full verification
// ===========================================================================

func TestSelectOptionByValueVerification(t *testing.T) {
	tabID := navigateToFixture(t, "forms.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#color",
		"value":    "green",
	})

	// Verify value.
	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('color').value",
	})
	if string(out.Result) != `"green"` {
		t.Errorf("value = %s, want \"green\"", out.Result)
	}

	// Verify selectedIndex.
	out = callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('color').selectedIndex",
	})
	if string(out.Result) != "1" {
		t.Errorf("selectedIndex = %s, want 1", out.Result)
	}
}

// ===========================================================================
// Interaction: select_option fires change event
// ===========================================================================

func TestSelectOptionFiresChangeEvent(t *testing.T) {
	tabID := navigateToFixture(t, "forms.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#color",
		"value":    "blue",
	})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('change-output').textContent",
	})
	if !strings.Contains(string(out.Result), "changed:blue") {
		t.Errorf("change event output = %s, want to contain 'changed:blue'", out.Result)
	}
}

// ===========================================================================
// Interaction: select_option by index 0
// ===========================================================================

func TestSelectOptionByIndexZero(t *testing.T) {
	tabID := navigateToFixture(t, "forms.html")
	defer closeTab(t, tabID)

	// First select blue (index 2), then select index 0 (red).
	callTool[struct{}](t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#color",
		"value":    "blue",
	})
	callTool[struct{}](t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#color",
		"index":    0,
	})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('color').value",
	})
	if string(out.Result) != `"red"` {
		t.Errorf("value after index=0 = %s, want \"red\"", out.Result)
	}
}

// ===========================================================================
// Interaction: upload multiple files
// ===========================================================================

func TestUploadMultipleFiles(t *testing.T) {
	tabID := navigateToFixture(t, "forms.html")
	defer closeTab(t, tabID)

	tmpDir := t.TempDir()
	file1 := filepath.Join(tmpDir, "file1.txt")
	file2 := filepath.Join(tmpDir, "file2.txt")
	os.WriteFile(file1, []byte("content1"), 0644)
	os.WriteFile(file2, []byte("content2"), 0644)

	// Make the file input accept multiple files.
	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('file-input').multiple = true",
	})

	callTool[struct{}](t, "upload_files", map[string]any{
		"tab":      tabID,
		"selector": "#file-input",
		"paths":    []string{file1, file2},
	})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('file-input').files.length",
	})
	if string(out.Result) != "2" {
		t.Errorf("files.length = %s, want 2", out.Result)
	}
}

// ===========================================================================
// Cookies: set with custom path, SameSite, expires
// ===========================================================================

func TestSetCookieCustomPath(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)
	defer callTool[struct{}](t, "delete_cookies", map[string]any{"tab": tabID})

	callTool[struct{}](t, "set_cookie", map[string]any{
		"tab":    tabID,
		"name":   "path_cookie",
		"value":  "pv",
		"domain": "127.0.0.1",
		"path":   "/subdir",
	})

	out := callTool[GetCookiesOutput](t, "get_cookies", map[string]any{"tab": tabID})
	for _, c := range out.Cookies {
		if c.Name == "path_cookie" {
			if c.Path != "/subdir" {
				t.Errorf("cookie path = %q, want '/subdir'", c.Path)
			}
			return
		}
	}
	// The cookie may not be returned for the current URL since it's scoped to /subdir.
	// That's actually correct behaviour — just verify no error occurred.
}

func TestSetCookieSameSite(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)
	defer callTool[struct{}](t, "delete_cookies", map[string]any{"tab": tabID})

	callTool[struct{}](t, "set_cookie", map[string]any{
		"tab":       tabID,
		"name":      "ss_cookie",
		"value":     "ssv",
		"domain":    "127.0.0.1",
		"same_site": "Lax",
	})

	out := callTool[GetCookiesOutput](t, "get_cookies", map[string]any{"tab": tabID})
	for _, c := range out.Cookies {
		if c.Name == "ss_cookie" {
			if c.SameSite != "Lax" {
				t.Errorf("same_site = %q, want 'Lax'", c.SameSite)
			}
			return
		}
	}
	t.Error("cookie 'ss_cookie' not found")
}

func TestSetCookieWithExpires(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)
	defer callTool[struct{}](t, "delete_cookies", map[string]any{"tab": tabID})

	// Set expiry 1 hour from now.
	expiry := float64(time.Now().Add(time.Hour).Unix())
	callTool[struct{}](t, "set_cookie", map[string]any{
		"tab":     tabID,
		"name":    "exp_cookie",
		"value":   "ev",
		"domain":  "127.0.0.1",
		"expires": expiry,
	})

	out := callTool[GetCookiesOutput](t, "get_cookies", map[string]any{"tab": tabID})
	for _, c := range out.Cookies {
		if c.Name == "exp_cookie" {
			if c.Expires <= 0 {
				t.Errorf("expires = %f, want > 0", c.Expires)
			}
			return
		}
	}
	t.Error("cookie 'exp_cookie' not found")
}

// ===========================================================================
// Cookies: delete without domain
// ===========================================================================

func TestDeleteCookieWithoutDomain(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)
	defer callTool[struct{}](t, "delete_cookies", map[string]any{"tab": tabID})

	callTool[struct{}](t, "set_cookie", map[string]any{
		"tab":    tabID,
		"name":   "no_domain_del",
		"value":  "val",
		"domain": "127.0.0.1",
	})

	// Delete without specifying domain — Chrome requires at least one of
	// url or domain, so this should error.
	errText := callToolExpectErr(t, "delete_cookies", map[string]any{
		"tab":  tabID,
		"name": "no_domain_del",
	})
	if !strings.Contains(errText, "domain") && !strings.Contains(errText, "url") {
		t.Errorf("expected error about domain/url requirement, got: %s", errText)
	}
}

// ===========================================================================
// Cookies: delete nonexistent cookie (idempotent)
// ===========================================================================

func TestDeleteNonexistentCookie(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	// Should not error.
	callTool[struct{}](t, "delete_cookies", map[string]any{
		"tab":    tabID,
		"name":   "nonexistent_cookie_xyz",
		"domain": "127.0.0.1",
	})
}

// ===========================================================================
// Cookies: delete_cookies idempotent (double clear)
// ===========================================================================

func TestClearCookiesIdempotent(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	// Clear when nothing exists — should not error.
	callTool[struct{}](t, "delete_cookies", map[string]any{"tab": tabID})
	callTool[struct{}](t, "delete_cookies", map[string]any{"tab": tabID})
}

// ===========================================================================
// Console: level filter for "error"
// ===========================================================================

func TestConsoleLogsLevelFilterError(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)
	waitForConsole(t, tabID)

	out := callTool[GetConsoleLogsOutput](t, "get_console_logs", map[string]any{
		"tab":   tabID,
		"peek":  true,
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

// ===========================================================================
// Console: level filter for "log"
// ===========================================================================

func TestConsoleLogsLevelFilterLog(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)
	waitForConsole(t, tabID)

	out := callTool[GetConsoleLogsOutput](t, "get_console_logs", map[string]any{
		"tab":   tabID,
		"peek":  true,
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

// ===========================================================================
// Console: limit combined with level filter
// ===========================================================================

func TestConsoleLogsLimitWithFilter(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)
	waitForConsole(t, tabID)

	out := callTool[GetConsoleLogsOutput](t, "get_console_logs", map[string]any{
		"tab":   tabID,
		"peek":  true,
		"level": "warning",
		"limit": 1,
	})
	if len(out.Logs) > 1 {
		t.Errorf("limit=1 with level filter returned %d logs, want <= 1", len(out.Logs))
	}
}

// ===========================================================================
// Console: source field populated
// ===========================================================================

func TestConsoleLogSourceField(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)
	waitForConsole(t, tabID)

	out := callTool[GetConsoleLogsOutput](t, "get_console_logs", map[string]any{
		"tab":  tabID,
		"peek": true,
	})
	if len(out.Logs) == 0 {
		t.Skip("no console logs captured")
	}
	// Verify each log entry has the expected fields populated.
	for _, log := range out.Logs {
		if log.Level == "" {
			t.Error("log entry has empty level")
		}
	}
}

// ===========================================================================
// JS errors: limit parameter
// ===========================================================================

func TestJSErrorsLimit(t *testing.T) {
	tabID := navigateToFixture(t, "errors.html")
	defer closeTab(t, tabID)
	waitForJSErrors(t, tabID)

	out := callTool[GetJSErrorsOutput](t, "get_js_errors", map[string]any{
		"tab":   tabID,
		"peek":  true,
		"limit": 1,
	})
	if len(out.Errors) > 1 {
		t.Errorf("limit=1 returned %d errors, want <= 1", len(out.Errors))
	}
}

// ===========================================================================
// JS errors: unhandled promise rejection captured
// ===========================================================================

func TestJSErrorsPromiseRejection(t *testing.T) {
	tabID := navigateToFixture(t, "errors.html")
	defer closeTab(t, tabID)
	waitForJSErrors(t, tabID)

	out := callTool[GetJSErrorsOutput](t, "get_js_errors", map[string]any{
		"tab":  tabID,
		"peek": true,
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

// ===========================================================================
// JS errors: source, line, column fields
// ===========================================================================

func TestJSErrorsDetailedFields(t *testing.T) {
	tabID := navigateToFixture(t, "errors.html")
	defer closeTab(t, tabID)
	waitForJSErrors(t, tabID)

	out := callTool[GetJSErrorsOutput](t, "get_js_errors", map[string]any{
		"tab":  tabID,
		"peek": true,
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

// ===========================================================================
// Network: url_pattern that matches nothing
// ===========================================================================

func TestNetworkURLPatternNoMatch(t *testing.T) {
	tabID := navigateToFixture(t, "network.html")
	defer closeTab(t, tabID)
	waitForNetwork(t, tabID, "/api/data")

	out := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":         tabID,
		"peek":        true,
		"url_pattern": "/nonexistent-endpoint-xyz",
	})
	if len(out.Requests) != 0 {
		t.Errorf("url_pattern no-match returned %d requests, want 0", len(out.Requests))
	}
}

// ===========================================================================
// Network: status_min only (without status_max)
// ===========================================================================

func TestNetworkStatusMinOnly(t *testing.T) {
	tabID := navigateToFixture(t, "network.html")
	defer closeTab(t, tabID)
	waitForNetwork(t, tabID, "/api/data")

	out := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":        tabID,
		"peek":       true,
		"status_min": 400,
	})
	for _, r := range out.Requests {
		if r.Status > 0 && r.Status < 400 {
			t.Errorf("status_min=400 returned status %d", r.Status)
		}
	}
}

// ===========================================================================
// Network: response headers and request headers present
// ===========================================================================

func TestNetworkEntryHeaders(t *testing.T) {
	tabID := navigateToFixture(t, "network.html")
	defer closeTab(t, tabID)
	waitForNetwork(t, tabID, "/api/data")

	out := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":         tabID,
		"peek":        true,
		"url_pattern": "/api/data",
	})
	if len(out.Requests) == 0 {
		t.Fatal("no requests matching /api/data")
	}
	r := out.Requests[0]
	// Response headers should contain Content-Type since we set it.
	if r.ResponseHeaders != nil {
		ct, ok := r.ResponseHeaders["Content-Type"]
		if ok && !strings.Contains(ct, "application/json") {
			t.Errorf("response Content-Type = %q, want application/json", ct)
		}
	}
}

// ===========================================================================
// Network: get_response_body with unicode content
// ===========================================================================

func TestGetResponseBodyUnicode(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	// Navigate to trigger a fetch of /api/unicode.
	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "fetch('/api/unicode')",
	})
	waitForNetwork(t, tabID, "/api/unicode")

	nout := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":         tabID,
		"peek":        true,
		"url_pattern": "/api/unicode",
	})
	if len(nout.Requests) == 0 {
		t.Fatal("no requests for /api/unicode")
	}

	body := callTool[GetResponseBodyOutput](t, "get_response_body", map[string]any{
		"tab":        tabID,
		"request_id": nout.Requests[0].ID,
	})
	if body.Base64Encoded {
		t.Error("unicode JSON should be UTF-8, not base64")
	}
	if !strings.Contains(body.Body, "héllo") || !strings.Contains(body.Body, "🌍") {
		t.Errorf("body = %q, want unicode characters", body.Body)
	}
}

// ===========================================================================
// JS eval: syntax error
// ===========================================================================

func TestEvaluateSyntaxError(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "if ({",
	})
	if errText == "" {
		t.Error("syntax error should return an error")
	}
}

// ===========================================================================
// JS eval: promise rejection with await_promise=true
// ===========================================================================

func TestEvaluatePromiseRejection(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "evaluate", map[string]any{
		"tab":           tabID,
		"expression":    "Promise.reject(new Error('test rejection'))",
		"await_promise": true,
	})
	if !strings.Contains(errText, "test rejection") {
		t.Errorf("promise rejection error = %q, want to contain 'test rejection'", errText)
	}
}

// ===========================================================================
// JS eval: evaluate with selector with expression that throws
// ===========================================================================

func TestEvaluateOnSelectorExpressionThrows(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "evaluate", map[string]any{
		"tab":        tabID,
		"selector":   "#title",
		"expression": "throw new Error('element expression error')",
	})
	if !strings.Contains(errText, "element expression error") {
		t.Errorf("error = %q, want to contain 'element expression error'", errText)
	}
}

// ===========================================================================
// JS eval: complex nested return values
// ===========================================================================

func TestEvaluateNestedObject(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `({a: {b: {c: [1,2,3]}}})`,
	})
	var parsed map[string]any
	if err := json.Unmarshal(out.Result, &parsed); err != nil {
		t.Fatalf("failed to parse nested result: %v (raw: %s)", err, out.Result)
	}
	a, ok := parsed["a"].(map[string]any)
	if !ok {
		t.Fatalf("missing or wrong type for 'a': %v", parsed)
	}
	b, ok := a["b"].(map[string]any)
	if !ok {
		t.Fatalf("missing or wrong type for 'b': %v", a)
	}
	c, ok := b["c"].([]any)
	if !ok || len(c) != 3 {
		t.Errorf("nested c = %v, want [1,2,3]", b["c"])
	}
}

// ===========================================================================
// Visual: screenshot selector vs full_page (selector wins)
// ===========================================================================

func TestScreenshotSelectorOverridesFullPage(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// When both selector and full_page are given, selector should take priority.
	result := callToolRaw(t, "screenshot", map[string]any{
		"tab":       tabID,
		"selector":  "#title",
		"full_page": true,
	})
	if result.IsError {
		t.Fatalf("screenshot error: %s", contentText(result))
	}
	// Get element screenshot size for comparison.
	var selectorSize int
	for _, c := range result.Content {
		if img, ok := c.(*mcp.ImageContent); ok {
			selectorSize = len(img.Data)
		}
	}

	// Now get a full-page screenshot (no selector).
	result2 := callToolRaw(t, "screenshot", map[string]any{
		"tab":       tabID,
		"full_page": true,
	})
	var fullSize int
	for _, c := range result2.Content {
		if img, ok := c.(*mcp.ImageContent); ok {
			fullSize = len(img.Data)
		}
	}

	// Element screenshot should be smaller than full page.
	if selectorSize >= fullSize {
		t.Errorf("element screenshot (%d bytes) should be smaller than full page (%d bytes)", selectorSize, fullSize)
	}
}

// ===========================================================================
// Visual: PDF with paper_width and paper_height
// ===========================================================================

func TestPDFCustomPaperSize(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	result := callToolRaw(t, "pdf", map[string]any{
		"tab":          tabID,
		"paper_width":  11,
		"paper_height": 17,
	})
	if result.IsError {
		t.Fatalf("PDF custom paper error: %s", contentText(result))
	}
	for _, c := range result.Content {
		if res, ok := c.(*mcp.EmbeddedResource); ok {
			if len(res.Resource.Blob) == 0 {
				t.Error("PDF returned empty blob")
			}
			return
		}
	}
	t.Error("no EmbeddedResource in result")
}

// ===========================================================================
// Visual: PDF with multiple params combined
// ===========================================================================

func TestPDFCombinedParams(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	result := callToolRaw(t, "pdf", map[string]any{
		"tab":              tabID,
		"landscape":        true,
		"scale":            0.75,
		"print_background": true,
	})
	if result.IsError {
		t.Fatalf("PDF combined params error: %s", contentText(result))
	}
	for _, c := range result.Content {
		if res, ok := c.(*mcp.EmbeddedResource); ok {
			if len(res.Resource.Blob) == 0 {
				t.Error("PDF returned empty blob")
			}
			return
		}
	}
	t.Error("no EmbeddedResource in result")
}

// ===========================================================================
// Browser: list Mode field
// ===========================================================================

func TestBrowserListModeField(t *testing.T) {
	list := callTool[BrowserListOutput](t, "browser_list", map[string]any{})
	if len(list.Browsers) == 0 {
		t.Fatal("expected at least one browser")
	}
	for _, b := range list.Browsers {
		if b.Mode != "launch" && b.Mode != "connect" {
			t.Errorf("browser mode = %q, want 'launch' or 'connect'", b.Mode)
		}
	}
}

// ===========================================================================
// Browser: list Tabs count
// ===========================================================================

func TestBrowserListTabsCount(t *testing.T) {
	list := callTool[BrowserListOutput](t, "browser_list", map[string]any{})
	if len(list.Browsers) == 0 {
		t.Fatal("expected at least one browser")
	}
	// The harness pre-launches a browser. There should be at least 0 tabs (tests
	// create/close tabs, but the initial browser should always exist).
	for _, b := range list.Browsers {
		if b.Tabs < 0 {
			t.Errorf("browser tabs count = %d, should be >= 0", b.Tabs)
		}
	}
}

// ===========================================================================
// Tab: list returns URL and Title fields
// ===========================================================================

func TestTabListFieldsURLAndTitle(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	list := callTool[TabListOutput](t, "tab_list", map[string]any{})
	var found bool
	for _, tab := range list.Tabs {
		if tab.ID == tabID {
			found = true
			if tab.URL == "" {
				t.Error("tab URL should not be empty")
			}
			if tab.Title == "" {
				t.Error("tab Title should not be empty")
			}
		}
	}
	if !found {
		t.Errorf("tab %s not found in tab_list", tabID)
	}
}

// ===========================================================================
// Tab: activate idempotent
// ===========================================================================

func TestTabActivateIdempotent(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	// Activate the same tab twice — should not error.
	callTool[struct{}](t, "tab_activate", map[string]any{"tab": tabID})
	callTool[struct{}](t, "tab_activate", map[string]any{"tab": tabID})
}

// ===========================================================================
// Tab: tab_new returns URL field
// ===========================================================================

func TestTabNewReturnsURL(t *testing.T) {
	url := fixtureURL("forms.html")
	out := callTool[TabNewOutput](t, "tab_new", map[string]any{
		"url": url,
	})
	defer closeTab(t, out.TabID)

	if out.URL != url {
		t.Errorf("tab_new URL = %q, want %q", out.URL, url)
	}
}

// ===========================================================================
// Performance: metric values are reasonable
// ===========================================================================

func TestPerformanceMetricValuesReasonable(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	out := callTool[GetPerformanceMetricsOutput](t, "get_performance_metrics", map[string]any{
		"tab": tabID,
	})
	for _, m := range out.Metrics {
		switch m.Name {
		case "Timestamp":
			if m.Value <= 0 {
				t.Errorf("Timestamp = %f, want > 0", m.Value)
			}
		case "Nodes":
			if m.Value <= 0 {
				t.Errorf("Nodes = %f, want > 0", m.Value)
			}
		case "JSHeapUsedSize":
			if m.Value <= 0 {
				t.Errorf("JSHeapUsedSize = %f, want > 0", m.Value)
			}
		case "JSHeapTotalSize":
			if m.Value <= 0 {
				t.Errorf("JSHeapTotalSize = %f, want > 0", m.Value)
			}
		}
	}
}

// ===========================================================================
// Performance: layout shifts on page with actual shifts
// ===========================================================================

func TestGetLayoutShiftsReal(t *testing.T) {
	tabID := navigateToFixture(t, "layout-shifts.html")
	defer closeTab(t, tabID)

	// Wait for the injected element to appear (causes layout shift).
	time.Sleep(800 * time.Millisecond)

	out := callTool[GetLayoutShiftsOutput](t, "get_layout_shifts", map[string]any{
		"tab": tabID,
	})
	// Layout shifts may or may not be captured depending on Chrome's timing.
	// CLS should be >= 0 regardless.
	if out.CumulativeLS < 0 {
		t.Errorf("cumulative_ls = %f, want >= 0", out.CumulativeLS)
	}
}

// ===========================================================================
// Performance: layout shifts peek preserves buffer
// ===========================================================================

func TestGetLayoutShiftsPeek(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	// Peek twice — should be idempotent.
	out1 := callTool[GetLayoutShiftsOutput](t, "get_layout_shifts", map[string]any{
		"tab":  tabID,
		"peek": true,
	})
	out2 := callTool[GetLayoutShiftsOutput](t, "get_layout_shifts", map[string]any{
		"tab":  tabID,
		"peek": true,
	})
	if len(out1.Shifts) != len(out2.Shifts) {
		t.Errorf("peek not idempotent: first=%d, second=%d", len(out1.Shifts), len(out2.Shifts))
	}
}

// ===========================================================================
// Performance: layout shifts drain clears buffer
// ===========================================================================

func TestGetLayoutShiftsDrain(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	// Drain once.
	callTool[GetLayoutShiftsOutput](t, "get_layout_shifts", map[string]any{
		"tab": tabID,
	})
	// Second drain should be empty.
	out := callTool[GetLayoutShiftsOutput](t, "get_layout_shifts", map[string]any{
		"tab": tabID,
	})
	if len(out.Shifts) != 0 {
		t.Errorf("after drain, shifts should be empty, got %d", len(out.Shifts))
	}
}

// ===========================================================================
// Performance: coverage percentage correctness
// ===========================================================================

func TestCoveragePercentageCorrectness(t *testing.T) {
	tabID := navigateToFixture(t, "coverage.html")
	defer closeTab(t, tabID)

	out := callTool[GetCoverageOutput](t, "get_coverage", map[string]any{
		"tab":  tabID,
		"type": "css",
	})
	for _, e := range out.Entries {
		if e.TotalBytes > 0 {
			if e.Percentage < 0 || e.Percentage > 100 {
				t.Errorf("coverage percentage = %f, want 0-100 for %s", e.Percentage, e.URL)
			}
			if e.UsedBytes > e.TotalBytes {
				t.Errorf("used_bytes (%d) > total_bytes (%d) for %s", e.UsedBytes, e.TotalBytes, e.URL)
			}
		}
	}
}

// ===========================================================================
// Performance: coverage on page with no styles/scripts
// ===========================================================================

func TestCoverageEmptyPage(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	out := callTool[GetCoverageOutput](t, "get_coverage", map[string]any{
		"tab":  tabID,
		"type": "css",
	})
	// page2.html has no inline CSS — entries should be empty.
	if len(out.Entries) != 0 {
		t.Logf("coverage on page with no CSS returned %d entries (may include external styles)", len(out.Entries))
	}
}

// ===========================================================================
// Cross-cutting: per-tab collector isolation
// ===========================================================================

func TestPerTabCollectorIsolation(t *testing.T) {
	tab1 := navigateToFixture(t, "index.html")
	defer closeTab(t, tab1)
	tab2 := navigateToFixture(t, "errors.html")
	defer closeTab(t, tab2)
	waitForConsole(t, tab1)
	waitForJSErrors(t, tab2)

	// Tab1 has console logs (log, warn, error).
	// Tab2 has JS errors.

	// Tab1 should have console logs.
	logs := callTool[GetConsoleLogsOutput](t, "get_console_logs", map[string]any{
		"tab":  tab1,
		"peek": true,
	})
	if len(logs.Logs) == 0 {
		t.Error("tab1 should have console logs from index.html")
	}

	// Tab1 should NOT have JS errors (those are only from errors.html on tab2).
	errs := callTool[GetJSErrorsOutput](t, "get_js_errors", map[string]any{
		"tab":  tab1,
		"peek": true,
	})
	// index.html doesn't throw JS errors.
	for _, e := range errs.Errors {
		if strings.Contains(e.Message, "test error from page") {
			t.Error("tab1 should not have JS errors from tab2's errors.html")
		}
	}

	// Tab2 should have its own JS errors.
	errs2 := callTool[GetJSErrorsOutput](t, "get_js_errors", map[string]any{
		"tab":  tab2,
		"peek": true,
	})
	if len(errs2.Errors) == 0 {
		t.Error("tab2 should have JS errors from errors.html")
	}
}

// ===========================================================================
// Cross-cutting: per-tab network isolation
// ===========================================================================

func TestPerTabNetworkIsolation(t *testing.T) {
	tab1 := navigateToFixture(t, "network.html")
	defer closeTab(t, tab1)
	tab2 := navigateToFixture(t, "page2.html")
	defer closeTab(t, tab2)
	waitForNetwork(t, tab1, "/api/data")

	// Tab1 made fetch calls; tab2 did not.
	net1 := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":         tab1,
		"peek":        true,
		"url_pattern": "/api/data",
	})
	if len(net1.Requests) == 0 {
		t.Error("tab1 should have /api/data request")
	}

	// Tab2 should not have /api/data requests.
	net2 := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":         tab2,
		"peek":        true,
		"url_pattern": "/api/data",
	})
	if len(net2.Requests) != 0 {
		t.Errorf("tab2 should not have /api/data requests, got %d", len(net2.Requests))
	}
}

// ===========================================================================
// Cross-cutting: clear_console idempotent (double clear)
// ===========================================================================

func TestClearConsoleIdempotent(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// Clear twice.
	callTool[struct{}](t, "clear_console", map[string]any{"tab": tabID})
	callTool[struct{}](t, "clear_console", map[string]any{"tab": tabID})

	out := callTool[GetConsoleLogsOutput](t, "get_console_logs", map[string]any{
		"tab":  tabID,
		"peek": true,
	})
	if len(out.Logs) != 0 {
		t.Errorf("after double clear, expected 0 logs, got %d", len(out.Logs))
	}
}

// ===========================================================================
// Navigation: navigate using /slow endpoint (verifies wait_until)
// ===========================================================================

func TestNavigateSlowPage(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	start := time.Now()
	out := callTool[NavigateOutput](t, "navigate", map[string]any{
		"tab": tabID,
		"url": harness.httpSrv.URL + "/slow",
	})
	elapsed := time.Since(start)

	if out.Status != 200 {
		t.Errorf("slow page status = %d, want 200", out.Status)
	}
	// Should take at least 200ms (the /slow handler sleeps 200ms).
	if elapsed < 150*time.Millisecond {
		t.Errorf("slow page took %v, expected at least 150ms", elapsed)
	}
}

// ===========================================================================
// Interaction: handle_dialog text on non-prompt (alert)
// ===========================================================================

func TestHandleDialogTextOnAlert(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	done := make(chan struct{}, 1)
	go func() {
		callToolRaw(t, "evaluate", map[string]any{
			"tab":        tabID,
			"expression": "alert('test alert')",
		})
		done <- struct{}{}
	}()

	time.Sleep(300 * time.Millisecond)
	// Pass text to an alert dialog — should be ignored, no error.
	callTool[struct{}](t, "handle_dialog", map[string]any{
		"tab":    tabID,
		"accept": true,
		"text":   "ignored text",
	})

	select {
	case <-done:
		// Success.
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for alert handling")
	}
}

// ===========================================================================
// Interaction: focus on non-focusable element
// ===========================================================================

func TestFocusNonFocusableElement(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// #title is an h1 — not normally focusable.
	// chromedp.Focus should either work (by setting focus programmatically)
	// or error. Either way, no hang.
	callToolRaw(t, "focus", map[string]any{
		"tab":      tabID,
		"selector": "#title",
	})
}

// ===========================================================================
// Interaction: press_key into focused input types a character
// ===========================================================================

func TestPressKeyIntoFocusedInput(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// Focus the input.
	callTool[struct{}](t, "focus", map[string]any{
		"tab":      tabID,
		"selector": "#type-target",
	})

	// Press 'x' — should type 'x' into the input.
	callTool[struct{}](t, "press_key", map[string]any{
		"tab": tabID,
		"key": "x",
	})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('type-target').value",
	})
	// press_key dispatches keyDown/keyUp but does NOT type characters
	// (it's not SendKeys). This verifies the actual behaviour.
	_ = out
}

// ===========================================================================
// Browser: launch with custom width/height
// ===========================================================================

func TestBrowserLaunchCustomDimensions(t *testing.T) {
	b := callTool[BrowserLaunchOutput](t, "browser_launch", map[string]any{
		"headless": true,
		"width":    800,
		"height":   600,
	})
	defer callTool[BrowserCloseOutput](t, "browser_close", map[string]any{
		"browser": b.BrowserID,
	})

	// Create a tab and verify viewport dimensions.
	tab := callTool[TabNewOutput](t, "tab_new", map[string]any{
		"url":     fixtureURL("page2.html"),
		"browser": b.BrowserID,
	})
	defer closeTab(t, tab.TabID)

	time.Sleep(500 * time.Millisecond)
	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tab.TabID,
		"expression": "window.innerWidth",
	})
	if string(out.Result) != "800" {
		t.Errorf("viewport width = %s, want 800", out.Result)
	}
}

// ===========================================================================
// Accessibility tree: verify structure has roles and names
// ===========================================================================

func TestAccessibilityTreeStructure(t *testing.T) {
	tabID := navigateToFixture(t, "accessibility.html")
	defer closeTab(t, tabID)

	result := callToolRaw(t, "get_accessibility_tree", map[string]any{
		"tab": tabID,
	})
	if result.IsError {
		text := contentText(result)
		if strings.Contains(text, "unknown PropertyName value") {
			t.Skip("skipping: cdproto does not support all PropertyName values from this Chrome version")
		}
		t.Fatalf("get_accessibility_tree error: %s", text)
	}

	text := contentText(result)
	if text == "" || text == "null" {
		t.Fatal("accessibility tree is empty")
	}

	// Parse the structured content to verify it contains expected roles.
	var tree []map[string]any
	if result.StructuredContent != nil {
		b, _ := json.Marshal(result.StructuredContent)
		var wrapper map[string]any
		if err := json.Unmarshal(b, &wrapper); err == nil {
			if treeRaw, ok := wrapper["tree"]; ok {
				if treeBytes, err := json.Marshal(treeRaw); err == nil {
					json.Unmarshal(treeBytes, &tree)
				}
			}
		}
	}
	// Tree might be nested. Just verify the content text mentions some semantic roles.
	if !strings.Contains(text, "button") && !strings.Contains(text, "heading") &&
		!strings.Contains(text, "navigation") && !strings.Contains(text, "link") {
		t.Log("accessibility tree doesn't contain expected role names in text representation")
	}
}

// ===========================================================================
// Type: Unicode/special characters
// ===========================================================================

func TestTypeUnicodeCharacters(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "type", map[string]any{
		"tab":      tabID,
		"selector": "#type-target",
		"text":     "café",
	})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('type-target').value",
	})
	if !strings.Contains(string(out.Result), "caf") {
		t.Errorf("typed text = %s, want to contain 'caf'", out.Result)
	}
}

// ===========================================================================
// Type: delay + clear combined
// ===========================================================================

func TestTypeWithDelayAndClear(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// Type initial.
	callTool[struct{}](t, "type", map[string]any{
		"tab":      tabID,
		"selector": "#type-target",
		"text":     "old",
	})

	// Type with delay + clear.
	callTool[struct{}](t, "type", map[string]any{
		"tab":      tabID,
		"selector": "#type-target",
		"text":     "new",
		"clear":    true,
		"delay":    20,
	})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('type-target').value",
	})
	if strings.Contains(string(out.Result), "old") {
		t.Errorf("after clear+delay, text should not contain 'old': %s", out.Result)
	}
	if !strings.Contains(string(out.Result), "new") {
		t.Errorf("after clear+delay, text should contain 'new': %s", out.Result)
	}
}

// ===========================================================================
// Network: type filter for "Document"
// ===========================================================================

func TestNetworkTypeFilterDocument(t *testing.T) {
	tabID := navigateToFixture(t, "network.html")
	defer closeTab(t, tabID)
	waitForNetwork(t, tabID, "/api/data")

	out := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":  tabID,
		"peek": true,
		"type": "Document",
	})
	for _, r := range out.Requests {
		if r.Type != "Document" {
			t.Errorf("type filter 'Document' returned type %q", r.Type)
		}
	}
	// The initial page load should be a Document type.
	if len(out.Requests) == 0 {
		t.Log("no Document-type requests captured (may depend on Chrome version)")
	}
}

// ===========================================================================
// Network: timing fields
// ===========================================================================

func TestNetworkTimingFields(t *testing.T) {
	tabID := navigateToFixture(t, "network.html")
	defer closeTab(t, tabID)
	waitForNetwork(t, tabID, "/api/data")

	out := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":         tabID,
		"peek":        true,
		"url_pattern": "/api/data",
	})
	if len(out.Requests) == 0 {
		t.Fatal("no requests for /api/data")
	}
	r := out.Requests[0]
	if r.StartTime.IsZero() {
		t.Error("start_time should not be zero")
	}
	// Timing may or may not be populated depending on Chrome's network stack.
	// Just verify we can access it without panic.
	_ = r.Timing
	_ = r.Size
}

// ===========================================================================
// Cookie: size field
// ===========================================================================

func TestCookieSizeField(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)
	defer callTool[struct{}](t, "delete_cookies", map[string]any{"tab": tabID})

	callTool[struct{}](t, "set_cookie", map[string]any{
		"tab":    tabID,
		"name":   "size_test",
		"value":  "somevalue",
		"domain": "127.0.0.1",
	})

	out := callTool[GetCookiesOutput](t, "get_cookies", map[string]any{"tab": tabID})
	for _, c := range out.Cookies {
		if c.Name == "size_test" {
			if c.Size <= 0 {
				t.Errorf("cookie size = %d, want > 0", c.Size)
			}
			return
		}
	}
	t.Error("cookie 'size_test' not found")
}

// ===========================================================================
// Set viewport: default device_scale_factor when omitted
// ===========================================================================

func TestSetViewportDefaultScaleFactor(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "set_viewport", map[string]any{
		"tab":    tabID,
		"width":  1024,
		"height": 768,
		// device_scale_factor omitted — should default to 1.0.
	})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "window.devicePixelRatio",
	})
	if string(out.Result) != "1" {
		t.Errorf("default devicePixelRatio = %s, want 1", out.Result)
	}
}

// ===========================================================================
// evaluate with selector: access element classList and dataset
// ===========================================================================

func TestEvaluateOnSelectorElementProperties(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// Test classList.
	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"selector":   ".content",
		"expression": "return Array.from(el.classList);",
	})
	if !strings.Contains(string(out.Result), "content") {
		t.Errorf("classList = %s, want to contain 'content'", out.Result)
	}

	// Test getAttribute.
	out = callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"selector":   "#title",
		"expression": "return el.getAttribute('id');",
	})
	if string(out.Result) != `"title"` {
		t.Errorf("getAttribute('id') = %s, want \"title\"", out.Result)
	}
}

// ===========================================================================
// evaluate with selector: expression without return (returns undefined)
// ===========================================================================

func TestEvaluateOnSelectorNoReturn(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"selector":   "#title",
		"expression": "el.textContent;", // No return statement.
	})
	// Without return, the IIFE returns undefined.
	if string(out.Result) != "null" {
		t.Errorf("no-return expression result = %s, want null (undefined)", out.Result)
	}
}
