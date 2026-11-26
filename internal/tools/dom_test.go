package tools

import (
	"strings"
	"testing"
)

func TestCapDOMContent(t *testing.T) {
	out := capDOMContent("hello", 10)
	if out.Content != "hello" || out.Truncated || out.TotalBytes != 0 {
		t.Errorf("untruncated = %+v", out)
	}
	// Multi-byte UTF-8 boundary: "aé" is 3 bytes; maxBytes=2 must trim 'é'.
	out = capDOMContent("aé", 2)
	if out.Content != "a" || !out.Truncated || out.TotalBytes != 3 {
		t.Errorf("truncated UTF-8 = %+v", out)
	}
}

func TestQuery(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	out := callTool[queryOutput](t, "query", map[string]any{
		"tab":      tabID,
		"selector": ".item",
	})
	if out.Total != 3 || len(out.Elements) != 3 {
		t.Errorf("query total=%d len=%d, want 3", out.Total, len(out.Elements))
	}
	if len(out.Elements) > 0 && out.Elements[0].TagName != "li" {
		t.Errorf("first element tag = %q, want li", out.Elements[0].TagName)
	}
}

func TestGetDOMHTMLAndText(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	htmlOut := callTool[getDOMOutput](t, "get_dom", map[string]any{
		"tab":      tabID,
		"format":   "html",
		"selector": "#title",
	})
	if !strings.Contains(htmlOut.Content, "<h1") || !strings.Contains(htmlOut.Content, "Hello World") {
		t.Errorf("get_dom(html) = %q, want h1 with Hello World", htmlOut.Content)
	}

	innerOut := callTool[getDOMOutput](t, "get_dom", map[string]any{
		"tab":      tabID,
		"format":   "html",
		"selector": "#title",
		"outer":    false,
	})
	if strings.Contains(innerOut.Content, "<h1") || !strings.Contains(innerOut.Content, "Hello World") {
		t.Errorf("get_dom(html, outer=false) = %q", innerOut.Content)
	}

	textOut := callTool[getDOMOutput](t, "get_dom", map[string]any{
		"tab":      tabID,
		"format":   "text",
		"selector": "#title",
	})
	if strings.TrimSpace(textOut.Content) != "Hello World" {
		t.Errorf("get_dom(text) = %q, want Hello World", textOut.Content)
	}
}

func TestGetDOMDefaultsAndTruncation(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	htmlOut := callTool[getDOMOutput](t, "get_dom", map[string]any{
		"tab":    tabID,
		"format": "html",
	})
	if !strings.Contains(htmlOut.Content, "<html") || !strings.Contains(htmlOut.Content, "Page Two") {
		t.Errorf("get_dom(html) default = %q", htmlOut.Content)
	}

	textOut := callTool[getDOMOutput](t, "get_dom", map[string]any{
		"tab":    tabID,
		"format": "text",
	})
	if !strings.Contains(textOut.Content, "Page Two") {
		t.Errorf("get_dom(text) default = %q", textOut.Content)
	}

	// max_bytes cap truncates cleanly and reports TotalBytes.
	capped := callTool[getDOMOutput](t, "get_dom", map[string]any{
		"tab":       tabID,
		"format":    "html",
		"max_bytes": 20,
	})
	if !capped.Truncated || len(capped.Content) > 20 || capped.TotalBytes <= 20 {
		t.Errorf("get_dom capped = %+v, want Truncated=true len<=20 TotalBytes>20", capped)
	}

	// Validation: bad format, negative max_bytes, ref+selector mutual exclusion.
	if err := callToolExpectErr(t, "get_dom", map[string]any{"tab": tabID, "format": "xml"}); !strings.Contains(err, "format") {
		t.Errorf("invalid format err = %q", err)
	}
	if err := callToolExpectErr(t, "get_dom", map[string]any{"tab": tabID, "format": "text", "max_bytes": -1}); !strings.Contains(err, "max_bytes") {
		t.Errorf("negative max_bytes err = %q", err)
	}
	if err := callToolExpectErr(t, "get_dom", map[string]any{"tab": tabID, "format": "text", "ref": "e1", "selector": "h1"}); !strings.Contains(err, "mutually exclusive") {
		t.Errorf("ref+selector err = %q", err)
	}
}

func TestGetDOMMissingSelector(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	selectorTimeout(t, tabID, "get_dom",
		map[string]any{"format": "html"},
		map[string]any{"format": "html", "selector": "#title"},
	)
	selectorTimeout(t, tabID, "get_dom",
		map[string]any{"format": "text"},
		map[string]any{"format": "text", "selector": "#title"},
	)
}

func TestGetDOMTextPermutations(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	tests := []struct {
		name      string
		selector  string
		hidden    bool
		wantErr   bool
		wantEmpty bool
		wantText  string
	}{
		{"visible/hidden=false", "#title", false, false, false, "Hello World"},
		{"visible/hidden=true", "#title", true, false, false, "Hello World"},
		{"hidden-element/hidden=false", "#hidden-el", false, false, true, ""},
		{"hidden-element/hidden=true", "#hidden-el", true, false, false, "Hidden content"},
		{"first-match-hidden/hidden=false", ".mv-item", false, false, true, ""},
		{"first-match-hidden/hidden=true", ".mv-item", true, false, false, "Hidden paragraph"},
		{"non-existing/hidden=false", "#does-not-exist", false, true, false, ""},
		{"non-existing/hidden=true", "#does-not-exist", true, true, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := map[string]any{
				"tab":      tabID,
				"format":   "text",
				"selector": tt.selector,
			}
			if tt.hidden {
				args["hidden"] = true
			}
			if tt.wantErr {
				args["timeout"] = 500
				if errText := callToolExpectErr(t, "get_dom", args); errText == "" {
					t.Error("expected error for non-existing element")
				}
				return
			}
			out := callTool[getDOMOutput](t, "get_dom", args)
			if tt.wantEmpty {
				if out.Content != "" {
					t.Errorf("got %q, want empty", out.Content)
				}
				return
			}
			if !strings.Contains(out.Content, tt.wantText) {
				t.Errorf("got %q, want substring %q", out.Content, tt.wantText)
			}
		})
	}
}

func TestGetDOMHiddenBodyAndNested(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	visible := callTool[getDOMOutput](t, "get_dom", map[string]any{"tab": tabID, "format": "text", "hidden": false})
	if !strings.Contains(visible.Content, "Hello World") || strings.Contains(visible.Content, "Hidden content") {
		t.Errorf("visible body text = %q", visible.Content)
	}
	withHidden := callTool[getDOMOutput](t, "get_dom", map[string]any{"tab": tabID, "format": "text", "hidden": true})
	if !strings.Contains(withHidden.Content, "Hidden content") {
		t.Errorf("hidden=true body text missing hidden content: %q", withHidden.Content)
	}

	tab2 := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tab2)

	nested := callTool[getDOMOutput](t, "get_dom", map[string]any{"tab": tab2, "format": "text", "selector": "#nested-text"})
	for _, want := range []string{"Hello", "bold", "italic"} {
		if !strings.Contains(nested.Content, want) {
			t.Errorf("nested text missing %q: %q", want, nested.Content)
		}
	}
	empty := callTool[getDOMOutput](t, "get_dom", map[string]any{"tab": tab2, "format": "text", "selector": "#empty-el"})
	if empty.Content != "" {
		t.Errorf("empty element text = %q, want empty", empty.Content)
	}
	innerNested := callTool[getDOMOutput](t, "get_dom", map[string]any{"tab": tab2, "format": "html", "selector": "#nested-text", "outer": false})
	if strings.Contains(innerNested.Content, `<div id="nested-text"`) || !strings.Contains(innerNested.Content, "<span>") || !strings.Contains(innerNested.Content, "<strong>") {
		t.Errorf("inner nested HTML = %q", innerNested.Content)
	}
}

func TestQueryFeaturesAndMutationSafety(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	if err := callToolExpectErr(t, "query", map[string]any{"tab": tabID, "selector": ""}); !strings.Contains(err, "selector") {
		t.Errorf("empty selector err = %q", err)
	}
	if err := callToolExpectErr(t, "query", map[string]any{"tab": tabID, "selector": ".item", "limit": -1}); !strings.Contains(err, "limit") {
		t.Errorf("negative limit err = %q", err)
	}
	selectorTimeout(t, tabID, "query", nil, map[string]any{"selector": ".item"})

	limited := callTool[queryOutput](t, "query", map[string]any{"tab": tabID, "selector": ".item", "limit": 2})
	if limited.Total != 3 || len(limited.Elements) != 2 {
		t.Errorf("limit=2: total=%d len=%d", limited.Total, len(limited.Elements))
	}

	full := callTool[queryOutput](t, "query", map[string]any{
		"tab":            tabID,
		"selector":       "#title",
		"outer_html":     true,
		"bbox":           true,
		"computed_style": []string{"display", "font-family"},
	})
	if len(full.Elements) != 1 {
		t.Fatalf("len = %d, want 1", len(full.Elements))
	}
	e := full.Elements[0]
	if e.TagName != "h1" || !strings.Contains(e.OuterHTML, "<h1") || e.BBox == nil || e.BBox.Width <= 0 || e.ComputedStyle["display"] != "block" || e.Attributes["id"] != "title" {
		t.Errorf("query full fields = %+v", e)
	}

	noAttrs := callTool[queryOutput](t, "query", map[string]any{"tab": tabID, "selector": "#title", "attributes": false, "text": false})
	if len(noAttrs.Elements[0].Attributes) != 0 || noAttrs.Elements[0].Text != "" {
		t.Errorf("attributes/text disabled = %+v", noAttrs.Elements[0])
	}

	// Delayed element appears within selector timeout.
	tabDelayed := navigateToFixture(t, "delayed.html")
	defer closeTab(t, tabDelayed)
	delayed := callTool[queryOutput](t, "query", map[string]any{"tab": tabDelayed, "selector": "#delayed-element"})
	if delayed.Total != 1 || !strings.Contains(delayed.Elements[0].Text, "appeared") {
		t.Errorf("delayed query = %+v", delayed)
	}

	// Default limit=10 on 12 items + display:none partial bbox + mutation safety.
	tab2 := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tab2)
	defLim := callTool[queryOutput](t, "query", map[string]any{"tab": tab2, "selector": ".query-item", "limit": 0})
	if defLim.Total != 12 || len(defLim.Elements) != 10 {
		t.Errorf("default limit: total=%d len=%d, want 12/10", defLim.Total, len(defLim.Elements))
	}
	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tab2,
		"expression": `document.body.insertAdjacentHTML('beforeend', '<div id="hidden-q" style="display:none">Hidden</div>')`,
	})
	hiddenQ := callTool[queryOutput](t, "query", map[string]any{"tab": tab2, "selector": "#hidden-q", "outer_html": true, "bbox": true})
	if len(hiddenQ.Elements) != 1 || hiddenQ.Elements[0].OuterHTML == "" {
		t.Errorf("hidden query = %+v", hiddenQ)
	}
}
