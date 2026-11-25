package collector

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/performancetimeline"
)

func TestPerformanceLayoutShiftAndHadRecentInput(t *testing.T) {
	p := NewPerformance(10, 10)
	ts := cdp.TimeSinceEpoch(1700000000.5)

	p.HandleTimelineEvent(&performancetimeline.EventTimelineEventAdded{
		Event: &performancetimeline.TimelineEvent{
			Type: "layout-shift",
			Time: ts,
			LayoutShiftDetails: &performancetimeline.LayoutShift{
				Value:          0.125,
				HadRecentInput: false,
				Sources: []*performancetimeline.LayoutShiftAttribution{
					nil,
					{
						NodeID:       cdp.BackendNodeID(42),
						PreviousRect: &dom.Rect{X: 1, Y: 2, Width: 30, Height: 40},
						CurrentRect:  &dom.Rect{X: 1, Y: 22, Width: 30, Height: 40},
					},
				},
			},
		},
	})
	p.HandleTimelineEvent(&performancetimeline.EventTimelineEventAdded{
		Event: &performancetimeline.TimelineEvent{
			Type: "layout-shift",
			Time: ts + 1,
			LayoutShiftDetails: &performancetimeline.LayoutShift{
				Value:          0.35,
				HadRecentInput: true,
			},
		},
	})

	shifts := p.PeekLayoutShifts(0)
	if len(shifts) != 2 {
		t.Fatalf("len = %d, want 2", len(shifts))
	}
	if shifts[0].Value != 0.125 || shifts[0].HadRecentInput {
		t.Errorf("shifts[0] = %+v", shifts[0])
	}
	if len(shifts[0].Sources) != 1 || shifts[0].Sources[0].NodeID != 42 {
		t.Fatalf("shifts[0].Sources = %+v", shifts[0].Sources)
	}
	if shifts[0].Sources[0].PreviousRect != [4]float64{1, 2, 30, 40} {
		t.Errorf("PreviousRect = %v", shifts[0].Sources[0].PreviousRect)
	}
	if shifts[0].Sources[0].CurrentRect != [4]float64{1, 22, 30, 40} {
		t.Errorf("CurrentRect = %v", shifts[0].Sources[0].CurrentRect)
	}
	if !shifts[1].HadRecentInput || shifts[1].Value != 0.35 {
		t.Errorf("shifts[1] = %+v, want HadRecentInput=true, Value=0.35", shifts[1])
	}

	b0, _ := json.Marshal(shifts[0])
	if strings.Contains(string(b0), "had_recent_input") {
		t.Errorf("expected had_recent_input omitted when false: %s", b0)
	}
	b1, _ := json.Marshal(shifts[1])
	if !strings.Contains(string(b1), `"had_recent_input":true`) {
		t.Errorf("expected had_recent_input:true when true: %s", b1)
	}

	// Peek with limit + Drain with limit.
	if got := p.PeekLayoutShifts(1); len(got) != 1 || got[0].Value != 0.125 {
		t.Errorf("PeekLayoutShifts(1) = %+v", got)
	}
	if got := p.DrainLayoutShifts(1); len(got) != 1 || got[0].Value != 0.125 {
		t.Errorf("DrainLayoutShifts(1) = %+v", got)
	}
	if got := p.PeekLayoutShifts(0); len(got) != 1 || !got[0].HadRecentInput {
		t.Errorf("remaining shifts = %+v", got)
	}
}

func TestPerformanceLCPAndNilGuards(t *testing.T) {
	p := NewPerformance(10, 10)
	p.HandleTimelineEvent(nil)
	p.HandleTimelineEvent(&performancetimeline.EventTimelineEventAdded{})
	p.HandleTimelineEvent(&performancetimeline.EventTimelineEventAdded{
		Event: &performancetimeline.TimelineEvent{Type: "unknown-metric"},
	})
	if len(p.PeekLayoutShifts(0)) != 0 || len(p.PeekLCP(0)) != 0 {
		t.Fatal("nil/unknown events should be ignored")
	}

	p.HandleTimelineEvent(&performancetimeline.EventTimelineEventAdded{
		Event: &performancetimeline.TimelineEvent{
			Type: "largest-contentful-paint",
			Time: cdp.TimeSinceEpoch(1700000001.0),
			LcpDetails: &performancetimeline.LargestContentfulPaint{
				RenderTime: cdp.TimeSinceEpoch(1700000000.8),
				LoadTime:   cdp.TimeSinceEpoch(1700000000.6),
				Size:       12345,
				URL:        "https://example.com/hero.jpg",
			},
		},
	})
	p.HandleTimelineEvent(&performancetimeline.EventTimelineEventAdded{
		Event: &performancetimeline.TimelineEvent{
			Type: "largest-contentful-paint",
			Time: cdp.TimeSinceEpoch(1700000002.0),
			LcpDetails: &performancetimeline.LargestContentfulPaint{
				Size: 500,
			},
		},
	})

	lcps := p.PeekLCP(0)
	if len(lcps) != 2 {
		t.Fatalf("len = %d, want 2", len(lcps))
	}
	if lcps[0].RenderTime == nil || lcps[0].LoadTime == nil || lcps[0].Size != 12345 || lcps[0].URL != "https://example.com/hero.jpg" {
		t.Errorf("lcps[0] = %+v", lcps[0])
	}
	if lcps[1].RenderTime != nil || lcps[1].LoadTime != nil {
		t.Errorf("expected nil RenderTime/LoadTime when zero, got %+v", lcps[1])
	}
	if got := p.PeekLCP(1); len(got) != 1 || got[0].Size != 12345 {
		t.Errorf("PeekLCP(1) = %+v", got)
	}
	if got := p.DrainLCP(1); len(got) != 1 || got[0].Size != 12345 {
		t.Errorf("DrainLCP(1) = %+v", got)
	}

	p.Clear()
	if len(p.PeekLayoutShifts(0)) != 0 || len(p.PeekLCP(0)) != 0 {
		t.Error("expected both buffers empty after Clear")
	}
}
