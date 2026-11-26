package tools

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/performancetimeline"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestGetPerformanceSessionWindowCLSAndRecentInput verifies that get_performance
// returns metrics, layout shifts with HadRecentInput, and W3C session-window CLS
// excluding recent-input shifts and taking the maximum session window.
func TestGetPerformanceSessionWindowCLSAndRecentInput(t *testing.T) {
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
			Time: cdp.TimeSinceEpoch(1700000001.0),
			LayoutShiftDetails: &performancetimeline.LayoutShift{
				Value:          0.12,
				HadRecentInput: false,
			},
		},
	})
	tb.Performance.HandleTimelineEvent(&performancetimeline.EventTimelineEventAdded{
		Event: &performancetimeline.TimelineEvent{
			Type: "layout-shift",
			Time: cdp.TimeSinceEpoch(1700000001.2),
			LayoutShiftDetails: &performancetimeline.LayoutShift{
				Value:          0.45,
				HadRecentInput: true,
			},
		},
	})
	tb.Performance.HandleTimelineEvent(&performancetimeline.EventTimelineEventAdded{
		Event: &performancetimeline.TimelineEvent{
			Type: "layout-shift",
			Time: cdp.TimeSinceEpoch(1700000001.5),
			LayoutShiftDetails: &performancetimeline.LayoutShift{
				Value:          0.08,
				HadRecentInput: false,
			},
		},
	})
	// A later shift >1s away begins a separate session window (0.15 < 0.20),
	// so W3C session-window CLS remains 0.20 rather than summing to 0.35.
	tb.Performance.HandleTimelineEvent(&performancetimeline.EventTimelineEventAdded{
		Event: &performancetimeline.TimelineEvent{
			Type: "layout-shift",
			Time: cdp.TimeSinceEpoch(1700000003.0),
			LayoutShiftDetails: &performancetimeline.LayoutShift{
				Value:          0.15,
				HadRecentInput: false,
			},
		},
	})

	// Default mode (omitted) is non-destructive peek.
	peeked := callTool[getPerformanceOutput](t, "get_performance", map[string]any{
		"tab": tabID,
	})
	if len(peeked.LayoutShifts) != 4 {
		t.Fatalf("default peek layout_shifts len = %d, want 4", len(peeked.LayoutShifts))
	}

	// Explicit drain returns all 4 shifts and clears the buffer.
	out := callTool[getPerformanceOutput](t, "get_performance", map[string]any{
		"tab":  tabID,
		"mode": "drain",
	})
	if len(out.LayoutShifts) != 4 {
		t.Fatalf("layout_shifts len = %d, want 4", len(out.LayoutShifts))
	}
	if !out.LayoutShifts[1].HadRecentInput {
		t.Error("layout_shifts[1].HadRecentInput = false, want true")
	}
	if math.Abs(out.CLS-0.20) > 1e-9 {
		t.Errorf("CLS = %v, want 0.20 (max session window, excluding recent-input and separate later window)", out.CLS)
	}

	after := callTool[getPerformanceOutput](t, "get_performance", map[string]any{
		"tab":  tabID,
		"mode": "peek",
	})
	if len(after.LayoutShifts) != 0 {
		t.Errorf("after drain, layout_shifts len = %d, want 0", len(after.LayoutShifts))
	}
}

func TestGetPerformanceLCPPeekDrainLimitAndLiveCapture(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	// Verify live Chrome PerformanceTimeline LCP capture. Wait for paint
	// BEFORE any user interaction because per W3C LCP spec a click/keydown
	// immediately stops LCP candidate reporting.
	var live getPerformanceOutput
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		live = callTool[getPerformanceOutput](t, "get_performance", map[string]any{
			"tab":  tabID,
			"mode": "peek",
		})
		if len(live.LCP) > 0 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if len(live.LCP) == 0 {
		t.Fatal("expected at least one live LCP entry from Chrome PerformanceTimeline, got 0")
	}
	if live.LCP[0].Size <= 0 || live.LCP[0].Timestamp.IsZero() {
		t.Errorf("live LCP entry missing size/timestamp: %+v", live.LCP[0])
	}

	b, err := harness.mgr.Resolve("")
	if err != nil {
		t.Fatalf("resolve browser: %v", err)
	}
	tb, err := b.Tabs.Get(tabID)
	if err != nil {
		t.Fatalf("get tab: %v", err)
	}
	tb.Performance.Clear()

	// Empty buffers must serialize as [], never null.
	emptyRaw := callToolRaw(t, "get_performance", map[string]any{
		"tab":  tabID,
		"mode": "peek",
	})
	if emptyRaw.IsError {
		t.Fatalf("get_performance empty error: %s", contentText(emptyRaw))
	}
	rawText := contentText(emptyRaw)
	if strings.Contains(rawText, `"layout_shifts":null`) || strings.Contains(rawText, `"lcp":null`) {
		t.Fatalf("get_performance serialized empty arrays as null: %s", rawText)
	}

	// Seed 3 deterministic LCP entries and 3 layout shifts to verify peek,
	// drain, and that limit caps only LCP (leaving layout_shifts unbounded for CLS).
	for i := 1; i <= 3; i++ {
		tb.Performance.HandleTimelineEvent(&performancetimeline.EventTimelineEventAdded{
			Event: &performancetimeline.TimelineEvent{
				Type: "largest-contentful-paint",
				Time: cdp.TimeSinceEpoch(1700000100 + float64(i)),
				LcpDetails: &performancetimeline.LargestContentfulPaint{
					RenderTime: cdp.TimeSinceEpoch(1700000100 + float64(i)),
					LoadTime:   cdp.TimeSinceEpoch(1700000090 + float64(i)),
					Size:       float64(i * 1000),
					URL:        "https://example.com/hero.png",
				},
			},
		})
		tb.Performance.HandleTimelineEvent(&performancetimeline.EventTimelineEventAdded{
			Event: &performancetimeline.TimelineEvent{
				Type: "layout-shift",
				Time: cdp.TimeSinceEpoch(1700000100 + float64(i)*0.1),
				LayoutShiftDetails: &performancetimeline.LayoutShift{
					Value: 0.05,
				},
			},
		})
	}

	// Peek with limit=2 caps LCP to 2, keeps all 3 layout shifts unbounded, and leaves all buffered.
	peekOut := callTool[getPerformanceOutput](t, "get_performance", map[string]any{
		"tab":   tabID,
		"mode":  "peek",
		"limit": 2,
	})
	if len(peekOut.LCP) != 2 {
		t.Fatalf("peek(limit=2) lcp len = %d, want 2", len(peekOut.LCP))
	}
	if len(peekOut.LayoutShifts) != 3 {
		t.Fatalf("peek(limit=2) layout_shifts len = %d, want 3 (unbounded to preserve CLS)", len(peekOut.LayoutShifts))
	}
	if math.Abs(peekOut.CLS-0.15) > 1e-9 {
		t.Errorf("peek(limit=2) CLS = %v, want 0.15 across all 3 shifts", peekOut.CLS)
	}
	if peekOut.LCP[0].Size != 1000 || peekOut.LCP[0].URL != "https://example.com/hero.png" || peekOut.LCP[0].RenderTime == nil || peekOut.LCP[0].LoadTime == nil {
		t.Errorf("unexpected LCP entry[0]: %+v", peekOut.LCP[0])
	}

	// Drain with limit=2 consumes only the first 2 and leaves the 3rd.
	drain1 := callTool[getPerformanceOutput](t, "get_performance", map[string]any{
		"tab":   tabID,
		"mode":  "drain",
		"limit": 2,
	})
	if len(drain1.LCP) != 2 || drain1.LCP[0].Size != 1000 || drain1.LCP[1].Size != 2000 {
		t.Fatalf("drain(limit=2) = %+v, want sizes [1000, 2000]", drain1.LCP)
	}

	drain2 := callTool[getPerformanceOutput](t, "get_performance", map[string]any{
		"tab":  tabID,
		"mode": "drain",
	})
	if len(drain2.LCP) != 1 || drain2.LCP[0].Size != 3000 {
		t.Fatalf("second drain = %+v, want remaining size 3000", drain2.LCP)
	}

	drain3 := callTool[getPerformanceOutput](t, "get_performance", map[string]any{
		"tab":  tabID,
		"mode": "drain",
	})
	if len(drain3.LCP) != 0 {
		t.Errorf("third drain len = %d, want 0", len(drain3.LCP))
	}

	// Validation: negative limit and invalid mode fail cleanly.
	if errText := callToolExpectErr(t, "get_performance", map[string]any{"tab": tabID, "mode": "peek", "limit": -1}); !strings.Contains(errText, "limit") {
		t.Errorf("negative limit err = %q", errText)
	}
	if errText := callToolExpectErr(t, "get_performance", map[string]any{"tab": tabID, "mode": "invalid"}); !strings.Contains(errText, "mode") {
		t.Errorf("invalid mode err = %q", errText)
	}
}

func TestGetPerformanceMetricsNamesAndReasonableValues(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	out := callTool[getPerformanceOutput](t, "get_performance", map[string]any{
		"tab": tabID,
	})
	if len(out.Metrics) == 0 {
		t.Fatal("expected performance metrics, got none")
	}

	// Chrome should always report these well-known metrics with positive values.
	expected := map[string]bool{
		"Timestamp":       false,
		"JSHeapUsedSize":  false,
		"JSHeapTotalSize": false,
		"Nodes":           false,
	}
	for _, m := range out.Metrics {
		if _, ok := expected[m.Name]; ok {
			expected[m.Name] = true
			if m.Value <= 0 {
				t.Errorf("%s = %f, want > 0", m.Name, m.Value)
			}
		}
	}
	for name, found := range expected {
		if !found {
			t.Errorf("expected metric %q not found in performance metrics", name)
		}
	}
}

func TestGetPerformanceLayoutShiftsReal(t *testing.T) {
	tabID := navigateToFixture(t, "layout-shifts.html")
	defer closeTab(t, tabID)

	// Wait for the injected element to appear (causes layout shift).
	callTool[struct{}](t, "wait_for", map[string]any{
		"tab":      tabID,
		"selector": "#injected",
		"timeout":  5000,
	})

	out := callTool[getPerformanceOutput](t, "get_performance", map[string]any{
		"tab":  tabID,
		"mode": "drain",
	})
	if out.CLS < 0 {
		t.Errorf("cls = %f, want >= 0", out.CLS)
	}
	if out.LayoutShifts == nil {
		t.Error("layout_shifts should be an empty array, not nil")
	}
}

// TestGetPerformanceFailsFastOnOpenDialogWithoutDraining verifies that
// get_performance fails fast when a JS dialog is open rather than stalling on
// Performance.getMetrics, and preserves buffered shifts/LCP on mode="drain".
func TestGetPerformanceFailsFastOnOpenDialogWithoutDraining(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	tb, err := harness.mgr.ResolveTab("", tabID)
	if err != nil {
		t.Fatalf("ResolveTab: %v", err)
	}
	tb.Performance.Clear()
	tb.Performance.HandleTimelineEvent(&performancetimeline.EventTimelineEventAdded{
		Event: &performancetimeline.TimelineEvent{
			Type:               "layout-shift",
			Time:               cdp.TimeSinceEpoch(1700000010.0),
			LayoutShiftDetails: &performancetimeline.LayoutShift{Value: 0.25},
		},
	})

	// Trigger an async alert so the renderer blocks on a modal dialog.
	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `setTimeout(() => alert('perf-block'), 10); true`,
	})
	defer handleDialog(t, tabID, map[string]any{"action": "accept"})

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if callTool[dialogOutput](t, "dialog", map[string]any{"tab": tabID, "action": "get"}).Open {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if !callTool[dialogOutput](t, "dialog", map[string]any{"tab": tabID, "action": "get"}).Open {
		t.Fatal("timed out waiting for alert dialog")
	}

	start := time.Now()
	errText := callToolExpectErr(t, "get_performance", map[string]any{
		"tab":  tabID,
		"mode": "drain",
	})
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("get_performance took %v while dialog open; expected fail-fast", elapsed)
	}
	if !strings.Contains(errText, "dialog") {
		t.Errorf("err = %q, want mention of dialog", errText)
	}

	// Buffered layout shifts must NOT have been drained by the failed call.
	if got := len(tb.Performance.PeekLayoutShifts(0)); got != 1 {
		t.Errorf("buffered layout shifts after failed drain = %d, want 1", got)
	}
}

// TestGetPerformanceConcurrentCallsSerialized verifies concurrent calls on the
// same tab never race Performance.enable / Performance.disable.
func TestGetPerformanceConcurrentCallsSerialized(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	const workers = 4
	var wg sync.WaitGroup
	errs := make(chan string, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			res, err := harness.session.CallTool(harness.ctx, &mcp.CallToolParams{
				Name:      "get_performance",
				Arguments: map[string]any{"tab": tabID, "mode": "peek"},
			})
			if err != nil {
				errs <- fmt.Sprintf("worker %d CallTool error: %v", idx, err)
				return
			}
			if res.IsError {
				errs <- fmt.Sprintf("worker %d tool error: %s", idx, contentText(res))
				return
			}
			raw, err := json.Marshal(res.StructuredContent)
			if err != nil {
				errs <- fmt.Sprintf("worker %d marshal error: %v", idx, err)
				return
			}
			var out getPerformanceOutput
			if err := json.Unmarshal(raw, &out); err != nil {
				errs <- fmt.Sprintf("worker %d unmarshal error: %v", idx, err)
				return
			}
			if len(out.Metrics) == 0 {
				errs <- fmt.Sprintf("worker %d received 0 metrics under concurrent load", idx)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Error(msg)
	}
}
