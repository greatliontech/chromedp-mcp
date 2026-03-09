package tools

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// ===========================================================================
// Gap 1: browser_connect via http:// URL (not just ws://)
// The design doc says url accepts "ws:// or http://". chromedp's
// NewRemoteAllocator should handle http:// → ws:// translation.
// ===========================================================================

func TestBrowserConnectHTTPURL(t *testing.T) {
	// Find Chrome binary.
	chromePath, err := exec.LookPath("chromium")
	if err != nil {
		chromePath, err = exec.LookPath("google-chrome")
		if err != nil {
			t.Skip("chromium/google-chrome not found in PATH")
		}
	}

	port := findFreePort(t)
	tmpDir, err := os.MkdirTemp("", "chromedp-connect-http-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cmd := exec.Command(chromePath,
		"--headless",
		"--no-sandbox",
		"--disable-gpu",
		fmt.Sprintf("--remote-debugging-port=%d", port),
		fmt.Sprintf("--user-data-dir=%s", tmpDir),
		"about:blank",
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start Chrome: %v", err)
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()

	// Wait for Chrome to start.
	_ = getWSURL(t, port)

	// Connect via http:// URL instead of ws:// — chromedp should handle this.
	httpURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	out := callTool[BrowserConnectOutput](t, "browser_connect", map[string]any{
		"url": httpURL,
	})
	if out.BrowserID == "" {
		t.Fatal("browser_connect via http:// returned empty browser ID")
	}

	// Verify it works by creating a tab and evaluating JS.
	tab := callTool[TabNewOutput](t, "tab_new", map[string]any{
		"url":     fixtureURL("index.html"),
		"browser": out.BrowserID,
	})

	eval := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tab.TabID,
		"expression": "document.title",
	})
	if !strings.Contains(string(eval.Result), "Test") {
		t.Errorf("connected via http:// but eval returned unexpected title: %s", eval.Result)
	}

	closeTab(t, tab.TabID)
	callTool[BrowserCloseOutput](t, "browser_close", map[string]any{
		"browser": out.BrowserID,
	})
}

// ===========================================================================
// Gap 2: query silently swallows sub-query errors
// When query returns elements, TextContent/OuterHTML/ComputedStyle/BBox
// failures are silently ignored (the field is just omitted). Test this
// by querying a detached node scenario where sub-queries might fail.
// In practice with live DOM nodes these don't fail, so we verify the
// positive case: all optional fields are populated when requested.
// The key behaviour to test: the tool never errors even if individual
// sub-queries fail — it returns partial data.
// ===========================================================================

func TestQuerySubQueryPartialData(t *testing.T) {
	tabID := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tabID)

	// Query with all optional fields enabled. Verify all are populated
	// for a normal visible element.
	out := callTool[QueryOutput](t, "query", map[string]any{
		"tab":            tabID,
		"selector":       "#title",
		"limit":          1,
		"outer_html":     true,
		"computed_style": []string{"font-size", "color"},
		"bbox":           true,
	})
	if len(out.Elements) == 0 {
		t.Fatal("query returned 0 elements")
	}
	elem := out.Elements[0]

	// All fields should be populated for a visible element.
	if elem.Text == "" {
		t.Error("text should be populated for a visible element")
	}
	if elem.OuterHTML == "" {
		t.Error("outer_html should be populated")
	}
	if len(elem.ComputedStyle) == 0 {
		t.Error("computed_style should have entries")
	}
	if elem.BBox == nil {
		t.Error("bbox should be populated for a visible element")
	}

	// Now query a display:none element. BBox may be zero but shouldn't error.
	// Use JS to add a hidden element.
	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `document.body.insertAdjacentHTML('beforeend', '<div id="hidden-query-test" style="display:none">Hidden</div>')`,
	})

	out2 := callTool[QueryOutput](t, "query", map[string]any{
		"tab":        tabID,
		"selector":   "#hidden-query-test",
		"limit":      1,
		"outer_html": true,
		"bbox":       true,
	})
	if len(out2.Elements) == 0 {
		t.Fatal("query returned 0 elements for hidden element")
	}
	// The tool should NOT error — it silently handles sub-query failures.
	// BBox for display:none is typically nil (Dimensions returns error).
	// This is the expected behaviour: partial data, no error.
	elem2 := out2.Elements[0]
	if elem2.OuterHTML == "" {
		t.Error("outer_html should still work for display:none elements")
	}
	// BBox might be nil for display:none — that's fine, the point is no error.
	t.Logf("hidden element bbox: %v (nil is acceptable)", elem2.BBox)
}

// ===========================================================================
// Gap 4: hover triggers both JS events AND CSS :hover pseudo-class
// Uses CDP Input.dispatchMouseEvent (mouseMoved) which activates the
// browser's native :hover state.
// ===========================================================================

func TestHoverTriggersCSSHover(t *testing.T) {
	tabID := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tabID)

	// Get the background color BEFORE hover.
	bgBefore := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "getComputedStyle(document.getElementById('hover-target')).backgroundColor",
	})

	// Hover over the element.
	callTool[struct{}](t, "hover", map[string]any{
		"tab":      tabID,
		"selector": "#hover-target",
	})

	// Verify JS mouseover event fired (CDP mouseMoved triggers native events).
	eval := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('hover-output').textContent",
	})
	if string(eval.Result) != `"hovered"` {
		t.Errorf("JS mouseover event not fired: got %s", eval.Result)
	}

	// Check background color AFTER hover — CSS :hover SHOULD activate
	// because we use CDP Input.dispatchMouseEvent (mouseMoved).
	bgAfter := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "getComputedStyle(document.getElementById('hover-target')).backgroundColor",
	})

	if string(bgBefore.Result) == string(bgAfter.Result) {
		t.Errorf("CSS :hover not activated: background before=%s, after=%s (should change to yellow)",
			bgBefore.Result, bgAfter.Result)
	}
}

// ===========================================================================
// Gap 5: select_option on <select multiple>
// Tests that select_option by value on a multi-select only selects a
// single option (limitation), and documents the behaviour.
// ===========================================================================

func TestSelectOptionMultipleSelect(t *testing.T) {
	tabID := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tabID)

	// Select "green" by value.
	callTool[struct{}](t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#multi-select",
		"value":    "green",
	})

	// Check which options are selected.
	eval := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "Array.from(document.getElementById('multi-select').selectedOptions).map(o => o.value).join(',')",
	})

	// With .value = "green", only "green" should be selected.
	// The limitation: you can't select multiple options with a single call.
	if string(eval.Result) != `"green"` {
		t.Errorf("selected options = %s, want \"green\"", eval.Result)
	}

	// Now "add" red — with option.selected = true on a multi-select,
	// this adds to the selection rather than replacing.
	callTool[struct{}](t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#multi-select",
		"value":    "red",
	})

	eval2 := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "Array.from(document.getElementById('multi-select').selectedOptions).map(o => o.value).join(',')",
	})

	// With option.selected = true, both "red" and "green" should be selected.
	if string(eval2.Result) != `"red,green"` {
		t.Errorf("after second select, selected = %s, want \"red,green\" (additive multi-select)", eval2.Result)
	}

	// Verify change event still fires.
	eval3 := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('multi-select-output').textContent",
	})
	if !strings.Contains(string(eval3.Result), "multi:") {
		t.Errorf("change event not fired: %s", eval3.Result)
	}
}

// ===========================================================================
// Gap 6: tab_activate MRU order correctness
// Verify that closing the active tab falls back to the most recently
// used tab (not just any tab).
// ===========================================================================

func TestTabMRUOrder(t *testing.T) {
	// Create three tabs: A, B, C.
	tabA := callTool[TabNewOutput](t, "tab_new", map[string]any{
		"url": fixtureURL("index.html"),
	})
	tabB := callTool[TabNewOutput](t, "tab_new", map[string]any{
		"url": fixtureURL("page2.html"),
	})
	tabC := callTool[TabNewOutput](t, "tab_new", map[string]any{
		"url": fixtureURL("forms.html"),
	})

	// MRU order should now be: A, B, C (C is most recent / active).

	// Activate A (MRU becomes: B, C, A).
	callTool[struct{}](t, "tab_activate", map[string]any{
		"tab": tabA.TabID,
	})

	// Close A. MRU fallback should go to C (the next most recent).
	closeTab(t, tabA.TabID)

	// Check which tab is now active.
	tabs := callTool[TabListOutput](t, "tab_list", map[string]any{})
	var activeID string
	for _, tab := range tabs.Tabs {
		if tab.Active {
			activeID = tab.ID
		}
	}

	if activeID != tabC.TabID {
		t.Errorf("after closing A, active tab = %q, want %q (C, the MRU fallback)", activeID, tabC.TabID)
		t.Logf("tabs: B=%s, C=%s", tabB.TabID, tabC.TabID)
	}

	// Clean up.
	closeTab(t, tabB.TabID)
	closeTab(t, tabC.TabID)
}

func TestTabMRUOrderComplexSequence(t *testing.T) {
	// Create tabs A, B, C, D.
	tabA := callTool[TabNewOutput](t, "tab_new", map[string]any{
		"url": fixtureURL("index.html"),
	})
	tabB := callTool[TabNewOutput](t, "tab_new", map[string]any{
		"url": fixtureURL("page2.html"),
	})
	tabC := callTool[TabNewOutput](t, "tab_new", map[string]any{
		"url": fixtureURL("forms.html"),
	})
	tabD := callTool[TabNewOutput](t, "tab_new", map[string]any{
		"url": fixtureURL("interaction.html"),
	})

	// MRU: A, B, C, D (D is active)

	// Activate B → MRU: A, C, D, B
	callTool[struct{}](t, "tab_activate", map[string]any{"tab": tabB.TabID})
	// Activate A → MRU: C, D, B, A
	callTool[struct{}](t, "tab_activate", map[string]any{"tab": tabA.TabID})
	// Activate C → MRU: D, B, A, C
	callTool[struct{}](t, "tab_activate", map[string]any{"tab": tabC.TabID})

	// Close C. Fallback should be A (MRU: D, B, A).
	closeTab(t, tabC.TabID)

	tabs := callTool[TabListOutput](t, "tab_list", map[string]any{})
	var activeAfterCloseC string
	for _, tab := range tabs.Tabs {
		if tab.Active {
			activeAfterCloseC = tab.ID
		}
	}
	if activeAfterCloseC != tabA.TabID {
		t.Errorf("after closing C, active = %q, want %q (A)", activeAfterCloseC, tabA.TabID)
	}

	// Close A. Fallback should be B (MRU: D, B).
	closeTab(t, tabA.TabID)
	tabs2 := callTool[TabListOutput](t, "tab_list", map[string]any{})
	var activeAfterCloseA string
	for _, tab := range tabs2.Tabs {
		if tab.Active {
			activeAfterCloseA = tab.ID
		}
	}
	if activeAfterCloseA != tabB.TabID {
		t.Errorf("after closing A, active = %q, want %q (B)", activeAfterCloseA, tabB.TabID)
	}

	// Clean up.
	closeTab(t, tabB.TabID)
	closeTab(t, tabD.TabID)
}

// ===========================================================================
// Gap 5 extension: select_option by label on <select multiple>
// ===========================================================================

func TestSelectOptionMultipleByLabel(t *testing.T) {
	tabID := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#multi-select",
		"label":    "Blue",
	})

	eval := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('multi-select').value",
	})
	if string(eval.Result) != `"blue"` {
		t.Errorf("selected by label = %s, want \"blue\"", eval.Result)
	}
}

// ===========================================================================
// Gap 5 extension: select_option by index on <select multiple>
// ===========================================================================

func TestSelectOptionMultipleByIndex(t *testing.T) {
	tabID := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tabID)

	idx := 2 // "Blue"
	callTool[struct{}](t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#multi-select",
		"index":    idx,
	})

	eval := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('multi-select').value",
	})
	if string(eval.Result) != `"blue"` {
		t.Errorf("selected by index = %s, want \"blue\"", eval.Result)
	}
}

// ===========================================================================
// navigate wait_until="load" (valid, explicit) should not error
// Complements existing tests that use default and other values.
// ===========================================================================

func TestNavigateWaitUntilLoadValid(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	out := callTool[NavigateOutput](t, "navigate", map[string]any{
		"tab":        tabID,
		"url":        fixtureURL("page2.html"),
		"wait_until": "load",
	})
	if out.Status != 200 {
		t.Errorf("status = %d, want 200", out.Status)
	}
}

// ===========================================================================
// select_option by value with nonexistent value returns an error.
// ===========================================================================

func TestSelectOptionByValueNonexistent(t *testing.T) {
	tabID := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tabID)

	// Set a value that doesn't match any option — should error.
	errText := callToolExpectErr(t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#big-select",
		"value":    "nonexistent",
	})
	if !strings.Contains(strings.ToLower(errText), "no option with value") {
		t.Errorf("expected 'no option with value' error, got: %s", errText)
	}
}

// ===========================================================================
// Click: hidden element (display:none) should timeout with clear error
// ===========================================================================

func TestClickHiddenElement(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// #hidden-btn is display:none. click should fail with a clear message
	// that the element exists but is not visible.
	errText := callToolExpectErr(t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#hidden-btn",
		"timeout":  500,
	})
	if errText == "" {
		t.Fatal("click on hidden element should error")
	}
	if !strings.Contains(errText, "not visible") {
		t.Errorf("error should mention 'not visible', got: %s", errText)
	}
}

// ===========================================================================
// Click: right-click on hidden element should also fail with clear error
// ===========================================================================

func TestClickRightHiddenElement(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#hidden-btn",
		"button":   "right",
		"timeout":  500,
	})
	if errText == "" {
		t.Fatal("right-click on hidden element should error")
	}
	if !strings.Contains(errText, "not visible") {
		t.Errorf("error should mention 'not visible', got: %s", errText)
	}
}

// ===========================================================================
// Click: non-existent element should fail with clear "not found" error
// ===========================================================================

func TestClickNonExistentElement(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#does-not-exist",
		"timeout":  500,
	})
	if errText == "" {
		t.Fatal("click on non-existent element should error")
	}
	if !strings.Contains(errText, "not found") {
		t.Errorf("error should mention 'not found', got: %s", errText)
	}
}

// ===========================================================================
// Click: zero-size element (chromedp clicks it — element exists in DOM)
// ===========================================================================

func TestClickZeroSizeElement(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// chromedp clicks zero-size elements without error (they exist in DOM,
	// just have no visual dimensions). This matches browser behavior —
	// you can programmatically click hidden inputs.
	callTool[struct{}](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#zero-size-input",
	})
}

// ===========================================================================
// Click: Wikipedia-like pattern — click toggle reveals input, then click input
// ===========================================================================

func TestClickToggleRevealsElement(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// The wiki-search-input is initially hidden. Clicking the toggle reveals it.
	callTool[struct{}](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#wiki-search-toggle",
	})

	// Now the input should be visible and clickable.
	callTool[struct{}](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#wiki-search-input",
	})

	// Verify the input is focused.
	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.activeElement.id",
	})
	if string(out.Result) != `"wiki-search-input"` {
		t.Errorf("active element = %s, want wiki-search-input", out.Result)
	}
}

// ===========================================================================
// Press Enter: submits a search form
// ===========================================================================

func TestPressEnterSubmitsForm(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// Type into search input.
	callTool[struct{}](t, "type", map[string]any{
		"tab":      tabID,
		"selector": "#search-input",
		"text":     "test query",
	})

	// Press Enter — should trigger form submission.
	callTool[struct{}](t, "press_key", map[string]any{
		"tab": tabID,
		"key": "Enter",
	})

	// Wait for the submit handler to update the DOM.
	callTool[struct{}](t, "wait_for", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('search-submitted').textContent !== ''",
		"timeout":    3000,
	})

	// Check that the form's submit handler was called.
	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('search-submitted').textContent",
	})
	if string(out.Result) != `"submitted:test query"` {
		t.Errorf("form submit result = %s, want 'submitted:test query'", out.Result)
	}
}

// ===========================================================================
// Press Enter: in a focused input, Enter submits the form
// ===========================================================================

func TestPressEnterAfterFocus(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// Focus the search input explicitly, then type + Enter.
	callTool[struct{}](t, "focus", map[string]any{
		"tab":      tabID,
		"selector": "#search-input",
	})

	// Type using press_key character by character.
	for _, ch := range "hello" {
		callTool[struct{}](t, "press_key", map[string]any{
			"tab": tabID,
			"key": string(ch),
		})
	}

	// Press Enter.
	callTool[struct{}](t, "press_key", map[string]any{
		"tab": tabID,
		"key": "Enter",
	})

	// Wait for the submit handler to update the DOM.
	callTool[struct{}](t, "wait_for", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('search-submitted').textContent !== ''",
		"timeout":    3000,
	})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('search-submitted').textContent",
	})
	if string(out.Result) != `"submitted:hello"` {
		t.Errorf("form submit after focus+type = %s, want 'submitted:hello'", out.Result)
	}
}

// TestGoBackDOMWorks verifies that after go_back, all selector-based DOM
// operations work correctly. This is the key bfcache regression test.
func TestGoBackDOMWorks(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// Navigate to a second page.
	callTool[NavigateOutput](t, "navigate", map[string]any{
		"tab": tabID,
		"url": fixtureURL("interaction.html"),
	})

	// Verify we're on the second page.
	text := callTool[GetTextOutput](t, "get_text", map[string]any{
		"tab":      tabID,
		"selector": "h1",
	})
	if !strings.Contains(text.Text, "Interaction") {
		t.Fatalf("expected interaction page heading, got %q", text.Text)
	}

	// Go back via MCP tool.
	callTool[struct{}](t, "go_back", map[string]any{"tab": tabID})

	// get_text must work after go_back.
	text = callTool[GetTextOutput](t, "get_text", map[string]any{
		"tab":      tabID,
		"selector": "h1",
	})
	if text.Text == "" {
		t.Fatal("get_text returned empty after go_back")
	}
	t.Logf("get_text after go_back: %q", text.Text)

	// query must work after go_back.
	qout := callTool[QueryOutput](t, "query", map[string]any{
		"tab":      tabID,
		"selector": "h1",
	})
	if len(qout.Elements) == 0 {
		t.Fatal("query returned no elements after go_back")
	}

	// click must work after go_back.
	callTool[struct{}](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "h1",
	})

	// get_html must work after go_back.
	html := callTool[GetHTMLOutput](t, "get_html", map[string]any{
		"tab":      tabID,
		"selector": "h1",
	})
	if html.HTML == "" {
		t.Fatal("get_html returned empty after go_back")
	}
}

// TestGoForwardDOMWorks verifies go_forward also resyncs the DOM.
func TestGoForwardDOMWorks(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// Navigate to second page.
	callTool[NavigateOutput](t, "navigate", map[string]any{
		"tab": tabID,
		"url": fixtureURL("interaction.html"),
	})

	// Go back.
	callTool[struct{}](t, "go_back", map[string]any{"tab": tabID})

	// Go forward.
	callTool[struct{}](t, "go_forward", map[string]any{"tab": tabID})

	// Selector-based queries must work after go_forward.
	text := callTool[GetTextOutput](t, "get_text", map[string]any{
		"tab":      tabID,
		"selector": "h1",
	})
	if !strings.Contains(text.Text, "Interaction") {
		t.Fatalf("expected interaction page after go_forward, got %q", text.Text)
	}
}

// TestGoBackMultipleHops verifies go_back works across 3+ pages.
func TestGoBackMultipleHops(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// Build a 3-page history: index -> interaction -> forms.
	callTool[NavigateOutput](t, "navigate", map[string]any{
		"tab": tabID,
		"url": fixtureURL("interaction.html"),
	})
	callTool[NavigateOutput](t, "navigate", map[string]any{
		"tab": tabID,
		"url": fixtureURL("forms.html"),
	})

	// Go back to interaction.
	callTool[struct{}](t, "go_back", map[string]any{"tab": tabID})
	text := callTool[GetTextOutput](t, "get_text", map[string]any{
		"tab":      tabID,
		"selector": "h1",
	})
	if !strings.Contains(text.Text, "Interaction") {
		t.Fatalf("first go_back: expected interaction, got %q", text.Text)
	}

	// Go back to index.
	callTool[struct{}](t, "go_back", map[string]any{"tab": tabID})
	text = callTool[GetTextOutput](t, "get_text", map[string]any{
		"tab":      tabID,
		"selector": "h1",
	})
	if text.Text == "" {
		t.Fatal("second go_back: get_text returned empty")
	}
	t.Logf("second go_back h1: %q", text.Text)
}

// TestGoBackThenInteract verifies you can interact with the page after
// go_back — type into inputs, click buttons, read results.
func TestGoBackThenInteract(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// Navigate away.
	callTool[NavigateOutput](t, "navigate", map[string]any{
		"tab": tabID,
		"url": fixtureURL("index.html"),
	})

	// Go back to interaction page.
	callTool[struct{}](t, "go_back", map[string]any{"tab": tabID})

	// Type into an input.
	callTool[struct{}](t, "type", map[string]any{
		"tab":      tabID,
		"selector": "#type-target",
		"text":     "after-goback",
	})

	// Read the value back.
	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('type-target').value",
	})
	if !strings.Contains(string(out.Result), "after-goback") {
		t.Errorf("type after go_back: value = %s, want 'after-goback'", out.Result)
	}

	// Click a button.
	callTool[struct{}](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#click-target",
	})
	out = callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('click-count').textContent",
	})
	if !strings.Contains(string(out.Result), "1") {
		t.Errorf("click after go_back: count = %s, want '1'", out.Result)
	}
}
