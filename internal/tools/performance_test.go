package tools

import (
	"math"
	"testing"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/performancetimeline"
)

// TestGetLayoutShiftsExcludesHadRecentInput verifies that get_layout_shifts
// exposes HadRecentInput on each entry and excludes recent-input shifts from
// CumulativeLS per the Core Web Vitals CLS definition.
func TestGetLayoutShiftsExcludesHadRecentInput(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	b, err := harness.mgr.Resolve("")
	if err != nil {
		t.Fatalf("resolve browser: %v", err)
	}
	tb, err := b.Tabs.Get(tabID)
	if err != nil {
		t.Fatalf("get tab: %v", err)
	}
	tb.Performance.Clear()

	tb.Performance.HandleTimelineEvent(&performancetimeline.EventTimelineEventAdded{
		Event: &performancetimeline.TimelineEvent{
			Type: "layout-shift",
			Time: cdp.TimeSinceEpoch(1700000001),
			LayoutShiftDetails: &performancetimeline.LayoutShift{
				Value:          0.12,
				HadRecentInput: false,
			},
		},
	})
	tb.Performance.HandleTimelineEvent(&performancetimeline.EventTimelineEventAdded{
		Event: &performancetimeline.TimelineEvent{
			Type: "layout-shift",
			Time: cdp.TimeSinceEpoch(1700000002),
			LayoutShiftDetails: &performancetimeline.LayoutShift{
				Value:          0.45,
				HadRecentInput: true,
			},
		},
	})
	tb.Performance.HandleTimelineEvent(&performancetimeline.EventTimelineEventAdded{
		Event: &performancetimeline.TimelineEvent{
			Type: "layout-shift",
			Time: cdp.TimeSinceEpoch(1700000003),
			LayoutShiftDetails: &performancetimeline.LayoutShift{
				Value:          0.08,
				HadRecentInput: false,
			},
		},
	})

	out := callTool[GetLayoutShiftsOutput](t, "get_layout_shifts", map[string]any{
		"tab":  tabID,
		"mode": "drain",
	})
	if len(out.Shifts) != 3 {
		t.Fatalf("shifts len = %d, want 3", len(out.Shifts))
	}
	if !out.Shifts[1].HadRecentInput {
		t.Error("shifts[1].HadRecentInput = false, want true")
	}
	if math.Abs(out.CumulativeLS-0.20) > 1e-9 {
		t.Errorf("CumulativeLS = %v, want 0.20 (excluding 0.45 recent-input shift)", out.CumulativeLS)
	}
}
