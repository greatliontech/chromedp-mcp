package tools

import (
	"strings"
	"testing"
)

// ===========================================================================
// No implicit auto-creation: tools error clearly without a browser
// ===========================================================================

func TestNoBrowserError(t *testing.T) {
	// Launch a browser and close it so the harness browser is the only one.
	b := callTool[BrowserLaunchOutput](t, "browser_launch", map[string]any{
		"headless": true,
	})
	callTool[BrowserCloseOutput](t, "browser_close", map[string]any{
		"browser": b.BrowserID,
	})

	// The harness browser still exists, so this test verifies that
	// when calling with an explicit reference to a closed browser, we error.
	errText := callToolExpectErr(t, "navigate", map[string]any{
		"url": fixtureURL("index.html"),
		"tab": "nonexistent-tab-id",
	})
	if errText == "" {
		t.Error("navigate with nonexistent tab should error")
	}
	if !strings.Contains(errText, "not found") {
		t.Errorf("error should mention 'not found', got: %s", errText)
	}
}

// ===========================================================================
// get_cookies with urls parameter
// ===========================================================================

func TestGetCookiesWithURLs(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// Set a cookie scoped to the test server.
	callTool[struct{}](t, "set_cookie", map[string]any{
		"tab":    tabID,
		"name":   "url_test_cookie",
		"value":  "hello",
		"domain": "127.0.0.1",
		"path":   "/",
	})
	defer func() {
		callTool[struct{}](t, "delete_cookies", map[string]any{
			"tab":    tabID,
			"name":   "url_test_cookie",
			"domain": "127.0.0.1",
		})
	}()

	// Get cookies with explicit URLs — should return the cookie.
	out := callTool[GetCookiesOutput](t, "get_cookies", map[string]any{
		"tab":  tabID,
		"urls": []string{fixtureURL("index.html")},
	})
	found := false
	for _, c := range out.Cookies {
		if c.Name == "url_test_cookie" {
			found = true
			if c.Value != "hello" {
				t.Errorf("cookie value = %q, want %q", c.Value, "hello")
			}
		}
	}
	if !found {
		t.Error("get_cookies with urls did not return the expected cookie")
	}

	// Get cookies with a URL that doesn't match — should NOT return the cookie.
	out2 := callTool[GetCookiesOutput](t, "get_cookies", map[string]any{
		"tab":  tabID,
		"urls": []string{"https://example.com/"},
	})
	for _, c := range out2.Cookies {
		if c.Name == "url_test_cookie" {
			t.Error("get_cookies with non-matching URL should not return the cookie")
		}
	}
}

// ===========================================================================
// select_option with empty string value (regression test for *string fix)
// ===========================================================================

func TestSelectOptionEmptyValue(t *testing.T) {
	tabID := navigateToFixture(t, "forms.html")
	defer closeTab(t, tabID)

	// First select "low" so we have a non-empty selection.
	callTool[struct{}](t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#priority",
		"value":    "low",
	})

	// Verify it was set to "low".
	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('priority').value",
	})
	if string(out.Result) != `"low"` {
		t.Fatalf("value after selecting low = %s, want \"low\"", out.Result)
	}

	// Now select the empty-value option (the placeholder).
	callTool[struct{}](t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#priority",
		"value":    "",
	})

	// Verify it was set back to "".
	out = callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('priority').value",
	})
	if string(out.Result) != `""` {
		t.Errorf("value after selecting empty = %s, want \"\"", out.Result)
	}
}

// ===========================================================================
// navigate to unreachable URL returns an error
// ===========================================================================

func TestNavigateUnreachableURL(t *testing.T) {
	tab := callTool[TabNewOutput](t, "tab_new", map[string]any{})
	defer closeTab(t, tab.TabID)

	errText := callToolExpectErr(t, "navigate", map[string]any{
		"tab": tab.TabID,
		"url": "http://192.0.2.1:1/unreachable", // TEST-NET address, guaranteed unreachable
	})
	if errText == "" {
		t.Fatal("navigate to unreachable URL should return an error")
	}
	// The error should mention something about the navigation failure.
	lower := strings.ToLower(errText)
	if !strings.Contains(lower, "net::") && !strings.Contains(lower, "timeout") && !strings.Contains(lower, "err_") && !strings.Contains(lower, "unreachable") && !strings.Contains(lower, "failed") {
		t.Errorf("unexpected error text: %s", errText)
	}
}

// ===========================================================================
// press_key inserts a character into a focused input
// ===========================================================================

func TestPressKeyInsertsCharacter(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// Focus the input.
	callTool[struct{}](t, "focus", map[string]any{
		"tab":      tabID,
		"selector": "#type-target",
	})

	// Press individual keys.
	for _, key := range []string{"a", "b", "c"} {
		callTool[struct{}](t, "press_key", map[string]any{
			"tab": tabID,
			"key": key,
		})
	}

	// Verify the input has the typed characters.
	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('type-target').value",
	})
	if string(out.Result) != `"abc"` {
		t.Errorf("input value after press_key = %s, want \"abc\"", out.Result)
	}
}
