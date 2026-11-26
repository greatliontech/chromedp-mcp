package collector

import (
	"math"
	"time"

	"github.com/chromedp/cdproto/performancetimeline"
)

// LayoutShiftEntry represents a captured layout shift event.
type LayoutShiftEntry struct {
	Value          float64             `json:"value"`
	HadRecentInput bool                `json:"had_recent_input,omitempty"`
	Sources        []LayoutShiftSource `json:"sources,omitempty"`
	Timestamp      time.Time           `json:"timestamp"`
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
	if ev == nil || ev.Event == nil {
		return
	}
	entry := ev.Event

	switch entry.Type {
	case "layout-shift":
		ls := LayoutShiftEntry{
			Timestamp: timeFromEpochSeconds(entry.Time),
		}
		if entry.LayoutShiftDetails != nil {
			ls.Value = entry.LayoutShiftDetails.Value
			ls.HadRecentInput = entry.LayoutShiftDetails.HadRecentInput
			for _, src := range entry.LayoutShiftDetails.Sources {
				if src == nil {
					continue
				}
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
			Timestamp: timeFromEpochSeconds(entry.Time),
		}
		if entry.LcpDetails != nil {
			if t := timeFromEpochSeconds(entry.LcpDetails.RenderTime); !t.IsZero() {
				lcp.RenderTime = &t
			}
			if t := timeFromEpochSeconds(entry.LcpDetails.LoadTime); !t.IsZero() {
				lcp.LoadTime = &t
			}
			lcp.Size = entry.LcpDetails.Size
			lcp.URL = entry.LcpDetails.URL
		}
		p.lcpEntries.Add(lcp)
	}
}

// DrainLayoutShifts returns up to limit layout shift entries, removing
// only the returned ones from the buffer.
func (p *Performance) DrainLayoutShifts(limit int) []LayoutShiftEntry {
	return p.layoutShifts.Drain(nil, limit)
}

// PeekLayoutShifts returns up to limit layout shift entries without clearing
// the buffer.
func (p *Performance) PeekLayoutShifts(limit int) []LayoutShiftEntry {
	return p.layoutShifts.Peek(nil, limit)
}

// DrainLCP returns up to limit LCP entries, removing only the returned
// ones from the buffer.
func (p *Performance) DrainLCP(limit int) []LCPEntry {
	return p.lcpEntries.Drain(nil, limit)
}

// PeekLCP returns up to limit LCP entries without clearing the buffer.
func (p *Performance) PeekLCP(limit int) []LCPEntry {
	return p.lcpEntries.Peek(nil, limit)
}

// Clear removes all entries from both buffers.
func (p *Performance) Clear() {
	p.layoutShifts.Clear()
	p.lcpEntries.Clear()
}

const (
	clsMaxGap      = 1 * time.Second
	clsMaxDuration = 5 * time.Second
)

// ComputeCLS computes the W3C Core Web Vitals Cumulative Layout Shift score
// using session windows (matching Google web-vitals): shifts with
// HadRecentInput == true or non-finite / non-positive Value are excluded, and
// remaining shifts are grouped into windows with < 1s gap between consecutive
// shifts and < 5s total duration from window start. Returns the maximum
// session window sum (0 when no eligible shifts exist).
func ComputeCLS(shifts []LayoutShiftEntry) float64 {
	var (
		maxScore    float64
		windowScore float64
		windowStart time.Time
		prevTime    time.Time
		inWindow    bool
	)
	for _, s := range shifts {
		if s.HadRecentInput || math.IsNaN(s.Value) || math.IsInf(s.Value, 0) || s.Value <= 0 {
			continue
		}
		if !inWindow ||
			s.Timestamp.Before(prevTime) ||
			s.Timestamp.Sub(prevTime) >= clsMaxGap ||
			s.Timestamp.Sub(windowStart) >= clsMaxDuration {
			windowStart = s.Timestamp
			windowScore = s.Value
			inWindow = true
		} else {
			windowScore += s.Value
		}
		prevTime = s.Timestamp
		if windowScore > maxScore {
			maxScore = windowScore
		}
	}
	return maxScore
}
