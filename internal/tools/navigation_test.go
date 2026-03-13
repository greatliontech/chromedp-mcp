package tools

import (
	"strings"
	"testing"
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
