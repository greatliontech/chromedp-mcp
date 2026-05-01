package tools

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/greatliontech/chromedp-mcp/internal/collector"
)

// wsURL builds a ws:// URL from the test HTTP server's URL.
func wsURL(path string) string {
	u := harness.httpSrv.URL // http://127.0.0.1:NNNN
	return "ws" + strings.TrimPrefix(u, "http") + path
}

// openWebSocket asks the page JS to open a ws:// connection, send a list
// of payloads, and stash the socket on window so subsequent tests can
// drive it. Returns once the server's greeting has been received.
func openWebSocket(t *testing.T, tabID, url string) {
	t.Helper()
	js := `(async () => {
		window.__ws = new WebSocket(` + jsString(url) + `);
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
	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":            tabID,
		"expression":     js,
		"await_promise":  true,
	})
}

// jsString wraps a Go string as a JS string literal (JSON-encoded).
func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// findWSConnection peeks get_network_requests for the given URL substring
// until a websocket entry appears or the timeout expires.
func findWSConnection(t *testing.T, tabID, urlPattern string) collector.NetworkEntry {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		out := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
			"tab":         tabID,
			"peek":        true,
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
		out := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
			"tab":  tabID,
			"peek": true,
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

	openWebSocket(t, tabID, wsURL("/ws-echo"))

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
	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `fetch('/api/data')`,
	})
	waitForNetwork(t, tabID, "/api/data")

	openWebSocket(t, tabID, wsURL("/ws-echo"))
	entry := findWSConnection(t, tabID, "/ws-echo")

	out := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":  tabID,
		"peek": true,
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

	openWebSocket(t, tabID, wsURL("/ws-echo"))
	entry := findWSConnection(t, tabID, "/ws-echo")

	// Send 3 text frames; server echoes each back.
	callTool[EvaluateOutput](t, "evaluate", map[string]any{
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

	frames := callTool[GetWebSocketFramesOutput](t, "get_websocket_frames", map[string]any{
		"tab":        tabID,
		"request_id": entry.ID,
		"peek":       true,
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

	openWebSocket(t, tabID, wsURL("/ws-echo"))
	entry := findWSConnection(t, tabID, "/ws-echo")

	// Send 4 raw bytes including non-UTF8 sequences.
	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":           tabID,
		"expression":    `window.__ws.send(new Uint8Array([0xde, 0xad, 0xbe, 0xef]))`,
		"await_promise": false,
	})

	// Greeting + binary echo = 2 received; 1 sent.
	waitForWSFrameCount(t, tabID, entry.ID, 1, 2)

	frames := callTool[GetWebSocketFramesOutput](t, "get_websocket_frames", map[string]any{
		"tab":        tabID,
		"request_id": entry.ID,
		"direction":  "sent",
		"peek":       true,
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

	openWebSocket(t, tabID, wsURL("/ws-echo"))
	entry := findWSConnection(t, tabID, "/ws-echo")

	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":           tabID,
		"expression":    `window.__ws.send('ping')`,
		"await_promise": false,
	})
	waitForWSFrameCount(t, tabID, entry.ID, 1, 2)

	sentOnly := callTool[GetWebSocketFramesOutput](t, "get_websocket_frames", map[string]any{
		"tab":        tabID,
		"request_id": entry.ID,
		"direction":  "sent",
		"peek":       true,
	})
	if len(sentOnly.Sent) == 0 {
		t.Error("direction=sent returned 0 sent frames")
	}
	if len(sentOnly.Received) != 0 {
		t.Errorf("direction=sent returned %d received frames, want 0", len(sentOnly.Received))
	}

	recvOnly := callTool[GetWebSocketFramesOutput](t, "get_websocket_frames", map[string]any{
		"tab":        tabID,
		"request_id": entry.ID,
		"direction":  "received",
		"peek":       true,
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

	openWebSocket(t, tabID, wsURL("/ws-echo"))
	entry := findWSConnection(t, tabID, "/ws-echo")

	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":           tabID,
		"expression":    `(window.__ws.send('one'), window.__ws.send('two'), 'sent')`,
		"await_promise": false,
	})
	waitForWSFrameCount(t, tabID, entry.ID, 2, 3)

	peeked := callTool[GetWebSocketFramesOutput](t, "get_websocket_frames", map[string]any{
		"tab":        tabID,
		"request_id": entry.ID,
		"peek":       true,
	})
	if len(peeked.Sent) < 2 {
		t.Fatalf("peek Sent len = %d, want >= 2", len(peeked.Sent))
	}
	// Second peek returns the same frames.
	peeked2 := callTool[GetWebSocketFramesOutput](t, "get_websocket_frames", map[string]any{
		"tab":        tabID,
		"request_id": entry.ID,
		"peek":       true,
	})
	if len(peeked2.Sent) != len(peeked.Sent) {
		t.Errorf("second peek Sent len = %d, want %d (peek must not drain)", len(peeked2.Sent), len(peeked.Sent))
	}

	// Drain.
	_ = callTool[GetWebSocketFramesOutput](t, "get_websocket_frames", map[string]any{
		"tab":        tabID,
		"request_id": entry.ID,
		"peek":       false,
	})
	post := callTool[GetWebSocketFramesOutput](t, "get_websocket_frames", map[string]any{
		"tab":        tabID,
		"request_id": entry.ID,
		"peek":       true,
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
		"peek":       true,
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
		"peek":       true,
	})
	if !strings.Contains(errText, "direction must be") {
		t.Errorf("error text = %q, want substring 'direction must be'", errText)
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
	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": `(async () => {
			for (let i = 0; i < 5; i++) await fetch('/api/data?n=' + i);
			return 'done';
		})()`,
		"await_promise": true,
	})
	openWebSocket(t, tabID, wsURL("/ws-echo"))
	wsEntry := findWSConnection(t, tabID, "/ws-echo")

	// Ask for limit=3. Even though 5 HTTP entries plus 1 WS entry exist,
	// the WS entry must be reachable somehow. The unified filter that
	// includes type=websocket gives it priority.
	out := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":   tabID,
		"peek":  true,
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

	openWebSocket(t, tabID, wsURL("/ws-echo"))
	entry := findWSConnection(t, tabID, "/ws-echo")

	// Drain (peek=false).
	out1 := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":  tabID,
		"peek": false,
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
	out2 := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":  tabID,
		"peek": false,
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

