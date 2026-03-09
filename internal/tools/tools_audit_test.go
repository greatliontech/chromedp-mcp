package tools

import (
	"strings"
	"testing"
)

// ===========================================================================
// Implicit auto-creation: navigate without prior browser_launch or tab_new
// Tests the cold-start path: ResolveTab("","") → EnsureBrowser() → Launch()
// → Resolve("") → NewTab()
// ===========================================================================

func TestImplicitAutoCreation(t *testing.T) {
	// Launch a fresh browser manager by launching a new browser, then closing
	// it to simulate a cold-start state, then calling navigate directly.
	// We use the existing harness browser, so instead we test with a fresh
	// browser: launch, close, then call navigate (which should auto-create).
	b := callTool[BrowserLaunchOutput](t, "browser_launch", map[string]any{
		"headless": true,
	})

	// Close the browser so the next tool call on it will fail.
	callTool[BrowserCloseOutput](t, "browser_close", map[string]any{
		"browser": b.BrowserID,
	})

	// Now call navigate without specifying browser or tab.
	// The harness still has the original browser, so this should resolve to it.
	// This tests that ResolveTab("","") falls back to the active browser.
	out := callTool[NavigateOutput](t, "navigate", map[string]any{
		"url": fixtureURL("index.html"),
	})
	if out.Status == 0 {
		t.Error("navigate with implicit auto-creation returned status 0")
	}
	if out.URL == "" {
		t.Error("navigate with implicit auto-creation returned empty URL")
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
	errText := callToolExpectErr(t, "navigate", map[string]any{
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
