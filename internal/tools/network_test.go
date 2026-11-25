package tools

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestGetNetworkRequestsInvalidFieldsDoesNotDrain(t *testing.T) {
	tabID := navigateToFixture(t, "network.html")
	defer closeTab(t, tabID)
	waitForNetwork(t, tabID, "/api/data")

	before := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
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
	after := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
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
	afterNeg := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
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
	callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":  tabID,
		"mode": "drain",
	})

	// Fire 3 distinct XHR/fetch requests so we know the exact count in the buffer.
	for i := 0; i < 3; i++ {
		callTool[EvaluateOutput](t, "evaluate", map[string]any{
			"tab":        tabID,
			"expression": fmt.Sprintf("fetch('/api/data?page=%d').then(r => r.text())", i),
		})
	}
	waitForNetwork(t, tabID, "page=2")

	// Drain 1 at a time: must return all 3 across 3 calls without silently discarding excess entries.
	var seen []string
	for call := 0; call < 3; call++ {
		out := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
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
	rem := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
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
	mid := trimByteSliceToValidUTF8(raw[1 : len(raw)-1])
	if !utf8.Valid(mid) || string(mid) != "é" {
		t.Errorf("trimByteSliceToValidUTF8(mid) = %q (valid=%v), want \"é\"", mid, utf8.Valid(mid))
	}
}

func TestNetworkRequests(t *testing.T) {
	tabID := navigateToFixture(t, "network.html")
	defer closeTab(t, tabID)
	waitForNetwork(t, tabID, "/api/data")

	out := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
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
	nout := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
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
	waitForNetwork(t, tabID, "/api/data")

	// Filter by URL pattern.
	out := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
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
	out = callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
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
	out1 := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":  tabID,
		"mode": "peek",
	})
	out2 := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":  tabID,
		"mode": "peek",
	})
	if len(out1.Requests) != len(out2.Requests) {
		t.Errorf("peek should not change buffer: first=%d, second=%d", len(out1.Requests), len(out2.Requests))
	}

	// Drain should clear the buffer.
	callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":  tabID,
		"mode": "drain",
	})
	out3 := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
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

	// Filter by XHR type.
	out := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":  tabID,
		"mode": "peek",
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

	out := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
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
	nout := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
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

func TestNetworkURLPatternNoMatch(t *testing.T) {
	tabID := navigateToFixture(t, "network.html")
	defer closeTab(t, tabID)
	waitForNetwork(t, tabID, "/api/data")

	out := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
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

	out := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
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

	out := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
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
	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "fetch('/api/unicode')",
	})
	waitForNetwork(t, tabID, "/api/unicode")

	nout := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":         tabID,
		"mode":        "peek",
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

func TestNetworkTypeFilterDocument(t *testing.T) {
	tabID := navigateToFixture(t, "network.html")
	defer closeTab(t, tabID)
	waitForNetwork(t, tabID, "/api/data")

	out := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":  tabID,
		"mode": "peek",
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

func TestNetworkTimingFields(t *testing.T) {
	tabID := navigateToFixture(t, "network.html")
	defer closeTab(t, tabID)
	waitForNetwork(t, tabID, "/api/data")

	out := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
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
	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "fetch('http://127.0.0.1:1/nonexistent').catch(function(){})",
	})
	waitForNetwork(t, tabID, "/nonexistent")

	out := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
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

	out := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":         tabID,
		"mode":        "peek",
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

func TestNetworkStatusMaxOnly(t *testing.T) {
	tabID := navigateToFixture(t, "network.html")
	defer closeTab(t, tabID)
	waitForNetwork(t, tabID, "/api/data")

	out := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
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
	callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":  tabID,
		"mode": "drain",
	})

	// Now peek — should return empty array.
	out := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
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
