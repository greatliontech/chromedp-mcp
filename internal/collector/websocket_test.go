package collector

import (
	"encoding/base64"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
)

// newWS creates a WebSocket collector with conservative defaults for tests.
func newWS(maxConns, maxFrames, maxPayload int) *WebSocket {
	return NewWebSocket(maxConns, maxFrames, maxPayload)
}

// fireCreated registers a connection and returns its request ID.
func fireCreated(w *WebSocket, id, url string) network.RequestID {
	rid := network.RequestID(id)
	w.HandleCreated(&network.EventWebSocketCreated{RequestID: rid, URL: url})
	return rid
}

// TestWSCreatedRegistersConnection verifies a Created event produces a
// connection summary visible via Connections.
func TestWSCreatedRegistersConnection(t *testing.T) {
	w := newWS(10, 10, 1024)
	fireCreated(w, "ws-1", "wss://example.com/ws")

	conns := w.Connections()
	if len(conns) != 1 {
		t.Fatalf("Connections len = %d, want 1", len(conns))
	}
	c := conns[0]
	if c.URL != "wss://example.com/ws" {
		t.Errorf("URL = %q, want wss://example.com/ws", c.URL)
	}
	if c.ID != "ws-1" {
		t.Errorf("ID = %q, want ws-1", c.ID)
	}
	if c.Closed {
		t.Error("Closed = true on fresh connection, want false")
	}
}

// TestWSHandshakeRecordsHeadersAndStatus verifies handshake events populate
// request/response headers and status.
func TestWSHandshakeRecordsHeadersAndStatus(t *testing.T) {
	w := newWS(10, 10, 1024)
	rid := fireCreated(w, "ws-1", "wss://example.com/ws")

	w.HandleHandshakeRequest(&network.EventWebSocketWillSendHandshakeRequest{
		RequestID: rid,
		Request: &network.WebSocketRequest{
			Headers: network.Headers{"Origin": "https://example.com"},
		},
	})
	w.HandleHandshakeResponse(&network.EventWebSocketHandshakeResponseReceived{
		RequestID: rid,
		Response: &network.WebSocketResponse{
			Status:  101,
			Headers: network.Headers{"Sec-WebSocket-Accept": "abc123"},
		},
	})

	conns := w.Connections()
	if conns[0].ResponseStatus != 101 {
		t.Errorf("ResponseStatus = %d, want 101", conns[0].ResponseStatus)
	}
	if conns[0].RequestHeaders["Origin"] != "https://example.com" {
		t.Errorf("RequestHeaders[Origin] = %q, want https://example.com", conns[0].RequestHeaders["Origin"])
	}
	if conns[0].ResponseHeaders["Sec-WebSocket-Accept"] != "abc123" {
		t.Errorf("ResponseHeaders[Sec-WebSocket-Accept] = %q, want abc123", conns[0].ResponseHeaders["Sec-WebSocket-Accept"])
	}
}

// TestWSFrameSentText verifies a text frame is captured with opcode 1 and
// payload as the original UTF-8 string.
func TestWSFrameSentText(t *testing.T) {
	w := newWS(10, 10, 1024)
	rid := fireCreated(w, "ws-1", "wss://example.com")

	w.HandleFrameSent(&network.EventWebSocketFrameSent{
		RequestID: rid,
		Timestamp: monoTime(time.Now()),
		Response: &network.WebSocketFrame{
			Opcode:      1,
			Mask:        true,
			PayloadData: `{"hello":"world"}`,
		},
	})

	frames, ok := w.Frames("ws-1", WSDirectionSent, 0, true)
	if !ok {
		t.Fatal("Frames returned ok=false for known connection")
	}
	if len(frames) != 1 {
		t.Fatalf("frames len = %d, want 1", len(frames))
	}
	f := frames[0]
	if f.Opcode != 1 {
		t.Errorf("Opcode = %d, want 1", f.Opcode)
	}
	if !f.Mask {
		t.Error("Mask = false, want true (client-sent frame)")
	}
	if f.PayloadBase64 {
		t.Error("PayloadBase64 = true on text frame, want false")
	}
	if f.Payload != `{"hello":"world"}` {
		t.Errorf("Payload = %q, want JSON string", f.Payload)
	}
	if f.PayloadTruncated {
		t.Error("PayloadTruncated = true on small frame, want false")
	}
	if f.Direction != WSDirectionSent {
		t.Errorf("Direction = %q, want sent", f.Direction)
	}

	// Sent counter increments.
	if w.Connections()[0].FramesSentCount != 1 {
		t.Errorf("FramesSentCount = %d, want 1", w.Connections()[0].FramesSentCount)
	}
}

// TestWSFrameReceivedBinary verifies opcode 2 frames are kept as base64.
func TestWSFrameReceivedBinary(t *testing.T) {
	w := newWS(10, 10, 1024)
	rid := fireCreated(w, "ws-1", "wss://example.com")

	binary := []byte{0xde, 0xad, 0xbe, 0xef}
	w.HandleFrameReceived(&network.EventWebSocketFrameReceived{
		RequestID: rid,
		Timestamp: monoTime(time.Now()),
		Response: &network.WebSocketFrame{
			Opcode:      2,
			PayloadData: base64.StdEncoding.EncodeToString(binary),
		},
	})

	frames, _ := w.Frames("ws-1", WSDirectionReceived, 0, true)
	if len(frames) != 1 {
		t.Fatalf("frames len = %d, want 1", len(frames))
	}
	f := frames[0]
	if f.Opcode != 2 {
		t.Errorf("Opcode = %d, want 2", f.Opcode)
	}
	if !f.PayloadBase64 {
		t.Error("PayloadBase64 = false on binary frame, want true")
	}
	decoded, err := base64.StdEncoding.DecodeString(f.Payload)
	if err != nil {
		t.Fatalf("Payload not base64: %v", err)
	}
	if string(decoded) != string(binary) {
		t.Errorf("decoded payload = %x, want %x", decoded, binary)
	}
	if w.Connections()[0].FramesReceivedCount != 1 {
		t.Errorf("FramesReceivedCount = %d, want 1", w.Connections()[0].FramesReceivedCount)
	}
}

// TestWSFrameTextTruncatedAtRuneBoundary verifies a text payload longer
// than the cap is truncated at the last valid rune.
func TestWSFrameTextTruncatedAtRuneBoundary(t *testing.T) {
	const payloadCap = 32
	w := newWS(10, 10, payloadCap)
	rid := fireCreated(w, "ws-1", "wss://example.com")

	// 30 ASCII bytes + 4-byte 🌍 emoji = 34 bytes; truncation to 32 lands
	// 2 bytes inside the emoji and should drop it.
	payload := strings.Repeat("A", 30) + "🌍"
	w.HandleFrameSent(&network.EventWebSocketFrameSent{
		RequestID: rid,
		Timestamp: monoTime(time.Now()),
		Response: &network.WebSocketFrame{
			Opcode:      1,
			PayloadData: payload,
		},
	})

	frames, _ := w.Frames("ws-1", WSDirectionSent, 0, true)
	f := frames[0]
	if !f.PayloadTruncated {
		t.Error("PayloadTruncated = false, want true")
	}
	if f.PayloadBase64 {
		t.Error("PayloadBase64 = true on text frame, want false")
	}
	// Only ASCII A's should remain (emoji byte fragment trimmed).
	if f.Payload != strings.Repeat("A", 30) {
		t.Errorf("Payload = %q, want 30 A's", f.Payload)
	}
}

// TestWSFrameBinaryTruncated verifies a binary payload (base64 input) is
// decoded, truncated at the byte cap, and re-encoded.
func TestWSFrameBinaryTruncated(t *testing.T) {
	const payloadCap = 16
	w := newWS(10, 10, payloadCap)
	rid := fireCreated(w, "ws-1", "wss://example.com")

	raw := make([]byte, 64)
	for i := range raw {
		raw[i] = byte(i)
	}
	w.HandleFrameReceived(&network.EventWebSocketFrameReceived{
		RequestID: rid,
		Timestamp: monoTime(time.Now()),
		Response: &network.WebSocketFrame{
			Opcode:      2,
			PayloadData: base64.StdEncoding.EncodeToString(raw),
		},
	})

	frames, _ := w.Frames("ws-1", WSDirectionReceived, 0, true)
	f := frames[0]
	if !f.PayloadTruncated {
		t.Error("PayloadTruncated = false, want true")
	}
	if !f.PayloadBase64 {
		t.Error("PayloadBase64 = false on binary frame, want true")
	}
	decoded, err := base64.StdEncoding.DecodeString(f.Payload)
	if err != nil {
		t.Fatalf("Payload not base64: %v", err)
	}
	if len(decoded) != payloadCap {
		t.Errorf("decoded length = %d, want %d", len(decoded), payloadCap)
	}
	for i := 0; i < payloadCap; i++ {
		if decoded[i] != byte(i) {
			t.Errorf("decoded[%d] = %d, want %d", i, decoded[i], i)
		}
	}
}

// TestWSFramesPeekVsDrain verifies peek leaves frames intact and drain
// removes them.
func TestWSFramesPeekVsDrain(t *testing.T) {
	w := newWS(10, 10, 1024)
	rid := fireCreated(w, "ws-1", "wss://example.com")

	for i := 0; i < 3; i++ {
		w.HandleFrameReceived(&network.EventWebSocketFrameReceived{
			RequestID: rid,
			Timestamp: monoTime(time.Now()),
			Response: &network.WebSocketFrame{
				Opcode:      1,
				PayloadData: "msg",
			},
		})
	}

	peeked, _ := w.Frames("ws-1", WSDirectionReceived, 0, true)
	if len(peeked) != 3 {
		t.Errorf("peek len = %d, want 3", len(peeked))
	}
	// Buffer should still hold all 3.
	peeked2, _ := w.Frames("ws-1", WSDirectionReceived, 0, true)
	if len(peeked2) != 3 {
		t.Errorf("second peek len = %d, want 3 (peek must not drain)", len(peeked2))
	}

	drained, _ := w.Frames("ws-1", WSDirectionReceived, 0, false)
	if len(drained) != 3 {
		t.Errorf("drain len = %d, want 3", len(drained))
	}
	post, _ := w.Frames("ws-1", WSDirectionReceived, 0, true)
	if len(post) != 0 {
		t.Errorf("after drain, peek len = %d, want 0", len(post))
	}
	// Counter is total observed, not buffer size; should still be 3.
	if w.Connections()[0].FramesReceivedCount != 3 {
		t.Errorf("FramesReceivedCount = %d, want 3 after drain", w.Connections()[0].FramesReceivedCount)
	}
}

// TestWSFramesUnknownConnection verifies a missing connection returns
// found=false rather than fabricating an empty result.
func TestWSFramesUnknownConnection(t *testing.T) {
	w := newWS(10, 10, 1024)
	_, ok := w.Frames("nonexistent", WSDirectionSent, 0, true)
	if ok {
		t.Error("Frames returned ok=true for unknown connection, want false")
	}
}

// TestWSFrameRingEvictsOldest verifies the per-direction ring bounds
// frames and drops oldest when full.
func TestWSFrameRingEvictsOldest(t *testing.T) {
	const maxFrames = 3
	w := newWS(10, maxFrames, 1024)
	rid := fireCreated(w, "ws-1", "wss://example.com")

	for i := 0; i < 5; i++ {
		w.HandleFrameReceived(&network.EventWebSocketFrameReceived{
			RequestID: rid,
			Timestamp: monoTime(time.Now()),
			Response: &network.WebSocketFrame{
				Opcode:      1,
				PayloadData: string(rune('A' + i)),
			},
		})
	}

	frames, _ := w.Frames("ws-1", WSDirectionReceived, 0, true)
	if len(frames) != maxFrames {
		t.Errorf("ring frame count = %d, want %d", len(frames), maxFrames)
	}
	// Oldest two frames (A, B) should be evicted; ring keeps C, D, E.
	wantPayloads := []string{"C", "D", "E"}
	for i, f := range frames {
		if f.Payload != wantPayloads[i] {
			t.Errorf("frame[%d] payload = %q, want %q", i, f.Payload, wantPayloads[i])
		}
	}
	// Counter shows total observed (not ring size).
	if w.Connections()[0].FramesReceivedCount != 5 {
		t.Errorf("FramesReceivedCount = %d, want 5", w.Connections()[0].FramesReceivedCount)
	}
}

// TestWSConnectionEvictsOldest verifies that exceeding the connection cap
// drops the oldest connection's metadata and frames.
func TestWSConnectionEvictsOldest(t *testing.T) {
	const maxConns = 2
	w := newWS(maxConns, 10, 1024)

	fireCreated(w, "ws-1", "wss://example.com/1")
	fireCreated(w, "ws-2", "wss://example.com/2")
	fireCreated(w, "ws-3", "wss://example.com/3")

	conns := w.Connections()
	if len(conns) != maxConns {
		t.Fatalf("Connections len = %d, want %d", len(conns), maxConns)
	}
	if conns[0].ID != "ws-2" || conns[1].ID != "ws-3" {
		t.Errorf("retained IDs = %q,%q; want ws-2,ws-3", conns[0].ID, conns[1].ID)
	}

	// Frames on the evicted connection should report not-found.
	if _, ok := w.Frames("ws-1", WSDirectionSent, 0, true); ok {
		t.Error("Frames(ws-1) returned ok=true after eviction; want false")
	}
}

// TestWSClosedRetainsConnection verifies a closed connection stays in the
// list (for post-mortem inspection) and frames remain queryable.
func TestWSClosedRetainsConnection(t *testing.T) {
	w := newWS(10, 10, 1024)
	rid := fireCreated(w, "ws-1", "wss://example.com")
	w.HandleFrameReceived(&network.EventWebSocketFrameReceived{
		RequestID: rid,
		Timestamp: monoTime(time.Now()),
		Response:  &network.WebSocketFrame{Opcode: 1, PayloadData: "msg"},
	})
	w.HandleClosed(&network.EventWebSocketClosed{
		RequestID: rid,
		Timestamp: monoTime(time.Now()),
	})

	conns := w.Connections()
	if len(conns) != 1 {
		t.Fatalf("Connections len = %d after close, want 1", len(conns))
	}
	if !conns[0].Closed {
		t.Error("Closed = false after webSocketClosed event, want true")
	}
	frames, ok := w.Frames("ws-1", WSDirectionReceived, 0, true)
	if !ok {
		t.Fatal("Frames(closed conn) ok=false, want true")
	}
	if len(frames) != 1 {
		t.Errorf("post-close frames len = %d, want 1", len(frames))
	}
}

// TestWSFrameError verifies frame errors are recorded on the connection.
func TestWSFrameError(t *testing.T) {
	w := newWS(10, 10, 1024)
	rid := fireCreated(w, "ws-1", "wss://example.com")
	w.HandleFrameError(&network.EventWebSocketFrameError{
		RequestID:    rid,
		Timestamp:    monoTime(time.Now()),
		ErrorMessage: "invalid utf-8 sequence",
	})

	if w.Connections()[0].Error != "invalid utf-8 sequence" {
		t.Errorf("Error = %q, want 'invalid utf-8 sequence'", w.Connections()[0].Error)
	}
}

// TestWSConcurrentHandlers verifies the WebSocket collector is race-safe
// under simultaneous create/frame/close events from multiple goroutines.
func TestWSConcurrentHandlers(t *testing.T) {
	const numConns = 50
	const framesPerConn = 20
	w := newWS(numConns, framesPerConn*2, 1024)

	var wg sync.WaitGroup
	for i := 0; i < numConns; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			rid := network.RequestID(string(rune('a'+idx%26)) + string(rune('a'+(idx/26)%26)))
			w.HandleCreated(&network.EventWebSocketCreated{
				RequestID: rid,
				URL:       "wss://example.com",
			})
			for j := 0; j < framesPerConn; j++ {
				w.HandleFrameSent(&network.EventWebSocketFrameSent{
					RequestID: rid,
					Timestamp: monoTime(time.Now()),
					Response:  &network.WebSocketFrame{Opcode: 1, PayloadData: "x"},
				})
			}
			w.HandleClosed(&network.EventWebSocketClosed{
				RequestID: rid,
				Timestamp: monoTime(time.Now()),
			})
		}(i)
	}

	// Concurrent readers.
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = w.Connections()
			}
		}()
	}
	wg.Wait()
}
