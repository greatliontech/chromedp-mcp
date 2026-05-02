package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestGetResponseBodyRangeSlicesUTF8 verifies range_start/range_end on
// get_response_body returns a byte slice of the body and reports the
// total length so the caller can plan paging.
func TestGetResponseBodyRangeSlicesUTF8(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `fetch('/api/data')`,
	})
	waitForNetwork(t, tabID, "/api/data")

	out := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":         tabID,
		"mode":        "peek",
		"url_pattern": "/api/data",
	})
	if len(out.Requests) == 0 {
		t.Fatal("no /api/data request captured")
	}
	rid := out.Requests[0].ID

	// Full body fetch — total_bytes set, truncated false.
	full := callTool[GetResponseBodyOutput](t, "get_response_body", map[string]any{
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
	slice := callTool[GetResponseBodyOutput](t, "get_response_body", map[string]any{
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
	tail := callTool[GetResponseBodyOutput](t, "get_response_body", map[string]any{
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

	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `fetch('/api/data')`,
	})
	waitForNetwork(t, tabID, "/api/data")

	rs := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":         tabID,
		"mode":        "peek",
		"url_pattern": "/api/data",
	})
	rid := rs.Requests[0].ID

	full := callTool[GetResponseBodyOutput](t, "get_response_body", map[string]any{
		"tab":        tabID,
		"request_id": rid,
	})

	// range_end past the total — clamped to total.
	clamped := callTool[GetResponseBodyOutput](t, "get_response_body", map[string]any{
		"tab":         tabID,
		"request_id":  rid,
		"range_start": 0,
		"range_end":   full.TotalBytes + 9999,
	})
	if clamped.Body != full.Body {
		t.Errorf("range_end past total should clamp; got %q want %q", clamped.Body, full.Body)
	}

	// range_start past the total — empty body, truncated=true.
	past := callTool[GetResponseBodyOutput](t, "get_response_body", map[string]any{
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

// TestEvaluateMaxResultBytesCaps verifies max_result_bytes truncates
// large evaluation results and reports the cut.
func TestEvaluateMaxResultBytesCaps(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":              tabID,
		"expression":       `'A'.repeat(2000)`,
		"max_result_bytes": 100,
	})
	if !out.Truncated {
		t.Fatal("Truncated should be true when result exceeds cap")
	}
	if out.TotalBytes != 2002 { // 2000 chars + 2 quote bytes
		t.Errorf("TotalBytes = %d, want 2002", out.TotalBytes)
	}
	// Result is now a JSON string containing the truncated prefix.
	var asString string
	if err := json.Unmarshal(out.Result, &asString); err != nil {
		t.Fatalf("Result should be a JSON string after truncation: %v", err)
	}
	if len(asString) != 100 {
		t.Errorf("truncated prefix length = %d, want 100", len(asString))
	}
	if !strings.HasPrefix(asString, `"AAA`) {
		t.Errorf("truncated prefix = %q, want to start with the JSON open quote+A's", asString[:10])
	}
}

// TestEvaluateMaxResultBytesUnderCap verifies that results under the
// cap pass through unchanged with no Truncated/TotalBytes.
func TestEvaluateMaxResultBytesUnderCap(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":              tabID,
		"expression":       `'small'`,
		"max_result_bytes": 100,
	})
	if out.Truncated {
		t.Error("Truncated should be false when result fits under cap")
	}
	if out.TotalBytes != 0 {
		t.Errorf("TotalBytes = %d, want 0 when not truncated", out.TotalBytes)
	}
	if string(out.Result) != `"small"` {
		t.Errorf("Result = %s, want \"small\"", string(out.Result))
	}
}

// TestGetNetworkRequestsFieldsProjects verifies fields restricts each
// entry to the named JSON keys.
func TestGetNetworkRequestsFieldsProjects(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `fetch('/api/data')`,
	})
	waitForNetwork(t, tabID, "/api/data")

	out := callTool[ProjectedNetworkRequestsOutput](t, "get_network_requests", map[string]any{
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

	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `fetch('/api/unicode')`,
	})
	waitForNetwork(t, tabID, "/api/unicode")

	rs := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
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
	full := callTool[GetResponseBodyOutput](t, "get_response_body", map[string]any{
		"tab":        tabID,
		"request_id": rid,
	})
	idx := strings.Index(full.Body, "é")
	if idx < 0 {
		t.Fatal("expected 'é' in unicode response body")
	}
	cut := callTool[GetResponseBodyOutput](t, "get_response_body", map[string]any{
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

	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `fetch('/api/data')`,
	})
	waitForNetwork(t, tabID, "/api/data")

	rs := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":         tabID,
		"mode":        "peek",
		"url_pattern": "/api/data",
	})
	rid := rs.Requests[0].ID

	for _, args := range []map[string]any{
		{"tab": tabID, "request_id": rid, "range_start": -5},
		{"tab": tabID, "request_id": rid, "range_end": -1},
	} {
		errText := callToolExpectErr(t, "get_response_body", args)
		if !strings.Contains(errText, ">= 0") {
			t.Errorf("negative range %v: error %q should reject explicitly", args, errText)
		}
	}
}

// TestEvaluateMaxResultBytesUTF8Safe verifies the truncation cut is
// trimmed to a valid UTF-8 rune boundary so the prefix doesn't contain
// stray U+FFFD replacement characters from json.Marshal silently
// fixing an invalid encoding.
func TestEvaluateMaxResultBytesUTF8Safe(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	// Result is "ééééé" (10 bytes = 5 × 2-byte é) plus the wrapping
	// quotes (12 bytes total). Cap to 5 bytes lands inside a codepoint.
	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":              tabID,
		"expression":       `'é'.repeat(5)`,
		"max_result_bytes": 5,
	})
	if !out.Truncated {
		t.Fatal("Truncated should be true")
	}
	var asString string
	if err := json.Unmarshal(out.Result, &asString); err != nil {
		t.Fatalf("Result should unmarshal as JSON string: %v", err)
	}
	if strings.ContainsRune(asString, '�') {
		t.Errorf("truncated prefix contains U+FFFD replacement — UTF-8 trim failed: %q", asString)
	}
}

// TestGetNetworkRequestsOutputSchemaPresent verifies the tool advertises
// an explicit OutputSchema even though the handler returns `any`. Without
// this, schema-aware MCP clients lose all hint about response shape.
func TestGetNetworkRequestsOutputSchemaPresent(t *testing.T) {
	listed, err := harness.session.ListTools(harness.ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	for _, tool := range listed.Tools {
		if tool.Name != "get_network_requests" {
			continue
		}
		if tool.OutputSchema == nil {
			t.Error("get_network_requests has no OutputSchema; clients lose response-shape hints")
		}
		return
	}
	t.Fatal("get_network_requests tool not found")
}

// TestGetNetworkRequestsFieldsEmptyReturnsAll verifies that omitting
// fields (or passing an empty slice) returns the full typed shape.
func TestGetNetworkRequestsFieldsEmptyReturnsAll(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `fetch('/api/data')`,
	})
	waitForNetwork(t, tabID, "/api/data")

	out := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
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
