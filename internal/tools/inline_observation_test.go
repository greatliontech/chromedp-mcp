package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestClickObserveWindowCatchesNetwork verifies inline observation on
// click reliably catches a network request fired by the click handler.
// This is the case a post-hoc observation call cannot catch: the
// request has come and gone during the caller's turnaround, so only a
// window opened inside the handler — before dispatch — sees it.
func TestClickObserveWindowCatchesNetwork(t *testing.T) {
	tabID := navigateToFixture(t, "observation.html")
	defer closeTab(t, tabID)

	out := callTool[ClickOutput](t, "click", map[string]any{
		"tab":               tabID,
		"selector":          "#fetch-btn",
		"observe_window_ms": 500,
	})
	if out.Observed == nil {
		t.Fatal("Observed = nil; want populated when observe_window_ms > 0")
	}
	if !out.Observed.HasAnyEffect {
		t.Errorf("HasAnyEffect = false; got %+v", out.Observed)
	}
	if out.Observed.NetworkRequests < 1 {
		t.Errorf("NetworkRequests = %d, want >= 1", out.Observed.NetworkRequests)
	}
}

// TestClickObserveWindowZeroOmitted verifies that observe_window_ms=0
// (the default) leaves Observed nil — no schema bloat for callers that
// don't want observation.
func TestClickObserveWindowZeroOmitted(t *testing.T) {
	tabID := navigateToFixture(t, "observation.html")
	defer closeTab(t, tabID)

	out := callTool[ClickOutput](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#noop-btn",
	})
	if out.Observed != nil {
		t.Errorf("Observed = %+v; want nil when observe_window_ms not passed", out.Observed)
	}
}

// TestClickObserveWindowNoOpReportsZero verifies that a click on a
// no-op button with observation enabled returns has_any_effect=false.
// This is the headline use case: the LLM clicks something and learns
// from the response that nothing happened.
func TestClickObserveWindowNoOpReportsZero(t *testing.T) {
	tabID := navigateToFixture(t, "observation.html")
	defer closeTab(t, tabID)

	// Drain ambient state so the no-op observation isn't confused by
	// page-load network activity still arriving.
	_ = callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":  tabID,
		"mode": "drain",
	})

	out := callTool[ClickOutput](t, "click", map[string]any{
		"tab":               tabID,
		"selector":          "#noop-btn",
		"observe_window_ms": 200,
	})
	if out.Observed == nil {
		t.Fatal("Observed = nil")
	}
	if out.Observed.HasAnyEffect {
		t.Errorf("HasAnyEffect = true on no-op click; got %+v", out.Observed)
	}
	if out.Observed.NetworkRequests != 0 {
		t.Errorf("NetworkRequests = %d, want 0", out.Observed.NetworkRequests)
	}
	if out.Observed.URLChanged {
		t.Errorf("URLChanged = true on no-op click; got %+v", out.Observed)
	}
	if out.Observed.ConsoleMessages != 0 {
		t.Errorf("ConsoleMessages = %d, want 0", out.Observed.ConsoleMessages)
	}
	if out.Observed.JSErrors != 0 {
		t.Errorf("JSErrors = %d, want 0", out.Observed.JSErrors)
	}
	if out.Observed.Truncated {
		t.Errorf("Truncated = true on a window that ran to completion; got %+v", out.Observed)
	}
}

// TestClickObserveWindowURLChange verifies that a click triggering a
// same-document URL change (pushState) sets URLChanged=true.
func TestClickObserveWindowURLChange(t *testing.T) {
	tabID := navigateToFixture(t, "observation.html")
	defer closeTab(t, tabID)

	out := callTool[ClickOutput](t, "click", map[string]any{
		"tab":               tabID,
		"selector":          "#pushstate-btn",
		"observe_window_ms": 300,
	})
	if out.Observed == nil {
		t.Fatal("Observed = nil")
	}
	if !out.Observed.URLChanged {
		t.Errorf("URLChanged = false on pushState click; got %+v", out.Observed)
	}
	// url_changed is one of the four disjuncts of has_any_effect, and it
	// is the only one this click produces — so this is what pins that
	// disjunct. Without it, dropping url_changed from the disjunction
	// would fail no test.
	if !out.Observed.HasAnyEffect {
		t.Errorf("HasAnyEffect = false despite a URL change; got %+v", out.Observed)
	}
}

// TestClickObserveWindowNegativeRejected verifies negative
// observe_window_ms is rejected.
func TestClickObserveWindowNegativeRejected(t *testing.T) {
	tabID := navigateToFixture(t, "observation.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "click", map[string]any{
		"tab":               tabID,
		"selector":          "#noop-btn",
		"observe_window_ms": -1,
	})
	if !strings.Contains(errText, "observe_window_ms") {
		t.Errorf("error %q should reference observe_window_ms", errText)
	}
}

// TestTypeObserveWindow verifies inline observation on type catches
// network activity from typeahead/search-as-you-type handlers.
func TestTypeObserveWindow(t *testing.T) {
	tabID := navigateToFixture(t, "observation.html")
	defer closeTab(t, tabID)

	// Add an oninput handler that fires a fetch — simulating typeahead.
	callTool[AddScriptOutput](t, "add_script", map[string]any{
		"tab": tabID,
		"source": `(function() {
			document.body.insertAdjacentHTML('beforeend', '<input id="typeahead">');
			document.getElementById('typeahead').addEventListener('input', function() {
				fetch('/api/data?q=' + this.value);
			});
		})()`,
		"evaluate_now": true,
	})

	out := callTool[ActionOutput](t, "type", map[string]any{
		"tab":               tabID,
		"selector":          "#typeahead",
		"text":              "x",
		"mode":              "replace",
		"observe_window_ms": 400,
	})
	if out.Observed == nil {
		t.Fatal("Observed = nil")
	}
	if !out.Observed.HasAnyEffect {
		t.Errorf("HasAnyEffect = false; got %+v", out.Observed)
	}
}

// TestClickObserveWindowDOMMutation verifies the DOM mutation count
// comes through. The window opens immediately before dispatch, so
// parser-driven page-load mutations fall outside it and the count
// reflects only what the click's handler appended.
func TestClickObserveWindowDOMMutation(t *testing.T) {
	tabID := navigateToFixture(t, "observation.html")
	defer closeTab(t, tabID)

	out := callTool[ClickOutput](t, "click", map[string]any{
		"tab":               tabID,
		"selector":          "#dom-btn",
		"observe_window_ms": 200,
	})
	if out.Observed == nil {
		t.Fatal("Observed = nil")
	}
	if out.Observed.DOMMutations < 1 {
		t.Errorf("DOMMutations = %d, want >= 1 after appending an <li>", out.Observed.DOMMutations)
	}
	// DOMMutations is deliberately excluded from has_any_effect, so a
	// mutation-only click must NOT flip it. See the ObservedActivity doc.
	if out.Observed.HasAnyEffect {
		t.Errorf("HasAnyEffect = true on a mutation-only click; got %+v", out.Observed)
	}
}

// TestClickObserveWindowConsoleAndJSError verifies the console_messages
// and js_errors counts come through, and that each on its own is enough
// to set has_any_effect.
func TestClickObserveWindowConsoleAndJSError(t *testing.T) {
	tabID := navigateToFixture(t, "observation.html")
	defer closeTab(t, tabID)

	out := callTool[ClickOutput](t, "click", map[string]any{
		"tab":               tabID,
		"selector":          "#console-btn",
		"observe_window_ms": 200,
	})
	if out.Observed == nil {
		t.Fatal("Observed = nil")
	}
	if out.Observed.ConsoleMessages < 1 {
		t.Errorf("ConsoleMessages = %d, want >= 1", out.Observed.ConsoleMessages)
	}
	if !out.Observed.HasAnyEffect {
		t.Errorf("HasAnyEffect = false despite a console message; got %+v", out.Observed)
	}

	out2 := callTool[ClickOutput](t, "click", map[string]any{
		"tab":               tabID,
		"selector":          "#throw-btn",
		"observe_window_ms": 300,
	})
	if out2.Observed == nil {
		t.Fatal("Observed = nil")
	}
	if out2.Observed.JSErrors < 1 {
		t.Errorf("JSErrors = %d, want >= 1 after throwing setTimeout error", out2.Observed.JSErrors)
	}
	if !out2.Observed.HasAnyEffect {
		t.Errorf("HasAnyEffect = false despite a JS error; got %+v", out2.Observed)
	}
}

// TestClickObserveWindowSlowRequestStillInFlightCounted verifies the
// "click fired a slow XHR" case: a request that starts inside the
// observation window but completes AFTER it closes must still be
// counted. Without CountStartedSince's pending+completed walk, the
// network ring only holds entries LoadingFinished has fired for, so a
// 200ms-server-delay fetch under an 80ms window would be missed
// entirely and the click would look like a no-op.
func TestClickObserveWindowSlowRequestStillInFlightCounted(t *testing.T) {
	tabID := navigateToFixture(t, "observation.html")
	defer closeTab(t, tabID)

	// The handler fires and forgets a request that takes 2s server-side,
	// against an 80ms window. The margin has to be this wide: if the
	// request could complete before the window closes, CountStartedSince
	// would find it in the *completed* buffer and the test would pass
	// even with the pending-map walk deleted, silently losing the pin.
	callTool[AddScriptOutput](t, "add_script", map[string]any{
		"tab": tabID,
		"source": `(function() {
			document.body.insertAdjacentHTML('beforeend', '<button id="slow-btn">slow</button>');
			document.getElementById('slow-btn').addEventListener('click', function() {
				fetch('/slow?ms=2000');
			});
		})()`,
		"evaluate_now": true,
	})

	// Drain prior network so the count reflects only what the click adds.
	_ = callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":  tabID,
		"mode": "drain",
	})

	out := callTool[ClickOutput](t, "click", map[string]any{
		"tab":               tabID,
		"selector":          "#slow-btn",
		"observe_window_ms": 80,
	})
	if out.Observed == nil {
		t.Fatal("Observed = nil")
	}
	if out.Observed.NetworkRequests < 1 {
		t.Errorf("NetworkRequests = %d for slow in-flight request; want >= 1 (the request started in-window even though it completes after the window closes)",
			out.Observed.NetworkRequests)
	}
	if !out.Observed.HasAnyEffect {
		t.Errorf("HasAnyEffect = false despite in-flight request; got %+v", out.Observed)
	}
}

// TestClickObserveWindowWindowMsReportsElapsed verifies window_ms
// reports the real elapsed observation span — dispatch duration plus
// the requested wait — not just the requested wait. The LLM needs the
// true span to judge whether a zero count means "nothing happened" or
// "I didn't wait long enough".
func TestClickObserveWindowWindowMsReportsElapsed(t *testing.T) {
	tabID := navigateToFixture(t, "observation.html")
	defer closeTab(t, tabID)

	const requested = 200
	out := callTool[ClickOutput](t, "click", map[string]any{
		"tab":               tabID,
		"selector":          "#noop-btn",
		"observe_window_ms": requested,
	})
	if out.Observed == nil {
		t.Fatal("Observed = nil")
	}
	// Strictly greater, not >=: the span must include the dispatch on top
	// of the requested wait. A >= assertion would also pass if window_ms
	// just echoed the requested value back, which is the bug this pins.
	// Sub-millisecond dispatch (which would truncate the int back down to
	// exactly `requested`) is not reachable over a real browser
	// connection.
	if out.Observed.WindowMs <= requested {
		t.Errorf("WindowMs = %d, want > %d — window_ms must report true elapsed span (dispatch + wait + buffer read), not the requested wait",
			out.Observed.WindowMs, requested)
	}
}

// TestSubmitFormObserveWindow verifies submit_form's inline observation —
// this is the highest-leverage case (a form submit short-circuited by
// validation produces no network activity, and observed.has_any_effect
// surfaces it immediately).
func TestSubmitFormObserveWindow(t *testing.T) {
	tabID := navigateToFixture(t, "observation.html")
	defer closeTab(t, tabID)

	// Create a tiny form whose submit is captured as a fetch (instead
	// of a real navigation) so we can detect the activity within the
	// observation window.
	callTool[AddScriptOutput](t, "add_script", map[string]any{
		"tab": tabID,
		"source": `(function() {
			document.body.insertAdjacentHTML('beforeend',
				'<form id="f"><input name="x"><button type="submit">go</button></form>');
			document.getElementById('f').addEventListener('submit', function(e) {
				e.preventDefault();
				fetch('/api/data?submitted=1');
			});
		})()`,
		"evaluate_now": true,
	})

	out := callTool[ActionOutput](t, "submit_form", map[string]any{
		"tab":               tabID,
		"selector":          "#f",
		"observe_window_ms": 400,
	})
	if out.Observed == nil {
		t.Fatal("Observed = nil")
	}
	if !out.Observed.HasAnyEffect || out.Observed.NetworkRequests < 1 {
		t.Errorf("submit handler should have fired network; got %+v", out.Observed)
	}
}

// TestFocusObserveWindowExcludesElementWait pins the boundary that
// separates waiting for a target from acting on it.
//
// Every selector-based action tool waits (up to selectorContext's 5s)
// for its element to exist. That wait must sit OUTSIDE the observation
// window: a page still bootstrapping fires requests while the element
// materializes, and attributing them to an action that had not yet been
// dispatched turns has_any_effect into a false positive — on exactly the
// slow, real, async pages the feature exists for.
//
// #late-btn schedules a fetch at +100ms and only creates #late-input at
// +500ms. focus() therefore spends ~500ms waiting, during which the
// fetch lands. The window opens after the wait, so that fetch is not the
// focus's doing and must not be counted.
func TestFocusObserveWindowExcludesElementWait(t *testing.T) {
	tabID := navigateToFixture(t, "observation.html")
	defer closeTab(t, tabID)

	callTool[ClickOutput](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#late-btn",
	})

	out := callTool[ActionOutput](t, "focus", map[string]any{
		"tab":               tabID,
		"selector":          "#late-input",
		"observe_window_ms": 100,
	})
	if out.Observed == nil {
		t.Fatal("Observed = nil")
	}
	if out.Observed.NetworkRequests != 0 {
		t.Errorf("NetworkRequests = %d, want 0 — the fetch fired while focus was WAITING for #late-input to exist, before the focus was dispatched, so it is not the focus's effect; got %+v",
			out.Observed.NetworkRequests, out.Observed)
	}
	if out.Observed.HasAnyEffect {
		t.Errorf("HasAnyEffect = true — focusing an empty input has no observable effect; the window must not have swallowed the pre-dispatch wait. got %+v", out.Observed)
	}
}

// TestClickObserveWindowExcludesVisibilityWait is the click-shaped twin
// of the test above, and it pins a boundary that is easy to get subtly
// wrong: waiting for the element to EXIST is not enough.
//
// chromedp.Click waits for the target to be *visible* before dispatching
// (it appends NodeVisible). #late-visible-btn is present at parse time
// but display:none until +900ms, so a pre-wait that only waits for
// existence returns instantly and leaves the visibility wait inside the
// observation window — where it swallows the unrelated +400ms fetch and
// reports it as the click's effect.
//
// The pre-wait must therefore match the wait the dispatch performs
// internally: WaitVisible for click, not WaitReady.
func TestClickObserveWindowExcludesVisibilityWait(t *testing.T) {
	tabID := navigateToFixture(t, "observation.html")
	defer closeTab(t, tabID)

	callTool[ClickOutput](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#late-btn",
	})

	out := callTool[ClickOutput](t, "click", map[string]any{
		"tab":               tabID,
		"selector":          "#late-visible-btn",
		"observe_window_ms": 100,
	})
	if out.Observed == nil {
		t.Fatal("Observed = nil")
	}
	if out.Observed.NetworkRequests != 0 {
		t.Errorf("NetworkRequests = %d, want 0 — the fetch fired while click was WAITING for #late-visible-btn to become visible, before the click was dispatched, so it is not the click's effect; got %+v",
			out.Observed.NetworkRequests, out.Observed)
	}
	if out.Observed.HasAnyEffect {
		t.Errorf("HasAnyEffect = true — #late-visible-btn has no click handler; the window must not have swallowed the pre-dispatch visibility wait. got %+v", out.Observed)
	}
}

// TestObserveWindowTruncatedOnTabClose pins the truncated flag.
//
// The flag carries a safety property: when the window is cut short, the
// counts are partial, so has_any_effect=false must NOT be read as "the
// action did nothing". An unpinned safety flag is worse than an unpinned
// count — nothing would catch it silently never being set.
//
// Closing the tab mid-window cancels the tab context, which is the
// reachable truncation path.
func TestObserveWindowTruncatedOnTabClose(t *testing.T) {
	tabID := navigateToFixture(t, "observation.html")

	// Close the tab while the click below is still inside its window.
	// This calls the session directly rather than the closeTab helper:
	// the helper reports failures via t.Fatalf, which is not safe from a
	// non-test goroutine. Any error here surfaces as the assertions below
	// failing instead.
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(300 * time.Millisecond)
		ctx, cancel := context.WithTimeout(harness.ctx, 30*time.Second)
		defer cancel()
		_, _ = harness.session.CallTool(ctx, &mcp.CallToolParams{
			Name:      "tab_close",
			Arguments: map[string]any{"tab": tabID},
		})
	}()

	out := callTool[ClickOutput](t, "click", map[string]any{
		"tab":               tabID,
		"selector":          "#noop-btn",
		"observe_window_ms": 5000,
	})
	<-done

	if out.Observed == nil {
		t.Fatal("Observed = nil")
	}
	if !out.Observed.Truncated {
		t.Errorf("Truncated = false after the tab closed mid-window; a partial measurement must say so, or has_any_effect=false reads as a real no-op. got %+v", out.Observed)
	}
	// The window was cut short long before its 5s elapsed.
	if out.Observed.WindowMs >= 5000 {
		t.Errorf("WindowMs = %d; want well under the 5000ms requested — the window was cut short. got %+v",
			out.Observed.WindowMs, out.Observed)
	}
}

// TestClickObserveWindowNavigationMakesDOMUnavailable pins the
// document-replacement case. The mutation buffer is per-document and
// performance.now() resets to 0 on navigation, so an anchor taken before
// the click is meaningless in the document that replaces it. Counting
// against it anyway yields a negative threshold, which sweeps up every
// parser-driven mutation of the freshly loaded page and reports the
// whole new document as the click's doing.
//
// The honest answer is "cannot attribute": dom_mutations=0 with
// dom_mutations_unavailable=true. url_changed carries the real signal.
func TestClickObserveWindowNavigationMakesDOMUnavailable(t *testing.T) {
	tabID := navigateToFixture(t, "observation.html")
	defer closeTab(t, tabID)

	out := callTool[ClickOutput](t, "click", map[string]any{
		"tab":               tabID,
		"selector":          "#nav-btn",
		"observe_window_ms": 800,
	})
	if out.Observed == nil {
		t.Fatal("Observed = nil")
	}
	if !out.Observed.DOMMutationsUnavailable {
		t.Errorf("DOMMutationsUnavailable = false after a cross-document navigation; the count cannot be attributed to the click. got %+v", out.Observed)
	}
	if out.Observed.DOMMutations != 0 {
		t.Errorf("DOMMutations = %d, want 0 when unavailable — a non-zero count here is the new document's parse mutations being blamed on the click; got %+v",
			out.Observed.DOMMutations, out.Observed)
	}
	if !out.Observed.URLChanged {
		t.Errorf("URLChanged = false after navigation; got %+v", out.Observed)
	}
	if !out.Observed.HasAnyEffect {
		t.Errorf("HasAnyEffect = false after navigation; got %+v", out.Observed)
	}
}

// TestSelectOptionObserveWindowCatchesSyncMutation pins the DOM window's
// lower edge on a single-round-trip dispatch.
//
// select_option dispatches via one chromedp.Evaluate. If the mutation
// threshold were derived from a Go-side elapsed time rather than a clock
// anchor read from the page before dispatch, the window's start would
// land one CDP round trip late — the same round trip the dispatch itself
// costs — and the handler's synchronous mutation would fall outside it
// about half the time. Anchoring in the page's own clock is what makes
// this deterministic.
func TestSelectOptionObserveWindowCatchesSyncMutation(t *testing.T) {
	tabID := navigateToFixture(t, "observation.html")
	defer closeTab(t, tabID)

	// A <select> whose change handler SYNCHRONOUSLY mutates the DOM and
	// fires a request — the cascading-dropdown shape.
	callTool[AddScriptOutput](t, "add_script", map[string]any{
		"tab": tabID,
		"source": `(function() {
			document.body.insertAdjacentHTML('beforeend',
				'<select id="sel"><option value="a">a</option><option value="b">b</option></select>');
			document.getElementById('sel').addEventListener('change', function() {
				var d = document.createElement('div');
				d.textContent = 'cascaded ' + this.value;
				document.body.appendChild(d);
				fetch('/api/data?cascade=' + this.value);
			});
		})()`,
		"evaluate_now": true,
	})

	out := callTool[ActionOutput](t, "select_option", map[string]any{
		"tab":               tabID,
		"selector":          "#sel",
		"value":             "b",
		"observe_window_ms": 400,
	})
	if out.Observed == nil {
		t.Fatal("Observed = nil")
	}
	if out.Observed.DOMMutationsUnavailable {
		t.Fatalf("DOMMutationsUnavailable = true with no navigation; got %+v", out.Observed)
	}
	if out.Observed.DOMMutations < 1 {
		t.Errorf("DOMMutations = %d, want >= 1 — the change handler's synchronous appendChild must fall inside the window; got %+v",
			out.Observed.DOMMutations, out.Observed)
	}
	if out.Observed.NetworkRequests < 1 {
		t.Errorf("NetworkRequests = %d, want >= 1; got %+v", out.Observed.NetworkRequests, out.Observed)
	}
}

// TestPressKeyObserveWindow covers inline observation on press_key —
// the Enter-submits-the-form shape.
func TestPressKeyObserveWindow(t *testing.T) {
	tabID := navigateToFixture(t, "observation.html")
	defer closeTab(t, tabID)

	callTool[AddScriptOutput](t, "add_script", map[string]any{
		"tab": tabID,
		"source": `(function() {
			document.addEventListener('keydown', function(e) {
				if (e.key === 'Enter') { fetch('/api/data?key=enter'); }
			});
		})()`,
		"evaluate_now": true,
	})

	out := callTool[ActionOutput](t, "press_key", map[string]any{
		"tab":               tabID,
		"key":               "Enter",
		"observe_window_ms": 400,
	})
	if out.Observed == nil {
		t.Fatal("Observed = nil")
	}
	if !out.Observed.HasAnyEffect || out.Observed.NetworkRequests < 1 {
		t.Errorf("Enter keydown handler should have fired a request; got %+v", out.Observed)
	}
}

// TestScrollObserveWindow covers inline observation on scroll — the
// infinite-scroll / lazy-load shape — and exercises the page-scroll
// branch (no selector), which opens its window on a different path than
// the scroll-into-view branch.
func TestScrollObserveWindow(t *testing.T) {
	tabID := navigateToFixture(t, "observation.html")
	defer closeTab(t, tabID)

	callTool[AddScriptOutput](t, "add_script", map[string]any{
		"tab": tabID,
		"source": `(function() {
			document.body.style.height = '5000px';
			window.addEventListener('scroll', function() { fetch('/api/data?page=2'); }, {once: true});
		})()`,
		"evaluate_now": true,
	})

	out := callTool[ScrollOutput](t, "scroll", map[string]any{
		"tab":               tabID,
		"y":                 400,
		"observe_window_ms": 400,
	})
	if out.Observed == nil {
		t.Fatal("Observed = nil")
	}
	if !out.Observed.HasAnyEffect || out.Observed.NetworkRequests < 1 {
		t.Errorf("scroll handler should have fired a lazy-load request; got %+v", out.Observed)
	}
}

// TestObserveWindowRejectsOverMax verifies the upper bound. The window is
// a blocking wait inside the handler, so an unbounded value would let one
// call pin an MCP request open indefinitely.
func TestObserveWindowRejectsOverMax(t *testing.T) {
	tabID := navigateToFixture(t, "observation.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "click", map[string]any{
		"tab":               tabID,
		"selector":          "#noop-btn",
		"observe_window_ms": MaxObserveWindowMs + 1,
	})
	if !strings.Contains(errText, "observe_window_ms") {
		t.Errorf("error %q should reference observe_window_ms", errText)
	}
}

// TestClickObserveWindowCountsIframeMutations pins cross-frame DOM
// mutation counting.
//
// The observer script is injected into every document, so each frame keeps
// its own mutation buffer in its own `window`. A count read only from the
// top frame's context reports a confident 0 for an action whose entire
// effect landed inside an iframe — a silent no-op signal that is simply
// wrong. Observation therefore evaluates in every frame's own execution
// context and sums.
func TestClickObserveWindowCountsIframeMutations(t *testing.T) {
	tabID := navigateToFixture(t, "observation.html")
	defer closeTab(t, tabID)

	// The frame must be loaded before we mutate into it.
	callTool[struct{}](t, "wait_for", map[string]any{
		"tab":        tabID,
		"expression": `document.getElementById('frame').contentDocument.readyState === 'complete'`,
	})

	out := callTool[ClickOutput](t, "click", map[string]any{
		"tab":               tabID,
		"selector":          "#iframe-mutate-btn",
		"observe_window_ms": 300,
	})
	if out.Observed == nil {
		t.Fatal("Observed = nil")
	}
	if out.Observed.DOMMutationsUnavailable {
		t.Fatalf("DOMMutationsUnavailable = true with no navigation; got %+v", out.Observed)
	}
	if out.Observed.DOMMutations < 1 {
		t.Errorf("DOMMutations = %d, want >= 1 — the click appended a node INSIDE the iframe, whose mutation buffer lives in the frame's own window; a top-frame-only count reports 0 here and calls a real effect a no-op. got %+v",
			out.Observed.DOMMutations, out.Observed)
	}
}

// TestClickObserveWindowIframeDoesNotInflateTopFrame guards the other
// direction: summing across frames must not turn an unrelated frame's
// ambient churn into a count against the action. A no-op click on a page
// that HAS an iframe must still report zero mutations.
func TestClickObserveWindowIframeDoesNotInflateTopFrame(t *testing.T) {
	tabID := navigateToFixture(t, "observation.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "wait_for", map[string]any{
		"tab":        tabID,
		"expression": `document.getElementById('frame').contentDocument.readyState === 'complete'`,
	})

	out := callTool[ClickOutput](t, "click", map[string]any{
		"tab":               tabID,
		"selector":          "#noop-btn",
		"observe_window_ms": 200,
	})
	if out.Observed == nil {
		t.Fatal("Observed = nil")
	}
	if out.Observed.DOMMutations != 0 {
		t.Errorf("DOMMutations = %d, want 0 — the iframe's load-time mutations happened before the window opened and must not be counted; got %+v",
			out.Observed.DOMMutations, out.Observed)
	}
	if out.Observed.HasAnyEffect {
		t.Errorf("HasAnyEffect = true on a no-op click; got %+v", out.Observed)
	}
	// The flag's noise floor. Every frame on this page is same-origin and
	// fully observable, so the count is complete. A partial flag that fires
	// here is a flag callers learn to ignore — which would restore the very
	// confident-zero failure it exists to prevent.
	if out.Observed.DOMMutationsPartial {
		t.Errorf("DOMMutationsPartial = true on a page whose only iframe is same-origin and fully observable; got %+v", out.Observed)
	}
}

// TestClickObserveWindowCrossOriginFrameReportsPartial pins the honesty
// boundary of the DOM mutation count.
//
// A cross-origin iframe is out-of-process: its JavaScript runs on a CDP
// session we are not attached to, so its mutation buffer is unreachable.
// We cannot count it — but we CAN see the frame exists (the page domain
// reports the whole frame tree, OOPIFs included), so we must not report a
// confident 0 for an action whose entire effect landed inside it. The
// count is a lower bound and says so.
//
// Without this, the exact bug that motivated cross-frame counting survives
// for the most common real-world iframe class: ads, embeds, payment frames.
func TestClickObserveWindowCrossOriginFrameReportsPartial(t *testing.T) {
	// A second origin. The harness serves on 127.0.0.1; "localhost" is a
	// different site, so Chrome gives the frame its own process.
	mux := http.NewServeMux()
	mux.HandleFunc("/f.html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><p>f</p><script>
			window.addEventListener('message', function() {
				document.body.appendChild(document.createElement('p'));
			});
		</script></body></html>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	crossURL := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1) + "/f.html"

	tabID := navigateToFixture(t, "observation.html")
	defer closeTab(t, tabID)

	callTool[AddScriptOutput](t, "add_script", map[string]any{
		"tab": tabID,
		"source": `(function() {
			var f = document.createElement('iframe');
			f.id = 'oopif';
			f.src = '` + crossURL + `';
			document.body.appendChild(f);
			document.getElementById('noop-btn').addEventListener('click', function() {
				document.getElementById('oopif').contentWindow.postMessage('go', '*');
			});
		})()`,
		"evaluate_now": true,
	})
	callTool[struct{}](t, "wait_for", map[string]any{
		"tab":        tabID,
		"expression": `!!document.getElementById('oopif')`,
	})
	// Let the cross-origin frame attach and load.
	callTool[ClickOutput](t, "click", map[string]any{
		"tab": tabID, "selector": "#dom-btn", "observe_window_ms": 300,
	})

	out := callTool[ClickOutput](t, "click", map[string]any{
		"tab":               tabID,
		"selector":          "#noop-btn",
		"observe_window_ms": 500,
	})
	if out.Observed == nil {
		t.Fatal("Observed = nil")
	}
	if !out.Observed.DOMMutationsPartial {
		t.Errorf("DOMMutationsPartial = false with an out-of-process iframe on the page; its mutation buffer is unreachable, so dom_mutations=%d is a lower bound and must not read as a complete count. got %+v",
			out.Observed.DOMMutations, out.Observed)
	}
}

// TestClickObserveWindowSubframeNavigationReportsPartial pins the one
// attribution rule cross-frame counting had to invent.
//
// A subframe replaced mid-window (a link with target="frame", an ad frame
// reloading itself) takes its mutation buffer with it. That is NOT the
// top-frame navigation case — the page is still here, and everything else
// still counts honestly — so it must not set dom_mutations_unavailable.
// But the lost frame means the count is a lower bound, and saying nothing
// would be a silent under-report.
func TestClickObserveWindowSubframeNavigationReportsPartial(t *testing.T) {
	tabID := navigateToFixture(t, "observation.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "wait_for", map[string]any{
		"tab":        tabID,
		"expression": `document.getElementById('frame').contentDocument.readyState === 'complete'`,
	})

	// Navigate the SUBframe (not the page) inside the window.
	callTool[AddScriptOutput](t, "add_script", map[string]any{
		"tab": tabID,
		"source": `(function() {
			document.getElementById('noop-btn').addEventListener('click', function() {
				document.getElementById('frame').src = '/observation_frame.html?v=2';
			});
		})()`,
		"evaluate_now": true,
	})

	out := callTool[ClickOutput](t, "click", map[string]any{
		"tab":               tabID,
		"selector":          "#noop-btn",
		"observe_window_ms": 600,
	})
	if out.Observed == nil {
		t.Fatal("Observed = nil")
	}
	if out.Observed.DOMMutationsUnavailable {
		t.Errorf("DOMMutationsUnavailable = true — only a SUBframe was replaced; the page is intact and the remaining frames still count honestly. got %+v", out.Observed)
	}
	if !out.Observed.DOMMutationsPartial {
		t.Errorf("DOMMutationsPartial = false — the replaced subframe took its mutation buffer with it, so dom_mutations=%d is a lower bound. got %+v",
			out.Observed.DOMMutations, out.Observed)
	}
}

// TestObserveWindowPartialNotStuckAfterNavigation pins the frame set's
// lifecycle, and it is the noise-floor test for dom_mutations_partial.
//
// When the top frame navigates, the old document's child frames cease to
// exist — and Chrome sends NO frameDetached for them, because that event
// only fires for frames removed from the *current* tree. A frame set that
// does not prune on top-frame navigation therefore accumulates every
// iframe of every page the tab ever visited. None of them will ever have
// an execution context again, so all of them count as "unobserved", and
// dom_mutations_partial latches true forever — on pages with no subframes
// at all.
//
// The scenario is utterly ordinary: visit a page with an iframe, go
// somewhere else, act. If the flag fires there, it is worthless.
func TestObserveWindowPartialNotStuckAfterNavigation(t *testing.T) {
	// observation.html has a same-origin iframe.
	tabID := navigateToFixture(t, "observation.html")
	defer closeTab(t, tabID)
	callTool[struct{}](t, "wait_for", map[string]any{
		"tab":        tabID,
		"expression": `document.getElementById('frame').contentDocument.readyState === 'complete'`,
	})

	// Navigate away, to a page with no frames whatsoever.
	callTool[NavigateOutput](t, "navigate", map[string]any{
		"tab": tabID,
		"url": harness.httpSrv.URL + "/page2.html",
	})

	out := callTool[ClickOutput](t, "click", map[string]any{
		"tab":               tabID,
		"selector":          "body",
		"observe_window_ms": 200,
	})
	if out.Observed == nil {
		t.Fatal("Observed = nil")
	}
	if out.Observed.DOMMutationsPartial {
		t.Errorf("DOMMutationsPartial = true on a page with NO frames — the previous page's iframe is still in the frame set, so the flag has latched on forever. got %+v", out.Observed)
	}
	if out.Observed.DOMMutationsUnavailable {
		t.Errorf("DOMMutationsUnavailable = true after the navigation completed; the new document is intact and countable. got %+v", out.Observed)
	}
}

// TestObserveWindowCrossOriginFramePartialSurvivesBackForward is the
// end-to-end check on the back/forward path, against real Chrome.
//
// Going back and forward can restore pages from the back/forward cache,
// which re-commits a document WITHOUT re-attaching its frames. An
// out-of-process iframe announces itself only via frameAttached, so a
// restore path that loses it reports a confident, complete DOM count for a
// page with a live, unobservable frame.
//
// Scope, stated honestly: this exercises the back/forward path end to end,
// but it does NOT pin the bfcache branch of ExecContexts.HandleFrameNavigated
// — it still passes when that branch is removed. Chrome does emit
// frameNavigated(type=BackForwardCacheRestore) here (verified), but chromedp's
// default allocator flags include --disable-features=site-per-process, so under
// this harness the cross-origin frame does not detach and re-attach the way it
// does in a site-isolated browser, and the frame survives the restore by
// accident. A real Chrome has site isolation on by default, where it does not.
//
// The bfcache branch is pinned instead by the collector's unit tests —
// TestExecContextsBFCacheRestoreKeepsUnobservableFrames and
// TestExecContextsBFCacheForwardRestoreKeepsFrames — which drive the exact
// event stream a site-isolated Chrome emits (contexts cleared, contexts
// re-created, frameNavigated(BackForwardCacheRestore) last, and no
// frameAttached for any restored child) and fail when the branch is removed.
func TestObserveWindowCrossOriginFramePartialSurvivesBackForward(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/f.html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><p>f</p></body></html>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	crossURL := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1) + "/f.html"

	// A page whose HTML carries the cross-origin iframe from the start, so
	// the frame belongs to the document and rides the bfcache with it.
	mux.HandleFunc("/host.html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><button id="b">b</button>
			<iframe src="` + crossURL + `"></iframe></body></html>`))
	})

	tabID := navigateToFixture(t, "observation.html")
	defer closeTab(t, tabID)

	callTool[NavigateOutput](t, "navigate", map[string]any{"tab": tabID, "url": srv.URL + "/host.html"})
	before := callTool[ClickOutput](t, "click", map[string]any{
		"tab": tabID, "selector": "#b", "observe_window_ms": 300,
	})
	if before.Observed == nil || !before.Observed.DOMMutationsPartial {
		t.Fatalf("precondition: the cross-origin iframe should make the count partial on first visit; got %+v", before.Observed)
	}

	// Leave for a frameless page, then come BACK (a bfcache restore of the
	// iframe page). Settling with a sleep rather than wait_for: evaluating
	// against a document mid-restore races the context swap.
	callTool[NavigateOutput](t, "navigate", map[string]any{
		"tab": tabID, "url": harness.httpSrv.URL + "/page2.html",
	})
	callTool[struct{}](t, "go_back", map[string]any{"tab": tabID})
	time.Sleep(700 * time.Millisecond)

	back := callTool[ClickOutput](t, "click", map[string]any{
		"tab": tabID, "selector": "#b", "observe_window_ms": 300,
	})
	if back.Observed == nil {
		t.Fatal("Observed = nil after go_back")
	}
	if !back.Observed.DOMMutationsPartial {
		t.Errorf("DOMMutationsPartial = false after going BACK to the cross-origin-iframe page — a bfcache restore re-commits the document without re-attaching its frames, so losing the frame here reports a confident complete count for a page we cannot fully see. got %+v", back.Observed)
	}

	// Now go FORWARD to the frameless page. Leaving the iframe page by going
	// back put page2 in the cache too, so this is a restore as well — and
	// page2 genuinely has no frames, so the count must NOT claim to be
	// partial. A flag that fires here is a flag callers learn to ignore.
	callTool[struct{}](t, "go_forward", map[string]any{"tab": tabID})
	time.Sleep(700 * time.Millisecond)

	fwd := callTool[ClickOutput](t, "click", map[string]any{
		"tab": tabID, "selector": "body", "observe_window_ms": 200,
	})
	if fwd.Observed == nil {
		t.Fatal("Observed = nil after go_forward")
	}
	if fwd.Observed.DOMMutationsPartial {
		t.Errorf("DOMMutationsPartial = true after going FORWARD to a page with NO frames — its frame set must have been stashed when we left it via the back button, or the restore reports 'frame set unknown' and the flag fires on an ordinary frameless page. got %+v", fwd.Observed)
	}
}
