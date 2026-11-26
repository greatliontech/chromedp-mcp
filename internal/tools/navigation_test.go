package tools

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
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
	out = callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.title",
	})
	if !strings.Contains(string(out.Result), "SPA About") {
		t.Fatalf("after click About, title = %s, want 'SPA About'", out.Result)
	}

	// go_back should trigger same-document navigation (popstate).
	callTool[navigateOutput](t, "navigate", map[string]any{"tab": tabID, "action": "back"})

	// Verify we're back on Home.
	out = callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.title",
	})
	if !strings.Contains(string(out.Result), "SPA Home") {
		t.Fatalf("after go_back, title = %s, want 'SPA Home'", out.Result)
	}

	// The DOM should also reflect the change.
	text := callTool[getDOMOutput](t, "get_dom", map[string]any{
		"tab":      tabID,
		"format":   "text",
		"selector": "#page",
	})
	if !strings.Contains(text.Content, "Home") {
		t.Errorf("after go_back, #page text = %q, want 'Home'", text.Content)
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
	callTool[navigateOutput](t, "navigate", map[string]any{"tab": tabID, "action": "back"})

	// Go forward to About.
	callTool[navigateOutput](t, "navigate", map[string]any{"tab": tabID, "action": "forward"})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.title",
	})
	if !strings.Contains(string(out.Result), "SPA About") {
		t.Fatalf("after go_forward, title = %s, want 'SPA About'", out.Result)
	}

	text := callTool[getDOMOutput](t, "get_dom", map[string]any{
		"tab":      tabID,
		"format":   "text",
		"selector": "#page",
	})
	if !strings.Contains(text.Content, "About") {
		t.Errorf("after go_forward, #page text = %q, want 'About'", text.Content)
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
	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.title",
	})
	if !strings.Contains(string(out.Result), "SPA Contact") {
		t.Fatalf("title = %s, want 'SPA Contact'", out.Result)
	}

	// Go back to About.
	callTool[navigateOutput](t, "navigate", map[string]any{"tab": tabID, "action": "back"})
	out = callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.title",
	})
	if !strings.Contains(string(out.Result), "SPA About") {
		t.Fatalf("after first go_back, title = %s, want 'SPA About'", out.Result)
	}

	// Go back to Home.
	callTool[navigateOutput](t, "navigate", map[string]any{"tab": tabID, "action": "back"})
	out = callTool[evaluateOutput](t, "evaluate", map[string]any{
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
	callTool[navigateOutput](t, "navigate", map[string]any{"tab": tabID, "action": "back"})

	// After SPA go_back, we should be able to interact with the DOM normally.
	// Click the Contact button.
	callTool[struct{}](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#go-contact",
	})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.title",
	})
	if !strings.Contains(string(out.Result), "SPA Contact") {
		t.Fatalf("after go_back + click contact, title = %s, want 'SPA Contact'", out.Result)
	}

	// query should also work.
	qout := callTool[queryOutput](t, "query", map[string]any{
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
	dcl := callTool[navigateOutput](t, "navigate", map[string]any{
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
	full := callTool[navigateOutput](t, "navigate", map[string]any{
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

	out := callTool[navigateOutput](t, "navigate", map[string]any{
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

	done := callTool[evaluateOutput](t, "evaluate", map[string]any{
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
		redir := callTool[navigateOutput](t, "navigate", map[string]any{
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
		frag := callTool[navigateOutput](t, "navigate", map[string]any{
			"tab":        tabID,
			"url":        fragURL,
			"wait_until": mode,
		})
		if frag.URL != fragURL {
			t.Errorf("%s fragment URL = %q, want %q", mode, frag.URL, fragURL)
		}
		same := callTool[navigateOutput](t, "navigate", map[string]any{
			"tab":        tabID,
			"url":        fragURL,
			"wait_until": mode,
		})
		if same.URL != fragURL {
			t.Errorf("%s no-op fragment URL = %q, want %q", mode, same.URL, fragURL)
		}

		blank := callTool[navigateOutput](t, "navigate", map[string]any{
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

	callTool[evaluateOutput](t, "evaluate", map[string]any{
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
		out := callTool[navigateOutput](t, "navigate", map[string]any{
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
	delayed := callTool[navigateOutput](t, "navigate", map[string]any{
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

func TestNavigate(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	out := callTool[navigateOutput](t, "navigate", map[string]any{
		"url": fixtureURL("page2.html"),
		"tab": tabID,
	})
	if !strings.HasSuffix(out.URL, "/page2.html") {
		t.Errorf("navigate URL = %q, want suffix /page2.html", out.URL)
	}
	if out.Title != "Page 2" {
		t.Errorf("navigate Title = %q, want %q", out.Title, "Page 2")
	}
}

func TestNavigateAndGoBack(t *testing.T) {
	tabID := navigateToFixture(t, "forms.html")
	defer closeTab(t, tabID)

	// Navigate to page2.
	callTool[navigateOutput](t, "navigate", map[string]any{
		"url": fixtureURL("page2.html"),
		"tab": tabID,
	})
	// Go back.
	callTool[navigateOutput](t, "navigate", map[string]any{"tab": tabID, "action": "back"})

	// Verify we're back on the forms page by checking the URL.
	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.title",
	})
	if !strings.Contains(string(out.Result), "Form") {
		t.Errorf("after go_back, title = %s, want to contain 'Form'", out.Result)
	}
}

func TestWaitFor(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// Wait for a selector that already exists.
	callTool[struct{}](t, "wait_for", map[string]any{
		"tab":      tabID,
		"selector": "#title",
		"timeout":  5000,
	})

	// Wait for a JS expression.
	callTool[struct{}](t, "wait_for", map[string]any{
		"tab":        tabID,
		"expression": "document.title === 'Test Page'",
		"timeout":    5000,
	})
}

func TestNavigateReturnsStatusCode(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	out := callTool[navigateOutput](t, "navigate", map[string]any{
		"url": fixtureURL("page2.html"),
		"tab": tabID,
	})
	if out.Status == 0 {
		t.Error("navigate should return a non-zero HTTP status code")
	}
	if out.Status != 200 {
		t.Errorf("navigate status = %d, want 200", out.Status)
	}
}

func TestNavigateFollowsRedirect(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	out := callTool[navigateOutput](t, "navigate", map[string]any{
		"url": harness.httpSrv.URL + "/redirect",
		"tab": tabID,
	})
	// The /redirect handler 302s to /page2.html. Because Go's FileServer
	// redirects /index.html to /, we use page2.html as the target.
	if !strings.HasSuffix(out.URL, "/page2.html") {
		t.Errorf("after redirect, URL = %q, want suffix /page2.html", out.URL)
	}
	if out.Title != "Page 2" {
		t.Errorf("after redirect, Title = %q, want 'Page 2'", out.Title)
	}
}

func TestNavigate404(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	out := callTool[navigateOutput](t, "navigate", map[string]any{
		"url": harness.httpSrv.URL + "/api/not-found",
		"tab": tabID,
	})
	if out.Status != 404 {
		t.Errorf("navigate to 404 page: status = %d, want 404", out.Status)
	}
}

func TestGoForward(t *testing.T) {
	tabID := navigateToFixture(t, "forms.html")
	defer closeTab(t, tabID)

	// Navigate to page2.
	callTool[navigateOutput](t, "navigate", map[string]any{
		"url": fixtureURL("page2.html"),
		"tab": tabID,
	})
	// Go back to forms.
	callTool[navigateOutput](t, "navigate", map[string]any{"tab": tabID, "action": "back"})
	// Go forward to page2 again.
	callTool[navigateOutput](t, "navigate", map[string]any{"tab": tabID, "action": "forward"})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.title",
	})
	if !strings.Contains(string(out.Result), "Page 2") {
		t.Errorf("after go_forward, title = %s, want to contain 'Page 2'", out.Result)
	}
}

func TestGoBackNoHistory(t *testing.T) {
	// A fresh tab with no navigation has no back history.
	out := callTool[tabNewOutput](t, "tab_new", map[string]any{})
	defer closeTab(t, out.TabID)

	errText := callToolExpectErr(t, "navigate", map[string]any{"tab": out.TabID, "action": "back"})
	if !strings.Contains(errText, "no previous history") {
		t.Errorf("go_back error = %q, want 'no previous history'", errText)
	}
}

func TestReload(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// Mutate the page via JS.
	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.title = 'mutated'",
	})
	// Reload should restore the original and return useful output.
	rout := callTool[navigateOutput](t, "navigate", map[string]any{"tab": tabID, "action": "reload"})
	if rout.Title == "mutated" {
		t.Errorf("after reload, title should not be 'mutated', got %s", rout.Title)
	}
	if rout.URL == "" {
		t.Error("reload output should include URL")
	}
	if rout.Title != "Interaction Test" {
		t.Errorf("after reload, title = %q, want %q", rout.Title, "Interaction Test")
	}
}

func TestWaitForValidation(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	// Neither selector nor expression → error.
	errText := callToolExpectErr(t, "wait_for", map[string]any{
		"tab": tabID,
	})
	if !strings.Contains(errText, "exactly one") {
		t.Errorf("expected validation error, got: %s", errText)
	}

	// Both selector and expression → error.
	errText = callToolExpectErr(t, "wait_for", map[string]any{
		"tab":        tabID,
		"selector":   "#title",
		"expression": "true",
	})
	if !strings.Contains(errText, "exactly one") {
		t.Errorf("expected validation error, got: %s", errText)
	}
}

func TestWaitForTimeout(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "wait_for", map[string]any{
		"tab":      tabID,
		"selector": "#does-not-exist",
		"timeout":  500, // 500ms timeout
	})
	if !strings.Contains(errText, "timed out") || !strings.Contains(errText, "not found in the DOM") {
		t.Errorf("expected descriptive selector timeout error, got: %s", errText)
	}
}

func TestNavigateWaitUntilDOMContentLoaded(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	out := callTool[navigateOutput](t, "navigate", map[string]any{
		"tab":        tabID,
		"url":        fixtureURL("index.html"),
		"wait_until": "domcontentloaded",
	})
	if out.Status != 200 {
		t.Errorf("navigate status = %d, want 200", out.Status)
	}
	// Go's http.FileServer 301-redirects /index.html to /, so the final
	// URL won't have the /index.html suffix.
	if out.Title != "Test Page" {
		t.Errorf("title = %q, want 'Test Page'", out.Title)
	}
}

func TestNavigateWaitUntilNetworkIdle(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	out := callTool[navigateOutput](t, "navigate", map[string]any{
		"tab":        tabID,
		"url":        fixtureURL("network.html"),
		"wait_until": "networkidle",
	})
	if out.Status != 200 {
		t.Errorf("navigate status = %d, want 200", out.Status)
	}
	if out.Title == "" {
		t.Error("title should not be empty after networkidle")
	}
}

func TestNavigateWaitUntilLoadExplicit(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	out := callTool[navigateOutput](t, "navigate", map[string]any{
		"tab":        tabID,
		"url":        fixtureURL("forms.html"),
		"wait_until": "load",
	})
	if out.Status != 200 {
		t.Errorf("status = %d, want 200", out.Status)
	}
	if out.Title != "Form Test" {
		t.Errorf("title = %q, want 'Form Test'", out.Title)
	}
}

func TestReloadBypassCache(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// Mutate the title.
	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.title = 'cached-mutant'",
	})

	// Reload with bypass_cache — should wait for page load and return output.
	rout := callTool[navigateOutput](t, "navigate", map[string]any{
		"tab":          tabID,
		"action":       "reload",
		"bypass_cache": true,
	})
	if rout.Title == "cached-mutant" {
		t.Error("after bypass_cache reload, title should be restored from server")
	}
	if rout.URL == "" {
		t.Error("reload output should include URL")
	}
}

func TestGoForwardNoHistory(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "navigate", map[string]any{"tab": tabID, "action": "forward"})
	if !strings.Contains(errText, "no forward history") {
		t.Errorf("go_forward error = %q, want 'no forward history'", errText)
	}
}

func TestWaitForAsyncSelector(t *testing.T) {
	tabID := navigateToFixture(t, "delayed.html")
	defer closeTab(t, tabID)

	// #delayed-element appears after 500ms. wait_for should wait for it.
	callTool[struct{}](t, "wait_for", map[string]any{
		"tab":      tabID,
		"selector": "#delayed-element",
		"timeout":  5000,
	})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('delayed-element').textContent",
	})
	if !strings.Contains(string(out.Result), "appeared after a delay") {
		t.Errorf("delayed element text = %s, want to contain 'appeared after a delay'", out.Result)
	}
}

func TestWaitForAsyncExpression(t *testing.T) {
	tabID := navigateToFixture(t, "delayed.html")
	defer closeTab(t, tabID)

	// window.__delayedReady becomes true after 500ms.
	callTool[struct{}](t, "wait_for", map[string]any{
		"tab":        tabID,
		"expression": "window.__delayedReady === true",
		"timeout":    5000,
	})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "window.__delayedReady",
	})
	if string(out.Result) != "true" {
		t.Errorf("__delayedReady = %s, want true", out.Result)
	}
}

func TestGoBackMultipleSteps(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// Navigate to page2, then forms (3 pages total).
	callTool[navigateOutput](t, "navigate", map[string]any{
		"tab": tabID,
		"url": fixtureURL("page2.html"),
	})
	callTool[navigateOutput](t, "navigate", map[string]any{
		"tab": tabID,
		"url": fixtureURL("forms.html"),
	})

	// Go back twice to reach index.
	callTool[navigateOutput](t, "navigate", map[string]any{"tab": tabID, "action": "back"})
	callTool[navigateOutput](t, "navigate", map[string]any{"tab": tabID, "action": "back"})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.title",
	})
	if !strings.Contains(string(out.Result), "Test Page") {
		t.Errorf("after two go_back, title = %s, want 'Test Page'", out.Result)
	}
}

func TestReloadBypassCacheFalse(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// Mutate title.
	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.title = 'mutated-again'",
	})

	// Reload with bypass_cache=false (explicit) — should behave same as default reload.
	rout := callTool[navigateOutput](t, "navigate", map[string]any{
		"tab":          tabID,
		"action":       "reload",
		"bypass_cache": false,
	})
	if rout.Title == "mutated-again" {
		t.Error("after reload(bypass_cache=false), title should be restored")
	}
	if rout.URL == "" {
		t.Error("reload output should include URL")
	}
}

func TestNavigateSlowPage(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	start := time.Now()
	out := callTool[navigateOutput](t, "navigate", map[string]any{
		"tab": tabID,
		"url": harness.httpSrv.URL + "/slow",
	})
	elapsed := time.Since(start)

	if out.Status != 200 {
		t.Errorf("slow page status = %d, want 200", out.Status)
	}
	// Should take at least 200ms (the /slow handler sleeps 200ms).
	if elapsed < 150*time.Millisecond {
		t.Errorf("slow page took %v, expected at least 150ms", elapsed)
	}
}

func TestWaitForExpressionTimeout(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "wait_for", map[string]any{
		"tab":        tabID,
		"expression": "window.__never_set_flag === true",
		"timeout":    500,
	})
	if errText == "" {
		t.Error("wait_for with never-truthy expression should timeout")
	}
}

func TestWaitForTimeoutZeroUsesDefault(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// Selector #title already exists, so with timeout=0 (should use default
	// 30s) this should return quickly.
	callTool[struct{}](t, "wait_for", map[string]any{
		"tab":      tabID,
		"selector": "#title",
		"timeout":  0,
	})
}

func TestGoForwardMultipleSteps(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// Navigate: index -> page2 -> forms (3 pages).
	callTool[navigateOutput](t, "navigate", map[string]any{
		"tab": tabID,
		"url": fixtureURL("page2.html"),
	})
	callTool[navigateOutput](t, "navigate", map[string]any{
		"tab": tabID,
		"url": fixtureURL("forms.html"),
	})

	// Go back twice to index.
	callTool[navigateOutput](t, "navigate", map[string]any{"tab": tabID, "action": "back"})
	callTool[navigateOutput](t, "navigate", map[string]any{"tab": tabID, "action": "back"})

	// Go forward twice to forms.
	callTool[navigateOutput](t, "navigate", map[string]any{"tab": tabID, "action": "forward"})
	callTool[navigateOutput](t, "navigate", map[string]any{"tab": tabID, "action": "forward"})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.title",
	})
	if !strings.Contains(string(out.Result), "Form Test") {
		t.Errorf("after two go_forward, title = %s, want 'Form Test'", out.Result)
	}
}

func TestNavigateWaitUntilInvalid(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	// Invalid wait_until value should return an error.
	errText := callToolExpectErr(t, "navigate", map[string]any{
		"tab":        tabID,
		"url":        fixtureURL("index.html"),
		"wait_until": "invalid_event",
	})
	if !strings.Contains(errText, "wait_until") || !strings.Contains(errText, "invalid_event") {
		t.Errorf("error = %q, want it to mention 'wait_until' and 'invalid_event'", errText)
	}
}

// TestGoBackDOMWorks verifies that after go_back, all selector-based DOM
// operations work correctly. This is the key bfcache regression test.
func TestGoBackDOMWorks(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// Navigate to a second page.
	callTool[navigateOutput](t, "navigate", map[string]any{
		"tab": tabID,
		"url": fixtureURL("interaction.html"),
	})

	// Verify we're on the second page.
	text := callTool[getDOMOutput](t, "get_dom", map[string]any{
		"tab":      tabID,
		"format":   "text",
		"selector": "h1",
	})
	if !strings.Contains(text.Content, "Interaction") {
		t.Fatalf("expected interaction page heading, got %q", text.Content)
	}

	// Go back via MCP tool.
	callTool[navigateOutput](t, "navigate", map[string]any{"tab": tabID, "action": "back"})

	// get_dom(text) must work after go_back.
	text = callTool[getDOMOutput](t, "get_dom", map[string]any{
		"tab":      tabID,
		"format":   "text",
		"selector": "h1",
	})
	if text.Content == "" {
		t.Fatal("get_dom(text) returned empty after go_back")
	}
	t.Logf("get_dom(text) after go_back: %q", text.Content)

	// query must work after go_back.
	qout := callTool[queryOutput](t, "query", map[string]any{
		"tab":      tabID,
		"selector": "h1",
	})
	if len(qout.Elements) == 0 {
		t.Fatal("query returned no elements after go_back")
	}

	// click must work after go_back.
	callTool[struct{}](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "h1",
	})

	// get_dom(html) must work after go_back.
	html := callTool[getDOMOutput](t, "get_dom", map[string]any{
		"tab":      tabID,
		"format":   "html",
		"selector": "h1",
	})
	if html.Content == "" {
		t.Fatal("get_dom(html) returned empty after go_back")
	}
}

// TestGoForwardDOMWorks verifies go_forward also resyncs the DOM.
func TestGoForwardDOMWorks(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// Navigate to second page.
	callTool[navigateOutput](t, "navigate", map[string]any{
		"tab": tabID,
		"url": fixtureURL("interaction.html"),
	})

	// Go back.
	callTool[navigateOutput](t, "navigate", map[string]any{"tab": tabID, "action": "back"})

	// Go forward.
	callTool[navigateOutput](t, "navigate", map[string]any{"tab": tabID, "action": "forward"})

	// Selector-based queries must work after go_forward.
	text := callTool[getDOMOutput](t, "get_dom", map[string]any{
		"tab":      tabID,
		"format":   "text",
		"selector": "h1",
	})
	if !strings.Contains(text.Content, "Interaction") {
		t.Fatalf("expected interaction page after go_forward, got %q", text.Content)
	}
}

// TestGoBackMultipleHops verifies go_back works across 3+ pages.
func TestGoBackMultipleHops(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// Build a 3-page history: index -> interaction -> forms.
	callTool[navigateOutput](t, "navigate", map[string]any{
		"tab": tabID,
		"url": fixtureURL("interaction.html"),
	})
	callTool[navigateOutput](t, "navigate", map[string]any{
		"tab": tabID,
		"url": fixtureURL("forms.html"),
	})

	// Go back to interaction.
	callTool[navigateOutput](t, "navigate", map[string]any{"tab": tabID, "action": "back"})
	text := callTool[getDOMOutput](t, "get_dom", map[string]any{
		"tab":      tabID,
		"format":   "text",
		"selector": "h1",
	})
	if !strings.Contains(text.Content, "Interaction") {
		t.Fatalf("first go_back: expected interaction, got %q", text.Content)
	}

	// Go back to index.
	callTool[navigateOutput](t, "navigate", map[string]any{"tab": tabID, "action": "back"})
	text = callTool[getDOMOutput](t, "get_dom", map[string]any{
		"tab":      tabID,
		"format":   "text",
		"selector": "h1",
	})
	if text.Content == "" {
		t.Fatal("second go_back: get_dom(text) returned empty")
	}
	t.Logf("second go_back h1: %q", text.Content)
}

// TestGoBackThenInteract verifies you can interact with the page after
// go_back — type into inputs, click buttons, read results.
func TestGoBackThenInteract(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// Navigate away.
	callTool[navigateOutput](t, "navigate", map[string]any{
		"tab": tabID,
		"url": fixtureURL("index.html"),
	})

	// Go back to interaction page.
	callTool[navigateOutput](t, "navigate", map[string]any{"tab": tabID, "action": "back"})

	// Type into an input.
	callTool[struct{}](t, "type", map[string]any{
		"tab":      tabID,
		"selector": "#type-target",
		"text":     "after-goback",
		"mode":     "replace",
	})

	// Read the value back.
	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('type-target').value",
	})
	if !strings.Contains(string(out.Result), "after-goback") {
		t.Errorf("type after go_back: value = %s, want 'after-goback'", out.Result)
	}

	// Click a button.
	callTool[struct{}](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#click-target",
	})
	out = callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('click-count').textContent",
	})
	if !strings.Contains(string(out.Result), "1") {
		t.Errorf("click after go_back: count = %s, want '1'", out.Result)
	}
}

func TestNavigateNetworkIdleTimesOut(t *testing.T) {
	unblock := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hang" {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusOK)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			select {
			case <-unblock:
			case <-r.Context().Done():
			}
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>Busy</title></head><body><script>fetch('/hang');</script></body></html>`))
	}))
	defer func() {
		close(unblock)
		srv.Close()
	}()

	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	tb, err := harness.mgr.ResolveTab("", tabID)
	if err != nil {
		t.Fatalf("ResolveTab: %v", err)
	}
	tctx, tcancel := tabContext(t.Context(), tb.Context())
	defer tcancel()
	navCtx, navCancel := context.WithTimeout(tctx, 600*time.Millisecond)
	defer navCancel()

	_, err = navigateUntilLifecycle(navCtx, tctx, srv.URL+"/", "networkidle")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("navigateUntilLifecycle(networkidle) err = %v, want context.DeadlineExceeded", err)
	}
	wrapped := wrapNavTimeout(err, tctx.Err(), fmt.Sprintf("navigate to %q (wait_until=networkidle)", srv.URL+"/"))
	if !strings.Contains(wrapped.Error(), "timed out") || !strings.Contains(wrapped.Error(), "networkidle") {
		t.Errorf("wrapNavTimeout = %q, want timed out + networkidle", wrapped.Error())
	}

	// Tab must remain usable after a lifecycle timeout.
	out := callTool[navigateOutput](t, "navigate", map[string]any{
		"tab": tabID,
		"url": fixtureURL("page2.html"),
	})
	if out.Title != "Page 2" {
		t.Errorf("follow-up navigate title = %q, want 'Page 2'", out.Title)
	}
}

func TestNavigateUnreachableURL(t *testing.T) {
	tab := callTool[tabNewOutput](t, "tab_new", map[string]any{})
	defer closeTab(t, tab.TabID)

	errText := callToolExpectErr(t, "navigate", map[string]any{
		"tab": tab.TabID,
		"url": "http://192.0.2.1:1/unreachable", // TEST-NET address, guaranteed unreachable
	})
	if errText == "" {
		t.Fatal("navigate to unreachable URL should return an error")
	}
	// The error should mention something about the navigation failure.
	lower := strings.ToLower(errText)
	if !strings.Contains(lower, "net::") && !strings.Contains(lower, "timeout") && !strings.Contains(lower, "err_") && !strings.Contains(lower, "unreachable") && !strings.Contains(lower, "failed") {
		t.Errorf("unexpected error text: %s", errText)
	}
}

func TestNavigateBackForwardReturnURLAndTitle(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/untitled", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><html><body><h1>No Title</h1></body></html>`))
	})
	mux.HandleFunc("/nostore-a", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>NoStore A</title></head><body>A</body></html>`))
	})
	mux.HandleFunc("/nostore-b", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>NoStore B</title></head><body>B</body></html>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	tabID := navigateToFixture(t, "forms.html")
	defer closeTab(t, tabID)

	// Default reload (bypass_cache=false) populates HTTP status.
	reloaded := callTool[navigateOutput](t, "navigate", map[string]any{
		"tab":    tabID,
		"action": "reload",
	})
	if reloaded.Status != 200 {
		t.Errorf("navigate(reload) status = %d, want 200", reloaded.Status)
	}

	// Untitled HTTP page must report empty Title rather than URL-as-title.
	untitled := callTool[navigateOutput](t, "navigate", map[string]any{
		"tab": tabID,
		"url": srv.URL + "/untitled",
	})
	if untitled.Title != "" {
		t.Errorf("untitled HTTP page Title = %q, want empty string", untitled.Title)
	}

	// Non-BFCache (Cache-Control: no-store) back/forward waits for load and returns exact title.
	callTool[navigateOutput](t, "navigate", map[string]any{"tab": tabID, "url": srv.URL + "/nostore-a"})
	callTool[navigateOutput](t, "navigate", map[string]any{"tab": tabID, "url": srv.URL + "/nostore-b"})
	backNS := callTool[navigateOutput](t, "navigate", map[string]any{"tab": tabID, "action": "back"})
	if !strings.HasSuffix(backNS.URL, "/nostore-a") || backNS.Title != "NoStore A" {
		t.Errorf("non-BFCache navigate(back) = %+v, want /nostore-a Title 'NoStore A'", backNS)
	}
	fwdNS := callTool[navigateOutput](t, "navigate", map[string]any{"tab": tabID, "action": "forward"})
	if !strings.HasSuffix(fwdNS.URL, "/nostore-b") || fwdNS.Title != "NoStore B" {
		t.Errorf("non-BFCache navigate(forward) = %+v, want /nostore-b Title 'NoStore B'", fwdNS)
	}

	// SPA pushState back/forward returns the updated document.title directly.
	callTool[navigateOutput](t, "navigate", map[string]any{"tab": tabID, "url": fixtureURL("spa.html")})
	callTool[clickOutput](t, "click", map[string]any{"tab": tabID, "selector": "#go-about"})
	backSPA := callTool[navigateOutput](t, "navigate", map[string]any{"tab": tabID, "action": "back"})
	if !strings.Contains(backSPA.Title, "SPA Home") {
		t.Errorf("SPA navigate(back) Title = %q, want 'SPA Home'", backSPA.Title)
	}
	fwdSPA := callTool[navigateOutput](t, "navigate", map[string]any{"tab": tabID, "action": "forward"})
	if !strings.Contains(fwdSPA.Title, "SPA About") {
		t.Errorf("SPA navigate(forward) Title = %q, want 'SPA About'", fwdSPA.Title)
	}
}

func TestWaitForStatesTextURLShadowAndFrame(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/sub.html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><html><body>
			<div id="sub-el">Subframe Ready</div>
			<script>window.__subReady = true;</script>
		</body></html>`))
	})
	mux.HandleFunc("/doc.svg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		_, _ = w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="120" height="40"><text y="20">SVG Text Ready</text></svg>`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>Wait Demo</title></head><body>
			<div id="hidden-only" style="display:none">Secret Hidden Text</div>
			<div id="vis-hidden" style="visibility:hidden; width:50px; height:20px;">Invisible</div>
			<div id="vis-collapse" style="visibility:collapse; width:50px; height:20px;">Collapsed</div>
			<div id="spinner">Loading...</div>
			<div id="shadow-host"></div>
			<iframe id="sub-frame" src="/sub.html"></iframe>
			<script>
				const sr = document.getElementById('shadow-host').attachShadow({mode: 'open'});
				window.__startAsync = function() {
					setTimeout(() => {
						sr.innerHTML = '<button id="s-ready">Shadow Ready</button>';
					}, 120);
					setTimeout(() => {
						document.getElementById('spinner').style.display = 'none';
					}, 150);
					setTimeout(() => {
						document.getElementById('spinner').remove();
					}, 320);
					setTimeout(() => {
						const p = document.createElement('p');
						p.textContent = 'Async Banner Appeared';
						document.body.appendChild(p);
						history.pushState({}, '', '/done-step');
					}, 200);
				};
			</script>
		</body></html>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)
	callTool[navigateOutput](t, "navigate", map[string]any{"tab": tabID, "url": srv.URL + "/"})

	// Synchronous state checks BEFORE starting async timers.
	callTool[struct{}](t, "wait_for", map[string]any{
		"tab":      tabID,
		"selector": "#hidden-only",
		"state":    "attached",
		"timeout":  1000,
	})
	for _, sel := range []string{"#vis-hidden", "#vis-collapse"} {
		if errText := callToolExpectErr(t, "wait_for", map[string]any{
			"tab":      tabID,
			"selector": sel,
			"state":    "visible",
			"timeout":  150,
		}); !strings.Contains(errText, "not visible") {
			t.Errorf("wait_for(%s, visible) err = %q, want 'not visible'", sel, errText)
		}
		callTool[struct{}](t, "wait_for", map[string]any{
			"tab":      tabID,
			"selector": sel,
			"state":    "hidden",
			"timeout":  1000,
		})
	}
	if errText := callToolExpectErr(t, "wait_for", map[string]any{
		"tab":     tabID,
		"text":    "Secret Hidden Text",
		"timeout": 150,
	}); !strings.Contains(errText, "timed out") {
		t.Errorf("wait_for(display:none text) err = %q, want timeout", errText)
	}

	// Start async mutations now so every wait genuinely polls across ticks.
	callTool[evaluateOutput](t, "evaluate", map[string]any{"tab": tabID, "expression": `window.__startAsync(); true`})

	// state="visible" pierces open Shadow DOM ('>>>') and waits for async mount.
	callTool[struct{}](t, "wait_for", map[string]any{
		"tab":      tabID,
		"selector": "#shadow-host >>> #s-ready",
		"state":    "visible",
		"timeout":  3000,
	})

	// state="hidden" waits for spinner display:none while still attached, then
	// state="detached" waits for removal.
	callTool[struct{}](t, "wait_for", map[string]any{
		"tab":      tabID,
		"selector": "#spinner",
		"state":    "hidden",
		"timeout":  3000,
	})
	callTool[struct{}](t, "wait_for", map[string]any{
		"tab":      tabID,
		"selector": "#spinner",
		"state":    "detached",
		"timeout":  3000,
	})

	// text waits for visible text and url waits for pushState URL substring.
	callTool[struct{}](t, "wait_for", map[string]any{
		"tab":     tabID,
		"text":    "Async Banner Appeared",
		"timeout": 3000,
	})
	callTool[struct{}](t, "wait_for", map[string]any{
		"tab":     tabID,
		"url":     "/done-step",
		"timeout": 3000,
	})

	// Invalid CSS syntax fails fast even on state="detached".
	start := time.Now()
	if errText := callToolExpectErr(t, "wait_for", map[string]any{
		"tab":      tabID,
		"selector": "[=bad]",
		"state":    "detached",
		"timeout":  4000,
	}); errText == "" {
		t.Error("invalid CSS selector should fail")
	}
	if time.Since(start) > 1500*time.Millisecond {
		t.Errorf("invalid CSS selector took %v, expected immediate syntax failure", time.Since(start))
	}

	// Same-origin iframe support for selector, text, and expression.
	frames := callTool[listFramesOutput](t, "list_frames", map[string]any{"tab": tabID})
	var subFrameID string
	for _, fr := range frames.Frames {
		if strings.HasSuffix(fr.URL, "/sub.html") {
			subFrameID = fr.ID
			break
		}
	}
	if subFrameID == "" {
		t.Fatalf("subframe not found in %+v", frames.Frames)
	}
	callTool[struct{}](t, "wait_for", map[string]any{
		"tab":      tabID,
		"frame":    subFrameID,
		"selector": "#sub-el",
		"state":    "visible",
		"timeout":  3000,
	})
	callTool[struct{}](t, "wait_for", map[string]any{
		"tab":     tabID,
		"frame":   subFrameID,
		"text":    "Subframe Ready",
		"timeout": 3000,
	})
	callTool[struct{}](t, "wait_for", map[string]any{
		"tab":        tabID,
		"frame":      subFrameID,
		"expression": "Promise.resolve(window.__subReady === true)",
		"timeout":    3000,
	})

	// wait_for(text) works on SVG documents where document.body is null.
	callTool[navigateOutput](t, "navigate", map[string]any{"tab": tabID, "url": srv.URL + "/doc.svg"})
	callTool[struct{}](t, "wait_for", map[string]any{
		"tab":     tabID,
		"text":    "SVG Text Ready",
		"timeout": 3000,
	})
}
