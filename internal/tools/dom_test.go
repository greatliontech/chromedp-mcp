package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestQuery(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	out := callTool[QueryOutput](t, "query", map[string]any{
		"tab":      tabID,
		"selector": ".item",
	})
	if out.Total != 3 {
		t.Errorf("query total = %d, want 3", out.Total)
	}
	if len(out.Elements) != 3 {
		t.Errorf("query elements = %d, want 3", len(out.Elements))
	}
	if len(out.Elements) > 0 && out.Elements[0].TagName != "li" {
		t.Errorf("first element tag = %q, want li", out.Elements[0].TagName)
	}
}

func TestGetHTML(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	out := callTool[GetHTMLOutput](t, "get_html", map[string]any{
		"tab":      tabID,
		"selector": "#title",
	})
	if !strings.Contains(out.HTML, "Hello World") {
		t.Errorf("get_html = %q, want to contain 'Hello World'", out.HTML)
	}
}

func TestGetText(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	out := callTool[GetTextOutput](t, "get_text", map[string]any{
		"tab":      tabID,
		"selector": "#title",
	})
	if strings.TrimSpace(out.Text) != "Hello World" {
		t.Errorf("get_text = %q, want 'Hello World'", out.Text)
	}
}

func countAXTreeNodes(nodes []AXNodeOutput) int {
	total := 0
	for _, n := range nodes {
		total++
		total += countAXTreeNodes(n.Children)
	}
	return total
}

func TestGetAccessibilityTree(t *testing.T) {
	tabID := navigateToFixture(t, "accessibility.html")
	defer closeTab(t, tabID)

	out := callTool[GetAccessibilityTreeOutput](t, "get_accessibility_tree", map[string]any{
		"tab": tabID,
	})
	if len(out.Tree) == 0 {
		t.Fatal("accessibility tree is empty")
	}
}

func TestQueryWithLimit(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	out := callTool[QueryOutput](t, "query", map[string]any{
		"tab":      tabID,
		"selector": ".item",
		"limit":    2,
	})
	if out.Total != 3 {
		t.Errorf("query total = %d, want 3", out.Total)
	}
	if len(out.Elements) != 2 {
		t.Errorf("query elements returned = %d, want 2 (limited)", len(out.Elements))
	}
}

func TestGetHTMLInner(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	out := callTool[GetHTMLOutput](t, "get_html", map[string]any{
		"tab":      tabID,
		"selector": "#title",
		"outer":    false,
	})
	// Inner HTML of <h1 id="title">Hello World</h1> should be just "Hello World".
	if strings.Contains(out.HTML, "<h1") {
		t.Errorf("inner HTML should not contain the h1 tag, got: %s", out.HTML)
	}
	if !strings.Contains(out.HTML, "Hello World") {
		t.Errorf("inner HTML should contain 'Hello World', got: %s", out.HTML)
	}
}

func TestGetHTMLDefaultsToFullPage(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	out := callTool[GetHTMLOutput](t, "get_html", map[string]any{
		"tab": tabID,
	})
	// Without a selector, defaults to "html" element which includes the whole page.
	if !strings.Contains(out.HTML, "<html") {
		t.Error("get_html without selector should return full page HTML")
	}
	if !strings.Contains(out.HTML, "Page Two") {
		t.Error("get_html without selector should contain page content")
	}
}

func TestGetTextDefaultsToBody(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	out := callTool[GetTextOutput](t, "get_text", map[string]any{
		"tab": tabID,
	})
	if !strings.Contains(out.Text, "Page Two") {
		t.Errorf("get_text without selector should return body text, got: %q", out.Text)
	}
}

func TestQueryMissingSelector(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	selectorTimeout(t, tabID, "query",
		nil,                                 // invalid
		map[string]any{"selector": ".item"}, // valid
	)
}

func TestGetHTMLMissingSelector(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	selectorTimeout(t, tabID, "get_html",
		nil,                                  // invalid
		map[string]any{"selector": "#title"}, // valid
	)
}

func TestGetTextMissingSelector(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	selectorTimeout(t, tabID, "get_text",
		nil,                                  // invalid
		map[string]any{"selector": "#title"}, // valid
	)

	// Verify text content returned for valid selector.
	out := callTool[GetTextOutput](t, "get_text", map[string]any{
		"tab":      tabID,
		"selector": "#title",
	})
	if strings.TrimSpace(out.Text) != "Hello World" {
		t.Errorf("get_text valid selector = %q, want 'Hello World'", out.Text)
	}
}

func TestGetAccessibilityTreeMissingSelector(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "get_accessibility_tree", map[string]any{
		"tab":      tabID,
		"selector": "#selector-that-does-not-exist-xyz",
		"timeout":  500,
	})
	if errText == "" {
		t.Error("get_accessibility_tree with missing selector should return an error")
	}
}

func TestQueryDelayedElement(t *testing.T) {
	tabID := navigateToFixture(t, "delayed.html")
	defer closeTab(t, tabID)

	out := callTool[QueryOutput](t, "query", map[string]any{
		"tab":      tabID,
		"selector": "#delayed-element",
	})
	if out.Total != 1 {
		t.Errorf("query delayed element total = %d, want 1", out.Total)
	}
	if len(out.Elements) > 0 && !strings.Contains(out.Elements[0].Text, "appeared") {
		t.Errorf("delayed element text = %q, want to contain 'appeared'", out.Elements[0].Text)
	}
}

func TestQueryComputedStyle(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	out := callTool[QueryOutput](t, "query", map[string]any{
		"tab":            tabID,
		"selector":       "#title",
		"computed_style": []string{"font-family", "display"},
	})
	if len(out.Elements) == 0 {
		t.Fatal("query returned no elements")
	}
	elem := out.Elements[0]
	if len(elem.ComputedStyle) == 0 {
		t.Error("computed_style should have entries")
	}
	if _, ok := elem.ComputedStyle["display"]; !ok {
		t.Error("computed_style should contain 'display'")
	}
}

func TestQueryOuterHTML(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	out := callTool[QueryOutput](t, "query", map[string]any{
		"tab":        tabID,
		"selector":   "#title",
		"outer_html": true,
	})
	if len(out.Elements) == 0 {
		t.Fatal("query returned no elements")
	}
	if !strings.Contains(out.Elements[0].OuterHTML, "<h1") {
		t.Errorf("outer_html = %q, want to contain '<h1'", out.Elements[0].OuterHTML)
	}
	if !strings.Contains(out.Elements[0].OuterHTML, "Hello World") {
		t.Errorf("outer_html = %q, want to contain 'Hello World'", out.Elements[0].OuterHTML)
	}
}

func TestQueryBBox(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	out := callTool[QueryOutput](t, "query", map[string]any{
		"tab":      tabID,
		"selector": "#title",
		"bbox":     true,
	})
	if len(out.Elements) == 0 {
		t.Fatal("query returned no elements")
	}
	if out.Elements[0].BBox == nil {
		t.Fatal("bbox should not be nil")
	}
	if out.Elements[0].BBox.Width <= 0 || out.Elements[0].BBox.Height <= 0 {
		t.Errorf("bbox dimensions should be positive, got %+v", out.Elements[0].BBox)
	}
}

func TestQueryAttributesDisabled(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	out := callTool[QueryOutput](t, "query", map[string]any{
		"tab":        tabID,
		"selector":   "#title",
		"attributes": false,
	})
	if len(out.Elements) == 0 {
		t.Fatal("query returned no elements")
	}
	if len(out.Elements[0].Attributes) > 0 {
		t.Errorf("attributes should be empty when disabled, got %v", out.Elements[0].Attributes)
	}
}

func TestQueryTextDisabled(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	out := callTool[QueryOutput](t, "query", map[string]any{
		"tab":      tabID,
		"selector": "#title",
		"text":     false,
	})
	if len(out.Elements) == 0 {
		t.Fatal("query returned no elements")
	}
	if out.Elements[0].Text != "" {
		t.Errorf("text should be empty when disabled, got %q", out.Elements[0].Text)
	}
}

func TestQueryDefaultLimit(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// .item has 3 elements, so default limit of 10 should return all 3.
	out := callTool[QueryOutput](t, "query", map[string]any{
		"tab":      tabID,
		"selector": ".item",
	})
	if len(out.Elements) != 3 {
		t.Errorf("default limit should return all 3 items, got %d", len(out.Elements))
	}
	if out.Total != 3 {
		t.Errorf("total = %d, want 3", out.Total)
	}
}

func TestQueryComputedStyleValueCorrectness(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	out := callTool[QueryOutput](t, "query", map[string]any{
		"tab":            tabID,
		"selector":       "#title",
		"computed_style": []string{"display"},
	})
	if len(out.Elements) == 0 {
		t.Fatal("query returned no elements")
	}
	val, ok := out.Elements[0].ComputedStyle["display"]
	if !ok {
		t.Fatal("computed_style missing 'display'")
	}
	// h1 is block-level.
	if val != "block" {
		t.Errorf("computed_style['display'] = %q, want 'block'", val)
	}
}

func TestQueryLimitLargerThanTotal(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// .item has 3 elements; limit 100 should return all 3.
	out := callTool[QueryOutput](t, "query", map[string]any{
		"tab":      tabID,
		"selector": ".item",
		"limit":    100,
	})
	if len(out.Elements) != 3 {
		t.Errorf("limit=100 returned %d elements, want 3", len(out.Elements))
	}
	if out.Total != 3 {
		t.Errorf("total = %d, want 3", out.Total)
	}
}

func TestQueryAllOptionalFields(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	out := callTool[QueryOutput](t, "query", map[string]any{
		"tab":            tabID,
		"selector":       "#title",
		"outer_html":     true,
		"bbox":           true,
		"computed_style": []string{"display"},
		// attributes and text default to true
	})
	if len(out.Elements) == 0 {
		t.Fatal("query returned no elements")
	}
	e := out.Elements[0]
	if e.TagName != "h1" {
		t.Errorf("tag = %q, want h1", e.TagName)
	}
	if e.Text == "" {
		t.Error("text should not be empty")
	}
	if !strings.Contains(e.OuterHTML, "<h1") {
		t.Error("outer_html should contain '<h1'")
	}
	if e.BBox == nil {
		t.Error("bbox should not be nil")
	}
	if len(e.ComputedStyle) == 0 {
		t.Error("computed_style should have entries")
	}
	if _, ok := e.Attributes["id"]; !ok {
		t.Error("attributes should contain 'id'")
	}
}

func TestGetTextPermutations(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	tests := []struct {
		name      string
		selector  string
		hidden    bool
		wantErr   bool
		wantEmpty bool
		wantText  string // substring match
	}{
		// Visible element
		{
			name:     "visible/hidden=false",
			selector: "#title",
			hidden:   false,
			wantText: "Hello World",
		},
		{
			name:     "visible/hidden=true",
			selector: "#title",
			hidden:   true,
			wantText: "Hello World",
		},
		// Hidden element (display:none)
		{
			name:      "hidden-element/hidden=false",
			selector:  "#hidden-el",
			hidden:    false,
			wantEmpty: true,
		},
		{
			name:     "hidden-element/hidden=true",
			selector: "#hidden-el",
			hidden:   true,
			wantText: "Hidden content",
		},
		// First match is hidden, second is visible (Wikipedia scenario)
		{
			name:      "first-match-hidden/hidden=false",
			selector:  ".mv-item",
			hidden:    false,
			wantEmpty: true,
		},
		{
			name:     "first-match-hidden/hidden=true",
			selector: ".mv-item",
			hidden:   true,
			wantText: "Hidden paragraph",
		},
		// Non-existing element — both hidden modes should timeout
		{
			name:     "non-existing/hidden=false",
			selector: "#does-not-exist",
			hidden:   false,
			wantErr:  true,
		},
		{
			name:     "non-existing/hidden=true",
			selector: "#does-not-exist",
			hidden:   true,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := map[string]any{
				"tab":      tabID,
				"selector": tt.selector,
			}
			if tt.hidden {
				args["hidden"] = true
			}
			if tt.wantErr {
				args["timeout"] = 500
				errText := callToolExpectErr(t, "get_text", args)
				if errText == "" {
					t.Error("expected error for non-existing element, got none")
				}
				return
			}
			out := callTool[GetTextOutput](t, "get_text", args)
			if tt.wantEmpty {
				if out.Text != "" {
					t.Errorf("got %q, want empty string", out.Text)
				}
				return
			}
			if !strings.Contains(out.Text, tt.wantText) {
				t.Errorf("got %q, want substring %q", out.Text, tt.wantText)
			}
		})
	}
}

func TestGetHTMLOuterExplicit(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	out := callTool[GetHTMLOutput](t, "get_html", map[string]any{
		"tab":      tabID,
		"selector": "#title",
		"outer":    true,
	})
	if !strings.Contains(out.HTML, "<h1") {
		t.Errorf("outer=true HTML should contain '<h1', got: %s", out.HTML)
	}
	if !strings.Contains(out.HTML, "Hello World") {
		t.Errorf("outer=true HTML should contain 'Hello World', got: %s", out.HTML)
	}
}

func TestAccessibilityTreeStructure(t *testing.T) {
	tabID := navigateToFixture(t, "accessibility.html")
	defer closeTab(t, tabID)

	out := callTool[GetAccessibilityTreeOutput](t, "get_accessibility_tree", map[string]any{
		"tab": tabID,
	})
	if len(out.Tree) == 0 {
		t.Fatal("accessibility tree is empty")
	}

	// Verify the default-depth tree includes semantic roles from descendants,
	// not just a depth-0 root node.
	var hasSemanticRole func([]AXNodeOutput) bool
	hasSemanticRole = func(nodes []AXNodeOutput) bool {
		for _, n := range nodes {
			switch n.Role {
			case "button", "heading", "navigation", "link":
				return true
			}
			if hasSemanticRole(n.Children) {
				return true
			}
		}
		return false
	}
	if !hasSemanticRole(out.Tree) {
		b, _ := json.Marshal(out.Tree)
		t.Errorf("accessibility tree doesn't contain expected descendant roles: %s", b)
	}
}

func TestGetTextNestedChildren(t *testing.T) {
	tabID := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tabID)

	out := callTool[GetTextOutput](t, "get_text", map[string]any{
		"tab":      tabID,
		"selector": "#nested-text",
	})
	// Should contain text from all child elements.
	if !strings.Contains(out.Text, "Hello") {
		t.Errorf("nested text missing 'Hello': %q", out.Text)
	}
	if !strings.Contains(out.Text, "bold") {
		t.Errorf("nested text missing 'bold': %q", out.Text)
	}
	if !strings.Contains(out.Text, "italic") {
		t.Errorf("nested text missing 'italic': %q", out.Text)
	}
}

func TestGetTextEmptyElement(t *testing.T) {
	tabID := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tabID)

	out := callTool[GetTextOutput](t, "get_text", map[string]any{
		"tab":      tabID,
		"selector": "#empty-el",
	})
	if out.Text != "" {
		t.Errorf("empty element text = %q, want empty string", out.Text)
	}
}

func TestQueryLimitZeroFallsToDefault(t *testing.T) {
	tabID := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tabID)

	// .query-item has 12 elements. limit=0 should default to 10.
	out := callTool[QueryOutput](t, "query", map[string]any{
		"tab":      tabID,
		"selector": ".query-item",
		"limit":    0,
	})
	if out.Total != 12 {
		t.Errorf("total = %d, want 12", out.Total)
	}
	if len(out.Elements) != 10 {
		t.Errorf("limit=0 returned %d elements, want 10 (default)", len(out.Elements))
	}
}

func TestQueryEmptyComputedStyle(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	out := callTool[QueryOutput](t, "query", map[string]any{
		"tab":            tabID,
		"selector":       "#title",
		"computed_style": []string{},
	})
	if len(out.Elements) == 0 {
		t.Fatal("query returned no elements")
	}
	if len(out.Elements[0].ComputedStyle) != 0 {
		t.Errorf("empty computed_style array should return no styles, got %d", len(out.Elements[0].ComputedStyle))
	}
}

func TestAccessibilityTreeScopedSelector(t *testing.T) {
	tabID := navigateToFixture(t, "accessibility.html")
	defer closeTab(t, tabID)

	full := callTool[GetAccessibilityTreeOutput](t, "get_accessibility_tree", map[string]any{
		"tab": tabID,
	})
	scoped := callTool[GetAccessibilityTreeOutput](t, "get_accessibility_tree", map[string]any{
		"tab":      tabID,
		"selector": "nav",
	})
	if len(scoped.Tree) == 0 {
		t.Fatal("scoped accessibility tree is empty")
	}
	if got, wantMax := countAXTreeNodes(scoped.Tree), countAXTreeNodes(full.Tree); got >= wantMax {
		t.Errorf("scoped nav tree (%d nodes) should be smaller than full tree (%d nodes)", got, wantMax)
	}
}

func TestAccessibilityTreeDepth(t *testing.T) {
	tabID := navigateToFixture(t, "accessibility.html")
	defer closeTab(t, tabID)

	full := callTool[GetAccessibilityTreeOutput](t, "get_accessibility_tree", map[string]any{
		"tab": tabID,
	})
	fullCount := countAXTreeNodes(full.Tree)
	if fullCount == 0 {
		t.Fatal("full accessibility tree is empty")
	}

	for _, depth := range []int{1, 2} {
		out := callTool[GetAccessibilityTreeOutput](t, "get_accessibility_tree", map[string]any{
			"tab":   tabID,
			"depth": depth,
		})
		if len(out.Tree) == 0 {
			t.Fatalf("depth=%d accessibility tree is empty", depth)
		}
		if depth == 1 {
			if got := countAXTreeNodes(out.Tree); got >= fullCount {
				t.Errorf("depth=1 tree (%d nodes) should be smaller than full tree (%d nodes)", got, fullCount)
			}
		}
	}
}

func TestGetHTMLInnerNested(t *testing.T) {
	tabID := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tabID)

	out := callTool[GetHTMLOutput](t, "get_html", map[string]any{
		"tab":      tabID,
		"selector": "#nested-text",
		"outer":    false,
	})
	// Inner HTML should contain child tags but NOT the wrapping div.
	if strings.Contains(out.HTML, "<div id=\"nested-text\"") {
		t.Error("inner HTML should not contain the wrapping div tag")
	}
	if !strings.Contains(out.HTML, "<span>") {
		t.Errorf("inner HTML should contain child <span>, got: %s", out.HTML)
	}
	if !strings.Contains(out.HTML, "<strong>") {
		t.Errorf("inner HTML should contain child <strong>, got: %s", out.HTML)
	}
}

func TestQuerySubQueryPartialData(t *testing.T) {
	tabID := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tabID)

	// Query with all optional fields enabled. Verify all are populated
	// for a normal visible element.
	out := callTool[QueryOutput](t, "query", map[string]any{
		"tab":            tabID,
		"selector":       "#title",
		"limit":          1,
		"outer_html":     true,
		"computed_style": []string{"font-size", "color"},
		"bbox":           true,
	})
	if len(out.Elements) == 0 {
		t.Fatal("query returned 0 elements")
	}
	elem := out.Elements[0]

	// All fields should be populated for a visible element.
	if elem.Text == "" {
		t.Error("text should be populated for a visible element")
	}
	if elem.OuterHTML == "" {
		t.Error("outer_html should be populated")
	}
	if len(elem.ComputedStyle) == 0 {
		t.Error("computed_style should have entries")
	}
	if elem.BBox == nil {
		t.Error("bbox should be populated for a visible element")
	}

	// Now query a display:none element. BBox may be zero but shouldn't error.
	// Use JS to add a hidden element.
	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `document.body.insertAdjacentHTML('beforeend', '<div id="hidden-query-test" style="display:none">Hidden</div>')`,
	})

	out2 := callTool[QueryOutput](t, "query", map[string]any{
		"tab":        tabID,
		"selector":   "#hidden-query-test",
		"limit":      1,
		"outer_html": true,
		"bbox":       true,
	})
	if len(out2.Elements) == 0 {
		t.Fatal("query returned 0 elements for hidden element")
	}
	// The tool should NOT error — it silently handles sub-query failures.
	// BBox for display:none is typically nil (Dimensions returns error).
	// This is the expected behaviour: partial data, no error.
	elem2 := out2.Elements[0]
	if elem2.OuterHTML == "" {
		t.Error("outer_html should still work for display:none elements")
	}
	// BBox might be nil for display:none — that's fine, the point is no error.
	t.Logf("hidden element bbox: %v (nil is acceptable)", elem2.BBox)
}

// TestAccessibilityTreeInterestingOnlyFiltering verifies that
// interestingOnly=true produces a smaller, non-empty tree than interestingOnly=false.
func TestAccessibilityTreeInterestingOnlyFiltering(t *testing.T) {
	tabID := navigateToFixture(t, "accessibility.html")
	defer closeTab(t, tabID)

	interesting := callTool[GetAccessibilityTreeOutput](t, "get_accessibility_tree", map[string]any{
		"tab": tabID,
	})
	if len(interesting.Tree) == 0 {
		t.Fatal("interesting_only=true returned an empty tree")
	}

	all := callTool[GetAccessibilityTreeOutput](t, "get_accessibility_tree", map[string]any{
		"tab":              tabID,
		"interesting_only": false,
	})
	if len(all.Tree) == 0 {
		t.Fatal("interesting_only=false returned an empty tree")
	}

	interestingNodes := countAXTreeNodes(interesting.Tree)
	allNodes := countAXTreeNodes(all.Tree)
	if allNodes <= interestingNodes {
		t.Errorf("expected full tree (%d nodes) to have more nodes than interesting-only tree (%d nodes)",
			allNodes, interestingNodes)
	}
}

// TestGetTextHiddenTrue verifies that hidden=true uses textContent
// (includes hidden elements) when targeting the default page body.
func TestGetTextHiddenTrue(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	visible := callTool[GetTextOutput](t, "get_text", map[string]any{
		"tab":    tabID,
		"hidden": false,
	})
	if !strings.Contains(visible.Text, "Hello World") {
		t.Errorf("get_text with hidden=false should include visible text 'Hello World', got: %q", visible.Text)
	}
	if strings.Contains(visible.Text, "Hidden content") {
		t.Errorf("get_text with hidden=false unexpectedly included hidden text: %q", visible.Text)
	}

	// hidden=true should include text from display:none elements.
	out := callTool[GetTextOutput](t, "get_text", map[string]any{
		"tab":    tabID,
		"hidden": true,
	})
	if !strings.Contains(out.Text, "Hidden content") {
		t.Errorf("get_text with hidden=true should include 'Hidden content', got: %q", out.Text)
	}
}
