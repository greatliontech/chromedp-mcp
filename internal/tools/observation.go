package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/browser"
	"github.com/greatliontech/chromedp-mcp/internal/collector"
)

// ObserveActivityInput is the input for observe_activity.
type ObserveActivityInput struct {
	TabInput
	WaitMs  int `json:"wait_ms" jsonschema:"How many milliseconds to observe forward from the call. Required. The handler waits this long before snapshotting counts; a typical value is 200 to give sync handlers, microtasks, and most network requests time to start."`
	SinceMs int `json:"since_ms,omitempty" jsonschema:"Lookback in milliseconds — entries with timestamps from this many ms before the call are also counted. Defaults to 50 to catch fast events that fired before observe_activity ran (cached XHRs, sync handlers). Set explicitly to 0 to disable lookback."`
}

// ObserveActivityOutput reports activity counts in the observation
// window [now-since_ms, now+wait_ms].
//
// has_any_effect is the disjunction of NetworkRequests, URLChanged,
// ConsoleMessages, and JSErrors — the signals that are unambiguous.
// DOMMutations is reported as a count but excluded from
// has_any_effect because real pages produce ambient childList
// mutations (parser-driven for fresh navigations, framework re-renders
// for SPAs, async resource insertion). The LLM should compare the
// DOMMutations count to its expectation rather than treating any
// non-zero value as "the action did something".
type ObserveActivityOutput struct {
	WindowMs        int  `json:"window_ms"`
	NetworkRequests int  `json:"network_requests"`
	DOMMutations    int  `json:"dom_mutations"`
	URLChanged      bool `json:"url_changed"`
	ConsoleMessages int  `json:"console_messages"`
	JSErrors        int  `json:"js_errors"`
	HasAnyEffect    bool `json:"has_any_effect"`
}

// defaultObserveSinceMs is the lookback applied when the caller omits
// since_ms (passes 0 implicitly via JSON omission). The handler
// rejects negative values, so the contract is "0 in JSON means 'use
// the default'" — matching the behavior of optional integer
// parameters elsewhere in the codebase. To genuinely opt out of
// lookback, the LLM has no escape hatch; lookback is small (50ms) and
// the "races caught" benefit outweighs the false-positive risk of
// catching a request fired just before the call.
const defaultObserveSinceMs = 50

func registerObservationTools(s *mcp.Server, mgr *browser.Manager) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "observe_activity",
		Description: "Measure observable browser activity in a time window: how many network requests, DOM mutations, console messages, and JS errors occurred, and whether the top-frame URL changed. Useful after dispatching an action (click, type, submit_form, etc.) to detect a silent no-op — has_any_effect=false with all counts at zero is a strong signal that the action did nothing observable. Always waits the full wait_ms; does not return early on first activity (use wait_for for that).",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: true,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ObserveActivityInput) (*mcp.CallToolResult, ObserveActivityOutput, error) {
		if input.WaitMs <= 0 {
			return nil, ObserveActivityOutput{}, fmt.Errorf("wait_ms must be > 0; got %d", input.WaitMs)
		}
		if input.SinceMs < 0 {
			return nil, ObserveActivityOutput{}, fmt.Errorf("since_ms must be >= 0; got %d (use 0 for the default, omit for no lookback)", input.SinceMs)
		}
		sinceMs := input.SinceMs
		if sinceMs == 0 {
			sinceMs = defaultObserveSinceMs
		}

		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, ObserveActivityOutput{}, err
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()

		// Mark the lookback boundary BEFORE waiting so all collectors
		// agree on the window's start. Existing entries with
		// timestamps >= sinceTime are counted; new entries that arrive
		// during the wait are too.
		sinceTime := time.Now().Add(-time.Duration(sinceMs) * time.Millisecond)

		// Wait for the forward observation window. Honor the caller's
		// context cancellation so the request returns promptly if the
		// MCP client disconnects mid-wait.
		select {
		case <-time.After(time.Duration(input.WaitMs) * time.Millisecond):
		case <-tctx.Done():
			return nil, ObserveActivityOutput{}, tctx.Err()
		}

		// Snapshot Go-side collectors. Network counts pending+completed
		// so a slow request that started in the window but hasn't
		// landed in the ring buffer yet (LoadingFinished still in
		// flight) is still counted — otherwise the "click fired a
		// slow XHR" case silently reports 0.
		netCount := t.Network.CountStartedSince(sinceTime)
		consCount := countConsoleSince(t.Console.Peek("", 0), sinceTime)
		errCount := countJSErrorsSince(t.JSErrors.Peek(0), sinceTime)
		urlCount := t.URLChanges.CountSince(sinceTime)

		// Read the JS-side mutation buffer. The observer maintains
		// performance.now() timestamps; we ask JS to count entries
		// whose age is at most (sinceMs + wait_ms) milliseconds.
		var domCount int
		elapsedMs := sinceMs + input.WaitMs
		if err := chromedp.Run(tctx, chromedp.Evaluate(fmt.Sprintf(
			`(function(){
				var arr = window.__mcpMutationTimestamps || [];
				var threshold = performance.now() - %d;
				var n = 0;
				for (var i = arr.length - 1; i >= 0; i--) {
					if (arr[i] >= threshold) n++;
					else break;
				}
				return n;
			})()`, elapsedMs), &domCount)); err != nil {
			// JS read failure is rare (target gone, page navigating
			// mid-evaluate). Don't fail the whole observation — report
			// the other counts and a 0 for DOM mutations. The caller
			// sees a value, not an error, which keeps the
			// "did anything happen" signal usable in transient states.
			domCount = 0
		}

		out := ObserveActivityOutput{
			WindowMs:        sinceMs + input.WaitMs,
			NetworkRequests: netCount,
			DOMMutations:    domCount,
			URLChanged:      urlCount > 0,
			ConsoleMessages: consCount,
			JSErrors:        errCount,
		}
		// has_any_effect intentionally excludes DOMMutations — see the
		// type comment for why. The LLM gets the dom_mutations count
		// and can interpret it relative to expectation.
		out.HasAnyEffect = out.NetworkRequests > 0 ||
			out.URLChanged ||
			out.ConsoleMessages > 0 ||
			out.JSErrors > 0
		return nil, out, nil
	})
}

// countConsoleSince counts console entries received at or after t.
// Uses ReceivedAt (wall-clock time the Go-side handler ran) rather
// than Timestamp (CDP's MonotonicTime conversion drifts vs time.Now).
func countConsoleSince(entries []collector.ConsoleEntry, t time.Time) int {
	n := 0
	for _, e := range entries {
		if !e.ReceivedAt.Before(t) {
			n++
		}
	}
	return n
}

// countJSErrorsSince counts JS error entries received at or after t.
func countJSErrorsSince(entries []collector.JSErrorEntry, t time.Time) int {
	n := 0
	for _, e := range entries {
		if !e.ReceivedAt.Before(t) {
			n++
		}
	}
	return n
}
