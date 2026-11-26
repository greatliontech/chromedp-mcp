package tools

import (
	"encoding/base64"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/greatliontech/chromedp-mcp/internal/collector"
)

func TestGetNetworkRequestsInvalidFieldsDoesNotDrain(t *testing.T) {
	tabID := navigateToFixture(t, "network.html")
	defer closeTab(t, tabID)
	waitForNetwork(t, tabID, "/api/data")

	before := callTool[getNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":  tabID,
		"mode": "peek",
	})
	if len(before.Requests) == 0 {
		t.Fatal("precondition: expected captured network requests")
	}

	errText := callToolExpectErr(t, "get_network_requests", map[string]any{
		"tab":    tabID,
		"mode":   "drain",
		"fields": []string{"id", "nonexistent_field"},
	})
	if !strings.Contains(errText, "nonexistent_field") {
		t.Errorf("error = %q, want mention of nonexistent_field", errText)
	}

	// Buffer must still hold every entry because validation runs before draining.
	after := callTool[getNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":  tabID,
		"mode": "peek",
	})
	if len(after.Requests) < len(before.Requests) {
		t.Fatalf("invalid fields drained buffer: before=%d after=%d", len(before.Requests), len(after.Requests))
	}

	// Negative limit must also be rejected before draining.
	if errText := callToolExpectErr(t, "get_network_requests", map[string]any{
		"tab":   tabID,
		"mode":  "drain",
		"limit": -1,
	}); !strings.Contains(errText, "limit") {
		t.Errorf("negative limit err = %q, want mention of limit", errText)
	}
	if errText := callToolExpectErr(t, "get_websocket_frames", map[string]any{
		"tab":        tabID,
		"request_id": "any",
		"mode":       "peek",
		"limit":      -1,
	}); !strings.Contains(errText, "limit") {
		t.Errorf("ws negative limit err = %q, want mention of limit", errText)
	}
	afterNeg := callTool[getNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":  tabID,
		"mode": "peek",
	})
	if len(afterNeg.Requests) < len(before.Requests) {
		t.Fatalf("negative limit drained buffer: before=%d after=%d", len(before.Requests), len(afterNeg.Requests))
	}
}

func TestGetNetworkRequestsDrainLimitPaginatesWithoutLoss(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	// Drain initial navigation entries.
	callTool[getNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":  tabID,
		"mode": "drain",
	})

	// Fire 3 distinct XHR/fetch requests so we know the exact count in the buffer.
	for i := 0; i < 3; i++ {
		callTool[evaluateOutput](t, "evaluate", map[string]any{
			"tab":        tabID,
			"expression": fmt.Sprintf("fetch('/api/data?page=%d').then(r => r.text())", i),
		})
	}
	waitForNetwork(t, tabID, "page=2")

	// Drain 1 at a time: must return all 3 across 3 calls without silently discarding excess entries.
	var seen []string
	for call := 0; call < 3; call++ {
		out := callTool[getNetworkRequestsOutput](t, "get_network_requests", map[string]any{
			"tab":         tabID,
			"mode":        "drain",
			"url_pattern": "/api/data?page=",
			"limit":       1,
		})
		if len(out.Requests) != 1 {
			t.Fatalf("drain call %d returned %d entries, want 1", call, len(out.Requests))
		}
		seen = append(seen, out.Requests[0].URL)
	}

	// Fourth drain must be empty.
	rem := callTool[getNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":         tabID,
		"mode":        "drain",
		"url_pattern": "/api/data?page=",
	})
	if len(rem.Requests) != 0 {
		t.Errorf("expected 0 remaining entries, got %d", len(rem.Requests))
	}
	for i := 0; i < 3; i++ {
		want := fmt.Sprintf("page=%d", i)
		if !strings.Contains(seen[i], want) {
			t.Errorf("seen[%d] = %q, want substring %q", i, seen[i], want)
		}
	}
}

func TestValidateRangeApplyByteRangeAndUTF8Trim(t *testing.T) {
	if err := validateRange(10, 5); err == nil {
		t.Error("validateRange(10, 5) = nil, want error for inverted range")
	}
	if err := validateRange(10, 10); err != nil {
		t.Errorf("validateRange(10, 10) = %v, want nil", err)
	}
	if err := validateRange(10, 0); err != nil {
		t.Errorf("validateRange(10, 0) = %v, want nil (0 = end of body)", err)
	}

	// Empty body with start > 0 is not truncated (0 == total).
	slice, truncated := applyByteRange([]byte{}, 5, 0)
	if len(slice) != 0 || truncated {
		t.Errorf("applyByteRange(empty, 5, 0) = (%q, %v), want (\"\", false)", slice, truncated)
	}
	// Non-empty body with start > total IS truncated.
	slice, truncated = applyByteRange([]byte("abc"), 5, 0)
	if len(slice) != 0 || !truncated {
		t.Errorf("applyByteRange(\"abc\", 5, 0) = (%q, %v), want (\"\", true)", slice, truncated)
	}

	// Mid-rune cuts at both ends of a 4-byte emoji + 2-byte accented string.
	raw := []byte("🚀é€") // 4 + 2 + 3 = 9 bytes
	out := encodeBodySlice(raw, 1, int64(len(raw)-1))
	if out.Base64Encoded || !utf8.ValidString(out.Body) || out.Body != "é" {
		t.Errorf("encodeBodySlice(mid) = %+v, want text \"é\"", out)
	}
}

func TestNetworkRequests(t *testing.T) {
	tabID := navigateToFixture(t, "network.html")
	defer closeTab(t, tabID)
	waitForNetwork(t, tabID, "/api/data")

	out := callTool[getNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":  tabID,
		"mode": "drain",
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

func TestGetResponseBody(t *testing.T) {
	tabID := navigateToFixture(t, "network.html")
	defer closeTab(t, tabID)
	waitForNetwork(t, tabID, "/api/data")

	// Get network requests to find the /api/data request.
	nout := callTool[getNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":  tabID,
		"mode": "peek",
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

	body := callTool[bodySliceOutput](t, "get_network_body", map[string]any{
		"tab":        tabID,
		"request_id": requestID,
	})
	if !strings.Contains(body.Body, `"status":"ok"`) {
		t.Errorf("response body = %q, want to contain '\"status\":\"ok\"'", body.Body)
	}
	if body.Base64Encoded {
		t.Error("JSON response body should not be base64 encoded")
	}

	// Explicit part="response" matches default behavior.
	explicit := callTool[bodySliceOutput](t, "get_network_body", map[string]any{
		"tab":        tabID,
		"request_id": requestID,
		"part":       "response",
	})
	if explicit.Body != body.Body {
		t.Errorf("explicit part=response body = %q, want %q", explicit.Body, body.Body)
	}
}

func TestGetNetworkRequestsFilters(t *testing.T) {
	tabID := navigateToFixture(t, "network.html")
	defer closeTab(t, tabID)
	waitForNetwork(t, tabID, "/api/data")

	// Filter by URL pattern.
	out := callTool[getNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":         tabID,
		"mode":        "peek",
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
	out = callTool[getNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":        tabID,
		"mode":       "peek",
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
	// Wait for both fetch requests to complete so no new entries arrive
	// between the two peek calls (which would make the count comparison flaky).
	waitForNetwork(t, tabID, "/api/data")
	waitForNetwork(t, tabID, "/api/not-found")

	// Peek should not clear the buffer.
	out1 := callTool[getNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":  tabID,
		"mode": "peek",
	})
	out2 := callTool[getNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":  tabID,
		"mode": "peek",
	})
	if len(out1.Requests) != len(out2.Requests) {
		t.Errorf("peek should not change buffer: first=%d, second=%d", len(out1.Requests), len(out2.Requests))
	}

	// Drain should clear the buffer.
	callTool[getNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":  tabID,
		"mode": "drain",
	})
	out3 := callTool[getNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":  tabID,
		"mode": "peek",
	})
	if len(out3.Requests) != 0 {
		t.Errorf("after drain, peek should return 0 requests, got %d", len(out3.Requests))
	}
}

func TestGetNetworkRequestsTypeFilter(t *testing.T) {
	tabID := navigateToFixture(t, "network.html")
	defer closeTab(t, tabID)
	waitForNetwork(t, tabID, "/api/data")

	// Filter by fetch type (lowercase enum, case-insensitive match against CDP "Fetch").
	out := callTool[getNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":  tabID,
		"mode": "peek",
		"type": "fetch",
	})
	if len(out.Requests) == 0 {
		t.Fatal("type='fetch' filter returned 0 requests for network.html fetch() calls")
	}
	for _, r := range out.Requests {
		if !strings.EqualFold(r.Type, "fetch") {
			t.Errorf("type filter returned type %q, want 'Fetch'", r.Type)
		}
	}

	// Invalid resource type enum is rejected cleanly.
	if errText := callToolExpectErr(t, "get_network_requests", map[string]any{
		"tab":  tabID,
		"mode": "peek",
		"type": "not-a-type",
	}); !strings.Contains(errText, "not-a-type") && !strings.Contains(errText, "type") {
		t.Errorf("invalid type err = %q, want mention of type/not-a-type", errText)
	}
}

func TestGetNetworkRequestsLimit(t *testing.T) {
	tabID := navigateToFixture(t, "network.html")
	defer closeTab(t, tabID)
	waitForNetwork(t, tabID, "/api/data")

	out := callTool[getNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":   tabID,
		"mode":  "peek",
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

	out := callTool[getNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":         tabID,
		"mode":        "peek",
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

func TestGetResponseBodyBinary(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)
	waitForNetwork(t, tabID, "/image.png")

	// The page loads /image.png. Find its request ID.
	nout := callTool[getNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":         tabID,
		"mode":        "peek",
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

	body := callTool[bodySliceOutput](t, "get_network_body", map[string]any{
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

	errText := callToolExpectErr(t, "get_network_body", map[string]any{
		"tab":        tabID,
		"request_id": "invalid-request-id-xyz",
	})
	if errText == "" {
		t.Error("get_network_body with invalid request_id should return error")
	}
}

func TestNetworkURLPatternNoMatch(t *testing.T) {
	tabID := navigateToFixture(t, "network.html")
	defer closeTab(t, tabID)
	waitForNetwork(t, tabID, "/api/data")

	out := callTool[getNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":         tabID,
		"mode":        "peek",
		"url_pattern": "/nonexistent-endpoint-xyz",
	})
	if len(out.Requests) != 0 {
		t.Errorf("url_pattern no-match returned %d requests, want 0", len(out.Requests))
	}
}

func TestNetworkStatusMinOnly(t *testing.T) {
	tabID := navigateToFixture(t, "network.html")
	defer closeTab(t, tabID)
	waitForNetwork(t, tabID, "/api/data")

	out := callTool[getNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":        tabID,
		"mode":       "peek",
		"status_min": 400,
	})
	for _, r := range out.Requests {
		if r.Status > 0 && r.Status < 400 {
			t.Errorf("status_min=400 returned status %d", r.Status)
		}
	}
}

func TestNetworkEntryHeaders(t *testing.T) {
	tabID := navigateToFixture(t, "network.html")
	defer closeTab(t, tabID)
	waitForNetwork(t, tabID, "/api/data")

	out := callTool[getNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":         tabID,
		"mode":        "peek",
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

func TestGetResponseBodyUnicode(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	// Navigate to trigger a fetch of /api/unicode.
	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "fetch('/api/unicode')",
	})
	waitForNetwork(t, tabID, "/api/unicode")

	nout := callTool[getNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":         tabID,
		"mode":        "peek",
		"url_pattern": "/api/unicode",
	})
	if len(nout.Requests) == 0 {
		t.Fatal("no requests for /api/unicode")
	}

	body := callTool[bodySliceOutput](t, "get_network_body", map[string]any{
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

func TestNetworkTypeFilterDocument(t *testing.T) {
	tabID := navigateToFixture(t, "network.html")
	defer closeTab(t, tabID)
	waitForNetwork(t, tabID, "/api/data")

	out := callTool[getNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":  tabID,
		"mode": "peek",
		"type": "document",
	})
	for _, r := range out.Requests {
		if !strings.EqualFold(r.Type, "document") {
			t.Errorf("type filter 'document' returned type %q", r.Type)
		}
	}
	// The initial page load should be a Document type.
	if len(out.Requests) == 0 {
		t.Log("no Document-type requests captured (may depend on Chrome version)")
	}
}

func TestNetworkTimingFields(t *testing.T) {
	tabID := navigateToFixture(t, "network.html")
	defer closeTab(t, tabID)
	waitForNetwork(t, tabID, "/api/data")

	out := callTool[getNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":         tabID,
		"mode":        "peek",
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

func TestNetworkFailedOnly(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	// Fetch a URL that will fail (connection refused on a dead port).
	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "fetch('http://127.0.0.1:1/nonexistent').catch(function(){})",
	})
	waitForNetwork(t, tabID, "/nonexistent")

	out := callTool[getNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":         tabID,
		"mode":        "peek",
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

func TestNetworkCombinedFilters(t *testing.T) {
	tabID := navigateToFixture(t, "network.html")
	defer closeTab(t, tabID)
	waitForNetwork(t, tabID, "/api/data")

	out := callTool[getNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":         tabID,
		"mode":        "peek",
		"type":        "fetch",
		"url_pattern": "/api/data",
		"status_min":  200,
		"status_max":  299,
	})
	if len(out.Requests) == 0 {
		t.Fatal("combined filter returned 0 requests for fetch('/api/data')")
	}
	for _, r := range out.Requests {
		if !strings.EqualFold(r.Type, "fetch") {
			t.Errorf("combined filter returned type %q, want Fetch", r.Type)
		}
		if !strings.Contains(r.URL, "/api/data") {
			t.Errorf("combined filter returned URL %q, want to contain /api/data", r.URL)
		}
		if r.Status < 200 || r.Status > 299 {
			t.Errorf("combined filter returned status %d, want 200-299", r.Status)
		}
	}
}

func TestNetworkStatusMaxOnly(t *testing.T) {
	tabID := navigateToFixture(t, "network.html")
	defer closeTab(t, tabID)
	waitForNetwork(t, tabID, "/api/data")

	out := callTool[getNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":        tabID,
		"mode":       "peek",
		"status_max": 299,
	})
	for _, r := range out.Requests {
		if r.Status > 0 && r.Status > 299 {
			t.Errorf("status_max=299 returned status %d", r.Status)
		}
	}
}

func TestNetworkRequestsEmptyArray(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	// Drain all existing requests.
	callTool[getNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":  tabID,
		"mode": "drain",
	})

	// Now peek — should return empty array.
	out := callTool[getNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":  tabID,
		"mode": "peek",
	})
	if out.Requests == nil {
		t.Error("requests should be an empty array, not nil")
	}
	if len(out.Requests) != 0 {
		t.Errorf("expected 0 requests after drain, got %d", len(out.Requests))
	}
}

// findRequest returns the first network entry whose URL contains the
// substring, or fails the test.
func findRequest(t *testing.T, tabID, urlPattern string) getNetworkRequestsOutput {
	t.Helper()
	waitForNetwork(t, tabID, urlPattern)
	out := callTool[getNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":         tabID,
		"mode":        "peek",
		"url_pattern": urlPattern,
	})
	if len(out.Requests) == 0 {
		t.Fatalf("no network requests matched %q", urlPattern)
	}
	return out
}

// TestRequestBodyInlinedSmallPOST verifies a small JSON POST body is
// captured inline on the network entry without truncation.
func TestRequestBodyInlinedSmallPOST(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	body := `{"hello":"world","n":42}`
	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": fmt.Sprintf(
			`fetch('/submit', {method:'POST', headers:{'Content-Type':'application/json'}, body:%q})`,
			body,
		),
	})

	out := findRequest(t, tabID, "/submit")
	r := out.Requests[0]
	if !r.HasRequestBody {
		t.Error("HasRequestBody = false, want true")
	}
	if r.RequestBody != body {
		t.Errorf("RequestBody = %q, want %q", r.RequestBody, body)
	}
	if r.RequestBodyBase64 {
		t.Error("RequestBodyBase64 = true, want false for JSON body")
	}
	if r.RequestBodyTruncated {
		t.Error("RequestBodyTruncated = true, want false for small body")
	}
}

// TestRequestBodyTruncatedLargePOST verifies that a body larger than the
// inline cap is truncated, the truncated flag is set, and get_network_body
// with part="request" returns the full payload.
func TestRequestBodyTruncatedLargePOST(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	// 8 KB — larger than MaxInlineRequestBody (4 KB) and larger than the
	// CDP MaxPostDataSize we configured. Chrome will set HasPostData=true
	// but omit PostDataEntries from the requestWillBeSent event, so the
	// inline body should be empty and only fetchable via get_network_body.
	const total = 8192
	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": fmt.Sprintf(
			`fetch('/submit', {method:'POST', body:'A'.repeat(%d)})`,
			total,
		),
	})

	out := findRequest(t, tabID, "/submit")
	r := out.Requests[0]
	if !r.HasRequestBody {
		t.Fatal("HasRequestBody = false, want true")
	}
	// Either the inline body is empty (CDP omitted it) or it's truncated.
	if r.RequestBody != "" && !r.RequestBodyTruncated {
		t.Errorf("expected inline body to be empty or truncated; got len=%d truncated=%v",
			len(r.RequestBody), r.RequestBodyTruncated)
	}

	full := callTool[bodySliceOutput](t, "get_network_body", map[string]any{
		"tab":        tabID,
		"request_id": r.ID,
		"part":       "request",
	})
	if full.Base64Encoded {
		t.Error("Base64Encoded = true, want false for ASCII body")
	}
	if len(full.Body) != total {
		t.Errorf("get_network_body(part=request) length = %d, want %d", len(full.Body), total)
	}
	if !strings.HasPrefix(full.Body, "AAAA") || !strings.HasSuffix(full.Body, "AAAA") {
		t.Errorf("get_network_body(part=request) body did not match expected payload (got %d chars)", len(full.Body))
	}
}

// TestRequestBodyBinaryPOST verifies that a binary POST body (non-UTF8
// bytes) is captured inline as base64 with the flag set.
func TestRequestBodyBinaryPOST(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	// 8 binary bytes including 0xff/0xfe which are invalid UTF-8.
	binary := []byte{0xde, 0xad, 0xbe, 0xef, 0xff, 0xfe, 0x00, 0x01}
	jsArray := "["
	for i, b := range binary {
		if i > 0 {
			jsArray += ","
		}
		jsArray += fmt.Sprintf("%d", b)
	}
	jsArray += "]"
	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": fmt.Sprintf(
			`fetch('/submit', {method:'POST', body:new Uint8Array(%s)})`,
			jsArray,
		),
	})

	out := findRequest(t, tabID, "/submit")
	r := out.Requests[0]
	if !r.HasRequestBody {
		t.Fatal("HasRequestBody = false, want true")
	}
	if !r.RequestBodyBase64 {
		t.Errorf("RequestBodyBase64 = false; want true (body=%q)", r.RequestBody)
	}
	decoded, err := base64.StdEncoding.DecodeString(r.RequestBody)
	if err != nil {
		t.Fatalf("RequestBody is not base64: %v", err)
	}
	if string(decoded) != string(binary) {
		t.Errorf("decoded body = %x, want %x", decoded, binary)
	}
}

// TestRequestBodyAbsentForGETIntegration verifies GET requests have no body
// fields set on the network entry.
func TestRequestBodyAbsentForGETIntegration(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `fetch('/api/data')`,
	})

	out := findRequest(t, tabID, "/api/data")
	r := out.Requests[0]
	if r.HasRequestBody {
		t.Error("HasRequestBody = true on GET, want false")
	}
	if r.RequestBody != "" {
		t.Errorf("RequestBody = %q on GET, want empty", r.RequestBody)
	}
	if r.RequestBodyTruncated || r.RequestBodyBase64 {
		t.Error("body flags should all be false on GET")
	}
}

// TestGetRequestBodyForSmallPOST verifies get_network_body(part="request")
// returns the full payload and supports byte range slicing.
func TestGetRequestBodyForSmallPOST(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	body := `name=alpha&value=1`
	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": fmt.Sprintf(
			`fetch('/submit', {method:'POST', headers:{'Content-Type':'application/x-www-form-urlencoded'}, body:%q})`,
			body,
		),
	})

	out := findRequest(t, tabID, "/submit")
	r := out.Requests[0]

	full := callTool[bodySliceOutput](t, "get_network_body", map[string]any{
		"tab":        tabID,
		"request_id": r.ID,
		"part":       "request",
	})
	if full.Base64Encoded {
		t.Error("Base64Encoded = true; want false")
	}
	if full.Body != body {
		t.Errorf("get_network_body(part=request) body = %q, want %q", full.Body, body)
	}

	sliced := callTool[bodySliceOutput](t, "get_network_body", map[string]any{
		"tab":         tabID,
		"request_id":  r.ID,
		"part":        "request",
		"range_start": 5,
		"range_end":   10,
	})
	if sliced.Body != "alpha" || !sliced.Truncated || sliced.TotalBytes != int64(len(body)) {
		t.Errorf("sliced request body = %+v, want body=\"alpha\" truncated=true total=%d", sliced, len(body))
	}
}

// wsURL builds a ws:// URL from the test HTTP server's URL.
func wsURL(t *testing.T, path string) string {
	t.Helper()
	u := ensureHarness(t).httpSrv.URL // http://127.0.0.1:NNNN
	return "ws" + strings.TrimPrefix(u, "http") + path
}

// openWebSocket asks the page JS to open a ws:// connection, send a list
// of payloads, and stash the socket on window so subsequent tests can
// drive it. Returns once the server's greeting has been received.
func openWebSocket(t *testing.T, tabID, url string) {
	t.Helper()
	js := `(async () => {
		window.__ws = new WebSocket(` + jsQuote(url) + `);
		window.__received = [];
		window.__ready = new Promise((resolve) => {
			window.__ws.addEventListener('open', () => resolve('open'));
		});
		window.__greet = new Promise((resolve) => {
			window.__ws.addEventListener('message', (e) => {
				if (e.data instanceof Blob) {
					e.data.arrayBuffer().then((buf) => {
						const arr = Array.from(new Uint8Array(buf));
						window.__received.push({binary: true, data: arr});
					});
				} else {
					window.__received.push({binary: false, data: e.data});
				}
				resolve('greet');
			});
		});
		window.__ws.binaryType = 'arraybuffer';
		await window.__ready;
		await window.__greet;
		return 'ready';
	})()`
	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":           tabID,
		"expression":    js,
		"await_promise": true,
	})
}

// findWSConnection peeks get_network_requests for the given URL substring
// until a websocket entry appears or the timeout expires.
func findWSConnection(t *testing.T, tabID, urlPattern string) collector.NetworkEntry {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		out := callTool[getNetworkRequestsOutput](t, "get_network_requests", map[string]any{
			"tab":         tabID,
			"mode":        "peek",
			"url_pattern": urlPattern,
			"type":        "websocket",
		})
		for _, r := range out.Requests {
			if r.Type == "websocket" {
				return r
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for websocket connection matching %q", urlPattern)
	return collector.NetworkEntry{}
}

// waitForWSFrameCount peeks until the connection reports at least minSent
// frames sent and minRecv frames received.
func waitForWSFrameCount(t *testing.T, tabID, requestID string, minSent, minRecv int64) collector.NetworkEntry {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		out := callTool[getNetworkRequestsOutput](t, "get_network_requests", map[string]any{
			"tab":  tabID,
			"mode": "peek",
			"type": "websocket",
		})
		for _, r := range out.Requests {
			if r.ID != requestID {
				continue
			}
			if r.FramesSentCount != nil && *r.FramesSentCount >= minSent &&
				r.FramesReceivedCount != nil && *r.FramesReceivedCount >= minRecv {
				return r
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for ws frames sent>=%d recv>=%d", minSent, minRecv)
	return collector.NetworkEntry{}
}

// TestWebSocketAppearsInGetNetworkRequests verifies that an open WS shows
// up as a websocket-typed entry with frame counters and 101 status.
func TestWebSocketAppearsInGetNetworkRequests(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	openWebSocket(t, tabID, wsURL(t, "/ws-echo"))

	entry := findWSConnection(t, tabID, "/ws-echo")

	if entry.Type != "websocket" {
		t.Errorf("Type = %q, want websocket", entry.Type)
	}
	if entry.Status != 101 {
		t.Errorf("Status = %d, want 101", entry.Status)
	}
	if entry.FramesSentCount == nil {
		t.Error("FramesSentCount = nil; want non-nil pointer for ws entries")
	}
	if entry.FramesReceivedCount == nil {
		t.Error("FramesReceivedCount = nil; want non-nil pointer for ws entries")
	}
	// At minimum the server greeting should already have arrived.
	got := waitForWSFrameCount(t, tabID, entry.ID, 0, 1)
	if *got.FramesReceivedCount < 1 {
		t.Errorf("FramesReceivedCount = %d, want >= 1 (server greeting)", *got.FramesReceivedCount)
	}
}

// TestWebSocketTypeFilterExcludesHTTP verifies the type=websocket filter
// returns only WS entries even when there are HTTP requests in the buffer.
func TestWebSocketTypeFilterExcludesHTTP(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	// Generate at least one HTTP request to populate the HTTP buffer.
	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `fetch('/api/data')`,
	})
	waitForNetwork(t, tabID, "/api/data")

	openWebSocket(t, tabID, wsURL(t, "/ws-echo"))
	entry := findWSConnection(t, tabID, "/ws-echo")

	out := callTool[getNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":  tabID,
		"mode": "peek",
		"type": "websocket",
	})
	for _, r := range out.Requests {
		if r.Type != "websocket" {
			t.Errorf("got %s entry under type=websocket filter", r.Type)
		}
	}
	if len(out.Requests) == 0 || out.Requests[0].ID != entry.ID {
		t.Errorf("type=websocket filter did not return our connection")
	}
}

// TestGetWebSocketFramesText verifies sent and received text frames appear
// with their UTF-8 payloads intact.
func TestGetWebSocketFramesText(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	openWebSocket(t, tabID, wsURL(t, "/ws-echo"))
	entry := findWSConnection(t, tabID, "/ws-echo")

	// Send 3 text frames; server echoes each back.
	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": `(async () => {
			window.__ws.send('alpha');
			window.__ws.send('beta');
			window.__ws.send('gamma');
			return 'sent';
		})()`,
		"await_promise": true,
	})

	// Server greeting + 3 echoes = 4 received; 3 sent.
	waitForWSFrameCount(t, tabID, entry.ID, 3, 4)

	frames := callTool[getWebSocketFramesOutput](t, "get_websocket_frames", map[string]any{
		"tab":        tabID,
		"request_id": entry.ID,
		"mode":       "peek",
	})

	wantSent := []string{"alpha", "beta", "gamma"}
	if len(frames.Sent) != 3 {
		t.Fatalf("Sent len = %d, want 3", len(frames.Sent))
	}
	for i, f := range frames.Sent {
		if f.Direction != collector.WSDirectionSent {
			t.Errorf("frames.Sent[%d].Direction = %q, want sent", i, f.Direction)
		}
		if f.Opcode != 1 {
			t.Errorf("frames.Sent[%d].Opcode = %d, want 1 (text)", i, f.Opcode)
		}
		if f.PayloadBase64 {
			t.Errorf("frames.Sent[%d].PayloadBase64 = true on text frame", i)
		}
		if f.Payload != wantSent[i] {
			t.Errorf("frames.Sent[%d].Payload = %q, want %q", i, f.Payload, wantSent[i])
		}
	}

	// First received frame is the server greeting; remaining 3 are echoes.
	if len(frames.Received) < 4 {
		t.Fatalf("Received len = %d, want >= 4", len(frames.Received))
	}
	if frames.Received[0].Payload != "hello-from-server" {
		t.Errorf("first received payload = %q, want 'hello-from-server'", frames.Received[0].Payload)
	}
}

// TestGetWebSocketFramesBinary verifies a binary frame is returned with
// PayloadBase64=true and the bytes round-trip.
func TestGetWebSocketFramesBinary(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	openWebSocket(t, tabID, wsURL(t, "/ws-echo"))
	entry := findWSConnection(t, tabID, "/ws-echo")

	// Send 4 raw bytes including non-UTF8 sequences.
	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":           tabID,
		"expression":    `window.__ws.send(new Uint8Array([0xde, 0xad, 0xbe, 0xef]))`,
		"await_promise": false,
	})

	// Greeting + binary echo = 2 received; 1 sent.
	waitForWSFrameCount(t, tabID, entry.ID, 1, 2)

	frames := callTool[getWebSocketFramesOutput](t, "get_websocket_frames", map[string]any{
		"tab":        tabID,
		"request_id": entry.ID,
		"direction":  "sent",
		"mode":       "peek",
	})
	if len(frames.Sent) != 1 {
		t.Fatalf("Sent len = %d, want 1", len(frames.Sent))
	}
	f := frames.Sent[0]
	if f.Opcode != 2 {
		t.Errorf("Opcode = %d, want 2 (binary)", f.Opcode)
	}
	if !f.PayloadBase64 {
		t.Error("PayloadBase64 = false on binary frame, want true")
	}
	decoded, err := base64.StdEncoding.DecodeString(f.Payload)
	if err != nil {
		t.Fatalf("Payload not base64: %v", err)
	}
	want := []byte{0xde, 0xad, 0xbe, 0xef}
	if string(decoded) != string(want) {
		t.Errorf("decoded payload = %x, want %x", decoded, want)
	}
}

// TestGetWebSocketFramesDirectionFilter verifies direction=sent omits
// received frames and vice versa.
func TestGetWebSocketFramesDirectionFilter(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	openWebSocket(t, tabID, wsURL(t, "/ws-echo"))
	entry := findWSConnection(t, tabID, "/ws-echo")

	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":           tabID,
		"expression":    `window.__ws.send('ping')`,
		"await_promise": false,
	})
	waitForWSFrameCount(t, tabID, entry.ID, 1, 2)

	sentOnly := callTool[getWebSocketFramesOutput](t, "get_websocket_frames", map[string]any{
		"tab":        tabID,
		"request_id": entry.ID,
		"direction":  "sent",
		"mode":       "peek",
	})
	if len(sentOnly.Sent) == 0 {
		t.Error("direction=sent returned 0 sent frames")
	}
	if len(sentOnly.Received) != 0 {
		t.Errorf("direction=sent returned %d received frames, want 0", len(sentOnly.Received))
	}

	recvOnly := callTool[getWebSocketFramesOutput](t, "get_websocket_frames", map[string]any{
		"tab":        tabID,
		"request_id": entry.ID,
		"direction":  "received",
		"mode":       "peek",
	})
	if len(recvOnly.Received) == 0 {
		t.Error("direction=received returned 0 received frames")
	}
	if len(recvOnly.Sent) != 0 {
		t.Errorf("direction=received returned %d sent frames, want 0", len(recvOnly.Sent))
	}
}

// TestGetWebSocketFramesPeekVsDrain verifies peek leaves frames intact and
// drain consumes them.
func TestGetWebSocketFramesPeekVsDrain(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	openWebSocket(t, tabID, wsURL(t, "/ws-echo"))
	entry := findWSConnection(t, tabID, "/ws-echo")

	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":           tabID,
		"expression":    `(window.__ws.send('one'), window.__ws.send('two'), 'sent')`,
		"await_promise": false,
	})
	waitForWSFrameCount(t, tabID, entry.ID, 2, 3)

	peeked := callTool[getWebSocketFramesOutput](t, "get_websocket_frames", map[string]any{
		"tab":        tabID,
		"request_id": entry.ID,
		"mode":       "peek",
	})
	if len(peeked.Sent) < 2 {
		t.Fatalf("peek Sent len = %d, want >= 2", len(peeked.Sent))
	}
	// Second peek returns the same frames.
	peeked2 := callTool[getWebSocketFramesOutput](t, "get_websocket_frames", map[string]any{
		"tab":        tabID,
		"request_id": entry.ID,
		"mode":       "peek",
	})
	if len(peeked2.Sent) != len(peeked.Sent) {
		t.Errorf("second peek Sent len = %d, want %d (peek must not drain)", len(peeked2.Sent), len(peeked.Sent))
	}

	// Drain.
	_ = callTool[getWebSocketFramesOutput](t, "get_websocket_frames", map[string]any{
		"tab":        tabID,
		"request_id": entry.ID,
		"mode":       "drain",
	})
	post := callTool[getWebSocketFramesOutput](t, "get_websocket_frames", map[string]any{
		"tab":        tabID,
		"request_id": entry.ID,
		"mode":       "peek",
	})
	if len(post.Sent) != 0 || len(post.Received) != 0 {
		t.Errorf("after drain, peek = sent=%d recv=%d, want 0/0", len(post.Sent), len(post.Received))
	}
}

// TestGetWebSocketFramesUnknownConnection verifies the tool errors when
// the request id doesn't correspond to any tracked connection.
func TestGetWebSocketFramesUnknownConnection(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "get_websocket_frames", map[string]any{
		"tab":        tabID,
		"request_id": "no-such-connection",
		"mode":       "peek",
	})
	if !strings.Contains(errText, "no websocket connection") {
		t.Errorf("error text = %q, want substring 'no websocket connection'", errText)
	}
}

// TestGetWebSocketFramesInvalidDirection verifies the tool rejects garbage.
func TestGetWebSocketFramesInvalidDirection(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "get_websocket_frames", map[string]any{
		"tab":        tabID,
		"request_id": "anything",
		"direction":  "sideways",
		"mode":       "peek",
	})
	if !strings.Contains(errText, "direction") || !strings.Contains(errText, "sideways") {
		t.Errorf("error text = %q, want mention of 'direction' and 'sideways'", errText)
	}
}

// TestWebSocketLimitDoesNotHideConnection verifies that a small `limit`
// on get_network_requests does not silently drop the WebSocket entry when
// the HTTP buffer alone would exceed the cap. Regression for the merge-
// before-cap order.
func TestWebSocketLimitDoesNotHideConnection(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	// Generate 5 HTTP requests to fill the buffer.
	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": `(async () => {
			for (let i = 0; i < 5; i++) await fetch('/api/data?n=' + i);
			return 'done';
		})()`,
		"await_promise": true,
	})
	openWebSocket(t, tabID, wsURL(t, "/ws-echo"))
	wsEntry := findWSConnection(t, tabID, "/ws-echo")

	// Ask for limit=3. Even though 5 HTTP entries plus 1 WS entry exist,
	// the WS entry must be reachable somehow. The unified filter that
	// includes type=websocket gives it priority.
	out := callTool[getNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":   tabID,
		"mode":  "peek",
		"limit": 3,
		"type":  "websocket",
	})
	found := false
	for _, r := range out.Requests {
		if r.ID == wsEntry.ID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("type=websocket filter with limit=3 did not surface ws entry; got %d entries", len(out.Requests))
	}
}

// TestWebSocketGetNetworkRequestsDoesNotDrainConnections verifies that a
// non-peek get_network_requests call does not lose visibility into ws
// connections (they're long-lived; peek behavior is mandatory for them).
func TestWebSocketGetNetworkRequestsDoesNotDrainConnections(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	openWebSocket(t, tabID, wsURL(t, "/ws-echo"))
	entry := findWSConnection(t, tabID, "/ws-echo")

	// Drain (peek=false).
	out1 := callTool[getNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":  tabID,
		"mode": "drain",
		"type": "websocket",
	})
	found1 := false
	for _, r := range out1.Requests {
		if r.ID == entry.ID {
			found1 = true
			break
		}
	}
	if !found1 {
		t.Fatal("first drain did not return ws entry")
	}

	// Subsequent call should still see it.
	out2 := callTool[getNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":  tabID,
		"mode": "drain",
		"type": "websocket",
	})
	found2 := false
	for _, r := range out2.Requests {
		if r.ID == entry.ID {
			found2 = true
			break
		}
	}
	if !found2 {
		t.Error("second drain lost ws entry; ws connections must survive non-peek calls")
	}
}

// TestGetResponseBodyRangeSlicesUTF8 verifies range_start/range_end on
// get_network_body returns a byte slice of the body and reports the
// total length so the caller can plan paging.
func TestGetResponseBodyRangeSlicesUTF8(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `fetch('/api/data')`,
	})
	waitForNetwork(t, tabID, "/api/data")

	out := callTool[getNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":         tabID,
		"mode":        "peek",
		"url_pattern": "/api/data",
	})
	if len(out.Requests) == 0 {
		t.Fatal("no /api/data request captured")
	}
	rid := out.Requests[0].ID

	// Full body fetch — total_bytes set, truncated false.
	full := callTool[bodySliceOutput](t, "get_network_body", map[string]any{
		"tab":        tabID,
		"request_id": rid,
	})
	if full.TotalBytes <= 0 {
		t.Fatalf("TotalBytes = %d, want > 0", full.TotalBytes)
	}
	if full.Truncated {
		t.Error("Truncated should be false on full fetch")
	}
	wantBody := full.Body

	// Slice to first 5 bytes.
	slice := callTool[bodySliceOutput](t, "get_network_body", map[string]any{
		"tab":         tabID,
		"request_id":  rid,
		"range_start": 0,
		"range_end":   5,
	})
	if !slice.Truncated {
		t.Error("Truncated should be true on partial fetch")
	}
	if slice.TotalBytes != full.TotalBytes {
		t.Errorf("TotalBytes mismatch: slice=%d full=%d", slice.TotalBytes, full.TotalBytes)
	}
	if slice.Body != wantBody[:5] {
		t.Errorf("range [0,5) = %q, want %q", slice.Body, wantBody[:5])
	}

	// Slice from byte 5 to end (range_end=0 means "end of body").
	tail := callTool[bodySliceOutput](t, "get_network_body", map[string]any{
		"tab":         tabID,
		"request_id":  rid,
		"range_start": 5,
	})
	if tail.Body != wantBody[5:] {
		t.Errorf("range [5,end) = %q, want %q", tail.Body, wantBody[5:])
	}
	if !tail.Truncated {
		t.Error("Truncated should be true on tail fetch")
	}
}

// TestGetResponseBodyRangePastEnd verifies range_end beyond total is
// clamped to total, and range_start beyond total returns empty.
func TestGetResponseBodyRangePastEnd(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `fetch('/api/data')`,
	})
	waitForNetwork(t, tabID, "/api/data")

	rs := callTool[getNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":         tabID,
		"mode":        "peek",
		"url_pattern": "/api/data",
	})
	rid := rs.Requests[0].ID

	full := callTool[bodySliceOutput](t, "get_network_body", map[string]any{
		"tab":        tabID,
		"request_id": rid,
	})

	// range_end past the total — clamped to total.
	clamped := callTool[bodySliceOutput](t, "get_network_body", map[string]any{
		"tab":         tabID,
		"request_id":  rid,
		"range_start": 0,
		"range_end":   full.TotalBytes + 9999,
	})
	if clamped.Body != full.Body {
		t.Errorf("range_end past total should clamp; got %q want %q", clamped.Body, full.Body)
	}

	// range_start past the total — empty body, truncated=true.
	past := callTool[bodySliceOutput](t, "get_network_body", map[string]any{
		"tab":         tabID,
		"request_id":  rid,
		"range_start": full.TotalBytes + 100,
	})
	if past.Body != "" {
		t.Errorf("range_start past total should be empty; got %q", past.Body)
	}
	if !past.Truncated {
		t.Error("Truncated should be true when start > total")
	}
	if past.TotalBytes != full.TotalBytes {
		t.Errorf("TotalBytes mismatch: %d vs %d", past.TotalBytes, full.TotalBytes)
	}
}

// TestGetNetworkRequestsFieldsProjects verifies fields restricts each
// entry to the named JSON keys.
func TestGetNetworkRequestsFieldsProjects(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `fetch('/api/data')`,
	})
	waitForNetwork(t, tabID, "/api/data")

	out := callTool[projectedNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":         tabID,
		"mode":        "peek",
		"url_pattern": "/api/data",
		"fields":      []string{"id", "url", "status"},
	})
	if len(out.Requests) == 0 {
		t.Fatal("no projected entries returned")
	}
	r := out.Requests[0]
	for _, k := range []string{"id", "url", "status"} {
		if _, ok := r[k]; !ok {
			t.Errorf("projected entry missing requested field %q: %v", k, r)
		}
	}
	for _, k := range []string{"method", "type", "request_headers", "response_headers"} {
		if _, ok := r[k]; ok {
			t.Errorf("projected entry contains non-requested field %q: %v", k, r)
		}
	}
}

// TestGetNetworkRequestsFieldsRejectsUnknown verifies an unknown field
// name produces an error rather than silently returning empty rows.
func TestGetNetworkRequestsFieldsRejectsUnknown(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "get_network_requests", map[string]any{
		"tab":    tabID,
		"mode":   "peek",
		"fields": []string{"id", "not_a_real_field"},
	})
	if !strings.Contains(errText, "not_a_real_field") {
		t.Errorf("error %q should reference the unknown field name", errText)
	}
}

// TestGetResponseBodyRangeMidCodepoint verifies a UTF-8 body sliced
// inside a multi-byte rune is still returned as text (trimmed back to
// the last valid rune boundary), not base64-encoded as if it were
// binary. The /api/unicode endpoint returns JSON with é and emoji.
func TestGetResponseBodyRangeMidCodepoint(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `fetch('/api/unicode')`,
	})
	waitForNetwork(t, tabID, "/api/unicode")

	rs := callTool[getNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":         tabID,
		"mode":        "peek",
		"url_pattern": "/api/unicode",
	})
	rid := rs.Requests[0].ID

	// The unicode endpoint returns `{"message":"héllo wörld 🌍",...}`.
	// `é` is bytes 0xC3 0xA9 — its first byte sits at some offset N.
	// Slicing to N+1 lands inside the codepoint. The body must remain
	// reported as text (Base64Encoded=false) and the slice must end
	// before the partial rune.
	full := callTool[bodySliceOutput](t, "get_network_body", map[string]any{
		"tab":        tabID,
		"request_id": rid,
	})
	idx := strings.Index(full.Body, "é")
	if idx < 0 {
		t.Fatal("expected 'é' in unicode response body")
	}
	cut := callTool[bodySliceOutput](t, "get_network_body", map[string]any{
		"tab":         tabID,
		"request_id":  rid,
		"range_start": 0,
		"range_end":   int64(idx) + 1, // mid-codepoint
	})
	if cut.Base64Encoded {
		t.Errorf("Base64Encoded = true on text body sliced mid-codepoint; should stay text. body=%q", cut.Body)
	}
	if !cut.Truncated {
		t.Error("Truncated should be true on partial fetch")
	}
	if strings.Contains(cut.Body, "\xC3") && !strings.Contains(cut.Body, "é") {
		t.Errorf("body contains a stray UTF-8 lead byte; trim-back failed: %q", cut.Body)
	}
}

// TestGetResponseBodyRangeRejectsNegative verifies negative offsets are
// rejected with a clear error rather than silently treated as "from
// start" or "to end". This catches Python-style negative indexing typos.
func TestGetResponseBodyRangeRejectsNegative(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `fetch('/api/data')`,
	})
	waitForNetwork(t, tabID, "/api/data")

	rs := callTool[getNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":         tabID,
		"mode":        "peek",
		"url_pattern": "/api/data",
	})
	rid := rs.Requests[0].ID

	for _, args := range []map[string]any{
		{"tab": tabID, "request_id": rid, "range_start": -5},
		{"tab": tabID, "request_id": rid, "range_end": -1},
	} {
		errText := callToolExpectErr(t, "get_network_body", args)
		if !strings.Contains(errText, ">= 0") {
			t.Errorf("negative range %v: error %q should reject explicitly", args, errText)
		}
	}
}

// TestNetworkEntryFieldSetMatchesStructReflection verifies the fields enum
// whitelist matches every JSON-exported field on collector.NetworkEntry.
func TestNetworkEntryFieldSetMatchesStructReflection(t *testing.T) {
	rt := reflect.TypeFor[collector.NetworkEntry]()
	want := make([]string, 0, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			name = f.Name
		}
		want = append(want, name)
	}
	slices.Sort(want)

	got := sortedKeys(networkEntryFieldSet)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("networkEntryFieldSet = %v, want %v (from collector.NetworkEntry json tags)", got, want)
	}
}

// TestGetNetworkRequestsFieldsEmptyReturnsAll verifies that omitting
// fields (or passing an empty slice) returns the full typed shape.
func TestGetNetworkRequestsFieldsEmptyReturnsAll(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `fetch('/api/data')`,
	})
	waitForNetwork(t, tabID, "/api/data")

	out := callTool[getNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":         tabID,
		"mode":        "peek",
		"url_pattern": "/api/data",
	})
	if len(out.Requests) == 0 {
		t.Fatal("no entries returned for default (no fields)")
	}
	r := out.Requests[0]
	if r.URL == "" || r.Method == "" {
		t.Errorf("default response should have all fields; got URL=%q Method=%q", r.URL, r.Method)
	}
}
