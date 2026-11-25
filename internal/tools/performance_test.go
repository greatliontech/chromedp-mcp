package tools

import (
	"math"
	"strings"
	"testing"

	"github.com/chromedp/cdproto/cdp"
	cdpcss "github.com/chromedp/cdproto/css"
	"github.com/chromedp/cdproto/performancetimeline"
	"github.com/chromedp/cdproto/profiler"
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

func TestComputeJSScriptCoverageNestedRanges(t *testing.T) {
	cases := []struct {
		name      string
		functions []*profiler.FunctionCoverage
		wantUsed  int64
		wantTotal int64
	}{
		{
			name: "outer_used_inner_unused",
			functions: []*profiler.FunctionCoverage{
				{Ranges: []*profiler.CoverageRange{{StartOffset: 0, EndOffset: 1000, Count: 1}}},
				{Ranges: []*profiler.CoverageRange{{StartOffset: 200, EndOffset: 500, Count: 0}}},
			},
			wantUsed:  700,
			wantTotal: 1000,
		},
		{
			name: "outer_used_inner_also_used_no_double_count",
			functions: []*profiler.FunctionCoverage{
				{Ranges: []*profiler.CoverageRange{{StartOffset: 0, EndOffset: 1000, Count: 1}}},
				{Ranges: []*profiler.CoverageRange{{StartOffset: 200, EndOffset: 500, Count: 5}}},
			},
			wantUsed:  1000,
			wantTotal: 1000,
		},
		{
			name: "outer_unused_inner_used",
			functions: []*profiler.FunctionCoverage{
				{Ranges: []*profiler.CoverageRange{{StartOffset: 0, EndOffset: 1000, Count: 0}}},
				{Ranges: []*profiler.CoverageRange{{StartOffset: 200, EndOffset: 500, Count: 1}}},
			},
			wantUsed:  300,
			wantTotal: 1000,
		},
		{
			name: "last_function_shorter_than_script_total",
			functions: []*profiler.FunctionCoverage{
				{Ranges: []*profiler.CoverageRange{{StartOffset: 0, EndOffset: 1000, Count: 1}}},
				{Ranges: []*profiler.CoverageRange{{StartOffset: 100, EndOffset: 200, Count: 1}}},
			},
			wantUsed:  1000,
			wantTotal: 1000,
		},
		{
			name: "degenerate_and_negative_ranges_ignored",
			functions: []*profiler.FunctionCoverage{
				nil,
				{Ranges: []*profiler.CoverageRange{
					nil,
					{StartOffset: -5, EndOffset: 10, Count: 1},
					{StartOffset: 50, EndOffset: 50, Count: 1},
					{StartOffset: 60, EndOffset: 40, Count: 1},
				}},
			},
			wantUsed:  0,
			wantTotal: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			used, total := computeJSScriptCoverage(&profiler.ScriptCoverage{
				URL:       "https://example.com/app.js",
				Functions: tc.functions,
			})
			if used != tc.wantUsed || total != tc.wantTotal {
				t.Errorf("computeJSScriptCoverage() = (used=%d, total=%d), want (%d, %d)", used, total, tc.wantUsed, tc.wantTotal)
			}
			if used > total {
				t.Errorf("used (%d) exceeded total (%d)", used, total)
			}
		})
	}
}

func TestGetCoverageValidationAndEmptyArray(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "get_coverage", map[string]any{
		"tab":  tabID,
		"type": "invalid",
	})
	if !strings.Contains(errText, "invalid coverage type") {
		t.Errorf("invalid type err = %q", errText)
	}

	// page2.html has no external/inline JS — entries must serialize as [], never null.
	raw := callToolRaw(t, "get_coverage", map[string]any{
		"tab":  tabID,
		"type": "js",
	})
	if raw.IsError {
		t.Fatalf("get_coverage error: %s", contentText(raw))
	}
	text := contentText(raw)
	if strings.Contains(text, `"entries":null`) {
		t.Errorf("get_coverage serialized entries as null instead of []: %s", text)
	}
}

func TestComputeCSSSheetCoverageOverlappingRules(t *testing.T) {
	// Nested/overlapping CSS rules must be unioned rather than summed twice.
	rules := []*cdpcss.RuleUsage{
		{StartOffset: 0, EndOffset: 100, Used: true},
		{StartOffset: 40, EndOffset: 80, Used: true},  // nested inside [0, 100)
		{StartOffset: 120, EndOffset: 200, Used: false},
		{StartOffset: 150, EndOffset: 220, Used: false}, // overlaps [120, 200) -> union [120, 220) = 100
		{StartOffset: -10, EndOffset: 5, Used: true},    // negative start ignored
		nil,
	}
	used, total := computeCSSSheetCoverage(rules)
	if used != 100 || total != 200 {
		t.Errorf("computeCSSSheetCoverage() = (used=%d, total=%d), want (100, 200)", used, total)
	}
}
