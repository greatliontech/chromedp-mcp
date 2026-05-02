package tools

import (
	"strings"
	"testing"
)

// TestObserveActivityNoOpClickReportsZero verifies the "did nothing"
// signal: clicking a button with no handler must produce zero counts
// across the unambiguous signals (network/url/console/errors) and
// has_any_effect=false. DOMMutations is allowed to be non-zero on
// real-page navigations — the type comment explains why it's excluded
// from has_any_effect.
func TestObserveActivityNoOpClickReportsZero(t *testing.T) {
	tabID := navigateToFixture(t, "observation.html")
	defer closeTab(t, tabID)

	// Drain prior activity so navigation noise doesn't bleed into the
	// observation window.
	_ = callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":  tabID,
		"mode": "drain",
	})
	_ = callTool[GetConsoleLogsOutput](t, "get_console_logs", map[string]any{
		"tab":  tabID,
		"mode": "drain",
	})
	_ = callTool[GetJSErrorsOutput](t, "get_js_errors", map[string]any{
		"tab":  tabID,
		"mode": "drain",
	})

	callTool[ClickOutput](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#noop-btn",
	})

	out := callTool[ObserveActivityOutput](t, "observe_activity", map[string]any{
		"tab":     tabID,
		"wait_ms": 200,
	})
	if out.HasAnyEffect {
		t.Errorf("HasAnyEffect = true on no-op click; got %+v", out)
	}
	if out.NetworkRequests != 0 {
		t.Errorf("NetworkRequests = %d, want 0", out.NetworkRequests)
	}
	if out.URLChanged {
		t.Error("URLChanged = true on no-op click")
	}
	if out.ConsoleMessages != 0 {
		t.Errorf("ConsoleMessages = %d, want 0", out.ConsoleMessages)
	}
	if out.JSErrors != 0 {
		t.Errorf("JSErrors = %d, want 0", out.JSErrors)
	}
}

// TestObserveActivityFetchClickReportsNetwork verifies a fetch-firing
// click is detected: network_requests > 0 and has_any_effect=true.
func TestObserveActivityFetchClickReportsNetwork(t *testing.T) {
	tabID := navigateToFixture(t, "observation.html")
	defer closeTab(t, tabID)

	// Drain prior activity.
	_ = callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":  tabID,
		"mode": "drain",
	})

	callTool[ClickOutput](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#fetch-btn",
	})

	out := callTool[ObserveActivityOutput](t, "observe_activity", map[string]any{
		"tab":     tabID,
		"wait_ms": 500,
	})
	if !out.HasAnyEffect {
		t.Errorf("HasAnyEffect = false; got %+v", out)
	}
	if out.NetworkRequests < 1 {
		t.Errorf("NetworkRequests = %d, want >= 1", out.NetworkRequests)
	}
}

// TestObserveActivityDOMMutationClick verifies DOM mutations are
// counted via the MutationObserver. A click that appends an <li> to a
// list reliably produces one childList mutation. Note: DOMMutations
// alone does NOT flip has_any_effect (DOM mutations are noisy on real
// pages — the type comment explains).
func TestObserveActivityDOMMutationClick(t *testing.T) {
	tabID := navigateToFixture(t, "observation.html")
	defer closeTab(t, tabID)

	// Burn a wait cycle so any parsing-time mutations exit the
	// lookback window before the click. Without this the count below
	// is conflated with page-load mutations.
	callTool[ObserveActivityOutput](t, "observe_activity", map[string]any{
		"tab":     tabID,
		"wait_ms": 200,
	})

	callTool[ClickOutput](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#dom-btn",
	})
	out := callTool[ObserveActivityOutput](t, "observe_activity", map[string]any{
		"tab":     tabID,
		"wait_ms": 200,
	})
	if out.DOMMutations < 1 {
		t.Errorf("DOMMutations = %d, want >= 1 after appending an <li>", out.DOMMutations)
	}
}

// TestObserveActivitySameDocumentURLChange verifies the URL change
// collector catches History API pushState (no full page reload).
func TestObserveActivitySameDocumentURLChange(t *testing.T) {
	tabID := navigateToFixture(t, "observation.html")
	defer closeTab(t, tabID)

	callTool[ClickOutput](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#pushstate-btn",
	})

	out := callTool[ObserveActivityOutput](t, "observe_activity", map[string]any{
		"tab":     tabID,
		"wait_ms": 300,
	})
	if !out.URLChanged {
		t.Errorf("URLChanged = false on pushState click; got %+v", out)
	}
	if !out.HasAnyEffect {
		t.Error("HasAnyEffect = false on URL change")
	}
}

// TestObserveActivityConsoleAndJSError verifies the console_messages
// and js_errors counts come through.
func TestObserveActivityConsoleAndJSError(t *testing.T) {
	tabID := navigateToFixture(t, "observation.html")
	defer closeTab(t, tabID)

	// Drain prior console/error activity.
	_ = callTool[GetConsoleLogsOutput](t, "get_console_logs", map[string]any{
		"tab":  tabID,
		"mode": "drain",
	})
	_ = callTool[GetJSErrorsOutput](t, "get_js_errors", map[string]any{
		"tab":  tabID,
		"mode": "drain",
	})

	callTool[ClickOutput](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#console-btn",
	})

	out := callTool[ObserveActivityOutput](t, "observe_activity", map[string]any{
		"tab":     tabID,
		"wait_ms": 200,
	})
	if out.ConsoleMessages < 1 {
		t.Errorf("ConsoleMessages = %d, want >= 1", out.ConsoleMessages)
	}

	callTool[ClickOutput](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#throw-btn",
	})

	out2 := callTool[ObserveActivityOutput](t, "observe_activity", map[string]any{
		"tab":     tabID,
		"wait_ms": 300,
	})
	if out2.JSErrors < 1 {
		t.Errorf("JSErrors = %d, want >= 1 after throwing setTimeout error", out2.JSErrors)
	}
}

// TestObserveActivitySlowRequestStillInFlightCounted verifies the
// "click fired a slow XHR" case: a request that starts in the
// observation window but completes AFTER wait_ms expires must still
// be counted. Without the pending+completed walk, the network ring
// only contains entries that LoadingFinished has fired for, so a
// 200ms-server-delay fetch with wait_ms=100 would be missed entirely.
func TestObserveActivitySlowRequestStillInFlightCounted(t *testing.T) {
	tabID := navigateToFixture(t, "observation.html")
	defer closeTab(t, tabID)

	// Drain prior network so the count reflects only what the new
	// request adds.
	_ = callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":  tabID,
		"mode": "drain",
	})

	// /slow takes 200ms server-side. Fire and forget (await_promise=false
	// so evaluate returns immediately without waiting for the fetch),
	// then observe with wait_ms=80 — way less than the response time.
	// The request is in the collector's pending map (requestWillBeSent
	// fired, LoadingFinished hasn't), so it must still be counted.
	awaitFalse := false
	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":           tabID,
		"expression":    `fetch('/slow')`,
		"await_promise": awaitFalse,
	})
	out := callTool[ObserveActivityOutput](t, "observe_activity", map[string]any{
		"tab":     tabID,
		"wait_ms": 80,
	})
	if out.NetworkRequests < 1 {
		t.Errorf("NetworkRequests = %d for slow in-flight request; want >= 1 (the request started in-window even though it completes after wait_ms)",
			out.NetworkRequests)
	}
	if !out.HasAnyEffect {
		t.Errorf("HasAnyEffect = false despite in-flight request; got %+v", out)
	}
}

// TestObserveActivityLookbackCatchesPreCallActivity verifies since_ms
// catches activity that fired before observe_activity was called. The
// click below dispatches synchronously and the network event arrives
// before observe_activity has a chance to start its forward window.
func TestObserveActivityLookbackCatchesPreCallActivity(t *testing.T) {
	tabID := navigateToFixture(t, "observation.html")
	defer closeTab(t, tabID)

	// Drain prior network activity.
	_ = callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":  tabID,
		"mode": "drain",
	})

	// Trigger a fetch and let the network collector see LoadingFinished.
	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `fetch('/api/data')`,
	})
	waitForNetwork(t, tabID, "/api/data")

	// Now call observe_activity with a generous lookback. The fetch
	// happened before this call but should still be counted.
	out := callTool[ObserveActivityOutput](t, "observe_activity", map[string]any{
		"tab":      tabID,
		"wait_ms":  100,
		"since_ms": 1000,
	})
	if out.NetworkRequests < 1 {
		t.Errorf("NetworkRequests = %d with 1s lookback after a fetch; want >= 1", out.NetworkRequests)
	}
}

// TestObserveActivityRejectsBadInputs verifies wait_ms <= 0 and
// since_ms < 0 are rejected with clear errors.
func TestObserveActivityRejectsBadInputs(t *testing.T) {
	tabID := navigateToFixture(t, "observation.html")
	defer closeTab(t, tabID)

	for _, args := range []map[string]any{
		{"tab": tabID, "wait_ms": 0},
		{"tab": tabID, "wait_ms": -100},
		{"tab": tabID, "wait_ms": 100, "since_ms": -1},
	} {
		errText := callToolExpectErr(t, "observe_activity", args)
		if errText == "" {
			t.Errorf("args %v should have errored", args)
		}
		if !strings.Contains(errText, "ms") {
			t.Errorf("args %v: error %q should mention the offending field", args, errText)
		}
	}
}

// TestObserveActivityWindowMsReportsTotal verifies the returned
// window_ms is sinceMs + wait_ms (with the default sinceMs=50 when
// since_ms is omitted).
func TestObserveActivityWindowMsReportsTotal(t *testing.T) {
	tabID := navigateToFixture(t, "observation.html")
	defer closeTab(t, tabID)

	out := callTool[ObserveActivityOutput](t, "observe_activity", map[string]any{
		"tab":     tabID,
		"wait_ms": 100,
	})
	if out.WindowMs != 150 {
		t.Errorf("WindowMs = %d, want 150 (default 50 lookback + 100 wait)", out.WindowMs)
	}

	out = callTool[ObserveActivityOutput](t, "observe_activity", map[string]any{
		"tab":      tabID,
		"wait_ms":  100,
		"since_ms": 250,
	})
	if out.WindowMs != 350 {
		t.Errorf("WindowMs = %d, want 350 (250 + 100)", out.WindowMs)
	}
}
