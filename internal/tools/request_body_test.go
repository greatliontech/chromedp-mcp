package tools

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

// findRequest returns the first network entry whose URL contains the
// substring, or fails the test.
func findRequest(t *testing.T, tabID, urlPattern string) GetNetworkRequestsOutput {
	t.Helper()
	waitForNetwork(t, tabID, urlPattern)
	out := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":         tabID,
		"peek":        true,
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
	callTool[EvaluateOutput](t, "evaluate", map[string]any{
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
// inline cap is truncated, the truncated flag is set, and get_request_body
// returns the full payload.
func TestRequestBodyTruncatedLargePOST(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	// 8 KB — larger than MaxInlineRequestBody (4 KB) and larger than the
	// CDP MaxPostDataSize we configured. Chrome will set HasPostData=true
	// but omit PostDataEntries from the requestWillBeSent event, so the
	// inline body should be empty and only fetchable via get_request_body.
	const total = 8192
	callTool[EvaluateOutput](t, "evaluate", map[string]any{
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

	full := callTool[GetRequestBodyOutput](t, "get_request_body", map[string]any{
		"tab":        tabID,
		"request_id": r.ID,
	})
	if full.Base64Encoded {
		t.Error("Base64Encoded = true, want false for ASCII body")
	}
	if len(full.Body) != total {
		t.Errorf("get_request_body length = %d, want %d", len(full.Body), total)
	}
	if !strings.HasPrefix(full.Body, "AAAA") || !strings.HasSuffix(full.Body, "AAAA") {
		t.Errorf("get_request_body body did not match expected payload (got %d chars)", len(full.Body))
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
	callTool[EvaluateOutput](t, "evaluate", map[string]any{
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

// TestRequestBodyAbsentForGET verifies GET requests have no body fields
// set on the network entry.
func TestRequestBodyAbsentForGETIntegration(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	callTool[EvaluateOutput](t, "evaluate", map[string]any{
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

// TestGetRequestBodyForSmallPOST verifies get_request_body returns the
// full payload for a POST whose body was already inlined.
func TestGetRequestBodyForSmallPOST(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	body := `name=alpha&value=1`
	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": fmt.Sprintf(
			`fetch('/submit', {method:'POST', headers:{'Content-Type':'application/x-www-form-urlencoded'}, body:%q})`,
			body,
		),
	})

	out := findRequest(t, tabID, "/submit")
	r := out.Requests[0]

	full := callTool[GetRequestBodyOutput](t, "get_request_body", map[string]any{
		"tab":        tabID,
		"request_id": r.ID,
	})
	if full.Base64Encoded {
		t.Error("Base64Encoded = true; want false")
	}
	if full.Body != body {
		t.Errorf("get_request_body body = %q, want %q", full.Body, body)
	}
}
