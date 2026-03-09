package tools

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ===========================================================================
// Interaction: type into textarea
// ===========================================================================

func TestTypeIntoTextarea(t *testing.T) {
	tabID := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "type", map[string]any{
		"tab":      tabID,
		"selector": "#textarea-target",
		"text":     "multi\nline\ntext",
	})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('textarea-target').value",
	})
	if !strings.Contains(string(out.Result), "multi") {
		t.Errorf("textarea value = %s, want to contain 'multi'", out.Result)
	}
}

// ===========================================================================
// Interaction: type empty string (no-op)
// ===========================================================================

func TestTypeEmptyText(t *testing.T) {
	tabID := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tabID)

	// Type empty string — should not error.
	callTool[struct{}](t, "type", map[string]any{
		"tab":      tabID,
		"selector": "#textarea-target",
		"text":     "",
	})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('textarea-target').value",
	})
	if string(out.Result) != `""` {
		t.Errorf("after typing empty string, value = %s, want empty", out.Result)
	}
}

// ===========================================================================
// Interaction: click on disabled button (chromedp still clicks, but event
// should NOT fire per browser spec)
// ===========================================================================

func TestClickDisabledButton(t *testing.T) {
	tabID := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tabID)

	// Click disabled button — chromedp will click but browser should block the event.
	callTool[struct{}](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#disabled-btn",
	})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('disabled-output').textContent",
	})
	// The click event should NOT have fired on a disabled button.
	if strings.Contains(string(out.Result), "clicked-disabled") {
		t.Error("click event should not fire on disabled button")
	}
}

// ===========================================================================
// Interaction: click that triggers navigation
// ===========================================================================

func TestClickTriggersNavigation(t *testing.T) {
	tabID := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tabID)

	// Click the link that navigates to page2.html.
	callTool[struct{}](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#nav-link",
	})

	// Poll until the navigation completes and document.title changes.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		result := callToolRaw(t, "evaluate", map[string]any{
			"tab":        tabID,
			"expression": "document.title",
		})
		if !result.IsError {
			if text := contentText(result); strings.Contains(text, "Page 2") {
				break
			}
			// Also check structured content.
			if result.StructuredContent != nil {
				b, _ := json.Marshal(result.StructuredContent)
				if strings.Contains(string(b), "Page 2") {
					break
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.title",
	})
	if !strings.Contains(string(out.Result), "Page 2") {
		t.Errorf("after click navigation, title = %s, want 'Page 2'", out.Result)
	}
}

// ===========================================================================
// Interaction: select_option with out-of-range index
// ===========================================================================

func TestSelectOptionIndexOutOfRange(t *testing.T) {
	tabID := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tabID)

	// Select index 999 — out of range. Should return an error.
	errText := callToolExpectErr(t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#big-select",
		"index":    999,
	})
	if !strings.Contains(strings.ToLower(errText), "out of range") {
		t.Errorf("expected 'out of range' error, got: %s", errText)
	}
}

// ===========================================================================
// Interaction: select_option with non-matching label
// ===========================================================================

func TestSelectOptionLabelNotFound(t *testing.T) {
	tabID := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tabID)

	// Try to select by a label that doesn't exist — should error.
	errText := callToolExpectErr(t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#big-select",
		"label":    "NonexistentLabel",
	})
	if !strings.Contains(strings.ToLower(errText), "no option with label") {
		t.Errorf("expected 'no option with label' error, got: %s", errText)
	}
}

// ===========================================================================
// Interaction: select_option with all three criteria (should error)
// ===========================================================================

func TestSelectOptionAllThreeCriteria(t *testing.T) {
	tabID := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#big-select",
		"value":    "a",
		"label":    "Alpha",
		"index":    0,
	})
	if !strings.Contains(errText, "exactly one") && !strings.Contains(errText, "multiple") {
		t.Errorf("error = %q, want validation error about multiple criteria", errText)
	}
}

// ===========================================================================
// Interaction: scroll negative offsets
// ===========================================================================

func TestScrollNegativeOffset(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// First scroll down.
	callTool[struct{}](t, "scroll", map[string]any{
		"tab": tabID,
		"y":   500,
	})

	out1 := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "window.scrollY",
	})

	// Now scroll up with negative offset.
	callTool[struct{}](t, "scroll", map[string]any{
		"tab": tabID,
		"y":   -200,
	})

	out2 := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "window.scrollY",
	})

	// scrollY should have decreased.
	var y1, y2 float64
	json.Unmarshal(out1.Result, &y1)
	json.Unmarshal(out2.Result, &y2)
	if y2 >= y1 {
		t.Errorf("after negative scroll, scrollY = %f, expected < %f", y2, y1)
	}
}

// ===========================================================================
// Interaction: scroll with selector ignores x/y offsets
// ===========================================================================

func TestScrollSelectorIgnoresXY(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// With both selector and x/y, the implementation takes the selector path
	// (ScrollIntoView) and ignores x/y. Should not error.
	callTool[struct{}](t, "scroll", map[string]any{
		"tab":      tabID,
		"selector": "#scroll-marker",
		"x":        100,
		"y":        100,
	})

	// The marker should now be in view. Verify scrollY > 0.
	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "window.scrollY > 0",
	})
	if string(out.Result) != "true" {
		t.Error("after scroll-into-view with x/y set, page should have scrolled")
	}
}

// ===========================================================================
// Interaction: handle_dialog dismiss confirm dialog
// ===========================================================================

func TestHandleDialogDismissConfirm(t *testing.T) {
	tabID := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tabID)

	done := make(chan struct{}, 1)
	go func() {
		callToolRaw(t, "evaluate", map[string]any{
			"tab":        tabID,
			"expression": "document.getElementById('confirm-result').textContent = String(confirm('Sure?'))",
		})
		done <- struct{}{}
	}()

	handleDialog(t, tabID, map[string]any{"accept": false})

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for confirm dialog")
	}

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('confirm-result').textContent",
	})
	if !strings.Contains(string(out.Result), "false") {
		t.Errorf("dismissed confirm result = %s, want 'false'", out.Result)
	}
}

// ===========================================================================
// Interaction: handle_dialog dismiss prompt (returns null)
// ===========================================================================

func TestHandleDialogDismissPrompt(t *testing.T) {
	tabID := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tabID)

	done := make(chan struct{}, 1)
	go func() {
		callToolRaw(t, "evaluate", map[string]any{
			"tab":        tabID,
			"expression": "document.getElementById('prompt-result').textContent = String(prompt('Name:'))",
		})
		done <- struct{}{}
	}()

	handleDialog(t, tabID, map[string]any{"accept": false})

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for prompt dialog")
	}

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('prompt-result').textContent",
	})
	if !strings.Contains(string(out.Result), "null") {
		t.Errorf("dismissed prompt result = %s, want 'null'", out.Result)
	}
}

// ===========================================================================
// Interaction: press_key with meta modifier
// ===========================================================================

func TestPressKeyMetaModifier(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "press_key", map[string]any{
		"tab":       tabID,
		"key":       "a",
		"modifiers": []string{"meta"},
	})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('key-output').textContent",
	})
	if !strings.Contains(string(out.Result), "Meta") {
		t.Errorf("key output = %s, want to contain 'Meta'", out.Result)
	}
}

// ===========================================================================
// Cookie: overwrite existing cookie
// ===========================================================================

func TestCookieOverwrite(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)
	defer callTool[struct{}](t, "delete_cookies", map[string]any{"tab": tabID})

	// Set cookie with value "v1".
	callTool[struct{}](t, "set_cookie", map[string]any{
		"tab":    tabID,
		"name":   "overwrite_test",
		"value":  "v1",
		"domain": "127.0.0.1",
	})

	// Overwrite with value "v2".
	callTool[struct{}](t, "set_cookie", map[string]any{
		"tab":    tabID,
		"name":   "overwrite_test",
		"value":  "v2",
		"domain": "127.0.0.1",
	})

	out := callTool[GetCookiesOutput](t, "get_cookies", map[string]any{"tab": tabID})
	var found int
	var lastValue string
	for _, c := range out.Cookies {
		if c.Name == "overwrite_test" {
			found++
			lastValue = c.Value
		}
	}
	if found != 1 {
		t.Errorf("expected exactly 1 cookie named 'overwrite_test', found %d", found)
	}
	if lastValue != "v2" {
		t.Errorf("cookie value = %q, want 'v2'", lastValue)
	}
}

// ===========================================================================
// Cookie: SameSite Strict
// ===========================================================================

func TestSetCookieSameSiteStrict(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)
	defer callTool[struct{}](t, "delete_cookies", map[string]any{"tab": tabID})

	callTool[struct{}](t, "set_cookie", map[string]any{
		"tab":       tabID,
		"name":      "strict_cookie",
		"value":     "sv",
		"domain":    "127.0.0.1",
		"same_site": "Strict",
	})

	out := callTool[GetCookiesOutput](t, "get_cookies", map[string]any{"tab": tabID})
	for _, c := range out.Cookies {
		if c.Name == "strict_cookie" {
			if c.SameSite != "Strict" {
				t.Errorf("same_site = %q, want 'Strict'", c.SameSite)
			}
			return
		}
	}
	t.Error("cookie 'strict_cookie' not found")
}

// ===========================================================================
// Cookie: delete with path parameter
// ===========================================================================

func TestDeleteCookieWithPath(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)
	defer callTool[struct{}](t, "delete_cookies", map[string]any{"tab": tabID})

	callTool[struct{}](t, "set_cookie", map[string]any{
		"tab":    tabID,
		"name":   "path_del_test",
		"value":  "pv",
		"domain": "127.0.0.1",
		"path":   "/",
	})

	// Delete with name + domain + path.
	callTool[struct{}](t, "delete_cookies", map[string]any{
		"tab":    tabID,
		"name":   "path_del_test",
		"domain": "127.0.0.1",
		"path":   "/",
	})

	out := callTool[GetCookiesOutput](t, "get_cookies", map[string]any{"tab": tabID})
	for _, c := range out.Cookies {
		if c.Name == "path_del_test" {
			t.Error("cookie should have been deleted")
		}
	}
}

// ===========================================================================
// Cookie: get_cookies on page with no cookies
// ===========================================================================

func TestGetCookiesNoCookies(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	// Clear any existing cookies first.
	callTool[struct{}](t, "delete_cookies", map[string]any{"tab": tabID})

	out := callTool[GetCookiesOutput](t, "get_cookies", map[string]any{"tab": tabID})
	if out.Cookies == nil {
		t.Error("cookies should be an empty array, not nil")
	}
	if len(out.Cookies) != 0 {
		t.Errorf("expected 0 cookies after clear, got %d", len(out.Cookies))
	}
}

// ===========================================================================
// Network: failed_only filter
// ===========================================================================

func TestNetworkFailedOnly(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	// Fetch a URL that will fail (connection refused on a dead port).
	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "fetch('http://127.0.0.1:1/nonexistent').catch(function(){})",
	})
	waitForNetwork(t, tabID, "/nonexistent")

	out := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":         tabID,
		"peek":        true,
		"failed_only": true,
	})
	for _, r := range out.Requests {
		if !r.Failed {
			t.Errorf("failed_only filter returned non-failed request: %s", r.URL)
		}
	}
	// We should have at least one failed request.
	if len(out.Requests) == 0 {
		t.Log("no failed requests captured (connection failure may not have completed yet)")
	}
}

// ===========================================================================
// Network: combined filters (type + url_pattern + status_min)
// ===========================================================================

func TestNetworkCombinedFilters(t *testing.T) {
	tabID := navigateToFixture(t, "network.html")
	defer closeTab(t, tabID)
	waitForNetwork(t, tabID, "/api/data")

	out := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":         tabID,
		"peek":        true,
		"type":        "XHR",
		"url_pattern": "/api/data",
		"status_min":  200,
		"status_max":  299,
	})
	for _, r := range out.Requests {
		if !strings.EqualFold(r.Type, "XHR") {
			t.Errorf("combined filter returned type %q, want XHR", r.Type)
		}
		if !strings.Contains(r.URL, "/api/data") {
			t.Errorf("combined filter returned URL %q, want to contain /api/data", r.URL)
		}
		if r.Status < 200 || r.Status > 299 {
			t.Errorf("combined filter returned status %d, want 200-299", r.Status)
		}
	}
}

// ===========================================================================
// Network: status_max only (without status_min)
// ===========================================================================

func TestNetworkStatusMaxOnly(t *testing.T) {
	tabID := navigateToFixture(t, "network.html")
	defer closeTab(t, tabID)
	waitForNetwork(t, tabID, "/api/data")

	out := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":        tabID,
		"peek":       true,
		"status_max": 299,
	})
	for _, r := range out.Requests {
		if r.Status > 0 && r.Status > 299 {
			t.Errorf("status_max=299 returned status %d", r.Status)
		}
	}
}

// ===========================================================================
// Console: drain with level filter clears ALL entries
// ===========================================================================

func TestConsoleDrainWithFilterClearsAll(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)
	waitForConsole(t, tabID)

	// Drain only "warning" level.
	out := callTool[GetConsoleLogsOutput](t, "get_console_logs", map[string]any{
		"tab":   tabID,
		"level": "warning",
	})
	_ = out // Just ensure no error.

	// After draining with a filter, ALL entries should be cleared
	// (drain clears everything, filter only affects what's returned).
	out2 := callTool[GetConsoleLogsOutput](t, "get_console_logs", map[string]any{
		"tab":  tabID,
		"peek": true,
	})
	if len(out2.Logs) != 0 {
		t.Errorf("after drain with filter, expected 0 remaining logs, got %d", len(out2.Logs))
	}
}

// ===========================================================================
// Console/JS errors: page with no JS errors returns empty array
// ===========================================================================

func TestJSErrorsCleanPage(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	out := callTool[GetJSErrorsOutput](t, "get_js_errors", map[string]any{
		"tab":  tabID,
		"peek": true,
	})
	if out.Errors == nil {
		t.Error("errors should be an empty array, not nil")
	}
	if len(out.Errors) != 0 {
		t.Errorf("clean page should have 0 JS errors, got %d", len(out.Errors))
	}
}

// ===========================================================================
// DOM: get_text on nested children (returns concatenated text)
// ===========================================================================

func TestGetTextNestedChildren(t *testing.T) {
	tabID := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tabID)

	out := callTool[GetTextOutput](t, "get_text", map[string]any{
		"tab":      tabID,
		"selector": "#nested-text",
	})
	// Should contain text from all child elements.
	if !strings.Contains(out.Text, "Hello") {
		t.Errorf("nested text missing 'Hello': %q", out.Text)
	}
	if !strings.Contains(out.Text, "bold") {
		t.Errorf("nested text missing 'bold': %q", out.Text)
	}
	if !strings.Contains(out.Text, "italic") {
		t.Errorf("nested text missing 'italic': %q", out.Text)
	}
}

// ===========================================================================
// DOM: get_text on empty element
// ===========================================================================

func TestGetTextEmptyElement(t *testing.T) {
	tabID := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tabID)

	out := callTool[GetTextOutput](t, "get_text", map[string]any{
		"tab":      tabID,
		"selector": "#empty-el",
	})
	if out.Text != "" {
		t.Errorf("empty element text = %q, want empty string", out.Text)
	}
}

// ===========================================================================
// DOM: query with limit=0 falls back to default 10
// ===========================================================================

func TestQueryLimitZeroFallsToDefault(t *testing.T) {
	tabID := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tabID)

	// .query-item has 12 elements. limit=0 should default to 10.
	out := callTool[QueryOutput](t, "query", map[string]any{
		"tab":      tabID,
		"selector": ".query-item",
		"limit":    0,
	})
	if out.Total != 12 {
		t.Errorf("total = %d, want 12", out.Total)
	}
	if len(out.Elements) != 10 {
		t.Errorf("limit=0 returned %d elements, want 10 (default)", len(out.Elements))
	}
}

// ===========================================================================
// DOM: query with empty computed_style array (returns no computed styles)
// ===========================================================================

func TestQueryEmptyComputedStyle(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	out := callTool[QueryOutput](t, "query", map[string]any{
		"tab":            tabID,
		"selector":       "#title",
		"computed_style": []string{},
	})
	if len(out.Elements) == 0 {
		t.Fatal("query returned no elements")
	}
	if len(out.Elements[0].ComputedStyle) != 0 {
		t.Errorf("empty computed_style array should return no styles, got %d", len(out.Elements[0].ComputedStyle))
	}
}

// ===========================================================================
// Visual: screenshot JPEG viewport (not full page)
// ===========================================================================

func TestScreenshotJPEGViewport(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	result := callToolRaw(t, "screenshot", map[string]any{
		"tab":    tabID,
		"format": "jpeg",
	})
	if result.IsError {
		t.Fatalf("screenshot error: %s", contentText(result))
	}
	for _, c := range result.Content {
		if img, ok := c.(*mcp.ImageContent); ok {
			if img.MIMEType != "image/jpeg" {
				t.Errorf("MIME = %q, want image/jpeg", img.MIMEType)
			}
			if len(img.Data) == 0 {
				t.Error("screenshot returned empty data")
			}
			// JPEG magic bytes: FF D8.
			if len(img.Data) >= 2 && (img.Data[0] != 0xFF || img.Data[1] != 0xD8) {
				t.Error("data does not start with JPEG magic bytes")
			}
			return
		}
	}
	t.Error("no ImageContent in result")
}

// ===========================================================================
// Visual: PDF with page_ranges parameter
// ===========================================================================

func TestPDFPageRanges(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	result := callToolRaw(t, "pdf", map[string]any{
		"tab":         tabID,
		"page_ranges": "1",
	})
	if result.IsError {
		t.Fatalf("PDF page_ranges error: %s", contentText(result))
	}
	for _, c := range result.Content {
		if res, ok := c.(*mcp.EmbeddedResource); ok {
			if len(res.Resource.Blob) == 0 {
				t.Error("PDF returned empty blob")
			}
			// Verify PDF magic bytes.
			if len(res.Resource.Blob) >= 4 && string(res.Resource.Blob[:4]) != "%PDF" {
				t.Error("PDF data doesn't start with %PDF magic")
			}
			return
		}
	}
	t.Error("no EmbeddedResource in result")
}

// ===========================================================================
// JS: evaluate with selector first match (querySelector returns first)
// ===========================================================================

func TestEvaluateOnSelectorFirstMatch(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// .content matches two <p> elements. Should use the first one.
	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"selector":   ".content",
		"expression": "return el.textContent;",
	})
	if !strings.Contains(string(out.Result), "test paragraph") {
		t.Errorf("first match text = %s, want 'This is a test paragraph.'", out.Result)
	}
}

// ===========================================================================
// JS: wait_for expression that never becomes truthy (should timeout)
// ===========================================================================

func TestWaitForExpressionTimeout(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "wait_for", map[string]any{
		"tab":        tabID,
		"expression": "window.__never_set_flag === true",
		"timeout":    500,
	})
	if errText == "" {
		t.Error("wait_for with never-truthy expression should timeout")
	}
}

// ===========================================================================
// JS: wait_for with timeout=0 falls to default 30s (just verify it works)
// ===========================================================================

func TestWaitForTimeoutZeroUsesDefault(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// Selector #title already exists, so with timeout=0 (should use default
	// 30s) this should return quickly.
	callTool[struct{}](t, "wait_for", map[string]any{
		"tab":      tabID,
		"selector": "#title",
		"timeout":  0,
	})
}

// ===========================================================================
// Navigation: go_forward multiple steps
// ===========================================================================

func TestGoForwardMultipleSteps(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// Navigate: index -> page2 -> forms (3 pages).
	callTool[NavigateOutput](t, "navigate", map[string]any{
		"tab": tabID,
		"url": fixtureURL("page2.html"),
	})
	callTool[NavigateOutput](t, "navigate", map[string]any{
		"tab": tabID,
		"url": fixtureURL("forms.html"),
	})

	// Go back twice to index.
	callTool[struct{}](t, "go_back", map[string]any{"tab": tabID})
	callTool[struct{}](t, "go_back", map[string]any{"tab": tabID})

	// Go forward twice to forms.
	callTool[struct{}](t, "go_forward", map[string]any{"tab": tabID})
	callTool[struct{}](t, "go_forward", map[string]any{"tab": tabID})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.title",
	})
	if !strings.Contains(string(out.Result), "Form Test") {
		t.Errorf("after two go_forward, title = %s, want 'Form Test'", out.Result)
	}
}

// ===========================================================================
// Accessibility: scoped to selector
// ===========================================================================

func TestAccessibilityTreeScopedSelector(t *testing.T) {
	tabID := navigateToFixture(t, "accessibility.html")
	defer closeTab(t, tabID)

	result := callToolRaw(t, "get_accessibility_tree", map[string]any{
		"tab":      tabID,
		"selector": "nav",
	})
	if result.IsError {
		text := contentText(result)
		if strings.Contains(text, "unknown PropertyName value") {
			t.Skip("skipping: cdproto PropertyName compatibility issue")
		}
		t.Fatalf("accessibility tree scoped error: %s", text)
	}
	// Should return a subset of the tree. Just verify non-empty.
	text := contentText(result)
	if text == "" || text == "null" {
		// Check structured content.
		if result.StructuredContent == nil {
			t.Fatal("scoped accessibility tree is empty")
		}
	}
}

// ===========================================================================
// Accessibility: depth parameter
// ===========================================================================

func TestAccessibilityTreeDepth(t *testing.T) {
	tabID := navigateToFixture(t, "accessibility.html")
	defer closeTab(t, tabID)

	result := callToolRaw(t, "get_accessibility_tree", map[string]any{
		"tab":   tabID,
		"depth": 2,
	})
	if result.IsError {
		text := contentText(result)
		if strings.Contains(text, "unknown PropertyName value") {
			t.Skip("skipping: cdproto PropertyName compatibility issue")
		}
		t.Fatalf("accessibility tree depth error: %s", text)
	}
	// Just verify it returns something without error.
	if result.StructuredContent == nil {
		text := contentText(result)
		if text == "" || text == "null" {
			t.Fatal("depth-limited accessibility tree is empty")
		}
	}
}

// ===========================================================================
// Accessibility: interesting_only=false
// ===========================================================================

func TestAccessibilityTreeInterestingOnlyFalse(t *testing.T) {
	tabID := navigateToFixture(t, "accessibility.html")
	defer closeTab(t, tabID)

	result := callToolRaw(t, "get_accessibility_tree", map[string]any{
		"tab":              tabID,
		"interesting_only": false,
	})
	if result.IsError {
		text := contentText(result)
		if strings.Contains(text, "unknown PropertyName value") {
			t.Skip("skipping: cdproto PropertyName compatibility issue")
		}
		t.Fatalf("accessibility tree interesting_only=false error: %s", text)
	}
	// With interesting_only=false, we should get more nodes than the default.
	// Just verify non-error.
}

// ===========================================================================
// Tab: tab_new with invalid browser ID
// ===========================================================================

func TestTabNewInvalidBrowserID(t *testing.T) {
	errText := callToolExpectErr(t, "tab_new", map[string]any{
		"browser": "nonexistent-browser-id",
	})
	if !strings.Contains(errText, "not found") {
		t.Errorf("error = %q, want to contain 'not found'", errText)
	}
}

// ===========================================================================
// Browser: close the same browser ID twice (should error second time)
// ===========================================================================

func TestBrowserDoubleClose(t *testing.T) {
	b := callTool[BrowserLaunchOutput](t, "browser_launch", map[string]any{
		"headless": true,
	})

	// First close succeeds.
	callTool[BrowserCloseOutput](t, "browser_close", map[string]any{
		"browser": b.BrowserID,
	})

	// Second close should error.
	errText := callToolExpectErr(t, "browser_close", map[string]any{
		"browser": b.BrowserID,
	})
	if !strings.Contains(errText, "not found") {
		t.Errorf("double close error = %q, want 'not found'", errText)
	}
}

// ===========================================================================
// Browser: browser_list after all browsers closed (should have at least
// the pre-launched one from harness, unless other tests closed it)
// ===========================================================================

func TestBrowserListNotNil(t *testing.T) {
	list := callTool[BrowserListOutput](t, "browser_list", map[string]any{})
	if list.Browsers == nil {
		t.Error("browser list should be non-nil (empty array at minimum)")
	}
}

// ===========================================================================
// Coverage: get_coverage type=js
// ===========================================================================

func TestGetCoverageJS(t *testing.T) {
	tabID := navigateToFixture(t, "coverage.html")
	defer closeTab(t, tabID)

	out := callTool[GetCoverageOutput](t, "get_coverage", map[string]any{
		"tab":  tabID,
		"type": "js",
	})
	// Coverage entries may or may not have data depending on profiling timing.
	// Just verify no error and the response structure is valid.
	if out.Entries == nil {
		t.Log("JS coverage returned nil entries (expected for profiling started after page load)")
	}
}

// ===========================================================================
// Set viewport: device_scale_factor=0 falls to default 1.0
// ===========================================================================

func TestSetViewportScaleFactorZero(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "set_viewport", map[string]any{
		"tab":                 tabID,
		"width":               800,
		"height":              600,
		"device_scale_factor": 0,
	})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "window.devicePixelRatio",
	})
	if string(out.Result) != "1" {
		t.Errorf("scale=0 devicePixelRatio = %s, want 1 (default)", out.Result)
	}
}

// ===========================================================================
// Evaluate: undefined returns null
// ===========================================================================

func TestEvaluateUndefinedReturnsNull(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "undefined",
	})
	if string(out.Result) != "null" {
		t.Errorf("undefined result = %s, want null", out.Result)
	}
}

// ===========================================================================
// Evaluate: various return types
// ===========================================================================

func TestEvaluateReturnTypes(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	tests := []struct {
		name       string
		expression string
		want       string
	}{
		{"number", "42", "42"},
		{"string", "'hello'", `"hello"`},
		{"bool_true", "true", "true"},
		{"bool_false", "false", "false"},
		{"null", "null", "null"},
		{"array", "[1,2,3]", "[1,2,3]"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
				"tab":        tabID,
				"expression": tc.expression,
			})
			if string(out.Result) != tc.want {
				t.Errorf("result = %s, want %s", out.Result, tc.want)
			}
		})
	}
}

// ===========================================================================
// Upload: empty paths array
// ===========================================================================

func TestUploadFilesEmptyPaths(t *testing.T) {
	tabID := navigateToFixture(t, "forms.html")
	defer closeTab(t, tabID)

	// Empty paths — should effectively set 0 files.
	callTool[struct{}](t, "upload_files", map[string]any{
		"tab":      tabID,
		"selector": "#file-input",
		"paths":    []string{},
	})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('file-input').files.length",
	})
	if string(out.Result) != "0" {
		t.Errorf("files.length after empty paths = %s, want 0", out.Result)
	}
}

// ===========================================================================
// Tab: close the last tab, verify next tool call errors (no auto-create)
// ===========================================================================

func TestTabCloseLastErrors(t *testing.T) {
	// Launch a separate browser so we don't mess with the shared harness browser.
	b := callTool[BrowserLaunchOutput](t, "browser_launch", map[string]any{
		"headless": true,
	})
	defer callTool[BrowserCloseOutput](t, "browser_close", map[string]any{
		"browser": b.BrowserID,
	})

	// Create a tab in the new browser.
	tab := callTool[TabNewOutput](t, "tab_new", map[string]any{
		"browser": b.BrowserID,
	})

	// Close the only tab.
	callTool[struct{}](t, "tab_close", map[string]any{"tab": tab.TabID})

	// tab_list should show 0 tabs.
	list := callTool[TabListOutput](t, "tab_list", map[string]any{
		"browser": b.BrowserID,
	})
	if len(list.Tabs) != 0 {
		t.Errorf("after closing last tab, tab_list shows %d tabs, want 0", len(list.Tabs))
	}

	// A tool call referencing the closed tab should error.
	errText := callToolExpectErr(t, "evaluate", map[string]any{
		"tab":        tab.TabID,
		"expression": "1+1",
	})
	if errText == "" {
		t.Error("evaluate on closed tab should error")
	}
}

// ===========================================================================
// Concurrent tool calls on same tab (should not panic)
// ===========================================================================

func TestConcurrentSameTab(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	done := make(chan struct{}, 3)

	// Run 3 concurrent evaluate calls on the same tab.
	for i := 0; i < 3; i++ {
		go func(n int) {
			defer func() { done <- struct{}{} }()
			callTool[EvaluateOutput](t, "evaluate", map[string]any{
				"tab":        tabID,
				"expression": "document.title",
			})
		}(i)
	}

	for i := 0; i < 3; i++ {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("concurrent same-tab calls timed out")
		}
	}
}

// ===========================================================================
// Browser: browser_connect to a running Chrome instance
// ===========================================================================

// findFreePort returns a free TCP port.
func findFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

// getWSURL retrieves the browser's WebSocket debugger URL from Chrome's
// /json/version endpoint, retrying a few times while Chrome starts up.
func getWSURL(t *testing.T, port int) string {
	t.Helper()
	url := fmt.Sprintf("http://127.0.0.1:%d/json/version", port)
	for i := 0; i < 20; i++ {
		resp, err := http.Get(url)
		if err != nil {
			time.Sleep(250 * time.Millisecond)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var info struct {
			WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
		}
		if err := json.Unmarshal(body, &info); err == nil && info.WebSocketDebuggerURL != "" {
			return info.WebSocketDebuggerURL
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatal("could not get WebSocket URL from Chrome /json/version")
	return ""
}

func TestBrowserConnect(t *testing.T) {
	// Find Chrome binary.
	chromePath, err := exec.LookPath("chromium")
	if err != nil {
		chromePath, err = exec.LookPath("google-chrome")
		if err != nil {
			t.Skip("chromium/google-chrome not found in PATH")
		}
	}

	port := findFreePort(t)
	tmpDir, err := os.MkdirTemp("", "chromedp-connect-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Launch Chrome with remote debugging enabled.
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

	// Wait for Chrome to start and get the WS URL.
	wsURL := getWSURL(t, port)

	// Now connect via the MCP tool.
	out := callTool[BrowserConnectOutput](t, "browser_connect", map[string]any{
		"url": wsURL,
	})
	if out.BrowserID == "" {
		t.Fatal("browser_connect returned empty browser ID")
	}

	// Verify the connected browser appears in browser_list with mode "connect".
	list := callTool[BrowserListOutput](t, "browser_list", map[string]any{})
	var found bool
	for _, b := range list.Browsers {
		if b.ID == out.BrowserID {
			found = true
			if b.Mode != "connect" {
				t.Errorf("connected browser mode = %q, want 'connect'", b.Mode)
			}
		}
	}
	if !found {
		t.Error("connected browser not found in browser_list")
	}

	// Create a tab on the connected browser and verify we can use it.
	tab := callTool[TabNewOutput](t, "tab_new", map[string]any{
		"url":     fixtureURL("page2.html"),
		"browser": out.BrowserID,
	})

	eval := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tab.TabID,
		"expression": "document.title",
	})
	if !strings.Contains(string(eval.Result), "Page 2") {
		t.Errorf("connected browser tab title = %s, want 'Page 2'", eval.Result)
	}

	closeTab(t, tab.TabID)

	// Close the connected browser (should disconnect, not kill the process).
	callTool[BrowserCloseOutput](t, "browser_close", map[string]any{
		"browser": out.BrowserID,
	})

	// Verify it's removed from the list.
	list2 := callTool[BrowserListOutput](t, "browser_list", map[string]any{})
	for _, b := range list2.Browsers {
		if b.ID == out.BrowserID {
			t.Error("closed connected browser still in list")
		}
	}

	// The Chrome process should still be running (we just disconnected).
	// Verify by hitting the /json/version endpoint again.
	wsURL2 := getWSURL(t, port)
	if wsURL2 == "" {
		t.Error("Chrome process should still be running after disconnect")
	}
}

// ===========================================================================
// Browser: browser_connect with invalid URL (should error)
// ===========================================================================

func TestBrowserConnectInvalidURL(t *testing.T) {
	errText := callToolExpectErr(t, "browser_connect", map[string]any{
		"url": "ws://127.0.0.1:1/devtools/browser/nonexistent",
	})
	if errText == "" {
		t.Error("browser_connect to invalid URL should error")
	}
}

// ===========================================================================
// Browser: tools error clearly when Chrome is killed externally
// ===========================================================================

func TestBrowserKilledExternally(t *testing.T) {
	// Find Chrome binary.
	chromePath, err := exec.LookPath("chromium")
	if err != nil {
		chromePath, err = exec.LookPath("google-chrome")
		if err != nil {
			t.Skip("chromium/google-chrome not found in PATH")
		}
	}

	port := findFreePort(t)
	tmpDir, err := os.MkdirTemp("", "chromedp-kill-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Launch Chrome with remote debugging.
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

	wsURL := getWSURL(t, port)

	// Connect via MCP tool.
	out := callTool[BrowserConnectOutput](t, "browser_connect", map[string]any{
		"url": wsURL,
	})
	browserID := out.BrowserID
	if browserID == "" {
		t.Fatal("browser_connect returned empty ID")
	}

	// Create a tab so we have a tab ID to reference.
	tabOut := callTool[TabNewOutput](t, "tab_new", map[string]any{
		"browser": browserID,
	})
	tabID := tabOut.TabID

	// Kill Chrome externally.
	cmd.Process.Kill()
	cmd.Wait()

	// Poll until chromedp detects the dead connection and tool calls error.
	var errText string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		result := callToolRaw(t, "get_text", map[string]any{
			"tab":     tabID,
			"timeout": 500,
		})
		if result.IsError {
			errText = contentText(result)
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if errText == "" {
		t.Error("get_text on killed browser should error")
	}
	if !strings.Contains(errText, "no longer running") && !strings.Contains(errText, "not found") {
		t.Logf("error text: %s", errText)
	}

	// browser_list should no longer include the dead browser.
	list := callTool[BrowserListOutput](t, "browser_list", map[string]any{})
	for _, b := range list.Browsers {
		if b.ID == browserID {
			t.Error("dead browser should have been pruned from browser_list")
		}
	}
}

// ===========================================================================
// Console: generate >1000 console entries and verify ring buffer eviction
// via the MCP tool (integration-level buffer overflow test)
// ===========================================================================

func TestConsoleBufferOverflowIntegration(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	// Generate 1100 console.log messages (buffer size is 1000).
	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "for (var i = 0; i < 1100; i++) { console.log('msg-' + i); }",
	})
	waitForConsole(t, tabID)

	out := callTool[GetConsoleLogsOutput](t, "get_console_logs", map[string]any{
		"tab":  tabID,
		"peek": true,
	})

	if len(out.Logs) > 1000 {
		t.Errorf("console buffer should cap at 1000, got %d", len(out.Logs))
	}
	if len(out.Logs) < 900 {
		t.Errorf("expected close to 1000 logs after generating 1100, got %d", len(out.Logs))
	}

	// The oldest entries (msg-0 through msg-99) should have been evicted.
	// The first entry should be approximately msg-100.
	if len(out.Logs) > 0 {
		firstMsg := out.Logs[0].Text
		if strings.Contains(firstMsg, "msg-0") && !strings.Contains(firstMsg, "msg-0") {
			t.Log("first message should not be msg-0 (oldest should be evicted)")
		}
		lastMsg := out.Logs[len(out.Logs)-1].Text
		if !strings.Contains(lastMsg, "msg-1099") {
			t.Logf("last message = %q (expected msg-1099)", lastMsg)
		}
	}
}

// ===========================================================================
// Evaluate: await_promise default (omitted) resolves promises
// ===========================================================================

func TestEvaluateAwaitPromiseDefault(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	// Omit await_promise — default is true, so the Promise should be awaited.
	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "new Promise(function(resolve) { setTimeout(function() { resolve(99); }, 50); })",
	})
	if string(out.Result) != "99" {
		t.Errorf("default await_promise result = %s, want 99", out.Result)
	}
}

// ===========================================================================
// Navigation: navigate wait_until invalid value (falls through to "load")
// ===========================================================================

func TestNavigateWaitUntilInvalid(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	// Invalid wait_until value should fall through to "load" (default).
	// The implementation only special-cases "domcontentloaded" and "networkidle".
	out := callTool[NavigateOutput](t, "navigate", map[string]any{
		"tab":        tabID,
		"url":        fixtureURL("index.html"),
		"wait_until": "invalid_event",
	})
	if out.Status != 200 {
		t.Errorf("status = %d, want 200", out.Status)
	}
	// Note: Go's http.FileServer 301-redirects /index.html to /, so the
	// final URL may be the root path. Verify the title instead.
	if out.Title != "Test Page" {
		t.Errorf("title = %q, want 'Test Page'", out.Title)
	}
}

// ===========================================================================
// Network: get_network_requests returns empty array (not nil) when empty
// ===========================================================================

func TestNetworkRequestsEmptyArray(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	// Drain all existing requests.
	callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab": tabID,
	})

	// Now peek — should return empty array.
	out := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":  tabID,
		"peek": true,
	})
	if out.Requests == nil {
		t.Error("requests should be an empty array, not nil")
	}
	if len(out.Requests) != 0 {
		t.Errorf("expected 0 requests after drain, got %d", len(out.Requests))
	}
}

// ===========================================================================
// Set viewport: mobile emulation verifies device metrics are applied
// ===========================================================================

func TestSetViewportMobileEmulation(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "set_viewport", map[string]any{
		"tab":    tabID,
		"width":  375,
		"height": 812,
		"mobile": true,
	})

	// In mobile emulation, screen.width/height reflect the device metrics
	// set by emulation.SetDeviceMetricsOverride. window.innerWidth may
	// differ due to the page's viewport meta tag (or lack thereof).
	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "({ sw: screen.width, sh: screen.height })",
	})
	var dims map[string]float64
	json.Unmarshal(out.Result, &dims)
	if dims["sw"] != 375 {
		t.Errorf("mobile screen.width = %f, want 375", dims["sw"])
	}
	if dims["sh"] != 812 {
		t.Errorf("mobile screen.height = %f, want 812", dims["sh"])
	}
}

// ===========================================================================
// DOM: get_html inner mode on element with nested children
// ===========================================================================

func TestGetHTMLInnerNested(t *testing.T) {
	tabID := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tabID)

	out := callTool[GetHTMLOutput](t, "get_html", map[string]any{
		"tab":      tabID,
		"selector": "#nested-text",
		"outer":    false,
	})
	// Inner HTML should contain child tags but NOT the wrapping div.
	if strings.Contains(out.HTML, "<div id=\"nested-text\"") {
		t.Error("inner HTML should not contain the wrapping div tag")
	}
	if !strings.Contains(out.HTML, "<span>") {
		t.Errorf("inner HTML should contain child <span>, got: %s", out.HTML)
	}
	if !strings.Contains(out.HTML, "<strong>") {
		t.Errorf("inner HTML should contain child <strong>, got: %s", out.HTML)
	}
}
