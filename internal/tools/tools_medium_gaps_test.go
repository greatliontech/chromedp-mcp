package tools

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
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
	time.Sleep(500 * time.Millisecond)

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
	time.Sleep(200 * time.Millisecond)

	tabB := callTool[TabNewOutput](t, "tab_new", map[string]any{
		"url": fixtureURL("page2.html"),
	})
	time.Sleep(200 * time.Millisecond)

	tabC := callTool[TabNewOutput](t, "tab_new", map[string]any{
		"url": fixtureURL("forms.html"),
	})
	time.Sleep(200 * time.Millisecond)

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
	time.Sleep(200 * time.Millisecond)

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
