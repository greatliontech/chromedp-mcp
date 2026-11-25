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
