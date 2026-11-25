package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestJSQuoteEdgeCases(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	inputs := []string{
		`simple`,
		`quote " and ' and \ backslash`,
		"newline\nand\rcarriage\tand\x00nul\x07bell\x0bvtab",
		"emoji 🚀 and accented é",
		"line\u2028sep and para\u2029sep",
	}

	for _, s := range inputs {
		q := jsQuote(s)
		if strings.ContainsRune(q, '\u2028') || strings.ContainsRune(q, '\u2029') {
			t.Errorf("jsQuote(%q) did not escape line/paragraph separator: %s", s, q)
		}
		out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
			"tab":        tabID,
			"expression": q,
		})
		var got string
		if err := json.Unmarshal(out.Result, &got); err != nil {
			t.Fatalf("unmarshal result for %q: %v (raw=%s)", s, err, out.Result)
		}
		if got != s {
			t.Errorf("V8 round-trip for %q: got %q", s, got)
		}
	}
}

func TestEvaluateSelectorTrailingLineCommentAndValidation(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"selector":   "#title",
		"expression": "return el.textContent // trailing line comment must not break IIFE",
	})
	var got string
	if err := json.Unmarshal(out.Result, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got != "Hello World" {
		t.Errorf("result = %q, want %q", got, "Hello World")
	}

	errText := callToolExpectErr(t, "evaluate", map[string]any{
		"tab":              tabID,
		"expression":       "1+1",
		"max_result_bytes": -1,
	})
	if !strings.Contains(errText, "max_result_bytes") {
		t.Errorf("error = %q, want mention of max_result_bytes", errText)
	}
}

func TestEvaluateAndAccessibilityTreeEmitOutputSchema(t *testing.T) {
	res, err := harness.session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	found := map[string]bool{}
	for _, tool := range res.Tools {
		if tool.Name == "evaluate" || tool.Name == "get_accessibility_tree" {
			found[tool.Name] = true
			if tool.OutputSchema == nil {
				t.Errorf("tool %q has nil OutputSchema", tool.Name)
			}
		}
	}
	if !found["evaluate"] || !found["get_accessibility_tree"] {
		t.Fatalf("missing expected tools in ListTools: %v", found)
	}
}
