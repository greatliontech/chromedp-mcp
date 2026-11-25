package collector

import (
	"encoding/base64"
	"math"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
)

// headersToMap converts CDP Headers (a map[string]any) to a plain
// string map. CDP delivers most header values as strings but uses []any
// for headers the wire format permits to repeat (notably Set-Cookie).
// Multi-value headers are joined with newlines so the LLM sees every
// value rather than silently losing all but one.
func headersToMap(h network.Headers) map[string]string {
	if len(h) == 0 {
		return nil
	}
	out := make(map[string]string, len(h))
	for k, v := range h {
		switch t := v.(type) {
		case string:
			out[k] = t
		case []any:
			parts := make([]string, 0, len(t))
			for _, item := range t {
				if s, ok := item.(string); ok {
					parts = append(parts, s)
				}
			}
			out[k] = strings.Join(parts, "\n")
		}
	}
	return out
}

// MaxInlineRequestBody caps how much of a POST/PUT/PATCH/DELETE body is
// embedded directly in NetworkEntry.RequestBody. Chrome is asked (via
// Network.Enable's MaxPostDataSize) to omit Request.PostDataEntries
// entirely when the body exceeds this cap — Chrome gates on size, it does
// not slice. So in practice decode receives either the full body or none
// of it; for the latter the caller falls back to get_request_body, which
// re-fetches via Network.getRequestPostData.
const MaxInlineRequestBody = 4096

// NetworkEntry represents a captured network request/response pair.
type NetworkEntry struct {
	ID              string            `json:"id"`
	URL             string            `json:"url"`
	Method          string            `json:"method"`
	Status          int64             `json:"status,omitempty"`
	Type            string            `json:"type,omitempty"`
	RequestHeaders  map[string]string `json:"request_headers,omitempty"`
	ResponseHeaders map[string]string `json:"response_headers,omitempty"`
	Size            float64           `json:"size,omitempty"`
	Timing          *TimingInfo       `json:"timing,omitempty"`
	Error           string            `json:"error,omitempty"`
	BlockedReason   string            `json:"blocked_reason,omitempty"`
	CorsError       string            `json:"cors_error,omitempty"`
	Canceled        bool              `json:"canceled,omitempty"`
	StartTime       time.Time         `json:"start_time"`
	EndTime         time.Time         `json:"end_time,omitempty"`
	// ReceivedAt is the wall-clock time the request-will-be-sent event
	// was processed by the Go-side collector. Used by activity
	// observation for time-window filtering on the same Go clock as
	// window start (StartTime comes from CDP WallTime, which can differ
	// slightly from Go's time.Now()). JSON-hidden.
	ReceivedAt        time.Time `json:"-"`
	startMono         cdp.MonotonicTime
	requestTime       float64
	receiveHeadersEnd float64
	Completed         bool `json:"-"`
	Failed            bool `json:"failed,omitempty"`

	// HasRequestBody is true when the request carried a body. It mirrors
	// CDP's Request.hasPostData and is set even when no inline body was
	// captured, so the caller knows it can fetch the full body via
	// get_request_body.
	HasRequestBody bool `json:"has_request_body,omitempty"`
	// RequestBody is the request body inlined up to MaxInlineRequestBody
	// bytes. Empty when the request has no body, or when the body is
	// larger than CDP's MaxPostDataSize and was not delivered with the
	// requestWillBeSent event.
	RequestBody string `json:"request_body,omitempty"`
	// RequestBodyBase64 is true when RequestBody contains base64-encoded
	// binary data (the body is not valid UTF-8 text).
	RequestBodyBase64 bool `json:"request_body_base64,omitempty"`
	// RequestBodyTruncated is true when the inline body was cut at
	// MaxInlineRequestBody. Use get_request_body to retrieve the full
	// payload.
	RequestBodyTruncated bool `json:"request_body_truncated,omitempty"`

	// FramesSentCount is set on websocket entries; pointer so the field
	// is omitted entirely from HTTP entries' JSON output. A zero value
	// (no frames yet) is still serialized when the connection exists.
	FramesSentCount *int64 `json:"frames_sent_count,omitempty"`
	// FramesReceivedCount is set on websocket entries; pointer for the
	// same reason as FramesSentCount.
	FramesReceivedCount *int64 `json:"frames_received_count,omitempty"`
}

// TimingInfo contains network timing data.
type TimingInfo struct {
	DNSLookup float64 `json:"dns_lookup_ms,omitempty"`
	Connect   float64 `json:"connect_ms,omitempty"`
	TLS       float64 `json:"tls_ms,omitempty"`
	TTFB      float64 `json:"ttfb_ms,omitempty"`
	Transfer  float64 `json:"transfer_ms,omitempty"`
	TotalTime float64 `json:"total_ms,omitempty"`
}

// Network collects network request/response data.
// Lock order: always n.mu before n.buf.mu (RingBuffer never acquires n.mu).
type Network struct {
	mu           sync.Mutex
	pending      map[network.RequestID]*NetworkEntry
	pendingOrder []network.RequestID
	maxPending   int
	buf          *RingBuffer[NetworkEntry]
}

// NewNetwork creates a network collector with the given buffer size.
func NewNetwork(maxSize int) *Network {
	if maxSize < 1 {
		maxSize = 1
	}
	return &Network{
		pending:    make(map[network.RequestID]*NetworkEntry),
		maxPending: max(maxSize*2, 200),
		buf:        NewRingBuffer[NetworkEntry](maxSize),
	}
}

// HandleRequestWillBeSent records a new outgoing request. When CDP signals an
// HTTP redirect (ev.RedirectResponse != nil), the previous pending hop sharing
// the same RequestID is finalized into the completed buffer before storing the
// redirected request.
func (n *Network) HandleRequestWillBeSent(ev *network.EventRequestWillBeSent) {
	if ev == nil || ev.Request == nil {
		return
	}
	headers := headersToMap(ev.Request.Headers)
	now := time.Now()
	startTime := now
	if wt := timeFromEpochSeconds(ev.WallTime); !wt.IsZero() {
		startTime = wt
	}
	entry := &NetworkEntry{
		ID:             string(ev.RequestID),
		URL:            ev.Request.URL,
		Method:         ev.Request.Method,
		Type:           string(ev.Type),
		RequestHeaders: headers,
		StartTime:      startTime,
		ReceivedAt:     now,
		startMono:      ev.Timestamp,
		HasRequestBody: ev.Request.HasPostData,
	}
	if ev.Request.HasPostData || len(ev.Request.PostDataEntries) > 0 {
		body, base64Encoded, truncated := decodePostDataEntries(ev.Request.PostDataEntries, MaxInlineRequestBody)
		entry.RequestBody = body
		entry.RequestBodyBase64 = base64Encoded
		entry.RequestBodyTruncated = truncated
	}

	n.mu.Lock()
	defer n.mu.Unlock()

	if ev.RedirectResponse != nil {
		if prev, ok := n.pending[ev.RequestID]; ok {
			applyResponse(prev, ev.RedirectResponse, ev.Type)
			prev.Completed = true
			prev.Size = ev.RedirectResponse.EncodedDataLength
			prev.EndTime = endTimeFromMonotonic(prev.StartTime, prev.startMono, ev.Timestamp)
			finalizeTiming(prev, ev.Timestamp)
			n.buf.Add(*prev)
			delete(n.pending, ev.RequestID)
			n.removePendingOrderLocked(ev.RequestID)
		}
	}

	if _, exists := n.pending[ev.RequestID]; !exists {
		if len(n.pendingOrder) >= n.maxPending {
			oldest := n.pendingOrder[0]
			copy(n.pendingOrder, n.pendingOrder[1:])
			n.pendingOrder[len(n.pendingOrder)-1] = ""
			n.pendingOrder = n.pendingOrder[:len(n.pendingOrder)-1]
			delete(n.pending, oldest)
		}
		n.pendingOrder = append(n.pendingOrder, ev.RequestID)
	}
	n.pending[ev.RequestID] = entry
}

// removePendingOrderLocked removes id from pendingOrder. Caller holds n.mu.
func (n *Network) removePendingOrderLocked(id network.RequestID) {
	n.pendingOrder = slices.DeleteFunc(n.pendingOrder, func(x network.RequestID) bool {
		return x == id
	})
}

// decodePostDataEntries concatenates CDP PostDataEntries (cdproto decodes
// each wire base64 Bytes field into []byte), trims the result to maxBytes,
// and reports the inline body in either UTF-8 string form or base64 form.
//
// Returns body, base64Encoded, truncated. body is empty when entries are
// missing or empty (which can happen when CDP omits PostData because the
// body exceeds MaxPostDataSize — caller should fall back to has_request_body
// + get_request_body).
func decodePostDataEntries(entries []*network.PostDataEntry, maxBytes int) (string, bool, bool) {
	if len(entries) == 0 {
		return "", false, false
	}
	var buf []byte
	for _, e := range entries {
		if e == nil || len(e.Bytes) == 0 {
			continue
		}
		buf = append(buf, e.Bytes...)
	}
	if len(buf) == 0 {
		return "", false, false
	}
	// Classify text vs binary on the *full* body so a UTF-8 string cut
	// mid-codepoint doesn't get misreported as binary after truncation.
	if utf8.Valid(buf) {
		trimmed, truncated := TruncateUTF8Bytes(buf, maxBytes)
		return string(trimmed), false, truncated
	}
	truncated := false
	if len(buf) > maxBytes {
		buf = buf[:maxBytes]
		truncated = true
	}
	return base64.StdEncoding.EncodeToString(buf), true, truncated
}

// timingDelta returns end - start when both endpoints are finite, non-negative,
// and ordered. CDP uses -1 as a sentinel for phases that did not occur.
func timingDelta(start, end float64) float64 {
	if math.IsNaN(start) || math.IsInf(start, 0) || math.IsNaN(end) || math.IsInf(end, 0) {
		return 0
	}
	if start < 0 || end < start {
		return 0
	}
	return end - start
}

// applyResponse populates response status, headers, and phase timings on entry.
func applyResponse(entry *NetworkEntry, resp *network.Response, typ network.ResourceType) {
	entry.Status = int64(resp.Status)
	if typ != "" {
		entry.Type = string(typ)
	}
	entry.ResponseHeaders = headersToMap(resp.Headers)

	if resp.Timing != nil {
		t := resp.Timing
		entry.requestTime = t.RequestTime
		entry.receiveHeadersEnd = t.ReceiveHeadersEnd
		entry.Timing = &TimingInfo{
			DNSLookup: timingDelta(t.DNSStart, t.DNSEnd),
			Connect:   timingDelta(t.ConnectStart, t.ConnectEnd),
			TLS:       timingDelta(t.SslStart, t.SslEnd),
			TTFB:      timingDelta(t.SendEnd, t.ReceiveHeadersEnd),
		}
	}
}

// finalizeTiming populates TotalTime and Transfer on entry.Timing once loading ends.
func finalizeTiming(entry *NetworkEntry, endMono cdp.MonotonicTime) {
	if entry.Timing == nil {
		return
	}
	if total := entry.EndTime.Sub(entry.StartTime).Seconds() * 1000; total > 0 {
		entry.Timing.TotalTime = total
	}
	if entry.requestTime > 0 && entry.receiveHeadersEnd >= 0 &&
		!math.IsNaN(entry.requestTime) && !math.IsInf(entry.requestTime, 0) &&
		!math.IsNaN(entry.receiveHeadersEnd) && !math.IsInf(entry.receiveHeadersEnd, 0) &&
		!math.IsNaN(float64(endMono)) && !math.IsInf(float64(endMono), 0) &&
		float64(endMono) >= entry.requestTime {
		elapsedMs := (float64(endMono) - entry.requestTime) * 1000
		if transfer := elapsedMs - entry.receiveHeadersEnd; transfer > 0 && !math.IsNaN(transfer) && !math.IsInf(transfer, 0) {
			entry.Timing.Transfer = transfer
		}
	}
}

// HandleResponseReceived records a response for a pending request.
func (n *Network) HandleResponseReceived(ev *network.EventResponseReceived) {
	if ev == nil || ev.Response == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	entry, ok := n.pending[ev.RequestID]
	if !ok {
		return
	}
	applyResponse(entry, ev.Response, ev.Type)
}

// HandleLoadingFinished records a completed request.
func (n *Network) HandleLoadingFinished(ev *network.EventLoadingFinished) {
	if ev == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	entry, ok := n.pending[ev.RequestID]
	if !ok {
		return
	}
	entry.Completed = true
	entry.Size = ev.EncodedDataLength
	entry.EndTime = endTimeFromMonotonic(entry.StartTime, entry.startMono, ev.Timestamp)
	finalizeTiming(entry, ev.Timestamp)
	// Add to buf BEFORE removing from pending while holding n.mu so a
	// concurrent CountStartedSince never observes a transition gap.
	n.buf.Add(*entry)
	delete(n.pending, ev.RequestID)
	n.removePendingOrderLocked(ev.RequestID)
}

// HandleLoadingFailed records a failed request.
func (n *Network) HandleLoadingFailed(ev *network.EventLoadingFailed) {
	if ev == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	entry, ok := n.pending[ev.RequestID]
	if !ok {
		return
	}
	entry.Failed = true
	entry.Error = ev.ErrorText
	entry.Canceled = ev.Canceled
	if ev.BlockedReason != "" {
		entry.BlockedReason = string(ev.BlockedReason)
	}
	if ev.CorsErrorStatus != nil && ev.CorsErrorStatus.CorsError != "" {
		entry.CorsError = string(ev.CorsErrorStatus.CorsError)
	}
	if entry.Error == "" {
		switch {
		case entry.BlockedReason != "":
			entry.Error = entry.BlockedReason
		case entry.CorsError != "":
			entry.Error = entry.CorsError
		case entry.Canceled:
			entry.Error = "canceled"
		}
	}
	entry.EndTime = endTimeFromMonotonic(entry.StartTime, entry.startMono, ev.Timestamp)
	finalizeTiming(entry, ev.Timestamp)
	// Add to buf BEFORE removing from pending while holding n.mu so a
	// concurrent CountStartedSince never observes a transition gap.
	n.buf.Add(*entry)
	delete(n.pending, ev.RequestID)
	n.removePendingOrderLocked(ev.RequestID)
}

// NetworkFilter specifies which network entries to return.
type NetworkFilter struct {
	Type       string
	StatusMin  int
	StatusMax  int
	URLPattern string
	FailedOnly bool
}

// Drain returns up to limit entries matching the filter, removing only
// the returned entries from the buffer. Entries that don't match the
// filter or fall beyond the limit are retained.
func (n *Network) Drain(f *NetworkFilter, limit int) []NetworkEntry {
	return n.buf.Drain(networkFilter(f), limit)
}

// Peek returns up to limit entries without clearing the buffer.
func (n *Network) Peek(f *NetworkFilter, limit int) []NetworkEntry {
	return n.buf.Peek(networkFilter(f), limit)
}

// CountStartedSince returns the number of requests whose ReceivedAt
// (the wall-clock time HandleRequestWillBeSent ran) is at or after t.
// Walks both the completed buffer and the pending map so a request
// that started in the window but hasn't completed yet is still counted
// — activity observation relies on this to avoid silently missing slow
// XHRs, which is exactly the case it exists to catch: an action fires a
// request that outlives the observation window.
//
// Holding n.mu across both pending and buf.Peek (preserving lock order
// n.mu -> n.buf.mu) makes the two sets strictly disjoint at the snapshot
// instant because HandleRequestWillBeSent/LoadingFinished/LoadingFailed all
// mutate pending and buf under n.mu. No entry is ever missed or double-counted,
// even across 3xx redirects that reuse RequestID.
func (n *Network) CountStartedSince(t time.Time) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	count := 0
	for _, e := range n.pending {
		if !e.ReceivedAt.Before(t) {
			count++
		}
	}
	for _, e := range n.buf.Peek(nil, 0) {
		if !e.ReceivedAt.Before(t) {
			count++
		}
	}
	return count
}

// Clear removes all entries from both the pending map and completed buffer.
func (n *Network) Clear() {
	n.mu.Lock()
	n.pending = make(map[network.RequestID]*NetworkEntry)
	clear(n.pendingOrder)
	n.pendingOrder = n.pendingOrder[:0]
	n.buf.Clear()
	n.mu.Unlock()
}

func networkFilter(f *NetworkFilter) func(NetworkEntry) bool {
	if f == nil {
		return nil
	}
	return func(e NetworkEntry) bool {
		return MatchesFilter(f, &e)
	}
}

// MatchesFilter reports whether a NetworkEntry passes a NetworkFilter. It
// is the single source of truth for HTTP and WebSocket entry filtering;
// callers that build entries outside this package (e.g. WS connections
// projected into NetworkEntry shape) use it directly.
//
// Status filters (StatusMin/StatusMax) apply to HTTP entries unconditionally
// (HTTP failures arrive with Status==0 and the user expects "status_min=200"
// to exclude them, matching the historical behavior). For WebSocket entries
// the filters are skipped while the handshake is pending (Status==0) so
// in-flight sockets remain visible.
func MatchesFilter(f *NetworkFilter, e *NetworkEntry) bool {
	if f == nil {
		return true
	}
	if f.FailedOnly && !e.Failed {
		return false
	}
	if f.Type != "" && !strings.EqualFold(e.Type, f.Type) {
		return false
	}
	skipStatusGate := strings.EqualFold(e.Type, "websocket") && e.Status == 0
	if !skipStatusGate {
		if f.StatusMin > 0 && int(e.Status) < f.StatusMin {
			return false
		}
		if f.StatusMax > 0 && int(e.Status) > f.StatusMax {
			return false
		}
	}
	if f.URLPattern != "" && !strings.Contains(e.URL, f.URLPattern) {
		return false
	}
	return true
}
