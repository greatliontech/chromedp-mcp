package tools

import (
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestNavigate(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	out := callTool[NavigateOutput](t, "navigate", map[string]any{
		"url": fixtureURL("page2.html"),
		"tab": tabID,
	})
	if !strings.HasSuffix(out.URL, "/page2.html") {
		t.Errorf("navigate URL = %q, want suffix /page2.html", out.URL)
	}
	if out.Title != "Page 2" {
		t.Errorf("navigate Title = %q, want %q", out.Title, "Page 2")
	}
}

func TestNavigateAndGoBack(t *testing.T) {
	tabID := navigateToFixture(t, "forms.html")
	defer closeTab(t, tabID)

	// Navigate to page2.
	callTool[NavigateOutput](t, "navigate", map[string]any{
		"url": fixtureURL("page2.html"),
		"tab": tabID,
	})
	// Go back.
	callTool[struct{}](t, "go_back", map[string]any{"tab": tabID})

	// Verify we're back on the forms page by checking the URL.
	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.title",
	})
	if !strings.Contains(string(out.Result), "Form") {
		t.Errorf("after go_back, title = %s, want to contain 'Form'", out.Result)
	}
}

func TestScreenshot(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	result := callToolRaw(t, "screenshot", map[string]any{"tab": tabID})
	if result.IsError {
		t.Fatalf("screenshot returned error")
	}
	// Should have image content.
	found := false
	for _, c := range result.Content {
		if img, ok := c.(*mcp.ImageContent); ok {
			found = true
			if len(img.Data) == 0 {
				t.Error("screenshot returned empty image data")
			}
			if img.MIMEType != "image/png" {
				t.Errorf("screenshot MIME = %q, want image/png", img.MIMEType)
			}
		}
	}
	if !found {
		t.Error("screenshot result has no ImageContent")
	}
}

func TestElementScreenshot(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	result := callToolRaw(t, "screenshot", map[string]any{
		"tab":      tabID,
		"selector": "#title",
	})
	if result.IsError {
		t.Fatalf("element screenshot returned error")
	}
	for _, c := range result.Content {
		if img, ok := c.(*mcp.ImageContent); ok {
			if len(img.Data) == 0 {
				t.Error("element screenshot returned empty data")
			}
			return
		}
	}
	t.Error("element screenshot has no ImageContent")
}

func TestQuery(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	out := callTool[QueryOutput](t, "query", map[string]any{
		"tab":      tabID,
		"selector": ".item",
	})
	if out.Total != 3 {
		t.Errorf("query total = %d, want 3", out.Total)
	}
	if len(out.Elements) != 3 {
		t.Errorf("query elements = %d, want 3", len(out.Elements))
	}
	if len(out.Elements) > 0 && out.Elements[0].TagName != "li" {
		t.Errorf("first element tag = %q, want li", out.Elements[0].TagName)
	}
}

func TestGetHTML(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	out := callTool[GetHTMLOutput](t, "get_html", map[string]any{
		"tab":      tabID,
		"selector": "#title",
	})
	if !strings.Contains(out.HTML, "Hello World") {
		t.Errorf("get_html = %q, want to contain 'Hello World'", out.HTML)
	}
}

func TestGetText(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	out := callTool[GetTextOutput](t, "get_text", map[string]any{
		"tab":      tabID,
		"selector": "#title",
	})
	if strings.TrimSpace(out.Text) != "Hello World" {
		t.Errorf("get_text = %q, want 'Hello World'", out.Text)
	}
}

func TestGetAccessibilityTree(t *testing.T) {
	tabID := navigateToFixture(t, "accessibility.html")
	defer closeTab(t, tabID)

	result := callToolRaw(t, "get_accessibility_tree", map[string]any{
		"tab": tabID,
	})
	if result.IsError {
		text := contentText(result)
		// cdproto may not support all PropertyName values from newer Chrome versions.
		if strings.Contains(text, "unknown PropertyName value") {
			t.Skip("skipping: cdproto does not support all PropertyName values from this Chrome version")
		}
		t.Fatalf("get_accessibility_tree returned error: %s", text)
	}
	text := contentText(result)
	if text == "" || text == "null" {
		t.Error("accessibility tree is empty")
	}
}

func TestConsoleLogs(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	out := callTool[GetConsoleLogsOutput](t, "get_console_logs", map[string]any{
		"tab": tabID,
	})
	if len(out.Logs) == 0 {
		t.Error("expected console logs, got none")
		return
	}
	// Check we got the expected messages.
	found := map[string]bool{}
	for _, log := range out.Logs {
		found[log.Level] = true
	}
	for _, level := range []string{"log", "warning", "error"} {
		if !found[level] {
			t.Errorf("expected console level %q in logs", level)
		}
	}
}

func TestJSErrors(t *testing.T) {
	tabID := navigateToFixture(t, "errors.html")
	defer closeTab(t, tabID)

	out := callTool[GetJSErrorsOutput](t, "get_js_errors", map[string]any{
		"tab": tabID,
	})
	if len(out.Errors) == 0 {
		t.Error("expected JS errors, got none")
		return
	}
	found := false
	for _, e := range out.Errors {
		if strings.Contains(e.Message, "test error from page") {
			found = true
		}
	}
	if !found {
		t.Error("expected error containing 'test error from page'")
	}
}

func TestNetworkRequests(t *testing.T) {
	tabID := navigateToFixture(t, "network.html")
	defer closeTab(t, tabID)

	out := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab": tabID,
	})
	if len(out.Requests) == 0 {
		t.Error("expected network requests, got none")
		return
	}
	// Should have the /api/data request.
	found := false
	for _, r := range out.Requests {
		if strings.Contains(r.URL, "/api/data") {
			found = true
			if r.Status != 200 {
				t.Errorf("/api/data status = %d, want 200", r.Status)
			}
		}
	}
	if !found {
		t.Error("expected network request for /api/data")
	}
}

func TestEvaluate(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "1 + 2",
	})
	if string(out.Result) != "3" {
		t.Errorf("evaluate result = %s, want 3", out.Result)
	}
}

func TestEvaluateOnSelector(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"selector":   "#title",
		"expression": "return el.textContent",
	})
	if string(out.Result) != `"Hello World"` {
		t.Errorf("evaluate with selector result = %s, want \"Hello World\"", out.Result)
	}
}

func TestClickAndType(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// Click the target.
	callTool[struct{}](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#click-target",
	})

	// Verify click was registered.
	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('click-count').textContent",
	})
	if string(out.Result) != `"1"` {
		t.Errorf("click count = %s, want \"1\"", out.Result)
	}

	// Type into the input.
	callTool[struct{}](t, "type", map[string]any{
		"tab":      tabID,
		"selector": "#type-target",
		"text":     "hello world",
	})

	// Verify typed text.
	out = callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('type-target').value",
	})
	if string(out.Result) != `"hello world"` {
		t.Errorf("typed text = %s, want \"hello world\"", out.Result)
	}
}

func TestFormInteraction(t *testing.T) {
	tabID := navigateToFixture(t, "forms.html")
	defer closeTab(t, tabID)

	// Type a name.
	callTool[struct{}](t, "type", map[string]any{
		"tab":      tabID,
		"selector": "#name",
		"text":     "Alice",
	})

	// Select an option.
	callTool[struct{}](t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#color",
		"value":    "blue",
	})

	// Verify selection.
	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('color').value",
	})
	if string(out.Result) != `"blue"` {
		t.Errorf("select value = %s, want \"blue\"", out.Result)
	}

	// Submit form.
	callTool[struct{}](t, "submit_form", map[string]any{
		"tab":      tabID,
		"selector": "#test-form",
	})

	// Wait for result to appear.
	callTool[struct{}](t, "wait_for", map[string]any{
		"tab":      tabID,
		"selector": "#result",
	})

	out = callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('result').textContent",
	})
	if !strings.Contains(string(out.Result), "Alice") {
		t.Errorf("form result = %s, want to contain 'Alice'", out.Result)
	}
}

func TestCookies(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// Set a cookie.
	callTool[struct{}](t, "set_cookie", map[string]any{
		"tab":    tabID,
		"name":   "test_cookie",
		"value":  "test_value",
		"domain": "127.0.0.1",
	})

	// Get cookies.
	out := callTool[GetCookiesOutput](t, "get_cookies", map[string]any{
		"tab": tabID,
	})
	found := false
	for _, c := range out.Cookies {
		if c.Name == "test_cookie" && c.Value == "test_value" {
			found = true
		}
	}
	if !found {
		t.Error("set cookie not found in get_cookies output")
	}

	// Delete the cookie.
	callTool[struct{}](t, "delete_cookies", map[string]any{
		"tab":    tabID,
		"name":   "test_cookie",
		"domain": "127.0.0.1",
	})

	out = callTool[GetCookiesOutput](t, "get_cookies", map[string]any{
		"tab": tabID,
	})
	for _, c := range out.Cookies {
		if c.Name == "test_cookie" {
			t.Error("cookie still exists after delete")
		}
	}
}

func TestPerformanceMetrics(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	out := callTool[GetPerformanceMetricsOutput](t, "get_performance_metrics", map[string]any{
		"tab": tabID,
	})
	if len(out.Metrics) == 0 {
		t.Error("expected performance metrics, got none")
	}
}

func TestTabManagement(t *testing.T) {
	// Create a new tab.
	out := callTool[TabNewOutput](t, "tab_new", map[string]any{
		"url": fixtureURL("index.html"),
	})
	tab1 := out.TabID

	// Create another tab.
	out = callTool[TabNewOutput](t, "tab_new", map[string]any{
		"url": fixtureURL("page2.html"),
	})
	tab2 := out.TabID

	// List tabs.
	list := callTool[TabListOutput](t, "tab_list", map[string]any{})
	if len(list.Tabs) < 2 {
		t.Errorf("tab_list returned %d tabs, want >= 2", len(list.Tabs))
	}

	// Activate first tab.
	callTool[struct{}](t, "tab_activate", map[string]any{"tab": tab1})

	// Close both.
	closeTab(t, tab1)
	closeTab(t, tab2)
}

func TestSetViewport(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "set_viewport", map[string]any{
		"tab":    tabID,
		"width":  375,
		"height": 667,
	})

	// Verify via JS.
	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "window.innerWidth",
	})
	if string(out.Result) != "375" {
		t.Errorf("viewport width = %s, want 375", out.Result)
	}
}

func TestWaitFor(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// Wait for a selector that already exists.
	callTool[struct{}](t, "wait_for", map[string]any{
		"tab":      tabID,
		"selector": "#title",
		"timeout":  5000,
	})

	// Wait for a JS expression.
	callTool[struct{}](t, "wait_for", map[string]any{
		"tab":        tabID,
		"expression": "document.title === 'Test Page'",
		"timeout":    5000,
	})
}

func TestScrollIntoView(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "scroll", map[string]any{
		"tab":      tabID,
		"selector": "#scroll-marker",
	})

	// Verify the element is in view.
	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": `(function() {
			var el = document.getElementById('scroll-marker');
			var rect = el.getBoundingClientRect();
			return rect.top >= 0 && rect.top < window.innerHeight;
		})()`,
	})
	if string(out.Result) != "true" {
		t.Error("scroll-marker not in view after scroll")
	}
}

func TestPressKey(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "press_key", map[string]any{
		"tab": tabID,
		"key": "a",
	})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('key-output').textContent",
	})
	if !strings.Contains(string(out.Result), "a") {
		t.Errorf("key output = %s, want to contain 'a'", out.Result)
	}
}

func TestClearConsole(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// Drain first to get any existing logs.
	callTool[GetConsoleLogsOutput](t, "get_console_logs", map[string]any{"tab": tabID})

	// Clear.
	callTool[struct{}](t, "clear_console", map[string]any{"tab": tabID})

	// Should be empty now.
	out := callTool[GetConsoleLogsOutput](t, "get_console_logs", map[string]any{
		"tab":  tabID,
		"peek": true,
	})
	if len(out.Logs) != 0 {
		t.Errorf("expected 0 logs after clear, got %d", len(out.Logs))
	}
}
