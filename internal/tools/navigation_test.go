package tools

import (
	"context"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// SPA (pushState) navigation via go_back / go_forward
// ---------------------------------------------------------------------------

// TestGoBackSPA verifies that go_back works for same-document (pushState)
// history entries. This exercises the historyNavSameDocument code path in
// navigateHistory, where EventNavigatedWithinDocument fires instead of
// EventFrameNavigated.
func TestGoBackSPA(t *testing.T) {
	tabID := navigateToFixture(t, "spa.html")
	defer closeTab(t, tabID)

	// Verify initial state.
	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.title",
	})
	if !strings.Contains(string(out.Result), "SPA Home") {
		t.Fatalf("initial title = %s, want 'SPA Home'", out.Result)
	}

	// Click the "About" button to trigger pushState.
	callTool[struct{}](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#go-about",
	})

	// Verify we're on the About page.
	out = callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.title",
	})
	if !strings.Contains(string(out.Result), "SPA About") {
		t.Fatalf("after click About, title = %s, want 'SPA About'", out.Result)
	}

	// go_back should trigger same-document navigation (popstate).
	callTool[struct{}](t, "go_back", map[string]any{"tab": tabID})

	// Verify we're back on Home.
	out = callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.title",
	})
	if !strings.Contains(string(out.Result), "SPA Home") {
		t.Fatalf("after go_back, title = %s, want 'SPA Home'", out.Result)
	}

	// The DOM should also reflect the change.
	text := callTool[GetTextOutput](t, "get_text", map[string]any{
		"tab":      tabID,
		"selector": "#page",
	})
	if !strings.Contains(text.Text, "Home") {
		t.Errorf("after go_back, #page text = %q, want 'Home'", text.Text)
	}
}

// TestGoForwardSPA verifies that go_forward works for same-document
// (pushState) history entries.
func TestGoForwardSPA(t *testing.T) {
	tabID := navigateToFixture(t, "spa.html")
	defer closeTab(t, tabID)

	// Push About state.
	callTool[struct{}](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#go-about",
	})
	// Go back to Home.
	callTool[struct{}](t, "go_back", map[string]any{"tab": tabID})

	// Go forward to About.
	callTool[struct{}](t, "go_forward", map[string]any{"tab": tabID})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.title",
	})
	if !strings.Contains(string(out.Result), "SPA About") {
		t.Fatalf("after go_forward, title = %s, want 'SPA About'", out.Result)
	}

	text := callTool[GetTextOutput](t, "get_text", map[string]any{
		"tab":      tabID,
		"selector": "#page",
	})
	if !strings.Contains(text.Text, "About") {
		t.Errorf("after go_forward, #page text = %q, want 'About'", text.Text)
	}
}

// TestGoBackSPAMultipleSteps verifies go_back across multiple pushState entries.
func TestGoBackSPAMultipleSteps(t *testing.T) {
	tabID := navigateToFixture(t, "spa.html")
	defer closeTab(t, tabID)

	// Push About, then Contact.
	callTool[struct{}](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#go-about",
	})
	callTool[struct{}](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#go-contact",
	})

	// Verify we're on Contact.
	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.title",
	})
	if !strings.Contains(string(out.Result), "SPA Contact") {
		t.Fatalf("title = %s, want 'SPA Contact'", out.Result)
	}

	// Go back to About.
	callTool[struct{}](t, "go_back", map[string]any{"tab": tabID})
	out = callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.title",
	})
	if !strings.Contains(string(out.Result), "SPA About") {
		t.Fatalf("after first go_back, title = %s, want 'SPA About'", out.Result)
	}

	// Go back to Home.
	callTool[struct{}](t, "go_back", map[string]any{"tab": tabID})
	out = callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.title",
	})
	if !strings.Contains(string(out.Result), "SPA Home") {
		t.Fatalf("after second go_back, title = %s, want 'SPA Home'", out.Result)
	}
}

// TestGoBackSPAThenInteract verifies that DOM operations (click, type, query)
// work correctly after a same-document go_back — confirming chromedp's internal
// state stays consistent for pushState navigations (no reload needed).
func TestGoBackSPAThenInteract(t *testing.T) {
	tabID := navigateToFixture(t, "spa.html")
	defer closeTab(t, tabID)

	// Push About.
	callTool[struct{}](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#go-about",
	})
	// Go back to Home.
	callTool[struct{}](t, "go_back", map[string]any{"tab": tabID})

	// After SPA go_back, we should be able to interact with the DOM normally.
	// Click the Contact button.
	callTool[struct{}](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#go-contact",
	})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.title",
	})
	if !strings.Contains(string(out.Result), "SPA Contact") {
		t.Fatalf("after go_back + click contact, title = %s, want 'SPA Contact'", out.Result)
	}

	// query should also work.
	qout := callTool[QueryOutput](t, "query", map[string]any{
		"tab":      tabID,
		"selector": "#page",
	})
	if qout.Total != 1 {
		t.Fatalf("query #page total = %d, want 1", qout.Total)
	}
	if !strings.Contains(qout.Elements[0].Text, "Contact") {
		t.Errorf("query #page text = %q, want to contain 'Contact'", qout.Elements[0].Text)
	}
}

func TestNavigateDOMContentLoadedFasterThanLoad(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	start := time.Now()
	dcl := callTool[NavigateOutput](t, "navigate", map[string]any{
		"tab":        tabID,
		"url":        harness.httpSrv.URL + "/slow-subresource",
		"wait_until": "domcontentloaded",
	})
	dclElapsed := time.Since(start)
	if dcl.Status != 200 {
		t.Errorf("domcontentloaded status = %d, want 200", dcl.Status)
	}
	if dcl.Title != "Slow Subresource" {
		t.Errorf("domcontentloaded title = %q, want %q", dcl.Title, "Slow Subresource")
	}
	if dclElapsed >= 1200*time.Millisecond {
		t.Errorf("domcontentloaded took %v, expected return before 1.5s subresource finished", dclElapsed)
	}

	start = time.Now()
	full := callTool[NavigateOutput](t, "navigate", map[string]any{
		"tab":        tabID,
		"url":        harness.httpSrv.URL + "/slow-subresource",
		"wait_until": "load",
	})
	loadElapsed := time.Since(start)
	if full.Status != 200 {
		t.Errorf("load status = %d, want 200", full.Status)
	}
	if loadElapsed < 1200*time.Millisecond {
		t.Errorf("load returned in %v, expected to wait for 1.5s subresource", loadElapsed)
	}
}

func TestNavigateNetworkIdleWaitsPastReplaceStateAndXHR(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	out := callTool[NavigateOutput](t, "navigate", map[string]any{
		"tab":        tabID,
		"url":        harness.httpSrv.URL + "/slow-xhr",
		"wait_until": "networkidle",
	})
	if out.Status != 200 {
		t.Errorf("networkidle status = %d, want 200", out.Status)
	}
	if !strings.Contains(out.URL, "spa=1") {
		t.Errorf("expected URL updated by replaceState, got %q", out.URL)
	}

	done := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `window.__xhrDone`,
	})
	if string(done.Result) != "true" {
		t.Fatalf("networkidle returned before 600ms XHR completed: __xhrDone = %s", done.Result)
	}
}

func TestNavigateLifecycleModesRedirectFragmentAndBlank(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	for _, mode := range []string{"domcontentloaded", "networkidle"} {
		redir := callTool[NavigateOutput](t, "navigate", map[string]any{
			"tab":        tabID,
			"url":        harness.httpSrv.URL + "/redirect",
			"wait_until": mode,
		})
		if redir.Status != 200 {
			t.Errorf("%s redirect status = %d, want 200", mode, redir.Status)
		}
		if !strings.HasSuffix(redir.URL, "/page2.html") {
			t.Errorf("%s redirect URL = %q, want suffix /page2.html", mode, redir.URL)
		}

		// Same-document fragment navigation and repeated no-op fragment navigation.
		fragURL := harness.httpSrv.URL + "/page2.html#sec1"
		frag := callTool[NavigateOutput](t, "navigate", map[string]any{
			"tab":        tabID,
			"url":        fragURL,
			"wait_until": mode,
		})
		if frag.URL != fragURL {
			t.Errorf("%s fragment URL = %q, want %q", mode, frag.URL, fragURL)
		}
		same := callTool[NavigateOutput](t, "navigate", map[string]any{
			"tab":        tabID,
			"url":        fragURL,
			"wait_until": mode,
		})
		if same.URL != fragURL {
			t.Errorf("%s no-op fragment URL = %q, want %q", mode, same.URL, fragURL)
		}

		blank := callTool[NavigateOutput](t, "navigate", map[string]any{
			"tab":        tabID,
			"url":        "about:blank",
			"wait_until": mode,
		})
		if blank.URL != "about:blank" {
			t.Errorf("%s about:blank URL = %q, want about:blank", mode, blank.URL)
		}
	}
}

func TestNavigateEmptyURLRejected(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "navigate", map[string]any{
		"tab": tabID,
		"url": "",
	})
	if !strings.Contains(errText, "url") {
		t.Errorf("expected url required error, got: %s", errText)
	}
}

func TestWaitForNegativeTimeoutAndHiddenSelectorDiagnostics(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "wait_for", map[string]any{
		"tab":      tabID,
		"selector": "h1",
		"timeout":  -100,
	})
	if !strings.Contains(errText, "timeout") {
		t.Errorf("expected negative timeout error, got: %s", errText)
	}

	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": `(function() {
			var d = document.createElement('div');
			d.id = 'hidden-target';
			d.style.display = 'none';
			d.textContent = 'Hidden';
			document.body.appendChild(d);
		})()`,
	})

	errText = callToolExpectErr(t, "wait_for", map[string]any{
		"tab":      tabID,
		"selector": "#hidden-target",
		"timeout":  300,
	})
	if !strings.Contains(errText, "display:none") && !strings.Contains(errText, "not visible") {
		t.Errorf("expected hidden-element diagnostic from wait_for, got: %s", errText)
	}
}

func TestNavigateClientRedirectAnd204DoNotHang(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	// Immediate <head> location.replace must complete cleanly under both
	// domcontentloaded and networkidle without hanging 30s.
	for _, mode := range []string{"domcontentloaded", "networkidle"} {
		start := time.Now()
		out := callTool[NavigateOutput](t, "navigate", map[string]any{
			"tab":        tabID,
			"url":        harness.httpSrv.URL + "/bounce",
			"wait_until": mode,
		})
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("%s client redirect took %v, expected prompt completion", mode, elapsed)
		}
		if out.Status != 200 {
			t.Errorf("%s client redirect status = %d, want 200", mode, out.Status)
		}
		if mode == "networkidle" && !strings.HasSuffix(out.URL, "/page2.html") {
			t.Errorf("%s client redirect URL = %q, want suffix /page2.html", mode, out.URL)
		}
	}

	// Delayed client-side redirect (50ms, before networkIdle's 500ms quiet
	// window) must adopt the destination loader and finish on /page2.html.
	start := time.Now()
	delayed := callTool[NavigateOutput](t, "navigate", map[string]any{
		"tab":        tabID,
		"url":        harness.httpSrv.URL + "/bounce-delayed",
		"wait_until": "networkidle",
	})
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("networkidle delayed client redirect took %v, expected prompt completion", elapsed)
	}
	if delayed.Status != 200 {
		t.Errorf("networkidle delayed client redirect status = %d, want 200", delayed.Status)
	}
	if !strings.HasSuffix(delayed.URL, "/page2.html") {
		t.Errorf("networkidle delayed client redirect URL = %q, want suffix /page2.html", delayed.URL)
	}

	// HTTP 204 No Content aborts navigation in Chromium (net::ERR_ABORTED)
	// and must return promptly in all wait_until modes rather than hanging.
	for _, mode := range []string{"load", "domcontentloaded", "networkidle"} {
		start = time.Now()
		errText := callToolExpectErr(t, "navigate", map[string]any{
			"tab":        tabID,
			"url":        harness.httpSrv.URL + "/no-content",
			"wait_until": mode,
		})
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("%s HTTP 204 took %v, expected prompt error", mode, elapsed)
		}
		if !strings.Contains(errText, "ERR_ABORTED") {
			t.Errorf("%s HTTP 204 error = %q, want ERR_ABORTED", mode, errText)
		}
	}
}

func TestWrapNavTimeoutUnit(t *testing.T) {
	err := wrapNavTimeout(context.DeadlineExceeded, nil, `navigate to "https://example.com" (wait_until=load)`)
	if err == nil || !strings.Contains(err.Error(), "https://example.com") || !strings.Contains(err.Error(), defaultLifecycleTimeout.String()) {
		t.Errorf("wrapNavTimeout = %v, want URL and timeout duration", err)
	}

	// When parent context itself is canceled/timed out, return unmodified error.
	unchanged := wrapNavTimeout(context.DeadlineExceeded, context.Canceled, "reload")
	if unchanged != context.DeadlineExceeded {
		t.Errorf("wrapNavTimeout with parentErr = %v, want original DeadlineExceeded", unchanged)
	}
}
