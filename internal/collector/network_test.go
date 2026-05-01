package collector

import (
	"encoding/base64"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
)

// monoTime returns a *cdp.MonotonicTime from a time.Time value.
func monoTime(t time.Time) *cdp.MonotonicTime {
	mt := cdp.MonotonicTime(t)
	return &mt
}

// TestNetworkConcurrentHandlers exercises the Network collector's handler
// methods from multiple goroutines to verify there are no data races. The
// fix for this (holding mu.Lock around HandleResponseReceived mutations)
// should be caught by the -race detector if regressed.
func TestNetworkConcurrentHandlers(t *testing.T) {
	const numRequests = 100
	n := NewNetwork(numRequests)

	var wg sync.WaitGroup
	now := time.Now()

	// Simulate concurrent request/response/finished cycles.
	for i := 0; i < numRequests; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			reqID := network.RequestID(fmt.Sprintf("req-%d", idx))

			// Simulate RequestWillBeSent.
			n.HandleRequestWillBeSent(&network.EventRequestWillBeSent{
				RequestID: reqID,
				Request: &network.Request{
					URL:     fmt.Sprintf("http://example.com/%d", idx),
					Method:  "GET",
					Headers: network.Headers{},
				},
				Type:      "XHR",
				Timestamp: monoTime(now),
			})

			// Simulate ResponseReceived (the previously-racy handler).
			n.HandleResponseReceived(&network.EventResponseReceived{
				RequestID: reqID,
				Type:      "XHR",
				Response: &network.Response{
					Status:  200,
					Headers: network.Headers{"Content-Type": "text/plain"},
				},
			})

			// Simulate LoadingFinished.
			n.HandleLoadingFinished(&network.EventLoadingFinished{
				RequestID:         reqID,
				Timestamp:         monoTime(now.Add(100 * time.Millisecond)),
				EncodedDataLength: 1024,
			})
		}(i)
	}

	// Also run concurrent readers while writers are active.
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_ = n.Peek(nil, 0)
			}
		}()
	}

	wg.Wait()

	// Verify all completed entries landed in the buffer.
	entries := n.Drain(nil, 0)
	if len(entries) != numRequests {
		t.Errorf("expected %d completed entries, got %d", numRequests, len(entries))
	}

	// Verify pending map is empty (all requests completed).
	n.mu.Lock()
	pendingLen := len(n.pending)
	n.mu.Unlock()
	if pendingLen != 0 {
		t.Errorf("pending map should be empty after all requests complete, got %d", pendingLen)
	}
}

// TestNetworkConcurrentHandlersWithFailures mixes successful and failed
// requests to exercise HandleLoadingFailed concurrently with other handlers.
func TestNetworkConcurrentHandlersWithFailures(t *testing.T) {
	const numRequests = 100
	n := NewNetwork(numRequests)

	var wg sync.WaitGroup
	now := time.Now()

	for i := 0; i < numRequests; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			reqID := network.RequestID(fmt.Sprintf("req-%d", idx))

			n.HandleRequestWillBeSent(&network.EventRequestWillBeSent{
				RequestID: reqID,
				Request: &network.Request{
					URL:     fmt.Sprintf("http://example.com/%d", idx),
					Method:  "GET",
					Headers: network.Headers{},
				},
				Type:      "XHR",
				Timestamp: monoTime(now),
			})

			n.HandleResponseReceived(&network.EventResponseReceived{
				RequestID: reqID,
				Type:      "XHR",
				Response: &network.Response{
					Status:  200,
					Headers: network.Headers{"Content-Type": "text/plain"},
				},
			})

			// Even-indexed requests fail, odd-indexed succeed.
			if idx%2 == 0 {
				n.HandleLoadingFailed(&network.EventLoadingFailed{
					RequestID: reqID,
					Timestamp: monoTime(now.Add(50 * time.Millisecond)),
					ErrorText: "net::ERR_FAILED",
				})
			} else {
				n.HandleLoadingFinished(&network.EventLoadingFinished{
					RequestID:         reqID,
					Timestamp:         monoTime(now.Add(50 * time.Millisecond)),
					EncodedDataLength: 512,
				})
			}
		}(i)
	}

	wg.Wait()

	entries := n.Drain(nil, 0)
	if len(entries) != numRequests {
		t.Errorf("expected %d entries, got %d", numRequests, len(entries))
	}

	var failed, completed int
	for _, e := range entries {
		if e.Failed {
			failed++
		}
		if e.Completed {
			completed++
		}
	}
	if failed != numRequests/2 {
		t.Errorf("expected %d failed entries, got %d", numRequests/2, failed)
	}
	if completed != numRequests/2 {
		t.Errorf("expected %d completed entries, got %d", numRequests/2, completed)
	}
}

// TestNetworkResponseReceivedWithoutRequest verifies that a response for an
// unknown request ID is silently ignored (no panic, no spurious entry).
func TestNetworkResponseReceivedWithoutRequest(t *testing.T) {
	n := NewNetwork(10)

	// Response for a request that was never sent.
	n.HandleResponseReceived(&network.EventResponseReceived{
		RequestID: "orphan-req",
		Type:      "XHR",
		Response: &network.Response{
			Status:  200,
			Headers: network.Headers{},
		},
	})

	entries := n.Drain(nil, 0)
	if len(entries) != 0 {
		t.Errorf("expected 0 entries for orphan response, got %d", len(entries))
	}
}

// TestNetworkClear verifies that Clear empties both pending and completed.
func TestNetworkClear(t *testing.T) {
	n := NewNetwork(10)
	now := time.Now()

	reqID := network.RequestID("req-1")
	n.HandleRequestWillBeSent(&network.EventRequestWillBeSent{
		RequestID: reqID,
		Request:   &network.Request{URL: "http://example.com", Method: "GET", Headers: network.Headers{}},
		Type:      "Document",
		Timestamp: monoTime(now),
	})

	// Add a completed entry.
	reqID2 := network.RequestID("req-2")
	n.HandleRequestWillBeSent(&network.EventRequestWillBeSent{
		RequestID: reqID2,
		Request:   &network.Request{URL: "http://example.com/2", Method: "GET", Headers: network.Headers{}},
		Type:      "Document",
		Timestamp: monoTime(now),
	})
	n.HandleResponseReceived(&network.EventResponseReceived{
		RequestID: reqID2,
		Type:      "Document",
		Response:  &network.Response{Status: 200, Headers: network.Headers{}},
	})
	n.HandleLoadingFinished(&network.EventLoadingFinished{
		RequestID: reqID2,
		Timestamp: monoTime(now.Add(50 * time.Millisecond)),
	})

	n.Clear()

	// Pending should be empty.
	n.mu.Lock()
	pendingLen := len(n.pending)
	n.mu.Unlock()
	if pendingLen != 0 {
		t.Errorf("after Clear, pending = %d, want 0", pendingLen)
	}

	// Buffer should be empty.
	entries := n.Drain(nil, 0)
	if len(entries) != 0 {
		t.Errorf("after Clear, buffer = %d, want 0", len(entries))
	}
}

// ===========================================================================
// Fix: Download collector mutex deadlock (download.go)
//
// HandleDownloadProgress previously had no default case in its switch
// statement. If an unexpected DownloadProgressState arrived, the mutex
// would never be unlocked, causing a deadlock on the next call.
// The fix adds a default case that unlocks and returns.
// ===========================================================================

func TestDownloadProgressConcurrent(t *testing.T) {
	d := NewDownload(100, "")

	var wg sync.WaitGroup
	const numDownloads = 50

	for i := 0; i < numDownloads; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			guid := fmt.Sprintf("guid-%d", idx)

			d.HandleDownloadWillBegin(&browser.EventDownloadWillBegin{
				GUID:              guid,
				URL:               fmt.Sprintf("http://example.com/file-%d.txt", idx),
				SuggestedFilename: fmt.Sprintf("file-%d.txt", idx),
			})

			d.HandleDownloadProgress(&browser.EventDownloadProgress{
				GUID:          guid,
				ReceivedBytes: 100,
				TotalBytes:    200,
				State:         browser.DownloadProgressStateInProgress,
			})

			// Complete or cancel based on index.
			if idx%2 == 0 {
				d.HandleDownloadProgress(&browser.EventDownloadProgress{
					GUID:          guid,
					ReceivedBytes: 200,
					TotalBytes:    200,
					State:         browser.DownloadProgressStateCompleted,
				})
			} else {
				d.HandleDownloadProgress(&browser.EventDownloadProgress{
					GUID:          guid,
					ReceivedBytes: 100,
					TotalBytes:    200,
					State:         browser.DownloadProgressStateCanceled,
				})
			}
		}(i)
	}
	wg.Wait()

	entries := d.Drain(0)
	if len(entries) != numDownloads {
		t.Errorf("expected %d entries, got %d", numDownloads, len(entries))
	}

	var completed, canceled int
	for _, e := range entries {
		switch e.State {
		case DownloadStateCompleted:
			completed++
		case DownloadStateCanceled:
			canceled++
		}
	}
	if completed != numDownloads/2 {
		t.Errorf("expected %d completed, got %d", numDownloads/2, completed)
	}
	if canceled != numDownloads/2 {
		t.Errorf("expected %d canceled, got %d", numDownloads/2, canceled)
	}
}

func TestDownloadProgressUnknownState(t *testing.T) {
	d := NewDownload(10, "")

	d.HandleDownloadWillBegin(&browser.EventDownloadWillBegin{
		GUID:              "test-guid",
		URL:               "http://example.com/file.txt",
		SuggestedFilename: "file.txt",
	})

	// Send an unknown state. Previously this would deadlock because the
	// mutex was never unlocked in the default case.
	d.HandleDownloadProgress(&browser.EventDownloadProgress{
		GUID:          "test-guid",
		ReceivedBytes: 50,
		TotalBytes:    100,
		State:         browser.DownloadProgressState("unknownState"),
	})

	// If we get here without deadlocking, the fix works.
	// The unknown state should NOT add an entry to the completed buffer.
	entries := d.Drain(0)
	if len(entries) != 0 {
		t.Errorf("unknown state should not add to buffer, got %d entries", len(entries))
	}

	// The entry should still be in pending (unknown state doesn't remove it).
	d.mu.Lock()
	pendingLen := len(d.pending)
	d.mu.Unlock()
	if pendingLen != 1 {
		t.Errorf("entry should still be pending after unknown state, got %d", pendingLen)
	}

	// Now send a real completion — should work without deadlock.
	d.HandleDownloadProgress(&browser.EventDownloadProgress{
		GUID:          "test-guid",
		ReceivedBytes: 100,
		TotalBytes:    100,
		State:         browser.DownloadProgressStateCompleted,
	})

	entries = d.Drain(0)
	if len(entries) != 1 {
		t.Errorf("after real completion, expected 1 entry, got %d", len(entries))
	}
}

// b64 returns the base64 encoding of s, the wire shape of CDP's
// PostDataEntry.Bytes field.
func b64(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

// postEntries builds CDP PostDataEntries from raw byte chunks.
func postEntries(chunks ...string) []*network.PostDataEntry {
	out := make([]*network.PostDataEntry, len(chunks))
	for i, c := range chunks {
		out[i] = &network.PostDataEntry{Bytes: b64(c)}
	}
	return out
}

// captureRequest sends a synthetic requestWillBeSent and returns the entry
// stored in the pending map (where it lives until response/finished).
func captureRequest(t *testing.T, n *Network, req *network.Request) *NetworkEntry {
	t.Helper()
	reqID := network.RequestID("req-test")
	n.HandleRequestWillBeSent(&network.EventRequestWillBeSent{
		RequestID: reqID,
		Request:   req,
		Type:      "XHR",
		Timestamp: monoTime(time.Now()),
	})
	n.mu.Lock()
	defer n.mu.Unlock()
	e, ok := n.pending[reqID]
	if !ok {
		t.Fatal("no pending entry after HandleRequestWillBeSent")
	}
	return e
}

// TestRequestBodyInlineUTF8 verifies a small JSON body is captured inline
// as a UTF-8 string with no truncation flag.
func TestRequestBodyInlineUTF8(t *testing.T) {
	n := NewNetwork(10)
	body := `{"hello":"world"}`
	e := captureRequest(t, n, &network.Request{
		URL:             "http://example.com/api",
		Method:          "POST",
		Headers:         network.Headers{"Content-Type": "application/json"},
		HasPostData:     true,
		PostDataEntries: postEntries(body),
	})

	if !e.HasRequestBody {
		t.Error("HasRequestBody = false, want true")
	}
	if e.RequestBody != body {
		t.Errorf("RequestBody = %q, want %q", e.RequestBody, body)
	}
	if e.RequestBodyBase64 {
		t.Error("RequestBodyBase64 = true, want false for UTF-8 body")
	}
	if e.RequestBodyTruncated {
		t.Error("RequestBodyTruncated = true, want false for small body")
	}
}

// TestRequestBodyInlineMultipleEntries verifies that several PostDataEntries
// (Chrome can split a body across chunks) are concatenated in order.
func TestRequestBodyInlineMultipleEntries(t *testing.T) {
	n := NewNetwork(10)
	e := captureRequest(t, n, &network.Request{
		URL:             "http://example.com/api",
		Method:          "POST",
		Headers:         network.Headers{},
		HasPostData:     true,
		PostDataEntries: postEntries("foo=", "bar&", "baz=qux"),
	})
	want := "foo=bar&baz=qux"
	if e.RequestBody != want {
		t.Errorf("RequestBody = %q, want %q", e.RequestBody, want)
	}
}

// TestRequestBodyInlineBinary verifies non-UTF8 bodies are base64-encoded
// and flagged.
func TestRequestBodyInlineBinary(t *testing.T) {
	n := NewNetwork(10)
	binary := []byte{0xff, 0xfe, 0xfd, 0x00, 0x01, 0x02}
	e := captureRequest(t, n, &network.Request{
		URL:    "http://example.com/upload",
		Method: "POST",
		Headers: network.Headers{
			"Content-Type": "application/octet-stream",
		},
		HasPostData: true,
		PostDataEntries: []*network.PostDataEntry{
			{Bytes: base64.StdEncoding.EncodeToString(binary)},
		},
	})

	if !e.RequestBodyBase64 {
		t.Error("RequestBodyBase64 = false, want true for binary body")
	}
	decoded, err := base64.StdEncoding.DecodeString(e.RequestBody)
	if err != nil {
		t.Fatalf("RequestBody is not base64: %v", err)
	}
	if string(decoded) != string(binary) {
		t.Errorf("decoded body = %v, want %v", decoded, binary)
	}
	if e.RequestBodyTruncated {
		t.Error("RequestBodyTruncated = true, want false")
	}
}

// TestRequestBodyInlineTruncated verifies bodies larger than the inline cap
// are cut and flagged.
func TestRequestBodyInlineTruncated(t *testing.T) {
	n := NewNetwork(10)
	big := strings.Repeat("A", MaxInlineRequestBody+512)
	e := captureRequest(t, n, &network.Request{
		URL:             "http://example.com/api",
		Method:          "POST",
		Headers:         network.Headers{},
		HasPostData:     true,
		PostDataEntries: postEntries(big),
	})

	if !e.RequestBodyTruncated {
		t.Error("RequestBodyTruncated = false, want true")
	}
	if len(e.RequestBody) != MaxInlineRequestBody {
		t.Errorf("RequestBody length = %d, want %d", len(e.RequestBody), MaxInlineRequestBody)
	}
}

// TestRequestBodyTruncatedMidUTF8 verifies that a UTF-8 body whose
// truncation point lands inside a multibyte rune is still returned as a
// string (trimmed to the last valid rune boundary), not base64-encoded as
// "binary". A 4-byte 🌍 emoji at the cap exercises the trim-back path.
func TestRequestBodyTruncatedMidUTF8(t *testing.T) {
	n := NewNetwork(10)
	// Build a body that's exactly MaxInlineRequestBody-1 ASCII bytes
	// followed by a 4-byte 🌍 (total = MaxInlineRequestBody+3). Truncation
	// to MaxInlineRequestBody lands 1 byte into the emoji.
	body := strings.Repeat("A", MaxInlineRequestBody-1) + "🌍"
	e := captureRequest(t, n, &network.Request{
		URL:             "http://example.com/api",
		Method:          "POST",
		Headers:         network.Headers{},
		HasPostData:     true,
		PostDataEntries: postEntries(body),
	})

	if !e.RequestBodyTruncated {
		t.Fatal("RequestBodyTruncated = false, want true")
	}
	if e.RequestBodyBase64 {
		t.Errorf("RequestBodyBase64 = true on UTF-8 body cut mid-rune; want false (got %q)", e.RequestBody[:min(40, len(e.RequestBody))])
	}
	if !utf8.ValidString(e.RequestBody) {
		t.Error("RequestBody is not valid UTF-8")
	}
	// The trim-back drops the partial emoji bytes, so length should be
	// MaxInlineRequestBody-1 (the ASCII portion) — emoji's first 1-3
	// bytes are stripped.
	if len(e.RequestBody) != MaxInlineRequestBody-1 {
		t.Errorf("RequestBody length = %d, want %d (ASCII portion only)",
			len(e.RequestBody), MaxInlineRequestBody-1)
	}
}

// TestRequestBodyHasButNoEntries verifies the case where Chrome reports
// HasPostData=true but omits PostDataEntries (body too large for the
// configured MaxPostDataSize). The flag should still be set so the LLM
// knows to call get_request_body.
func TestRequestBodyHasButNoEntries(t *testing.T) {
	n := NewNetwork(10)
	e := captureRequest(t, n, &network.Request{
		URL:         "http://example.com/api",
		Method:      "POST",
		Headers:     network.Headers{},
		HasPostData: true,
		// No PostDataEntries.
	})

	if !e.HasRequestBody {
		t.Error("HasRequestBody = false, want true")
	}
	if e.RequestBody != "" {
		t.Errorf("RequestBody = %q, want empty", e.RequestBody)
	}
	if e.RequestBodyTruncated {
		t.Error("RequestBodyTruncated = true, want false (nothing to truncate)")
	}
}

// TestRequestBodyMalformedBase64 verifies that a PostDataEntry whose Bytes
// field is not valid base64 (a CDP protocol violation) does not produce a
// mangled inline body. The entry should be flagged as having a body but
// the inline body is empty so the LLM falls back to get_request_body.
func TestRequestBodyMalformedBase64(t *testing.T) {
	n := NewNetwork(10)
	e := captureRequest(t, n, &network.Request{
		URL:         "http://example.com/api",
		Method:      "POST",
		Headers:     network.Headers{},
		HasPostData: true,
		// "!!!" is not valid base64 (padding/charset).
		PostDataEntries: []*network.PostDataEntry{
			{Bytes: "!!!"},
		},
	})

	if !e.HasRequestBody {
		t.Error("HasRequestBody = false, want true")
	}
	if e.RequestBody != "" {
		t.Errorf("RequestBody = %q, want empty for malformed base64", e.RequestBody)
	}
	if e.RequestBodyTruncated {
		t.Error("RequestBodyTruncated = true, want false")
	}
}

// TestRequestBodyAbsentForGET verifies GET requests have no body fields set.
func TestRequestBodyAbsentForGET(t *testing.T) {
	n := NewNetwork(10)
	e := captureRequest(t, n, &network.Request{
		URL:     "http://example.com",
		Method:  "GET",
		Headers: network.Headers{},
	})

	if e.HasRequestBody {
		t.Error("HasRequestBody = true on GET, want false")
	}
	if e.RequestBody != "" {
		t.Errorf("RequestBody = %q on GET, want empty", e.RequestBody)
	}
}

// TestMatchesFilterWSPendingHandshakePasses verifies that a WebSocket
// connection mid-handshake (Status==0, Type==websocket) is not silently
// filtered out by status_min/status_max.
func TestMatchesFilterWSPendingHandshakePasses(t *testing.T) {
	e := NetworkEntry{URL: "wss://example.com", Type: "websocket", Status: 0}
	f := &NetworkFilter{StatusMin: 200, StatusMax: 299}
	if !MatchesFilter(f, &e) {
		t.Error("WS entry with Status=0 should pass status filters; was rejected")
	}
}

// TestMatchesFilterStatusFiltersWhenSet verifies status bounds are
// enforced once Status is non-zero.
func TestMatchesFilterStatusFiltersWhenSet(t *testing.T) {
	e := NetworkEntry{URL: "http://example.com", Status: 404}
	f := &NetworkFilter{StatusMin: 200, StatusMax: 299}
	if MatchesFilter(f, &e) {
		t.Error("entry with Status=404 should be rejected by status_max=299")
	}
}

// TestMatchesFilterFailedHTTPRejectedByStatusMin verifies an HTTP entry
// that failed before getting a response (Status==0, Failed==true) is
// still rejected by status_min=200, preserving historical semantics.
// Without the type-gated carve-out for WebSocket, the status-zero skip
// would over-broadly let connection-phase failures slip through.
func TestMatchesFilterFailedHTTPRejectedByStatusMin(t *testing.T) {
	e := NetworkEntry{
		URL:    "http://example.com",
		Type:   "Document",
		Status: 0,
		Failed: true,
		Error:  "net::ERR_CONNECTION_REFUSED",
	}
	f := &NetworkFilter{StatusMin: 200}
	if MatchesFilter(f, &e) {
		t.Error("failed HTTP entry with Status=0 must be excluded by status_min=200")
	}
}

// TestHeadersToMapStringValues verifies the common case: single-string
// values pass through untouched.
func TestHeadersToMapStringValues(t *testing.T) {
	in := network.Headers{
		"Content-Type": "application/json",
		"X-Custom":     "abc",
	}
	out := headersToMap(in)
	if out["Content-Type"] != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", out["Content-Type"])
	}
	if out["X-Custom"] != "abc" {
		t.Errorf("X-Custom = %q, want abc", out["X-Custom"])
	}
}

// TestHeadersToMapArrayValues verifies array-valued headers (e.g.
// repeated Set-Cookie) are joined with newlines rather than dropped.
func TestHeadersToMapArrayValues(t *testing.T) {
	in := network.Headers{
		"Set-Cookie": []any{"a=1; Path=/", "b=2; Path=/"},
	}
	out := headersToMap(in)
	want := "a=1; Path=/\nb=2; Path=/"
	if out["Set-Cookie"] != want {
		t.Errorf("Set-Cookie = %q, want %q", out["Set-Cookie"], want)
	}
}

// TestHeadersToMapMixedArrayDropsNonStrings verifies array entries that
// aren't strings are silently skipped (defensive).
func TestHeadersToMapMixedArrayDropsNonStrings(t *testing.T) {
	in := network.Headers{
		"X-Mixed": []any{"keep", 42, "also-keep"},
	}
	out := headersToMap(in)
	if out["X-Mixed"] != "keep\nalso-keep" {
		t.Errorf("X-Mixed = %q, want 'keep\\nalso-keep'", out["X-Mixed"])
	}
}

// TestHeadersToMapEmpty verifies nil/empty inputs return nil.
func TestHeadersToMapEmpty(t *testing.T) {
	if headersToMap(nil) != nil {
		t.Error("headersToMap(nil) returned non-nil")
	}
	if headersToMap(network.Headers{}) != nil {
		t.Error("headersToMap(empty) returned non-nil")
	}
}

// TestNetworkDrainPeekFilter verifies filter and limit work on the Network collector.
func TestNetworkDrainPeekFilter(t *testing.T) {
	n := NewNetwork(20)
	now := time.Now()

	// Add 10 requests: 5 XHR with status 200, 5 Document with status 404.
	for i := 0; i < 10; i++ {
		reqID := network.RequestID(fmt.Sprintf("req-%d", i))
		typ := network.ResourceType("XHR")
		status := int64(200)
		if i >= 5 {
			typ = "Document"
			status = 404
		}
		n.HandleRequestWillBeSent(&network.EventRequestWillBeSent{
			RequestID: reqID,
			Request:   &network.Request{URL: fmt.Sprintf("http://example.com/%d", i), Method: "GET", Headers: network.Headers{}},
			Type:      typ,
			Timestamp: monoTime(now),
		})
		n.HandleResponseReceived(&network.EventResponseReceived{
			RequestID: reqID,
			Type:      typ,
			Response:  &network.Response{Status: status, Headers: network.Headers{}},
		})
		n.HandleLoadingFinished(&network.EventLoadingFinished{
			RequestID: reqID,
			Timestamp: monoTime(now.Add(10 * time.Millisecond)),
		})
	}

	// Peek with XHR filter.
	xhrEntries := n.Peek(&NetworkFilter{Type: "XHR"}, 0)
	if len(xhrEntries) != 5 {
		t.Errorf("XHR filter: expected 5, got %d", len(xhrEntries))
	}

	// Peek with limit.
	limited := n.Peek(nil, 3)
	if len(limited) != 3 {
		t.Errorf("limit=3: expected 3, got %d", len(limited))
	}

	// Peek with status filter.
	notFound := n.Peek(&NetworkFilter{StatusMin: 400, StatusMax: 499}, 0)
	if len(notFound) != 5 {
		t.Errorf("status 400-499 filter: expected 5, got %d", len(notFound))
	}
}
