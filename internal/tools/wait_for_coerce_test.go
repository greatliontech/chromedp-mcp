package tools

import (
	"strings"
	"testing"
)

// TestWaitForExpressionNonPrimitive verifies a predicate that returns a
// non-primitive value (e.g. a chained property access ultimately
// returning an object/array) does not fail with CDP error "Object
// reference chain is too long". The handler coerces the expression to
// Boolean(...) before sending to chromedp.Poll, which makes any
// truthy/falsy semantics work without the LLM having to wrap manually.
//
// This was issue 010 from the exploration session; the original failure
// was a chained optional access on a DOM node whose intermediate value
// was itself a complex object.
func TestWaitForExpressionNonPrimitive(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	// document.body is an HTMLElement (non-primitive). Without Boolean
	// coercion this fails with "Object reference chain is too long".
	// With coercion: Boolean(document.body) === true, predicate matches,
	// wait_for returns immediately.
	callTool[struct{}](t, "wait_for", map[string]any{
		"tab":        tabID,
		"expression": "document.body",
		"timeout":    2000,
	})
}

// TestWaitForExpressionFalsyPrimitive verifies the coercion preserves
// the timeout-on-falsy semantics. A predicate that evaluates to 0 (a
// primitive but falsy value) must not match, so wait_for times out.
func TestWaitForExpressionFalsyPrimitive(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "wait_for", map[string]any{
		"tab":        tabID,
		"expression": "0",
		"timeout":    300,
	})
	if errText == "" {
		t.Error("wait_for on falsy predicate '0' should have timed out")
	}
}

// TestWaitForExpressionReturnsObject reproduces the original issue 010
// scenario: a predicate whose terminal value is a non-primitive (a
// DOMRect object from getBoundingClientRect). Without coercion, CDP
// errors with "Object reference chain is too long" trying to serialize
// the result. With Promise.resolve(...).then(Boolean), the object is
// truthy → predicate matches → wait_for returns immediately.
func TestWaitForExpressionReturnsObject(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "wait_for", map[string]any{
		"tab":        tabID,
		"expression": "document.querySelector('h1')?.getBoundingClientRect()",
		"timeout":    2000,
	})
}

// TestWaitForExpressionAsyncPredicate verifies that an expression that
// evaluates to a Promise is awaited, not silently coerced to true. Pre-
// fix this would have silently succeeded immediately (a bare Boolean
// wrap on a Promise is always true); after the fix the Promise is
// awaited and its resolved boolean drives the poll.
func TestWaitForExpressionAsyncPredicate(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	// fetch('/api/data') resolves to a Response. Then(r => r.ok) is
	// true on a 200. The wait_for must actually wait for the fetch to
	// resolve — if the wrap doesn't await, the Promise is truthy
	// immediately and we'd succeed without observing the network call.
	// Verify by calling get_network_requests after wait_for and
	// asserting the request landed.
	// Stash the fetch on window so we can prove the .then ran (and
	// therefore the Promise was awaited) without racing the network
	// collector's event dispatch.
	callTool[struct{}](t, "wait_for", map[string]any{
		"tab":        tabID,
		"expression": "fetch('/api/data').then(r => { window.__fetchOk = r.ok; return r.ok; })",
		"timeout":    5000,
	})

	res := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "window.__fetchOk === true",
	})
	if string(res.Result) != "true" {
		t.Error("wait_for returned but window.__fetchOk wasn't set; the Promise was not awaited (a bare Boolean(promise) wrap would succeed instantly without running the .then)")
	}
}

// TestWaitForExpressionAsyncRejection verifies that a Promise rejection
// in the predicate surfaces as a poll error rather than being silently
// coerced to true.
func TestWaitForExpressionAsyncRejection(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "wait_for", map[string]any{
		"tab":        tabID,
		"expression": "Promise.reject(new Error('async-predicate-failure'))",
		"timeout":    1000,
	})
	if errText == "" {
		t.Error("rejected Promise predicate should surface an error")
	}
}

// TestWaitForExpressionTrailingLineComment verifies a trailing line
// comment in the user's expression doesn't swallow the wrapping syntax.
// The wrap inserts newlines around EXPR specifically to defend against
// this.
func TestWaitForExpressionTrailingLineComment(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "wait_for", map[string]any{
		"tab":        tabID,
		"expression": "document.body // trailing comment",
		"timeout":    2000,
	})
}

// TestWaitForExpressionSyntaxError verifies that a syntax error in the
// expression is still surfaced clearly even after the coercion wrap.
// The wrap is `Promise.resolve(EXPR).then(Boolean)` — a parse error in
// EXPR breaks the whole expression, so V8 reports the underlying parse
// failure cleanly.
func TestWaitForExpressionSyntaxError(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "wait_for", map[string]any{
		"tab":        tabID,
		"expression": "if (true) { return",
		"timeout":    1000,
	})
	if errText == "" {
		t.Fatal("syntax error in expression should produce an error")
	}
	if !strings.Contains(strings.ToLower(errText), "syntax") &&
		!strings.Contains(errText, "Unexpected") {
		t.Errorf("syntax-error text %q should reference 'syntax' or 'Unexpected'", errText)
	}
}
