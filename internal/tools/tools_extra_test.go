package tools

import (
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---------------------------------------------------------------------------
// Navigation behaviour
// ---------------------------------------------------------------------------

func TestNavigateReturnsStatusCode(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	out := callTool[NavigateOutput](t, "navigate", map[string]any{
		"url": fixtureURL("page2.html"),
		"tab": tabID,
	})
	if out.Status == 0 {
		t.Error("navigate should return a non-zero HTTP status code")
	}
	if out.Status != 200 {
		t.Errorf("navigate status = %d, want 200", out.Status)
	}
}

func TestNavigateFollowsRedirect(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	out := callTool[NavigateOutput](t, "navigate", map[string]any{
		"url": harness.httpSrv.URL + "/redirect",
		"tab": tabID,
	})
	// The /redirect handler 302s to /page2.html. Because Go's FileServer
	// redirects /index.html to /, we use page2.html as the target.
	if !strings.HasSuffix(out.URL, "/page2.html") {
		t.Errorf("after redirect, URL = %q, want suffix /page2.html", out.URL)
	}
	if out.Title != "Page 2" {
		t.Errorf("after redirect, Title = %q, want 'Page 2'", out.Title)
	}
}

func TestNavigate404(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	out := callTool[NavigateOutput](t, "navigate", map[string]any{
		"url": harness.httpSrv.URL + "/api/not-found",
		"tab": tabID,
	})
	if out.Status != 404 {
		t.Errorf("navigate to 404 page: status = %d, want 404", out.Status)
	}
}

func TestGoForward(t *testing.T) {
	tabID := navigateToFixture(t, "forms.html")
	defer closeTab(t, tabID)

	// Navigate to page2.
	callTool[NavigateOutput](t, "navigate", map[string]any{
		"url": fixtureURL("page2.html"),
		"tab": tabID,
	})
	// Go back to forms.
	callTool[struct{}](t, "go_back", map[string]any{"tab": tabID})
	// Go forward to page2 again.
	callTool[struct{}](t, "go_forward", map[string]any{"tab": tabID})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.title",
	})
	if !strings.Contains(string(out.Result), "Page 2") {
		t.Errorf("after go_forward, title = %s, want to contain 'Page 2'", out.Result)
	}
}

func TestGoBackNoHistory(t *testing.T) {
	// A fresh tab with no navigation has no back history.
	out := callTool[TabNewOutput](t, "tab_new", map[string]any{})
	defer closeTab(t, out.TabID)

	errText := callToolExpectErr(t, "go_back", map[string]any{"tab": out.TabID})
	if !strings.Contains(errText, "no previous history") {
		t.Errorf("go_back error = %q, want 'no previous history'", errText)
	}
}

func TestReload(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// Mutate the page via JS.
	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.title = 'mutated'",
	})
	// Reload should restore the original.
	callTool[struct{}](t, "reload", map[string]any{"tab": tabID})
	// Give the page time to reload.
	time.Sleep(500 * time.Millisecond)

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.title",
	})
	if strings.Contains(string(out.Result), "mutated") {
		t.Errorf("after reload, title should not be 'mutated', got %s", out.Result)
	}
}

// ---------------------------------------------------------------------------
// Wait-for validation & timeout
// ---------------------------------------------------------------------------

func TestWaitForValidation(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	// Neither selector nor expression → error.
	errText := callToolExpectErr(t, "wait_for", map[string]any{
		"tab": tabID,
	})
	if !strings.Contains(errText, "exactly one") {
		t.Errorf("expected validation error, got: %s", errText)
	}

	// Both selector and expression → error.
	errText = callToolExpectErr(t, "wait_for", map[string]any{
		"tab":        tabID,
		"selector":   "#title",
		"expression": "true",
	})
	if !strings.Contains(errText, "exactly one") {
		t.Errorf("expected validation error, got: %s", errText)
	}
}

func TestWaitForTimeout(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "wait_for", map[string]any{
		"tab":      tabID,
		"selector": "#does-not-exist",
		"timeout":  500, // 500ms timeout
	})
	if !strings.Contains(errText, "context deadline") {
		t.Errorf("expected timeout error, got: %s", errText)
	}
}

// ---------------------------------------------------------------------------
// Browser lifecycle
// ---------------------------------------------------------------------------

func TestBrowserLaunchListClose(t *testing.T) {
	// Launch a second browser.
	lo := callTool[BrowserLaunchOutput](t, "browser_launch", map[string]any{
		"headless": true,
	})
	if lo.BrowserID == "" {
		t.Fatal("browser_launch returned empty browser_id")
	}

	// List should show at least 2 browsers.
	list := callTool[BrowserListOutput](t, "browser_list", map[string]any{})
	if len(list.Browsers) < 2 {
		t.Errorf("browser_list returned %d browsers, want >= 2", len(list.Browsers))
	}

	// The new browser should be active.
	var foundActive bool
	for _, b := range list.Browsers {
		if b.ID == lo.BrowserID && b.Active {
			foundActive = true
		}
	}
	if !foundActive {
		t.Errorf("newly launched browser %s is not marked active", lo.BrowserID)
	}

	// Close the new browser.
	callTool[struct{}](t, "browser_close", map[string]any{
		"browser": lo.BrowserID,
	})

	// List should now have one fewer.
	list2 := callTool[BrowserListOutput](t, "browser_list", map[string]any{})
	for _, b := range list2.Browsers {
		if b.ID == lo.BrowserID {
			t.Errorf("closed browser %s still in list", lo.BrowserID)
		}
	}
}

// ---------------------------------------------------------------------------
// Tab semantics
// ---------------------------------------------------------------------------

func TestTabActiveTabSwitch(t *testing.T) {
	// Create tab A.
	a := callTool[TabNewOutput](t, "tab_new", map[string]any{
		"url": fixtureURL("page2.html"),
	})
	defer closeTab(t, a.TabID)

	// Create tab B — should now be active.
	b := callTool[TabNewOutput](t, "tab_new", map[string]any{
		"url": fixtureURL("forms.html"),
	})
	defer closeTab(t, b.TabID)

	// List tabs and verify B is active.
	type tabListOut struct {
		Tabs []struct {
			ID     string `json:"id"`
			Active bool   `json:"active"`
		} `json:"tabs"`
	}
	list := callTool[tabListOut](t, "tab_list", map[string]any{})
	for _, tab := range list.Tabs {
		if tab.ID == b.TabID && !tab.Active {
			t.Error("newly created tab B should be active")
		}
		if tab.ID == a.TabID && tab.Active {
			t.Error("tab A should not be active after creating B")
		}
	}

	// Activate A explicitly.
	callTool[struct{}](t, "tab_activate", map[string]any{"tab": a.TabID})

	list = callTool[tabListOut](t, "tab_list", map[string]any{})
	for _, tab := range list.Tabs {
		if tab.ID == a.TabID && !tab.Active {
			t.Error("tab A should be active after tab_activate")
		}
	}
}

func TestTabCloseActiveSwitches(t *testing.T) {
	// Create two tabs.
	a := callTool[TabNewOutput](t, "tab_new", map[string]any{
		"url": fixtureURL("page2.html"),
	})
	b := callTool[TabNewOutput](t, "tab_new", map[string]any{
		"url": fixtureURL("forms.html"),
	})
	// B is active. Close B — A (MRU) should become active.
	closeTab(t, b.TabID)
	defer closeTab(t, a.TabID)

	type tabListOut struct {
		Tabs []struct {
			ID     string `json:"id"`
			Active bool   `json:"active"`
		} `json:"tabs"`
	}
	list := callTool[tabListOut](t, "tab_list", map[string]any{})
	for _, tab := range list.Tabs {
		if tab.ID == a.TabID && !tab.Active {
			t.Error("after closing active tab, MRU tab should become active")
		}
	}
}

func TestTabInvalidID(t *testing.T) {
	errText := callToolExpectErr(t, "tab_activate", map[string]any{
		"tab": "nonexistent-id",
	})
	if !strings.Contains(errText, "not found") {
		t.Errorf("expected 'not found' error, got: %s", errText)
	}
}

// ---------------------------------------------------------------------------
// Interaction tools
// ---------------------------------------------------------------------------

func TestHover(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "hover", map[string]any{
		"tab":      tabID,
		"selector": "#hover-target",
	})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('hover-target').textContent",
	})
	if !strings.Contains(string(out.Result), "Hovered") {
		t.Errorf("hover target text = %s, want to contain 'Hovered'", out.Result)
	}
}

func TestFocus(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "focus", map[string]any{
		"tab":      tabID,
		"selector": "#type-target",
	})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.activeElement.id",
	})
	if !strings.Contains(string(out.Result), "type-target") {
		t.Errorf("focused element = %s, want 'type-target'", out.Result)
	}
}

func TestTypeWithClear(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// Type initial text.
	callTool[struct{}](t, "type", map[string]any{
		"tab":      tabID,
		"selector": "#type-target",
		"text":     "initial",
	})

	// Type again with clear.
	callTool[struct{}](t, "type", map[string]any{
		"tab":      tabID,
		"selector": "#type-target",
		"text":     "replaced",
		"clear":    true,
	})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('type-target').value",
	})
	if !strings.Contains(string(out.Result), "replaced") {
		t.Errorf("typed text = %s, want 'replaced'", out.Result)
	}
	if strings.Contains(string(out.Result), "initial") {
		t.Errorf("typed text = %s, should not contain 'initial' after clear", out.Result)
	}
}

func TestPressKeyWithModifiers(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "press_key", map[string]any{
		"tab":       tabID,
		"key":       "a",
		"modifiers": []string{"ctrl"},
	})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('key-output').textContent",
	})
	if !strings.Contains(string(out.Result), "Ctrl") {
		t.Errorf("key output = %s, want to contain 'Ctrl'", out.Result)
	}
}

func TestSelectOptionByLabel(t *testing.T) {
	tabID := navigateToFixture(t, "forms.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#color",
		"label":    "Green",
	})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('color').value",
	})
	if string(out.Result) != `"green"` {
		t.Errorf("select value = %s, want \"green\"", out.Result)
	}
}

func TestSelectOptionByIndex(t *testing.T) {
	tabID := navigateToFixture(t, "forms.html")
	defer closeTab(t, tabID)

	idx := 2 // "Blue" is index 2 (Red=0, Green=1, Blue=2)
	callTool[struct{}](t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#color",
		"index":    idx,
	})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('color').value",
	})
	if string(out.Result) != `"blue"` {
		t.Errorf("select value = %s, want \"blue\"", out.Result)
	}
}

func TestSelectOptionValidation(t *testing.T) {
	tabID := navigateToFixture(t, "forms.html")
	defer closeTab(t, tabID)

	// No value, label, or index provided → error.
	errText := callToolExpectErr(t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#color",
	})
	if !strings.Contains(errText, "exactly one") {
		t.Errorf("expected validation error, got: %s", errText)
	}
}

func TestHandleDialog(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	// Set up a dialog handler before triggering the dialog.
	// Chrome auto-dismisses dialogs unless we handle them. We need to
	// trigger the dialog and handle it in the right order. The simplest
	// approach: trigger an alert via evaluate (which blocks until dialog
	// is dismissed), but do so from a separate goroutine.
	done := make(chan struct{})
	go func() {
		defer close(done)
		// This will block until the dialog is handled.
		callToolRaw(t, "evaluate", map[string]any{
			"tab":        tabID,
			"expression": "alert('test dialog')",
		})
	}()

	// Give Chrome time to show the dialog.
	time.Sleep(300 * time.Millisecond)

	// Handle the dialog.
	callTool[struct{}](t, "handle_dialog", map[string]any{
		"tab":    tabID,
		"accept": true,
	})

	// Wait for the evaluate to complete.
	select {
	case <-done:
		// Success — dialog was handled and evaluate returned.
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for dialog handling to complete")
	}
}

func TestScrollByOffset(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "scroll", map[string]any{
		"tab": tabID,
		"y":   500,
	})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "window.scrollY > 0",
	})
	if string(out.Result) != "true" {
		t.Errorf("after scroll by y=500, scrollY > 0 = %s, want true", out.Result)
	}
}

// ---------------------------------------------------------------------------
// DOM tools
// ---------------------------------------------------------------------------

func TestQueryWithLimit(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	out := callTool[QueryOutput](t, "query", map[string]any{
		"tab":      tabID,
		"selector": ".item",
		"limit":    2,
	})
	if out.Total != 3 {
		t.Errorf("query total = %d, want 3", out.Total)
	}
	if len(out.Elements) != 2 {
		t.Errorf("query elements returned = %d, want 2 (limited)", len(out.Elements))
	}
}

func TestGetHTMLInner(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	out := callTool[GetHTMLOutput](t, "get_html", map[string]any{
		"tab":      tabID,
		"selector": "#title",
		"outer":    false,
	})
	// Inner HTML of <h1 id="title">Hello World</h1> should be just "Hello World".
	if strings.Contains(out.HTML, "<h1") {
		t.Errorf("inner HTML should not contain the h1 tag, got: %s", out.HTML)
	}
	if !strings.Contains(out.HTML, "Hello World") {
		t.Errorf("inner HTML should contain 'Hello World', got: %s", out.HTML)
	}
}

func TestGetHTMLDefaultsToFullPage(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	out := callTool[GetHTMLOutput](t, "get_html", map[string]any{
		"tab": tabID,
	})
	// Without a selector, defaults to "html" element which includes the whole page.
	if !strings.Contains(out.HTML, "<html") {
		t.Error("get_html without selector should return full page HTML")
	}
	if !strings.Contains(out.HTML, "Page Two") {
		t.Error("get_html without selector should contain page content")
	}
}

func TestGetTextDefaultsToBody(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	out := callTool[GetTextOutput](t, "get_text", map[string]any{
		"tab": tabID,
	})
	if !strings.Contains(out.Text, "Page Two") {
		t.Errorf("get_text without selector should return body text, got: %q", out.Text)
	}
}

// ---------------------------------------------------------------------------
// Network tools
// ---------------------------------------------------------------------------

func TestGetResponseBody(t *testing.T) {
	tabID := navigateToFixture(t, "network.html")
	defer closeTab(t, tabID)

	// Get network requests to find the /api/data request.
	nout := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":  tabID,
		"peek": true,
	})
	var requestID string
	for _, r := range nout.Requests {
		if strings.Contains(r.URL, "/api/data") && r.Status == 200 {
			requestID = r.ID
			break
		}
	}
	if requestID == "" {
		t.Fatal("could not find /api/data request in network log")
	}

	body := callTool[GetResponseBodyOutput](t, "get_response_body", map[string]any{
		"tab":        tabID,
		"request_id": requestID,
	})
	if !strings.Contains(body.Body, `"status":"ok"`) {
		t.Errorf("response body = %q, want to contain '\"status\":\"ok\"'", body.Body)
	}
	if body.Base64Encoded {
		t.Error("JSON response body should not be base64 encoded")
	}
}

func TestGetNetworkRequestsFilters(t *testing.T) {
	tabID := navigateToFixture(t, "network.html")
	defer closeTab(t, tabID)

	// Filter by URL pattern.
	out := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":         tabID,
		"peek":        true,
		"url_pattern": "/api/data",
	})
	if len(out.Requests) == 0 {
		t.Fatal("url_pattern filter for /api/data returned no results")
	}
	for _, r := range out.Requests {
		if !strings.Contains(r.URL, "/api/data") {
			t.Errorf("url_pattern filter returned unrelated URL: %s", r.URL)
		}
	}

	// Filter by status range for 404s.
	out = callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":        tabID,
		"peek":       true,
		"status_min": 400,
		"status_max": 499,
	})
	for _, r := range out.Requests {
		if r.Status < 400 || r.Status > 499 {
			t.Errorf("status filter returned status %d, want 400-499", r.Status)
		}
	}
}

func TestGetNetworkRequestsDrainVsPeek(t *testing.T) {
	tabID := navigateToFixture(t, "network.html")
	defer closeTab(t, tabID)

	// Peek should not clear the buffer.
	out1 := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":  tabID,
		"peek": true,
	})
	out2 := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":  tabID,
		"peek": true,
	})
	if len(out1.Requests) != len(out2.Requests) {
		t.Errorf("peek should not change buffer: first=%d, second=%d", len(out1.Requests), len(out2.Requests))
	}

	// Drain should clear the buffer.
	callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab": tabID,
	})
	out3 := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":  tabID,
		"peek": true,
	})
	if len(out3.Requests) != 0 {
		t.Errorf("after drain, peek should return 0 requests, got %d", len(out3.Requests))
	}
}

// ---------------------------------------------------------------------------
// Error paths
// ---------------------------------------------------------------------------

func TestEvaluateJSError(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "throw new Error('test error')",
	})
	if !strings.Contains(errText, "test error") {
		t.Errorf("evaluate JS error = %q, want to contain 'test error'", errText)
	}
}

func TestEvaluateOnSelectorNoMatch(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "evaluate", map[string]any{
		"tab":        tabID,
		"selector":   "#nonexistent",
		"expression": "return el.textContent",
	})
	if !strings.Contains(errText, "not found") && !strings.Contains(errText, "matched no elements") {
		t.Errorf("evaluate with selector error = %q, want 'not found' or 'matched no elements'", errText)
	}
}

// ---------------------------------------------------------------------------
// Selector timeout: missing selectors error within the configured timeout.
// The timeout parameter controls how long chromedp polls for the element.
// ---------------------------------------------------------------------------

// selectorTimeout is a helper that verifies a tool call with a missing
// selector returns an error within the configured timeout, and that
// the same tool succeeds with a valid selector.
func selectorTimeout(t *testing.T, tabID, tool string, extraInvalid, extraValid map[string]any) {
	t.Helper()

	// --- invalid selector: must error within timeout ---
	args := map[string]any{
		"tab":      tabID,
		"selector": "#selector-that-does-not-exist-xyz",
		"timeout":  500, // 500ms timeout for quick test turnaround
	}
	for k, v := range extraInvalid {
		args[k] = v
	}

	start := time.Now()
	errText := callToolExpectErr(t, tool, args)
	elapsed := time.Since(start)

	if elapsed > 3*time.Second {
		t.Errorf("%s with invalid selector took %v, want < 3s with 500ms timeout", tool, elapsed)
	}
	if errText == "" {
		t.Errorf("%s with invalid selector should return an error", tool)
	}

	// --- valid selector: must succeed ---
	validArgs := map[string]any{
		"tab": tabID,
	}
	for k, v := range extraValid {
		validArgs[k] = v
	}
	result := callToolRaw(t, tool, validArgs)
	if result.IsError {
		t.Errorf("%s with valid selector failed: %s", tool, contentText(result))
	}
}

func TestClickMissingSelector(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	selectorTimeout(t, tabID, "click",
		nil, // no extra args for invalid
		map[string]any{"selector": "#click-target"}, // valid
	)

	// Verify the valid click actually worked.
	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('click-count').textContent",
	})
	if !strings.Contains(string(out.Result), "1") {
		t.Errorf("after valid click, click count = %s, want to contain '1'", out.Result)
	}
}

func TestDoubleClickMissingSelector(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	selectorTimeout(t, tabID, "click",
		map[string]any{"click_count": 2},                              // invalid
		map[string]any{"selector": "#click-target", "click_count": 2}, // valid
	)
}

func TestTypeMissingSelector(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	selectorTimeout(t, tabID, "type",
		map[string]any{"text": "hello"},                               // invalid
		map[string]any{"selector": "#type-target", "text": "test123"}, // valid
	)

	// Verify the valid type actually worked.
	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('type-target').value",
	})
	if !strings.Contains(string(out.Result), "test123") {
		t.Errorf("after valid type, value = %s, want to contain 'test123'", out.Result)
	}
}

func TestTypeWithClearMissingSelector(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	selectorTimeout(t, tabID, "type",
		map[string]any{"text": "hello", "clear": true},                               // invalid
		map[string]any{"selector": "#type-target", "text": "cleared", "clear": true}, // valid
	)
}

func TestFocusMissingSelector(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	selectorTimeout(t, tabID, "focus",
		nil, // invalid
		map[string]any{"selector": "#type-target"}, // valid
	)

	// Verify focus actually moved.
	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.activeElement.id",
	})
	if !strings.Contains(string(out.Result), "type-target") {
		t.Errorf("after valid focus, activeElement = %s, want 'type-target'", out.Result)
	}
}

func TestScrollIntoViewMissingSelector(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	selectorTimeout(t, tabID, "scroll",
		nil, // invalid
		map[string]any{"selector": "#scroll-marker"}, // valid
	)
}

func TestQueryMissingSelector(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	selectorTimeout(t, tabID, "query",
		nil,                                 // invalid
		map[string]any{"selector": ".item"}, // valid
	)
}

func TestGetHTMLMissingSelector(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	selectorTimeout(t, tabID, "get_html",
		nil,                                  // invalid
		map[string]any{"selector": "#title"}, // valid
	)
}

func TestGetTextMissingSelector(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	selectorTimeout(t, tabID, "get_text",
		nil,                                  // invalid
		map[string]any{"selector": "#title"}, // valid
	)

	// Verify text content returned for valid selector.
	out := callTool[GetTextOutput](t, "get_text", map[string]any{
		"tab":      tabID,
		"selector": "#title",
	})
	if strings.TrimSpace(out.Text) != "Hello World" {
		t.Errorf("get_text valid selector = %q, want 'Hello World'", out.Text)
	}
}

func TestGetAccessibilityTreeMissingSelector(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "get_accessibility_tree", map[string]any{
		"tab":      tabID,
		"selector": "#selector-that-does-not-exist-xyz",
		"timeout":  500,
	})
	if errText == "" {
		t.Error("get_accessibility_tree with missing selector should return an error")
	}
}

func TestUploadFilesMissingSelector(t *testing.T) {
	tabID := navigateToFixture(t, "forms.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "upload_files", map[string]any{
		"tab":      tabID,
		"selector": "#nonexistent-file-input",
		"paths":    []string{"/dev/null"},
		"timeout":  500,
	})
	if errText == "" {
		t.Error("upload_files with missing selector should return an error")
	}
}

func TestSubmitFormMissingSelector(t *testing.T) {
	tabID := navigateToFixture(t, "forms.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "submit_form", map[string]any{
		"tab":      tabID,
		"selector": "#nonexistent-form",
		"timeout":  1000,
	})
	if !strings.Contains(errText, "not found") {
		t.Errorf("error = %q, want to contain 'not found'", errText)
	}
}

func TestHoverMissingSelector(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "hover", map[string]any{
		"tab":      tabID,
		"selector": "#nonexistent-hover",
		"timeout":  1000,
	})
	if errText == "" {
		t.Error("hover on missing selector should return an error")
	}
}

func TestSelectOptionMissingSelector(t *testing.T) {
	tabID := navigateToFixture(t, "forms.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#nonexistent-select",
		"value":    "red",
	})
	if errText == "" {
		t.Error("select_option with missing selector should return an error")
	}
}

// ---------------------------------------------------------------------------
// Selector timeout: element appearing dynamically succeeds within timeout
// ---------------------------------------------------------------------------

func TestClickDelayedElement(t *testing.T) {
	tabID := navigateToFixture(t, "delayed.html")
	defer closeTab(t, tabID)

	// #delayed-element appears after 500ms. With default 5s timeout,
	// chromedp should poll and find it.
	callTool[struct{}](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#delayed-element",
	})
	// If we get here without error, the element was found after it appeared.
}

func TestQueryDelayedElement(t *testing.T) {
	tabID := navigateToFixture(t, "delayed.html")
	defer closeTab(t, tabID)

	out := callTool[QueryOutput](t, "query", map[string]any{
		"tab":      tabID,
		"selector": "#delayed-element",
	})
	if out.Total != 1 {
		t.Errorf("query delayed element total = %d, want 1", out.Total)
	}
	if len(out.Elements) > 0 && !strings.Contains(out.Elements[0].Text, "appeared") {
		t.Errorf("delayed element text = %q, want to contain 'appeared'", out.Elements[0].Text)
	}
}

// ---------------------------------------------------------------------------
// Cookies
// ---------------------------------------------------------------------------

func TestClearCookies(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	// Set two cookies.
	callTool[struct{}](t, "set_cookie", map[string]any{
		"tab":    tabID,
		"name":   "cookie_a",
		"value":  "val_a",
		"domain": "127.0.0.1",
	})
	callTool[struct{}](t, "set_cookie", map[string]any{
		"tab":    tabID,
		"name":   "cookie_b",
		"value":  "val_b",
		"domain": "127.0.0.1",
	})

	// Clear all cookies.
	callTool[struct{}](t, "delete_cookies", map[string]any{"tab": tabID})

	// Verify none remain.
	out := callTool[GetCookiesOutput](t, "get_cookies", map[string]any{"tab": tabID})
	if len(out.Cookies) != 0 {
		t.Errorf("after delete_cookies, expected 0 cookies, got %d", len(out.Cookies))
	}
}

// ---------------------------------------------------------------------------
// Console drain vs peek semantics
// ---------------------------------------------------------------------------

func TestConsoleLogsDrainClearsBuffer(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// First drain — should return logs.
	out1 := callTool[GetConsoleLogsOutput](t, "get_console_logs", map[string]any{
		"tab": tabID,
	})
	if len(out1.Logs) == 0 {
		t.Fatal("expected console logs from index.html, got none")
	}

	// Second drain — buffer should be empty now.
	out2 := callTool[GetConsoleLogsOutput](t, "get_console_logs", map[string]any{
		"tab": tabID,
	})
	if len(out2.Logs) != 0 {
		t.Errorf("after drain, expected 0 logs, got %d", len(out2.Logs))
	}
}

func TestConsoleLogsLevelFilter(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	out := callTool[GetConsoleLogsOutput](t, "get_console_logs", map[string]any{
		"tab":   tabID,
		"peek":  true,
		"level": "warning",
	})
	for _, log := range out.Logs {
		if log.Level != "warning" {
			t.Errorf("level filter returned log with level %q, want 'warning'", log.Level)
		}
	}
	if len(out.Logs) == 0 {
		t.Error("expected at least one warning log from index.html")
	}
}

// ---------------------------------------------------------------------------
// Visual tools
// ---------------------------------------------------------------------------

func TestFullPageScreenshot(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	result := callToolRaw(t, "screenshot", map[string]any{
		"tab":       tabID,
		"full_page": true,
	})
	if result.IsError {
		t.Fatalf("full_page screenshot error: %s", contentText(result))
	}
	var found bool
	for _, c := range result.Content {
		if img, ok := c.(*mcp.ImageContent); ok {
			found = true
			if len(img.Data) == 0 {
				t.Error("full_page screenshot returned empty data")
			}
		}
	}
	if !found {
		t.Error("full_page screenshot did not return ImageContent")
	}
}

func TestPDF(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	result := callToolRaw(t, "pdf", map[string]any{"tab": tabID})
	if result.IsError {
		t.Fatalf("pdf error: %s", contentText(result))
	}
	var found bool
	for _, c := range result.Content {
		if res, ok := c.(*mcp.EmbeddedResource); ok {
			found = true
			if res.Resource.MIMEType != "application/pdf" {
				t.Errorf("pdf MIME = %q, want application/pdf", res.Resource.MIMEType)
			}
			if len(res.Resource.Blob) == 0 {
				t.Error("pdf returned empty blob")
			}
		}
	}
	if !found {
		t.Error("pdf did not return EmbeddedResource")
	}
}

// ---------------------------------------------------------------------------
// JS evaluate edge cases
// ---------------------------------------------------------------------------

func TestEvaluateReturnsVariousTypes(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	tests := []struct {
		expr string
		want string
	}{
		{"42", "42"},
		{`"hello"`, `"hello"`},
		{"true", "true"},
		{"null", "null"},
		{"[1,2,3]", "[1,2,3]"},
		{`({a: 1})`, `{"a":1}`},
	}
	for _, tc := range tests {
		out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
			"tab":        tabID,
			"expression": tc.expr,
		})
		got := string(out.Result)
		if got != tc.want {
			t.Errorf("evaluate(%s) = %s, want %s", tc.expr, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Performance: coverage
// ---------------------------------------------------------------------------

func TestGetCoverage(t *testing.T) {
	tabID := navigateToFixture(t, "coverage.html")
	defer closeTab(t, tabID)

	// Get JS coverage. The profiler only captures scripts that run while
	// it is active, so inline scripts from page load may not appear. We
	// primarily verify the tool runs without error and returns a valid
	// structure.
	out := callTool[GetCoverageOutput](t, "get_coverage", map[string]any{
		"tab":  tabID,
		"type": "js",
	})
	// Entries may be empty if no scripts executed during the profiling
	// window. Just verify the tool works.
	_ = out.Entries

	// CSS coverage should capture stylesheets since tracking starts/stops
	// inline and the page has <style> blocks.
	outCSS := callTool[GetCoverageOutput](t, "get_coverage", map[string]any{
		"tab":  tabID,
		"type": "css",
	})
	if len(outCSS.Entries) == 0 {
		t.Error("get_coverage(css) returned no entries for a page with stylesheets")
	}
	// Verify at least one entry has non-zero total bytes.
	var hasBytes bool
	for _, e := range outCSS.Entries {
		if e.TotalBytes > 0 {
			hasBytes = true
			break
		}
	}
	if !hasBytes {
		t.Error("get_coverage(css) entries all have zero total_bytes")
	}
}
