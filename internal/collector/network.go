package collector

import (
	"encoding/base64"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/chromedp/cdproto/network"
)

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
	StartTime       time.Time         `json:"start_time"`
	EndTime         time.Time         `json:"end_time,omitempty"`
	Completed       bool              `json:"-"`
	Failed          bool              `json:"failed,omitempty"`

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
type Network struct {
	mu      sync.Mutex
	pending map[network.RequestID]*NetworkEntry
	buf     *RingBuffer[NetworkEntry]
}

// NewNetwork creates a network collector with the given buffer size.
func NewNetwork(maxSize int) *Network {
	return &Network{
		pending: make(map[network.RequestID]*NetworkEntry),
		buf:     NewRingBuffer[NetworkEntry](maxSize),
	}
}

// HandleRequestWillBeSent records a new outgoing request.
func (n *Network) HandleRequestWillBeSent(ev *network.EventRequestWillBeSent) {
	headers := make(map[string]string)
	for k, v := range ev.Request.Headers {
		if s, ok := v.(string); ok {
			headers[k] = s
		}
	}
	entry := &NetworkEntry{
		ID:             string(ev.RequestID),
		URL:            ev.Request.URL,
		Method:         ev.Request.Method,
		Type:           string(ev.Type),
		RequestHeaders: headers,
		StartTime:      ev.Timestamp.Time(),
		HasRequestBody: ev.Request.HasPostData,
	}
	if ev.Request.HasPostData || len(ev.Request.PostDataEntries) > 0 {
		body, base64Encoded, truncated := decodePostDataEntries(ev.Request.PostDataEntries, MaxInlineRequestBody)
		entry.RequestBody = body
		entry.RequestBodyBase64 = base64Encoded
		entry.RequestBodyTruncated = truncated
	}
	n.mu.Lock()
	n.pending[ev.RequestID] = entry
	n.mu.Unlock()
}

// decodePostDataEntries concatenates CDP PostDataEntries (each Bytes field
// is base64-encoded binary), trims the result to maxBytes, and reports the
// inline body in either UTF-8 string form or base64 form.
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
		if e == nil || e.Bytes == "" {
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(e.Bytes)
		if err != nil {
			// CDP serializes Bytes as base64. A decode failure means the
			// event is malformed; rather than guess (raw bytes? literal
			// base64-looking text?), surface nothing inline. The caller
			// still sees has_request_body=true and can fetch the full
			// body via get_request_body.
			return "", false, false
		}
		buf = append(buf, decoded...)
	}
	if len(buf) == 0 {
		return "", false, false
	}
	// Classify text vs binary on the *full* body so a UTF-8 string cut
	// mid-codepoint doesn't get misreported as binary after truncation.
	isText := utf8.Valid(buf)
	truncated := false
	if len(buf) > maxBytes {
		// Defensive: in current Chrome behavior MaxPostDataSize gates
		// the entire entries array, so decoded bodies arrive either
		// complete or absent. Still, if a future Chrome ever delivers
		// an oversized body, we cap and flag.
		buf = buf[:maxBytes]
		truncated = true
	}
	if isText {
		// Trim trailing bytes that fall inside a partial rune. UTF-8
		// runes are at most 4 bytes, so this loop runs at most 3 times.
		for len(buf) > 0 && !utf8.Valid(buf) {
			buf = buf[:len(buf)-1]
		}
		return string(buf), false, truncated
	}
	return base64.StdEncoding.EncodeToString(buf), true, truncated
}

// HandleResponseReceived records a response for a pending request.
func (n *Network) HandleResponseReceived(ev *network.EventResponseReceived) {
	n.mu.Lock()
	defer n.mu.Unlock()
	entry, ok := n.pending[ev.RequestID]
	if !ok {
		return
	}
	entry.Status = int64(ev.Response.Status)
	entry.Type = string(ev.Type)

	respHeaders := make(map[string]string)
	for k, v := range ev.Response.Headers {
		if s, ok := v.(string); ok {
			respHeaders[k] = s
		}
	}
	entry.ResponseHeaders = respHeaders

	if ev.Response.Timing != nil {
		t := ev.Response.Timing
		entry.Timing = &TimingInfo{
			DNSLookup: t.DNSEnd - t.DNSStart,
			Connect:   t.ConnectEnd - t.ConnectStart,
			TLS:       t.SslEnd - t.SslStart,
			TTFB:      t.ReceiveHeadersEnd - t.SendEnd,
		}
	}
}

// HandleLoadingFinished records a completed request.
func (n *Network) HandleLoadingFinished(ev *network.EventLoadingFinished) {
	n.mu.Lock()
	entry, ok := n.pending[ev.RequestID]
	if ok {
		delete(n.pending, ev.RequestID)
	}
	n.mu.Unlock()
	if !ok {
		return
	}
	// Safe to mutate entry without lock: it has been removed from pending,
	// so no other handler can access it concurrently.
	entry.Completed = true
	entry.Size = ev.EncodedDataLength
	entry.EndTime = ev.Timestamp.Time()
	if entry.Timing != nil {
		entry.Timing.TotalTime = entry.EndTime.Sub(entry.StartTime).Seconds() * 1000
	}
	n.buf.Add(*entry)
}

// HandleLoadingFailed records a failed request.
func (n *Network) HandleLoadingFailed(ev *network.EventLoadingFailed) {
	n.mu.Lock()
	entry, ok := n.pending[ev.RequestID]
	if ok {
		delete(n.pending, ev.RequestID)
	}
	n.mu.Unlock()
	if !ok {
		return
	}
	// Safe to mutate entry without lock: it has been removed from pending,
	// so no other handler can access it concurrently.
	entry.Failed = true
	entry.Error = ev.ErrorText
	entry.EndTime = ev.Timestamp.Time()
	n.buf.Add(*entry)
}

// NetworkFilter specifies which network entries to return.
type NetworkFilter struct {
	Type       string
	StatusMin  int
	StatusMax  int
	URLPattern string
	FailedOnly bool
}

// Drain returns all completed entries and clears the buffer.
func (n *Network) Drain(f *NetworkFilter, limit int) []NetworkEntry {
	entries := n.buf.Drain(networkFilter(f))
	return applyLimit(entries, limit)
}

// Peek returns entries without clearing the buffer.
func (n *Network) Peek(f *NetworkFilter, limit int) []NetworkEntry {
	entries := n.buf.Peek(networkFilter(f))
	return applyLimit(entries, limit)
}

// Clear removes all entries from both the pending map and completed buffer.
func (n *Network) Clear() {
	n.mu.Lock()
	n.pending = make(map[network.RequestID]*NetworkEntry)
	n.mu.Unlock()
	n.buf.Clear()
}

func networkFilter(f *NetworkFilter) func(NetworkEntry) bool {
	if f == nil {
		return nil
	}
	return func(e NetworkEntry) bool {
		if f.FailedOnly && !e.Failed {
			return false
		}
		if f.Type != "" && !strings.EqualFold(e.Type, f.Type) {
			return false
		}
		if f.StatusMin > 0 && int(e.Status) < f.StatusMin {
			return false
		}
		if f.StatusMax > 0 && int(e.Status) > f.StatusMax {
			return false
		}
		if f.URLPattern != "" && !strings.Contains(e.URL, f.URLPattern) {
			return false
		}
		return true
	}
}
