package collector

import (
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
)

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
	}
	n.mu.Lock()
	n.pending[ev.RequestID] = entry
	n.mu.Unlock()
}

// HandleResponseReceived records a response for a pending request.
func (n *Network) HandleResponseReceived(ev *network.EventResponseReceived) {
	n.mu.Lock()
	entry, ok := n.pending[ev.RequestID]
	n.mu.Unlock()
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
