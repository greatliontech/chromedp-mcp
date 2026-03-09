package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---------------------------------------------------------------------------
// Navigation: wait_until modes
// ---------------------------------------------------------------------------

func TestNavigateWaitUntilDOMContentLoaded(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	out := callTool[NavigateOutput](t, "navigate", map[string]any{
		"tab":        tabID,
		"url":        fixtureURL("index.html"),
		"wait_until": "domcontentloaded",
	})
	if out.Status != 200 {
		t.Errorf("navigate status = %d, want 200", out.Status)
	}
	// Go's http.FileServer 301-redirects /index.html to /, so the final
	// URL won't have the /index.html suffix.
	if out.Title != "Test Page" {
		t.Errorf("title = %q, want 'Test Page'", out.Title)
	}
}

func TestNavigateWaitUntilNetworkIdle(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	out := callTool[NavigateOutput](t, "navigate", map[string]any{
		"tab":        tabID,
		"url":        fixtureURL("network.html"),
		"wait_until": "networkidle",
	})
	if out.Status != 200 {
		t.Errorf("navigate status = %d, want 200", out.Status)
	}
	if out.Title == "" {
		t.Error("title should not be empty after networkidle")
	}
}

func TestNavigateWaitUntilLoadExplicit(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	out := callTool[NavigateOutput](t, "navigate", map[string]any{
		"tab":        tabID,
		"url":        fixtureURL("forms.html"),
		"wait_until": "load",
	})
	if out.Status != 200 {
		t.Errorf("status = %d, want 200", out.Status)
	}
	if out.Title != "Form Test" {
		t.Errorf("title = %q, want 'Form Test'", out.Title)
	}
}

// ---------------------------------------------------------------------------
// Navigation: reload bypass_cache
// ---------------------------------------------------------------------------

func TestReloadBypassCache(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// Mutate the title.
	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.title = 'cached-mutant'",
	})

	// Reload with bypass_cache — should wait for page load and return output.
	rout := callTool[ReloadOutput](t, "reload", map[string]any{
		"tab":          tabID,
		"bypass_cache": true,
	})
	if rout.Title == "cached-mutant" {
		t.Error("after bypass_cache reload, title should be restored from server")
	}
	if rout.URL == "" {
		t.Error("reload output should include URL")
	}
}

// ---------------------------------------------------------------------------
// Navigation: go_forward no history
// ---------------------------------------------------------------------------

func TestGoForwardNoHistory(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "go_forward", map[string]any{"tab": tabID})
	if !strings.Contains(errText, "no forward history") {
		t.Errorf("go_forward error = %q, want 'no forward history'", errText)
	}
}

// ---------------------------------------------------------------------------
// Click: right-click, middle-click, triple-click, double-click behaviour
// ---------------------------------------------------------------------------

func TestClickRightButton(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// Set up a contextmenu listener.
	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": `document.getElementById('click-target').addEventListener('contextmenu', function(e) {
			e.preventDefault();
			this.textContent = 'Right-clicked';
		})`,
	})

	callTool[struct{}](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#click-target",
		"button":   "right",
	})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('click-target').textContent",
	})
	// The JS dispatch sends mousedown/mouseup/click with button=2 but not
	// contextmenu. The click event listener should fire (it fires for any button).
	if !strings.Contains(string(out.Result), "Clicked") && !strings.Contains(string(out.Result), "Right-clicked") {
		t.Errorf("right-click result = %s, want evidence of click or right-click", out.Result)
	}
}

func TestClickDoubleClickBehaviour(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// Set up a dblclick listener.
	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": `document.getElementById('click-target').addEventListener('dblclick', function() {
			document.getElementById('click-count').textContent = 'dblclick';
		})`,
	})

	callTool[struct{}](t, "click", map[string]any{
		"tab":         tabID,
		"selector":    "#click-target",
		"click_count": 2,
	})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('click-count').textContent",
	})
	if string(out.Result) != `"dblclick"` {
		t.Errorf("double-click result = %s, want \"dblclick\"", out.Result)
	}
}

// ---------------------------------------------------------------------------
// Type: delay parameter
// ---------------------------------------------------------------------------

func TestTypeWithDelay(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	start := time.Now()
	callTool[struct{}](t, "type", map[string]any{
		"tab":      tabID,
		"selector": "#type-target",
		"text":     "abc",
		"delay":    50,
	})
	elapsed := time.Since(start)

	// With 3 characters and 50ms delay each, should take at least ~150ms.
	if elapsed < 100*time.Millisecond {
		t.Errorf("type with delay took %v, expected at least 100ms for 3 chars at 50ms delay", elapsed)
	}

	// Verify the text was actually typed.
	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('type-target').value",
	})
	if !strings.Contains(string(out.Result), "abc") {
		t.Errorf("typed text = %s, want to contain 'abc'", out.Result)
	}
}

// ---------------------------------------------------------------------------
// Upload files: success path
// ---------------------------------------------------------------------------

func TestUploadFilesSuccess(t *testing.T) {
	tabID := navigateToFixture(t, "forms.html")
	defer closeTab(t, tabID)

	// Create a temp file to upload.
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "test-upload.txt")
	if err := os.WriteFile(tmpFile, []byte("upload content"), 0644); err != nil {
		t.Fatalf("create temp file: %v", err)
	}

	callTool[struct{}](t, "upload_files", map[string]any{
		"tab":      tabID,
		"selector": "#file-input",
		"paths":    []string{tmpFile},
	})

	// Verify the file input now has a file.
	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('file-input').files.length",
	})
	if string(out.Result) != "1" {
		t.Errorf("file input files.length = %s, want 1", out.Result)
	}

	out = callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('file-input').files[0].name",
	})
	if !strings.Contains(string(out.Result), "test-upload.txt") {
		t.Errorf("file name = %s, want 'test-upload.txt'", out.Result)
	}
}

// ---------------------------------------------------------------------------
// Handle dialog: dismiss, confirm, prompt
// ---------------------------------------------------------------------------

func TestHandleDialogDismiss(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	done := make(chan string, 1)
	go func() {
		out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
			"tab":        tabID,
			"expression": "confirm('Are you sure?')",
		})
		done <- string(out.Result)
	}()

	handleDialog(t, tabID, map[string]any{"accept": false})

	select {
	case result := <-done:
		if result != "false" {
			t.Errorf("confirm() after dismiss = %s, want false", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for confirm dialog")
	}
}

func TestHandleDialogConfirmAccept(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	done := make(chan string, 1)
	go func() {
		out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
			"tab":        tabID,
			"expression": "confirm('Are you sure?')",
		})
		done <- string(out.Result)
	}()

	handleDialog(t, tabID, map[string]any{"accept": true})

	select {
	case result := <-done:
		if result != "true" {
			t.Errorf("confirm() after accept = %s, want true", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for confirm dialog")
	}
}

func TestHandleDialogPrompt(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	done := make(chan string, 1)
	go func() {
		out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
			"tab":        tabID,
			"expression": "prompt('Enter name:')",
		})
		done <- string(out.Result)
	}()

	handleDialog(t, tabID, map[string]any{"accept": true, "text": "Alice"})

	select {
	case result := <-done:
		if !strings.Contains(result, "Alice") {
			t.Errorf("prompt() result = %s, want 'Alice'", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for prompt dialog")
	}
}

// ---------------------------------------------------------------------------
// Query: computed_style, outer_html, bbox, attributes=false, text=false
// ---------------------------------------------------------------------------

func TestQueryComputedStyle(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	out := callTool[QueryOutput](t, "query", map[string]any{
		"tab":            tabID,
		"selector":       "#title",
		"computed_style": []string{"font-family", "display"},
	})
	if len(out.Elements) == 0 {
		t.Fatal("query returned no elements")
	}
	elem := out.Elements[0]
	if len(elem.ComputedStyle) == 0 {
		t.Error("computed_style should have entries")
	}
	if _, ok := elem.ComputedStyle["display"]; !ok {
		t.Error("computed_style should contain 'display'")
	}
}

func TestQueryOuterHTML(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	out := callTool[QueryOutput](t, "query", map[string]any{
		"tab":        tabID,
		"selector":   "#title",
		"outer_html": true,
	})
	if len(out.Elements) == 0 {
		t.Fatal("query returned no elements")
	}
	if !strings.Contains(out.Elements[0].OuterHTML, "<h1") {
		t.Errorf("outer_html = %q, want to contain '<h1'", out.Elements[0].OuterHTML)
	}
	if !strings.Contains(out.Elements[0].OuterHTML, "Hello World") {
		t.Errorf("outer_html = %q, want to contain 'Hello World'", out.Elements[0].OuterHTML)
	}
}

func TestQueryBBox(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	out := callTool[QueryOutput](t, "query", map[string]any{
		"tab":      tabID,
		"selector": "#title",
		"bbox":     true,
	})
	if len(out.Elements) == 0 {
		t.Fatal("query returned no elements")
	}
	if out.Elements[0].BBox == nil {
		t.Fatal("bbox should not be nil")
	}
	if out.Elements[0].BBox.Width <= 0 || out.Elements[0].BBox.Height <= 0 {
		t.Errorf("bbox dimensions should be positive, got %+v", out.Elements[0].BBox)
	}
}

func TestQueryAttributesDisabled(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	out := callTool[QueryOutput](t, "query", map[string]any{
		"tab":        tabID,
		"selector":   "#title",
		"attributes": false,
	})
	if len(out.Elements) == 0 {
		t.Fatal("query returned no elements")
	}
	if len(out.Elements[0].Attributes) > 0 {
		t.Errorf("attributes should be empty when disabled, got %v", out.Elements[0].Attributes)
	}
}

func TestQueryTextDisabled(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	out := callTool[QueryOutput](t, "query", map[string]any{
		"tab":      tabID,
		"selector": "#title",
		"text":     false,
	})
	if len(out.Elements) == 0 {
		t.Fatal("query returned no elements")
	}
	if out.Elements[0].Text != "" {
		t.Errorf("text should be empty when disabled, got %q", out.Elements[0].Text)
	}
}

func TestQueryDefaultLimit(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// .item has 3 elements, so default limit of 10 should return all 3.
	out := callTool[QueryOutput](t, "query", map[string]any{
		"tab":      tabID,
		"selector": ".item",
	})
	if len(out.Elements) != 3 {
		t.Errorf("default limit should return all 3 items, got %d", len(out.Elements))
	}
	if out.Total != 3 {
		t.Errorf("total = %d, want 3", out.Total)
	}
}

// ---------------------------------------------------------------------------
// Screenshot: JPEG format and quality
// ---------------------------------------------------------------------------

func TestScreenshotJPEG(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	result := callToolRaw(t, "screenshot", map[string]any{
		"tab":       tabID,
		"format":    "jpeg",
		"quality":   50,
		"full_page": true,
	})
	if result.IsError {
		t.Fatalf("JPEG screenshot error: %s", contentText(result))
	}
	var found bool
	for _, c := range result.Content {
		if img, ok := c.(*mcp.ImageContent); ok {
			found = true
			if img.MIMEType != "image/jpeg" {
				t.Errorf("JPEG screenshot MIME = %q, want image/jpeg", img.MIMEType)
			}
			if len(img.Data) == 0 {
				t.Error("JPEG screenshot returned empty data")
			}
		}
	}
	if !found {
		t.Error("JPEG screenshot did not return ImageContent")
	}
}

func TestScreenshotPNGDefault(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// Default format should be PNG.
	result := callToolRaw(t, "screenshot", map[string]any{
		"tab": tabID,
	})
	if result.IsError {
		t.Fatalf("screenshot error: %s", contentText(result))
	}
	for _, c := range result.Content {
		if img, ok := c.(*mcp.ImageContent); ok {
			if img.MIMEType != "image/png" {
				t.Errorf("default format MIME = %q, want image/png", img.MIMEType)
			}
			// PNG magic bytes: 0x89 0x50 0x4E 0x47
			if len(img.Data) > 4 && img.Data[0] != 0x89 {
				t.Error("PNG data doesn't start with PNG magic bytes")
			}
			return
		}
	}
	t.Error("no ImageContent in result")
}

// ---------------------------------------------------------------------------
// PDF: optional parameters
// ---------------------------------------------------------------------------

func TestPDFLandscape(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	result := callToolRaw(t, "pdf", map[string]any{
		"tab":       tabID,
		"landscape": true,
	})
	if result.IsError {
		t.Fatalf("landscape PDF error: %s", contentText(result))
	}
	for _, c := range result.Content {
		if res, ok := c.(*mcp.EmbeddedResource); ok {
			if len(res.Resource.Blob) == 0 {
				t.Error("landscape PDF returned empty blob")
			}
			return
		}
	}
	t.Error("no EmbeddedResource in result")
}

func TestPDFPrintBackgroundFalse(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	result := callToolRaw(t, "pdf", map[string]any{
		"tab":              tabID,
		"print_background": false,
	})
	if result.IsError {
		t.Fatalf("PDF print_background=false error: %s", contentText(result))
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

func TestPDFCustomScale(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	result := callToolRaw(t, "pdf", map[string]any{
		"tab":   tabID,
		"scale": 0.5,
	})
	if result.IsError {
		t.Fatalf("PDF scale=0.5 error: %s", contentText(result))
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

// ---------------------------------------------------------------------------
// Set viewport: height, device_scale_factor, mobile
// ---------------------------------------------------------------------------

func TestSetViewportDeviceScaleFactor(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "set_viewport", map[string]any{
		"tab":                 tabID,
		"width":               800,
		"height":              600,
		"device_scale_factor": 2.0,
	})

	// Verify width.
	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "window.innerWidth",
	})
	if string(out.Result) != "800" {
		t.Errorf("viewport width = %s, want 800", out.Result)
	}

	// Verify height.
	out = callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "window.innerHeight",
	})
	if string(out.Result) != "600" {
		t.Errorf("viewport height = %s, want 600", out.Result)
	}

	// Verify device pixel ratio matches scale factor.
	out = callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "window.devicePixelRatio",
	})
	if string(out.Result) != "2" {
		t.Errorf("devicePixelRatio = %s, want 2", out.Result)
	}
}

func TestSetViewportMobile(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "set_viewport", map[string]any{
		"tab":    tabID,
		"width":  390,
		"height": 844,
		"mobile": true,
	})

	// In mobile mode, screen.width/height reflects the device metrics
	// regardless of layout viewport.
	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "screen.width",
	})
	if string(out.Result) != "390" {
		t.Errorf("screen.width = %s, want 390", out.Result)
	}

	out = callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "screen.height",
	})
	if string(out.Result) != "844" {
		t.Errorf("screen.height = %s, want 844", out.Result)
	}
}

// ---------------------------------------------------------------------------
// JS errors: peek, drain semantics, entry fields
// ---------------------------------------------------------------------------

func TestJSErrorsDrainClearsBuffer(t *testing.T) {
	tabID := navigateToFixture(t, "errors.html")
	defer closeTab(t, tabID)
	waitForJSErrors(t, tabID)

	// First drain.
	out1 := callTool[GetJSErrorsOutput](t, "get_js_errors", map[string]any{
		"tab": tabID,
	})
	if len(out1.Errors) == 0 {
		t.Fatal("expected JS errors from errors.html, got none")
	}

	// Second drain — should be empty.
	out2 := callTool[GetJSErrorsOutput](t, "get_js_errors", map[string]any{
		"tab": tabID,
	})
	if len(out2.Errors) != 0 {
		t.Errorf("after drain, expected 0 errors, got %d", len(out2.Errors))
	}
}

func TestJSErrorsPeek(t *testing.T) {
	tabID := navigateToFixture(t, "errors.html")
	defer closeTab(t, tabID)
	waitForJSErrors(t, tabID)

	// Peek should not clear the buffer.
	out1 := callTool[GetJSErrorsOutput](t, "get_js_errors", map[string]any{
		"tab":  tabID,
		"peek": true,
	})
	if len(out1.Errors) == 0 {
		t.Fatal("expected JS errors from errors.html")
	}

	out2 := callTool[GetJSErrorsOutput](t, "get_js_errors", map[string]any{
		"tab":  tabID,
		"peek": true,
	})
	if len(out2.Errors) != len(out1.Errors) {
		t.Errorf("peek changed buffer: first=%d, second=%d", len(out1.Errors), len(out2.Errors))
	}
}

func TestJSErrorsEntryFields(t *testing.T) {
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

	// Find the thrown error.
	var foundError bool
	for _, e := range out.Errors {
		if strings.Contains(e.Message, "test error from page") {
			foundError = true
			if e.Timestamp.IsZero() {
				t.Error("error timestamp should not be zero")
			}
		}
	}
	if !foundError {
		t.Error("expected 'test error from page' in errors")
	}
}

// ---------------------------------------------------------------------------
// Network: type filter, limit, request fields
// ---------------------------------------------------------------------------

func TestGetNetworkRequestsTypeFilter(t *testing.T) {
	tabID := navigateToFixture(t, "network.html")
	defer closeTab(t, tabID)
	waitForNetwork(t, tabID, "/api/data")

	// Filter by XHR type.
	out := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":  tabID,
		"peek": true,
		"type": "XHR",
	})
	for _, r := range out.Requests {
		if r.Type != "XHR" {
			t.Errorf("type filter returned type %q, want 'XHR'", r.Type)
		}
	}
}

func TestGetNetworkRequestsLimit(t *testing.T) {
	tabID := navigateToFixture(t, "network.html")
	defer closeTab(t, tabID)
	waitForNetwork(t, tabID, "/api/data")

	out := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":   tabID,
		"peek":  true,
		"limit": 1,
	})
	if len(out.Requests) > 1 {
		t.Errorf("limit=1 returned %d requests, want <= 1", len(out.Requests))
	}
}

func TestGetNetworkRequestEntryFields(t *testing.T) {
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
	if r.ID == "" {
		t.Error("request ID should not be empty")
	}
	if r.Method == "" {
		t.Error("request method should not be empty")
	}
	if r.URL == "" {
		t.Error("request URL should not be empty")
	}
	if r.Status == 0 {
		t.Error("request status should not be 0 for completed request")
	}
	if r.StartTime.IsZero() {
		t.Error("request start_time should not be zero")
	}
}

// ---------------------------------------------------------------------------
// Network: get_response_body binary (base64) and invalid request_id
// ---------------------------------------------------------------------------

func TestGetResponseBodyBinary(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)
	waitForNetwork(t, tabID, "/image.png")

	// The page loads /image.png. Find its request ID.
	nout := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":         tabID,
		"peek":        true,
		"url_pattern": "/image.png",
	})
	var requestID string
	for _, r := range nout.Requests {
		if strings.Contains(r.URL, "/image.png") && r.Status == 200 {
			requestID = r.ID
			break
		}
	}
	if requestID == "" {
		t.Fatal("could not find /image.png request")
	}

	body := callTool[GetResponseBodyOutput](t, "get_response_body", map[string]any{
		"tab":        tabID,
		"request_id": requestID,
	})
	if !body.Base64Encoded {
		t.Error("binary response body should be base64 encoded")
	}
	if body.Body == "" {
		t.Error("body should not be empty")
	}
}

func TestGetResponseBodyInvalidRequestID(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "get_response_body", map[string]any{
		"tab":        tabID,
		"request_id": "invalid-request-id-xyz",
	})
	if errText == "" {
		t.Error("get_response_body with invalid request_id should return error")
	}
}

// ---------------------------------------------------------------------------
// Evaluate: await_promise, promise resolution
// ---------------------------------------------------------------------------

func TestEvaluateAwaitPromise(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "new Promise(resolve => setTimeout(() => resolve('resolved!'), 50))",
	})
	if !strings.Contains(string(out.Result), "resolved!") {
		t.Errorf("promise result = %s, want 'resolved!'", out.Result)
	}
}

func TestEvaluateAwaitPromiseFalse(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":           tabID,
		"expression":    "new Promise(resolve => resolve(42))",
		"await_promise": false,
	})
	// When not awaiting, should return an object descriptor, not the resolved value.
	if string(out.Result) == "42" {
		t.Error("await_promise=false should not resolve the promise to its value")
	}
}

func TestEvaluateUndefined(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "undefined",
	})
	if string(out.Result) != "null" {
		t.Errorf("undefined result = %s, want null (JSON serialization of undefined)", out.Result)
	}
}

// ---------------------------------------------------------------------------
// Evaluate on selector: side effects, return types
// ---------------------------------------------------------------------------

func TestEvaluateOnSelectorSideEffect(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// Modify the element via the expression.
	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"selector":   "#title",
		"expression": "el.textContent = 'Modified'; return el.textContent;",
	})

	// Verify the modification.
	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('title').textContent",
	})
	if !strings.Contains(string(out.Result), "Modified") {
		t.Errorf("after evaluate with selector side effect, text = %s, want 'Modified'", out.Result)
	}
}

func TestEvaluateOnSelectorReturnTypes(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	tests := []struct {
		expr string
		want string
	}{
		{"return el.tagName.toLowerCase();", `"h1"`},
		{"return el.children.length;", "0"},
		{"return el.id === 'title';", "true"},
	}
	for _, tc := range tests {
		out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
			"tab":        tabID,
			"selector":   "#title",
			"expression": tc.expr,
		})
		if string(out.Result) != tc.want {
			t.Errorf("evaluate with selector(%s) = %s, want %s", tc.expr, out.Result, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Browser: close active default, close invalid ID, close with MRU fallback
// ---------------------------------------------------------------------------

func TestBrowserCloseInvalidID(t *testing.T) {
	errText := callToolExpectErr(t, "browser_close", map[string]any{
		"browser": "nonexistent-browser-id",
	})
	if !strings.Contains(errText, "not found") {
		t.Errorf("browser_close error = %q, want 'not found'", errText)
	}
}

func TestBrowserCloseActiveFallsBackToMRU(t *testing.T) {
	// Launch two browsers.
	b1 := callTool[BrowserLaunchOutput](t, "browser_launch", map[string]any{"headless": true})
	b2 := callTool[BrowserLaunchOutput](t, "browser_launch", map[string]any{"headless": true})

	// b2 is now active. Close it without specifying ID (should close active).
	callTool[BrowserCloseOutput](t, "browser_close", map[string]any{})

	// b1 should now be active.
	list := callTool[BrowserListOutput](t, "browser_list", map[string]any{})
	var b1Active, b2Found bool
	for _, b := range list.Browsers {
		if b.ID == b1.BrowserID && b.Active {
			b1Active = true
		}
		if b.ID == b2.BrowserID {
			b2Found = true
		}
	}
	if b2Found {
		t.Error("closed browser b2 should not be in list")
	}
	if !b1Active {
		t.Error("b1 should be active after closing active browser b2")
	}

	// Clean up.
	callTool[BrowserCloseOutput](t, "browser_close", map[string]any{"browser": b1.BrowserID})
}

// ---------------------------------------------------------------------------
// Tab: close invalid ID, new without URL
// ---------------------------------------------------------------------------

func TestTabCloseInvalidID(t *testing.T) {
	errText := callToolExpectErr(t, "tab_close", map[string]any{
		"tab": "nonexistent-tab-id",
	})
	if !strings.Contains(errText, "not found") {
		t.Errorf("tab_close error = %q, want 'not found'", errText)
	}
}

func TestTabNewWithoutURL(t *testing.T) {
	out := callTool[TabNewOutput](t, "tab_new", map[string]any{})
	defer closeTab(t, out.TabID)

	if out.TabID == "" {
		t.Fatal("tab_new returned empty tab_id")
	}
	if out.URL != "" {
		t.Errorf("tab_new without URL should have empty URL field, got %q", out.URL)
	}
}

// ---------------------------------------------------------------------------
// Cookies: fields, set options, delete without domain
// ---------------------------------------------------------------------------

func TestCookieFields(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "set_cookie", map[string]any{
		"tab":       tabID,
		"name":      "field_test",
		"value":     "field_value",
		"domain":    "127.0.0.1",
		"http_only": true,
	})
	defer callTool[struct{}](t, "delete_cookies", map[string]any{"tab": tabID})

	out := callTool[GetCookiesOutput](t, "get_cookies", map[string]any{"tab": tabID})
	var found *CookieInfo
	for i := range out.Cookies {
		if out.Cookies[i].Name == "field_test" {
			found = &out.Cookies[i]
			break
		}
	}
	if found == nil {
		t.Fatal("cookie 'field_test' not found")
	}
	if found.Value != "field_value" {
		t.Errorf("cookie value = %q, want 'field_value'", found.Value)
	}
	if found.Path != "/" {
		t.Errorf("cookie path = %q, want '/' (default)", found.Path)
	}
	if !found.HTTPOnly {
		t.Error("cookie http_only should be true")
	}
	if found.Domain == "" {
		t.Error("cookie domain should not be empty")
	}
}

func TestSetCookieDefaultPath(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "set_cookie", map[string]any{
		"tab":    tabID,
		"name":   "path_test",
		"value":  "pv",
		"domain": "127.0.0.1",
	})
	defer callTool[struct{}](t, "delete_cookies", map[string]any{"tab": tabID})

	out := callTool[GetCookiesOutput](t, "get_cookies", map[string]any{"tab": tabID})
	for _, c := range out.Cookies {
		if c.Name == "path_test" {
			if c.Path != "/" {
				t.Errorf("default path = %q, want '/'", c.Path)
			}
			return
		}
	}
	t.Error("cookie 'path_test' not found")
}

// ---------------------------------------------------------------------------
// Press key: special keys, multi-modifier
// ---------------------------------------------------------------------------

func TestPressKeyEnter(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "press_key", map[string]any{
		"tab": tabID,
		"key": "Enter",
	})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('key-output').textContent",
	})
	if !strings.Contains(string(out.Result), "Enter") {
		t.Errorf("key output = %s, want to contain 'Enter'", out.Result)
	}
}

func TestPressKeyMultiModifier(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "press_key", map[string]any{
		"tab":       tabID,
		"key":       "a",
		"modifiers": []string{"ctrl", "shift"},
	})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('key-output').textContent",
	})
	result := string(out.Result)
	if !strings.Contains(result, "Ctrl") || !strings.Contains(result, "Shift") {
		t.Errorf("key output = %s, want to contain both 'Ctrl' and 'Shift'", result)
	}
}

// ---------------------------------------------------------------------------
// Submit form: element-within-form, no-form error
// ---------------------------------------------------------------------------

func TestSubmitFormFromInputElement(t *testing.T) {
	tabID := navigateToFixture(t, "forms.html")
	defer closeTab(t, tabID)

	// Type a name first.
	callTool[struct{}](t, "type", map[string]any{
		"tab":      tabID,
		"selector": "#name",
		"text":     "Bob",
	})

	// Submit using the input element (not the form), tests the .closest('form') path.
	callTool[struct{}](t, "submit_form", map[string]any{
		"tab":      tabID,
		"selector": "#name",
	})

	// Wait for result to appear.
	callTool[struct{}](t, "wait_for", map[string]any{
		"tab":      tabID,
		"selector": "#result",
	})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('result').textContent",
	})
	if !strings.Contains(string(out.Result), "Bob") {
		t.Errorf("form result = %s, want to contain 'Bob'", out.Result)
	}
}

func TestSubmitFormNoFormError(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	// #title is an h1, not inside a form.
	errText := callToolExpectErr(t, "submit_form", map[string]any{
		"tab":      tabID,
		"selector": "#title",
	})
	if !strings.Contains(errText, "no form found") {
		t.Errorf("submit_form error = %q, want 'no form found'", errText)
	}
}

// ---------------------------------------------------------------------------
// Select option: multiple criteria validation
// ---------------------------------------------------------------------------

func TestSelectOptionMultipleCriteria(t *testing.T) {
	tabID := navigateToFixture(t, "forms.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#color",
		"value":    "red",
		"label":    "Green",
	})
	if !strings.Contains(errText, "exactly one") || !strings.Contains(errText, "not multiple") {
		t.Errorf("expected multiple criteria error, got: %s", errText)
	}
}

// ---------------------------------------------------------------------------
// Performance metrics: specific metric names
// ---------------------------------------------------------------------------

func TestPerformanceMetricsSpecificNames(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	out := callTool[GetPerformanceMetricsOutput](t, "get_performance_metrics", map[string]any{
		"tab": tabID,
	})

	// Chrome should always report these well-known metrics.
	expected := map[string]bool{
		"Timestamp":       false,
		"JSHeapUsedSize":  false,
		"JSHeapTotalSize": false,
		"Nodes":           false,
	}
	for _, m := range out.Metrics {
		if _, ok := expected[m.Name]; ok {
			expected[m.Name] = true
		}
	}
	for name, found := range expected {
		if !found {
			t.Errorf("expected metric %q not found in performance metrics", name)
		}
	}
}

// ---------------------------------------------------------------------------
// Coverage: type=all returns both JS and CSS
// ---------------------------------------------------------------------------

func TestGetCoverageAll(t *testing.T) {
	tabID := navigateToFixture(t, "coverage.html")
	defer closeTab(t, tabID)

	out := callTool[GetCoverageOutput](t, "get_coverage", map[string]any{
		"tab": tabID,
		// type defaults to "all"
	})
	// Should have CSS entries at minimum (from <style> blocks).
	if len(out.Entries) == 0 {
		t.Error("get_coverage(all) returned no entries")
	}
	for _, e := range out.Entries {
		if e.TotalBytes > 0 && e.Percentage < 0 {
			t.Errorf("coverage percentage should be >= 0, got %f for %s", e.Percentage, e.URL)
		}
	}
}

// ---------------------------------------------------------------------------
// Console logs: limit parameter, peek idempotence
// ---------------------------------------------------------------------------

func TestConsoleLogsLimit(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)
	waitForConsole(t, tabID)

	// index.html emits at least 3 console messages (log, warn, error).
	out := callTool[GetConsoleLogsOutput](t, "get_console_logs", map[string]any{
		"tab":   tabID,
		"peek":  true,
		"limit": 1,
	})
	if len(out.Logs) > 1 {
		t.Errorf("limit=1 returned %d logs, want <= 1", len(out.Logs))
	}
}

func TestConsoleLogsPeekIdempotence(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)
	waitForConsole(t, tabID)

	out1 := callTool[GetConsoleLogsOutput](t, "get_console_logs", map[string]any{
		"tab":  tabID,
		"peek": true,
	})
	out2 := callTool[GetConsoleLogsOutput](t, "get_console_logs", map[string]any{
		"tab":  tabID,
		"peek": true,
	})
	if len(out1.Logs) != len(out2.Logs) {
		t.Errorf("peek not idempotent: first=%d, second=%d", len(out1.Logs), len(out2.Logs))
	}
}

func TestConsoleLogEntryFields(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)
	waitForConsole(t, tabID)

	out := callTool[GetConsoleLogsOutput](t, "get_console_logs", map[string]any{
		"tab":  tabID,
		"peek": true,
	})
	if len(out.Logs) == 0 {
		t.Fatal("expected console logs")
	}
	for _, log := range out.Logs {
		if log.Level == "" {
			t.Error("log level should not be empty")
		}
		if log.Text == "" {
			t.Error("log text should not be empty")
		}
		if log.Timestamp.IsZero() {
			t.Error("log timestamp should not be zero")
		}
	}
}

// ---------------------------------------------------------------------------
// Active tab default: omit tab param
// ---------------------------------------------------------------------------

func TestActiveTabDefault(t *testing.T) {
	// Create a tab and navigate it — it becomes the active tab.
	tabOut := callTool[TabNewOutput](t, "tab_new", map[string]any{
		"url": fixtureURL("page2.html"),
	})
	defer closeTab(t, tabOut.TabID)

	// Call get_text WITHOUT the tab parameter — should use the active tab.
	out := callTool[GetTextOutput](t, "get_text", map[string]any{})
	if !strings.Contains(out.Text, "Page Two") {
		t.Errorf("get_text without tab param = %q, want to contain 'Page Two'", out.Text)
	}
}

// ---------------------------------------------------------------------------
// Layout shifts: basic retrieval (no shifts expected on static page)
// ---------------------------------------------------------------------------

func TestGetLayoutShiftsEmpty(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	out := callTool[GetLayoutShiftsOutput](t, "get_layout_shifts", map[string]any{
		"tab": tabID,
	})
	// Static page should have no/minimal layout shifts.
	if out.CumulativeLS < 0 {
		t.Errorf("cumulative_ls should be >= 0, got %f", out.CumulativeLS)
	}
	// Shifts array should be initialized (not nil).
	if out.Shifts == nil {
		t.Error("shifts should be an empty array, not nil")
	}
}

// ---------------------------------------------------------------------------
// Clear console: also clears JS errors buffer
// ---------------------------------------------------------------------------

func TestClearConsoleAlsoClearsJSErrors(t *testing.T) {
	tabID := navigateToFixture(t, "errors.html")
	defer closeTab(t, tabID)
	waitForJSErrors(t, tabID)

	// Verify JS errors exist.
	out := callTool[GetJSErrorsOutput](t, "get_js_errors", map[string]any{
		"tab":  tabID,
		"peek": true,
	})
	if len(out.Errors) == 0 {
		t.Fatal("expected JS errors before clearing")
	}

	// Clear console (should also clear JS errors per design).
	callTool[struct{}](t, "clear_console", map[string]any{"tab": tabID})

	// Verify JS errors are cleared.
	out = callTool[GetJSErrorsOutput](t, "get_js_errors", map[string]any{
		"tab":  tabID,
		"peek": true,
	})
	if len(out.Errors) != 0 {
		t.Errorf("after clear_console, expected 0 JS errors, got %d", len(out.Errors))
	}
}

// ---------------------------------------------------------------------------
// Concurrent tool calls on different tabs
// ---------------------------------------------------------------------------

func TestConcurrentToolCallsOnDifferentTabs(t *testing.T) {
	tab1 := navigateToFixture(t, "index.html")
	defer closeTab(t, tab1)
	tab2 := navigateToFixture(t, "page2.html")
	defer closeTab(t, tab2)

	// Run evaluate on both tabs concurrently.
	type result struct {
		tab   string
		title string
	}
	ch := make(chan result, 2)

	go func() {
		out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
			"tab":        tab1,
			"expression": "document.title",
		})
		ch <- result{tab: "tab1", title: string(out.Result)}
	}()
	go func() {
		out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
			"tab":        tab2,
			"expression": "document.title",
		})
		ch <- result{tab: "tab2", title: string(out.Result)}
	}()

	results := make(map[string]string)
	for i := 0; i < 2; i++ {
		select {
		case r := <-ch:
			results[r.tab] = r.title
		case <-time.After(10 * time.Second):
			t.Fatal("timed out waiting for concurrent calls")
		}
	}

	if !strings.Contains(results["tab1"], "Test Page") {
		t.Errorf("tab1 title = %s, want 'Test Page'", results["tab1"])
	}
	if !strings.Contains(results["tab2"], "Page 2") {
		t.Errorf("tab2 title = %s, want 'Page 2'", results["tab2"])
	}
}
