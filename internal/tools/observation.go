package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"

	"github.com/greatliontech/chromedp-mcp/internal/collector"
	"github.com/greatliontech/chromedp-mcp/internal/tab"
)

// MaxObserveWindowMs caps observe_window_ms. The observation window is a
// blocking wait inside the tool handler, so an unbounded value lets one
// call pin an MCP request open indefinitely. 30s is far above any useful
// window (the longest value the tool descriptions suggest is 1s) while
// still bounding a caller that passes the wrong unit.
const MaxObserveWindowMs = 30_000

// ObservedActivity reports what the page did in response to an action,
// measured over the action's observation window.
//
// has_any_effect is the disjunction of NetworkRequests, URLChanged,
// ConsoleMessages, and JSErrors — the unambiguous signals. DOMMutations
// is reported as a count but excluded from has_any_effect because real
// pages produce ambient childList mutations on every navigation that
// would fire false positives. The caller should compare DOMMutations to
// its expectation rather than treating any non-zero value as "the action
// did something".
//
// This shape is shared across all action tools that opt into inline
// observation. Observation is deliberately not exposed as a standalone
// tool the caller invokes after an action: an LLM-driven workflow has
// 1–10s of latency between consecutive tool calls (model thinking +
// token streaming + MCP transit), so a post-hoc observe call always
// arrives after the interesting window has closed. No amount of
// "lookback" tuning fixes that, because the caller cannot know how long
// its own turnaround took. Observing inline sidesteps it entirely: the
// window is opened immediately before the action dispatches, so there is
// no gap for effects to escape through.
type ObservedActivity struct {
	WindowMs        int  `json:"window_ms"`
	NetworkRequests int  `json:"network_requests"`
	DOMMutations    int  `json:"dom_mutations"`
	URLChanged      bool `json:"url_changed"`
	ConsoleMessages int  `json:"console_messages"`
	JSErrors        int  `json:"js_errors"`
	// HasAnyEffect is meaningless when Truncated is set — see below.
	HasAnyEffect bool `json:"has_any_effect"`
	// Truncated is true when the window was cut short: the tab closed or
	// the client disconnected before the window elapsed. (A navigation
	// does not truncate — it leaves the tab's context live.) The counts
	// are then partial, and in particular a false has_any_effect is NOT
	// evidence that the action did nothing.
	Truncated bool `json:"truncated,omitempty"`
	// DOMMutationsUnavailable is true when the mutation count cannot be
	// attributed to the action: the TOP-frame document was replaced during
	// the window (the buffer the window anchored against is gone, and the
	// new document's parser-driven mutations are not the action's doing),
	// or the page could not be read. DOMMutations is then 0 and carries no
	// information; url_changed is the signal to use instead.
	//
	// A replaced SUBframe does not set this: it contributes 0 to the count
	// while the rest of the frames still report honestly. See
	// countMutations.
	DOMMutationsUnavailable bool `json:"dom_mutations_unavailable,omitempty"`
	// DOMMutationsPartial is true when DOMMutations is a LOWER BOUND: some
	// frame's mutations could not be seen. Causes, all of them real:
	//
	//   - A cross-origin (out-of-process) iframe. Its JS runs on a CDP
	//     session we are not attached to, so its mutation buffer is
	//     unreachable. This is the common case on any page with a
	//     third-party ad, embed, or payment frame.
	//   - A frame created during the window: it had no anchor, so its
	//     mutations (which are mostly parser-driven anyway) are not counted.
	//   - A frame destroyed during the window: its buffer died with it.
	//
	// The count is still honest for the frames we could see. This flag
	// exists so a zero is never mistaken for "the DOM did not change" when
	// the truth is "we could not look everywhere".
	DOMMutationsPartial bool `json:"dom_mutations_partial,omitempty"`
}

// observation is an open observation window.
//
// Open it with openObservation immediately BEFORE dispatching an action,
// and — this is the part that is easy to get wrong — *after* any wait for
// the target element to appear. A wait folded inside the window (the
// selector waits are bounded by selectorContext, up to 5s by default)
// would attribute every request a slow page makes while the element is
// still materializing to the action that had not yet been dispatched.
//
// Close it with close() once the dispatch returns. A nil *observation is
// valid and closes to nil, so a caller whose observe_window_ms was 0 can
// call both halves unconditionally.
type observation struct {
	tab      *tab.Tab
	windowMs int
	// since is the wall-clock window start, used to filter the Go-side
	// collectors (network, console, JS errors, URL changes), which stamp
	// entries with time.Now() as they are processed.
	since time.Time
	// anchors maps a frame's main-world execution context (by CDP's
	// system-unique ID) to performance.now() read in that frame at window
	// open.
	//
	// Per-frame, because the DOM mutation buffer is per-document: the
	// observer script runs in every frame and writes into that frame's own
	// window. Reading only the top frame would silently ignore every
	// iframe mutation.
	//
	// Anchored in the page's own clock, because the buffer holds
	// performance.now() values. Deriving the threshold from a Go-side
	// elapsed time instead shifts the window later by one CDP round trip,
	// which silently drops the synchronous mutations of dispatches that
	// are themselves a single round trip (select_option, submit_form).
	//
	// Keyed by the system-unique context ID, because that ID is never
	// reused: a navigation destroys the context, so the stale anchor
	// cannot be applied to the document that replaced it — the read fails
	// instead of counting the new document's parser-driven mutations.
	anchors map[string]float64
	// mainCtxID is the top frame's context at window open. Losing it means
	// the page itself was replaced, which makes the whole count
	// unattributable. Empty if no main-frame context was known.
	mainCtxID string
	// unobservedAtOpen is the number of frames the page had at window open
	// that we hold no execution context for — in practice cross-origin
	// (out-of-process) iframes, whose JS lives on a CDP session we are not
	// attached to. Their mutations are simply invisible to us, so any count
	// we report is a lower bound.
	unobservedAtOpen int
	// framesAtOpen is the set of frames that existed when the window
	// opened, used to notice frames born mid-window (which are likewise
	// uncounted).
	framesAtOpen map[string]struct{}
	// anchorFailed records that a frame we knew about could not be anchored
	// at all — its context died between the snapshot and the read. Its
	// mutations are uncountable, so the result is a lower bound. Without
	// this, such a frame is dropped silently whenever it has a fresh
	// context again by the time the window closes.
	anchorFailed bool
}

// openObservation opens an observation window on t. Returns nil when
// windowMs <= 0 — the "caller didn't ask for observation" case.
func openObservation(ctx context.Context, t *tab.Tab, windowMs int) *observation {
	if windowMs <= 0 {
		return nil
	}
	contexts, unobserved := t.ExecContexts.Snapshot()
	o := &observation{
		tab:              t,
		windowMs:         windowMs,
		anchors:          make(map[string]float64, len(contexts)),
		unobservedAtOpen: unobserved,
		framesAtOpen:     make(map[string]struct{}, len(contexts)),
	}

	// Anchor the main frame LAST. Each anchor costs a CDP round trip, so on
	// a page with many frames the first anchor is taken measurably earlier
	// than the dispatch, and anything that frame does in the gap is
	// wrongly attributed to the action. Ordering main last puts the
	// smallest possible gap where accuracy matters most — the frame the
	// action actually lands in.
	ordered := make([]collector.ExecContext, 0, len(contexts))
	var main *collector.ExecContext
	for i := range contexts {
		if contexts[i].IsMain {
			main = &contexts[i]
			continue
		}
		ordered = append(ordered, contexts[i])
	}
	if main != nil {
		ordered = append(ordered, *main)
	}

	// Anchor every frame we can reach. A frame whose context dies between
	// the snapshot and the read is simply not anchored: it contributes
	// nothing rather than contributing a wrong number.
	for _, ec := range ordered {
		o.framesAtOpen[ec.FrameID] = struct{}{}
		var now float64
		if err := evalInContext(ctx, ec.UniqueID, `performance.now()`, &now); err != nil {
			o.anchorFailed = true
			continue
		}
		o.anchors[ec.UniqueID] = now
		if ec.IsMain {
			o.mainCtxID = ec.UniqueID
		}
	}

	// Take the wall-clock start AFTER the anchor round trips, so the
	// Go-side collectors don't attribute the anchor reads' own traffic to
	// the action. This leaves the JS anchors marginally earlier than
	// `since`, erring toward over-counting DOM mutations rather than
	// missing the synchronous one the action is about to cause.
	o.since = time.Now()
	return o
}

// close waits out the window and reports what happened in it. Safe on a
// nil receiver.
//
// close never returns an error: the action it observed has already
// succeeded, and failing the tool call because a measurement came back
// partial would turn a working action into an error the caller cannot
// act on. A window cut short is reported via Truncated instead.
func (o *observation) close(ctx context.Context) *ObservedActivity {
	if o == nil {
		return nil
	}

	out := &ObservedActivity{}
	// NewTimer + Stop rather than time.After: on the cancellation branch
	// a time.After timer would stay armed for the rest of the window (up
	// to MaxObserveWindowMs).
	timer := time.NewTimer(time.Duration(o.windowMs) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		// The tab closed or the client disconnected. (Note: a navigation
		// does NOT land here — it does not cancel the tab's context.) The
		// Go-side collectors still hold whatever arrived before the
		// cancellation, so report those counts — but mark them partial:
		// a zero here means "we stopped looking", not "nothing happened".
		out.Truncated = true
	}

	out.WindowMs = int(time.Since(o.since) / time.Millisecond)
	out.NetworkRequests = o.tab.Network.CountStartedSince(o.since)
	out.ConsoleMessages = countConsoleSince(o.tab.Console.Peek("", 0), o.since)
	out.JSErrors = countJSErrorsSince(o.tab.JSErrors.Peek(0), o.since)
	out.URLChanged = o.tab.URLChanges.CountSince(o.since) > 0
	out.DOMMutations, out.DOMMutationsUnavailable, out.DOMMutationsPartial = o.countMutations(ctx)

	// has_any_effect intentionally excludes DOMMutations — see the
	// ObservedActivity doc for why.
	out.HasAnyEffect = out.NetworkRequests > 0 ||
		out.URLChanged ||
		out.ConsoleMessages > 0 ||
		out.JSErrors > 0
	return out
}

// countMutations sums each anchored frame's mutations since its anchor.
//
// Attribution rules:
//   - The top frame is decisive. If its context is gone — the page
//     navigated, so the buffer we anchored no longer exists and the new
//     document's parser-driven mutations are not the action's doing — the
//     whole count is unattributable (unavailable).
//   - Anything else we could not see makes the count a LOWER BOUND rather
//     than a lie (partial): a cross-origin iframe we cannot attach to, a
//     frame born during the window (never anchored), or a subframe whose
//     document was replaced mid-window (its buffer died with it). In each
//     case the frames we *could* see are still counted honestly, and
//     whatever the top frame did — removing an <iframe> element, say — is
//     still counted there.
//
// Reporting a bare 0 in any of those cases would be the exact failure this
// whole mechanism exists to prevent: a confident "nothing happened" for an
// action that in fact did something we simply were not looking at.
func (o *observation) countMutations(ctx context.Context) (count int, unavailable, partial bool) {
	if len(o.anchors) == 0 || o.mainCtxID == "" {
		return 0, true, false
	}

	// Frames we could never see (cross-process), frames we failed to anchor,
	// and frames that appeared after the window opened all make the count a
	// lower bound.
	partial = o.unobservedAtOpen > 0 || o.anchorFailed
	nowContexts, unobservedNow := o.tab.ExecContexts.Snapshot()
	if unobservedNow > o.unobservedAtOpen {
		partial = true
	}
	for _, ec := range nowContexts {
		if _, existed := o.framesAtOpen[ec.FrameID]; !existed {
			partial = true
		}
	}

	total := 0
	for uniqueID, anchor := range o.anchors {
		js := fmt.Sprintf(`(function(){
			var arr = window.__mcpMutationTimestamps || [];
			var threshold = %v;
			var count = 0;
			for (var i = arr.length - 1; i >= 0; i--) {
				if (arr[i] >= threshold) count++;
				else break;
			}
			return count;
		})()`, anchor)

		var n int
		if err := evalInContext(ctx, uniqueID, js, &n); err != nil {
			// Context gone (frame navigated, removed, or the whole target
			// died), or ctx already cancelled.
			if uniqueID == o.mainCtxID {
				return 0, true, false
			}
			// A subframe we anchored but can no longer read: its mutations
			// are lost, so say so rather than quietly under-reporting.
			partial = true
			continue
		}
		total += n
	}
	return total, false, partial
}

// evalInContext evaluates expr in one specific execution context, by CDP's
// system-unique context ID, and unmarshals the result into dst.
//
// chromedp.Evaluate always targets the page's *current* top-frame context,
// which is wrong twice over here: it cannot see an iframe's buffer, and
// after a navigation it would happily evaluate against the new document
// using the old document's anchor. Pinning the unique ID makes both
// mistakes unrepresentable — a replaced context is an error, not a wrong
// answer.
func evalInContext(ctx context.Context, uniqueContextID, expr string, dst any) error {
	return chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		res, exc, err := runtime.Evaluate(expr).
			WithUniqueContextID(uniqueContextID).
			WithReturnByValue(true).
			Do(ctx)
		if err != nil {
			return err
		}
		if exc != nil {
			return fmt.Errorf("evaluate in context %s: %s", uniqueContextID, exc.Text)
		}
		if res == nil || len(res.Value) == 0 {
			return fmt.Errorf("evaluate in context %s: no value", uniqueContextID)
		}
		return json.Unmarshal([]byte(res.Value), dst)
	}))
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

// validateObserveWindow checks an observe_window_ms parameter. Called
// first in every action handler, before any CDP work, so a bad value
// never dispatches a half-observed action.
func validateObserveWindow(ms int) error {
	if ms < 0 {
		return fmt.Errorf("observe_window_ms must be >= 0; got %d", ms)
	}
	if ms > MaxObserveWindowMs {
		return fmt.Errorf("observe_window_ms must be <= %d; got %d", MaxObserveWindowMs, ms)
	}
	return nil
}
