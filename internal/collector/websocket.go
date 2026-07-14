package collector

import (
	"encoding/base64"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/chromedp/cdproto/network"
)

// WebSocket frame and connection limits. Frames can flood (a real-time
// feed at 100 msg/s would overwhelm a small ring), so we cap aggressively
// per direction per socket. Connections are also bounded so a long-running
// tab that opens many short-lived sockets doesn't grow forever.
const (
	// MaxInlineFramePayload caps how many bytes of frame payload are
	// embedded in WSFrame.Payload. Text frames longer than this are
	// trimmed at the last UTF-8 rune boundary; binary frames have the
	// raw bytes capped before re-base64 encoding.
	MaxInlineFramePayload = 4096
	// DefaultWSFramesPerDirection caps frames-per-direction per socket.
	DefaultWSFramesPerDirection = 500
	// DefaultWSConnections caps live+closed connections retained per tab.
	DefaultWSConnections = 100
)

// WSDirection enumerates which side of the wire a frame travelled.
type WSDirection string

const (
	WSDirectionSent     WSDirection = "sent"
	WSDirectionReceived WSDirection = "received"
)

// WSFrame is a single WebSocket message (CDP reassembles fragments before
// emitting it, despite the "frame" name).
type WSFrame struct {
	// Opcode is the WebSocket opcode: 0x1 text, 0x2 binary, 0x8 close,
	// 0x9 ping, 0xA pong.
	Opcode int `json:"opcode"`
	// Mask is the WebSocket mask bit (true on client→server frames).
	Mask bool `json:"mask,omitempty"`
	// Direction is "sent" (browser→server) or "received".
	Direction WSDirection `json:"direction"`
	// Payload is the message body. Text frames (opcode 1) are UTF-8;
	// other frames are base64-encoded binary (PayloadBase64=true).
	Payload string `json:"payload,omitempty"`
	// PayloadBase64 is true when Payload is base64-encoded binary.
	PayloadBase64 bool `json:"payload_base64,omitempty"`
	// PayloadTruncated is true when the inline payload was cut at
	// MaxInlineFramePayload bytes.
	PayloadTruncated bool `json:"payload_truncated,omitempty"`
	// Time is when the frame was observed.
	Time time.Time `json:"time"`
}

// WSConnection summarizes a single WebSocket connection. Frame buffers are
// maintained separately and exposed via WebSocket.Frames.
type WSConnection struct {
	ID                  string            `json:"id"`
	URL                 string            `json:"url"`
	RequestHeaders      map[string]string `json:"request_headers,omitempty"`
	ResponseStatus      int64             `json:"response_status,omitempty"`
	ResponseHeaders     map[string]string `json:"response_headers,omitempty"`
	StartTime           time.Time         `json:"start_time"`
	EndTime             time.Time         `json:"end_time,omitempty"`
	Closed              bool              `json:"closed,omitempty"`
	Error               string            `json:"error,omitempty"`
	FramesSentCount     int64             `json:"frames_sent_count"`
	FramesReceivedCount int64             `json:"frames_received_count"`
}

// wsConnState couples connection metadata with its frame ring buffers.
// Each connection has its own mutex so that frame events on one socket
// do not block reads/writes on others.
type wsConnState struct {
	mu       sync.Mutex
	info     *WSConnection
	sent     *RingBuffer[WSFrame]
	received *RingBuffer[WSFrame]
}

// WebSocket collects WebSocket connection metadata and frames.
type WebSocket struct {
	mu         sync.Mutex
	conns      map[network.RequestID]*wsConnState
	order      []network.RequestID // creation-order; oldest at index 0
	maxConns   int
	maxFrames  int
	maxPayload int
}

// NewWebSocket creates a WebSocket collector. maxConns bounds retained
// connections (oldest evicted first). maxFrames bounds frames per
// direction per socket. maxPayload caps inline frame payload bytes.
func NewWebSocket(maxConns, maxFrames, maxPayload int) *WebSocket {
	if maxConns < 1 {
		maxConns = 1
	}
	if maxFrames < 1 {
		maxFrames = 1
	}
	if maxPayload < 1 {
		maxPayload = 1
	}
	return &WebSocket{
		conns:      make(map[network.RequestID]*wsConnState),
		maxConns:   maxConns,
		maxFrames:  maxFrames,
		maxPayload: maxPayload,
	}
}

// HandleCreated registers a new WebSocket connection.
func (w *WebSocket) HandleCreated(ev *network.EventWebSocketCreated) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.conns[ev.RequestID]; ok {
		return // duplicate; ignore
	}
	w.evictIfFullLocked()
	w.conns[ev.RequestID] = &wsConnState{
		info: &WSConnection{
			ID:        string(ev.RequestID),
			URL:       ev.URL,
			StartTime: time.Now(),
		},
		sent:     NewRingBuffer[WSFrame](w.maxFrames),
		received: NewRingBuffer[WSFrame](w.maxFrames),
	}
	w.order = append(w.order, ev.RequestID)
}

// lookupConn finds a connection by request id under w.mu and returns it
// without holding the map lock. The wsConnState pointer remains valid
// even after eviction (Go GC keeps it alive while the caller references
// it), so the caller can safely take c.mu and operate on the connection.
// In-flight frames on an evicted connection still land in its ring
// buffers, but those buffers become unreachable and are GC'd once the
// caller's reference is dropped.
func (w *WebSocket) lookupConn(id network.RequestID) *wsConnState {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.conns[id]
}

// HandleHandshakeRequest records the request headers from the upgrade
// handshake. May arrive before or after Created depending on Chrome
// version, so we tolerate the missing-connection case by ignoring the
// event rather than synthesizing an entry.
func (w *WebSocket) HandleHandshakeRequest(ev *network.EventWebSocketWillSendHandshakeRequest) {
	if ev.Request == nil {
		return
	}
	c := w.lookupConn(ev.RequestID)
	if c == nil {
		return
	}
	headers := headersToMap(ev.Request.Headers)
	c.mu.Lock()
	c.info.RequestHeaders = headers
	c.mu.Unlock()
}

// HandleHandshakeResponse records the upgrade response status and headers.
func (w *WebSocket) HandleHandshakeResponse(ev *network.EventWebSocketHandshakeResponseReceived) {
	if ev.Response == nil {
		return
	}
	c := w.lookupConn(ev.RequestID)
	if c == nil {
		return
	}
	respHeaders := headersToMap(ev.Response.Headers)
	var fallback map[string]string
	if ev.Response.RequestHeaders != nil {
		fallback = headersToMap(ev.Response.RequestHeaders)
	}
	c.mu.Lock()
	c.info.ResponseStatus = ev.Response.Status
	c.info.ResponseHeaders = respHeaders
	// CDP also exposes RequestHeaders on the response (the handshake
	// upgrade request as seen on the wire, plaintext). Prefer the
	// dedicated WillSendHandshakeRequest event but fall back here if it
	// hasn't arrived yet.
	if c.info.RequestHeaders == nil && fallback != nil {
		c.info.RequestHeaders = fallback
	}
	c.mu.Unlock()
}

// HandleFrameSent records an outgoing frame.
func (w *WebSocket) HandleFrameSent(ev *network.EventWebSocketFrameSent) {
	if ev.Response == nil {
		return
	}
	c := w.lookupConn(ev.RequestID)
	if c == nil {
		return
	}
	frame := w.buildFrame(ev.Response, WSDirectionSent, ev.Timestamp.Time())
	// Hold c.mu across both the Add and the counter increment so a
	// concurrent Connections() snapshot cannot observe a counter that
	// disagrees with the buffered frame count for this connection.
	// Cross-connection parallelism is preserved (each conn has its own mu).
	c.mu.Lock()
	c.sent.Add(frame)
	c.info.FramesSentCount++
	c.mu.Unlock()
}

// HandleFrameReceived records an incoming frame.
func (w *WebSocket) HandleFrameReceived(ev *network.EventWebSocketFrameReceived) {
	if ev.Response == nil {
		return
	}
	c := w.lookupConn(ev.RequestID)
	if c == nil {
		return
	}
	frame := w.buildFrame(ev.Response, WSDirectionReceived, ev.Timestamp.Time())
	c.mu.Lock()
	c.received.Add(frame)
	c.info.FramesReceivedCount++
	c.mu.Unlock()
}

// HandleFrameError records a frame-level error on the connection.
func (w *WebSocket) HandleFrameError(ev *network.EventWebSocketFrameError) {
	c := w.lookupConn(ev.RequestID)
	if c == nil {
		return
	}
	c.mu.Lock()
	c.info.Error = ev.ErrorMessage
	c.mu.Unlock()
}

// HandleClosed marks the connection closed.
func (w *WebSocket) HandleClosed(ev *network.EventWebSocketClosed) {
	c := w.lookupConn(ev.RequestID)
	if c == nil {
		return
	}
	c.mu.Lock()
	c.info.Closed = true
	c.info.EndTime = ev.Timestamp.Time()
	c.mu.Unlock()
}

// Connections returns a snapshot of all retained connections, sorted by
// creation order (oldest first). Each connection is snapshotted under its
// own mutex so the global lock is held only long enough to gather the
// connection pointer list.
func (w *WebSocket) Connections() []WSConnection {
	w.mu.Lock()
	conns := make([]*wsConnState, 0, len(w.order))
	for _, id := range w.order {
		if c, ok := w.conns[id]; ok {
			conns = append(conns, c)
		}
	}
	w.mu.Unlock()

	out := make([]WSConnection, 0, len(conns))
	for _, c := range conns {
		c.mu.Lock()
		snap := *c.info
		snap.RequestHeaders = cloneStringMap(c.info.RequestHeaders)
		snap.ResponseHeaders = cloneStringMap(c.info.ResponseHeaders)
		c.mu.Unlock()
		out = append(out, snap)
	}
	return out
}

// cloneStringMap returns a shallow copy of a string-to-string map, or nil
// if the input is empty/nil.
func cloneStringMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Frames returns frames for a connection in the given direction. peek=true
// returns a copy without consuming; peek=false drains the matching ring.
// found is false when no connection has the given request id.
func (w *WebSocket) Frames(reqID string, dir WSDirection, limit int, peek bool) (frames []WSFrame, found bool) {
	c := w.lookupConn(network.RequestID(reqID))
	if c == nil {
		return nil, false
	}
	var rb *RingBuffer[WSFrame]
	switch dir {
	case WSDirectionSent:
		rb = c.sent
	case WSDirectionReceived:
		rb = c.received
	default:
		return nil, false
	}
	if peek {
		return applyLimit(rb.Peek(nil), limit), true
	}
	return rb.Drain(nil, limit), true
}

// Clear removes all connections and their frames.
func (w *WebSocket) Clear() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.conns = make(map[network.RequestID]*wsConnState)
	w.order = w.order[:0]
}

// evictIfFullLocked drops the oldest connection when the cap is reached.
// Caller holds w.mu.
func (w *WebSocket) evictIfFullLocked() {
	if len(w.order) < w.maxConns {
		return
	}
	oldest := w.order[0]
	w.order = w.order[1:]
	delete(w.conns, oldest)
}

// buildFrame converts a CDP WebSocketFrame to our internal WSFrame,
// applying the payload truncation policy.
func (w *WebSocket) buildFrame(f *network.WebSocketFrame, dir WSDirection, ts time.Time) WSFrame {
	frame := WSFrame{
		Opcode:    int(f.Opcode),
		Mask:      f.Mask,
		Direction: dir,
		Time:      ts,
	}
	// CDP contract: opcode 1 means text payload (UTF-8 string); any
	// other opcode means binary, base64-encoded over the wire.
	if int(f.Opcode) == 1 {
		payload, truncated := truncateText(f.PayloadData, w.maxPayload)
		frame.Payload = payload
		frame.PayloadTruncated = truncated
		return frame
	}
	payload, truncated := truncateBase64(f.PayloadData, w.maxPayload)
	frame.Payload = payload
	frame.PayloadBase64 = true
	frame.PayloadTruncated = truncated
	return frame
}

// truncateText caps a UTF-8 string at maxBytes, trimming back to the last
// valid rune boundary if the cap lands mid-codepoint.
func truncateText(s string, maxBytes int) (string, bool) {
	if len(s) <= maxBytes {
		return s, false
	}
	out := s[:maxBytes]
	for len(out) > 0 && !utf8.ValidString(out) {
		out = out[:len(out)-1]
	}
	return out, true
}

// truncateBase64 caps a base64-encoded binary payload to a string whose
// decoded byte length is at most maxBytes. Returns the re-encoded
// truncated payload. CDP's WebSocketFrame.PayloadData is guaranteed to be
// base64 when the opcode is non-text; if a malformed value still arrives,
// return an empty payload rather than garbage that downstream consumers
// will fail to decode anyway.
func truncateBase64(b64 string, maxBytes int) (string, bool) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", false
	}
	if len(raw) <= maxBytes {
		return b64, false
	}
	return base64.StdEncoding.EncodeToString(raw[:maxBytes]), true
}
