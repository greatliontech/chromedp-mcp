package collector

import (
	"fmt"
	"sync"
	"testing"
	"time"

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
