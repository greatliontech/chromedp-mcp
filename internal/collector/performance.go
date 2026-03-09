package collector

import (
	"time"

	"github.com/chromedp/cdproto/performancetimeline"
)

// LayoutShiftEntry represents a captured layout shift event.
type LayoutShiftEntry struct {
	Value     float64             `json:"value"`
	Sources   []LayoutShiftSource `json:"sources,omitempty"`
	Timestamp time.Time           `json:"timestamp"`
}

// LayoutShiftSource identifies the element that shifted.
type LayoutShiftSource struct {
	NodeID       int64      `json:"node_id,omitempty"`
	PreviousRect [4]float64 `json:"previous_rect,omitempty"`
	CurrentRect  [4]float64 `json:"current_rect,omitempty"`
}

// LCPEntry represents a captured Largest Contentful Paint event.
type LCPEntry struct {
	RenderTime *time.Time `json:"render_time,omitempty"`
	LoadTime   *time.Time `json:"load_time,omitempty"`
	Size       float64    `json:"size"`
	URL        string     `json:"url,omitempty"`
	Timestamp  time.Time  `json:"timestamp"`
}

// Performance collects performance timeline events (layout shifts, LCP).
type Performance struct {
	layoutShifts *RingBuffer[LayoutShiftEntry]
	lcpEntries   *RingBuffer[LCPEntry]
}

// NewPerformance creates a performance collector with the given buffer sizes.
func NewPerformance(maxLayoutShifts, maxLCP int) *Performance {
	return &Performance{
		layoutShifts: NewRingBuffer[LayoutShiftEntry](maxLayoutShifts),
		lcpEntries:   NewRingBuffer[LCPEntry](maxLCP),
	}
}

// HandleTimelineEvent processes a performancetimeline.EventTimelineEventAdded event.
func (p *Performance) HandleTimelineEvent(ev *performancetimeline.EventTimelineEventAdded) {
	entry := ev.Event

	switch entry.Type {
	case "layout-shift":
		ls := LayoutShiftEntry{
			Timestamp: entry.Time.Time(),
		}
		if entry.LayoutShiftDetails != nil {
			ls.Value = entry.LayoutShiftDetails.Value
			for _, src := range entry.LayoutShiftDetails.Sources {
				lss := LayoutShiftSource{}
				if src.NodeID > 0 {
					lss.NodeID = int64(src.NodeID)
				}
				if src.PreviousRect != nil {
					lss.PreviousRect = [4]float64{
						src.PreviousRect.X, src.PreviousRect.Y,
						src.PreviousRect.Width, src.PreviousRect.Height,
					}
				}
				if src.CurrentRect != nil {
					lss.CurrentRect = [4]float64{
						src.CurrentRect.X, src.CurrentRect.Y,
						src.CurrentRect.Width, src.CurrentRect.Height,
					}
				}
				ls.Sources = append(ls.Sources, lss)
			}
		}
		p.layoutShifts.Add(ls)

	case "largest-contentful-paint":
		lcp := LCPEntry{
			Timestamp: entry.Time.Time(),
		}
		if entry.LcpDetails != nil {
			if entry.LcpDetails.RenderTime != nil {
				t := entry.LcpDetails.RenderTime.Time()
				lcp.RenderTime = &t
			}
			if entry.LcpDetails.LoadTime != nil {
				t := entry.LcpDetails.LoadTime.Time()
				lcp.LoadTime = &t
			}
			lcp.Size = entry.LcpDetails.Size
			lcp.URL = entry.LcpDetails.URL
		}
		p.lcpEntries.Add(lcp)
	}
}

// DrainLayoutShifts returns all layout shift entries and clears the buffer.
func (p *Performance) DrainLayoutShifts(limit int) []LayoutShiftEntry {
	entries := p.layoutShifts.Drain(nil)
	return applyLimit(entries, limit)
}

// PeekLayoutShifts returns layout shift entries without clearing the buffer.
func (p *Performance) PeekLayoutShifts(limit int) []LayoutShiftEntry {
	entries := p.layoutShifts.Peek(nil)
	return applyLimit(entries, limit)
}

// DrainLCP returns all LCP entries and clears the buffer.
func (p *Performance) DrainLCP(limit int) []LCPEntry {
	entries := p.lcpEntries.Drain(nil)
	return applyLimit(entries, limit)
}

// PeekLCP returns LCP entries without clearing the buffer.
func (p *Performance) PeekLCP(limit int) []LCPEntry {
	entries := p.lcpEntries.Peek(nil)
	return applyLimit(entries, limit)
}

// Clear removes all entries from both buffers.
func (p *Performance) Clear() {
	p.layoutShifts.Clear()
	p.lcpEntries.Clear()
}
