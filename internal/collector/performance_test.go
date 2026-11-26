package collector

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

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

func TestComputeCLSSessionWindows(t *testing.T) {
	base := time.Unix(1700000000, 0)
	mk := func(sec float64, val float64, recent bool) LayoutShiftEntry {
		return LayoutShiftEntry{
			Value:          val,
			HadRecentInput: recent,
			Timestamp:      base.Add(time.Duration(sec * float64(time.Second))),
		}
	}

	cases := []struct {
		name   string
		shifts []LayoutShiftEntry
		want   float64
	}{
		{"empty", nil, 0},
		{"recent_input_excluded", []LayoutShiftEntry{mk(0, 0.5, true)}, 0},
		{"single_window", []LayoutShiftEntry{
			mk(0.0, 0.1, false),
			mk(0.5, 0.2, false),
			mk(0.8, 0.9, true), // ignored
			mk(1.2, 0.05, false),
		}, 0.35},
		{"gap_over_1s_splits_window_picks_max", []LayoutShiftEntry{
			mk(0.0, 0.10, false),
			mk(0.9, 0.10, false), // window 1 = 0.20
			mk(2.0, 0.15, false), // gap 1.1s > 1s -> window 2 starts
			mk(2.5, 0.25, false), // window 2 = 0.40
		}, 0.40},
		{"duration_over_5s_splits_window", []LayoutShiftEntry{
			mk(0.0, 0.10, false),
			mk(0.9, 0.10, false),
			mk(1.8, 0.10, false),
			mk(2.7, 0.10, false),
			mk(3.6, 0.10, false),
			mk(4.5, 0.10, false), // window 1 = 0.60 (dur 4.5s < 5s)
			mk(5.0, 0.20, false), // dur 5.0s >= 5s -> window 2 = 0.20
		}, 0.60},
		{"sub_1s_gap_and_sub_5s_dur_stay_in_window", []LayoutShiftEntry{
			mk(0.000, 0.10, false),
			mk(0.999, 0.10, false),
			mk(1.998, 0.10, false),
			mk(2.997, 0.10, false),
			mk(3.996, 0.10, false),
			mk(4.995, 0.10, false),
		}, 0.60},
		{"exact_1s_gap_splits_window", []LayoutShiftEntry{
			mk(0.0, 0.10, false),
			mk(1.0, 0.25, false), // gap == 1s starts new window
		}, 0.25},
		{"non_finite_and_non_positive_values_ignored", []LayoutShiftEntry{
			mk(0.0, 0.10, false),
			mk(0.1, math.NaN(), false),
			mk(0.2, math.Inf(1), false),
			mk(0.3, math.Inf(-1), false),
			mk(0.4, -0.5, false),
			mk(0.5, 0.0, false),
			mk(0.6, 0.30, false),
		}, 0.40},
		{"non_monotonic_timestamp_starts_new_window", []LayoutShiftEntry{
			mk(2.0, 0.10, false),
			mk(1.5, 0.25, false), // earlier than prevTime -> splits window
		}, 0.25},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ComputeCLS(tc.shifts)
			if math.IsNaN(got) || math.IsInf(got, 0) || math.Abs(got-tc.want) > 1e-9 {
				t.Errorf("ComputeCLS = %v, want %v", got, tc.want)
			}
		})
	}
}
