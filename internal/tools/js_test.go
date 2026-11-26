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

func TestEvaluateAndSnapshotEmitOutputSchema(t *testing.T) {
	res, err := harness.session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	found := map[string]bool{}
	for _, tool := range res.Tools {
		if tool.Name == "evaluate" || tool.Name == "snapshot" || tool.Name == "get_dom" {
			found[tool.Name] = true
			if tool.OutputSchema == nil {
				t.Errorf("tool %q has nil OutputSchema", tool.Name)
			}
		}
	}
	if !found["evaluate"] || !found["snapshot"] || !found["get_dom"] {
		t.Fatalf("missing expected tools in ListTools: %v", found)
	}
}

func TestEvaluate(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "1 + 2",
	})
	if string(out.Result) != "3" {
		t.Errorf("evaluate result = %s, want 3", out.Result)
	}
}

func TestEvaluateOnSelector(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"selector":   "#title",
		"expression": "return el.textContent",
	})
	if string(out.Result) != `"Hello World"` {
		t.Errorf("evaluate with selector result = %s, want \"Hello World\"", out.Result)
	}
}

func TestEvaluateJSError(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "throw new Error('test error')",
	})
	if !strings.Contains(errText, "test error") {
		t.Errorf("evaluate JS error = %q, want to contain 'test error'", errText)
	}
}

func TestEvaluateOnSelectorNoMatch(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "evaluate", map[string]any{
		"tab":        tabID,
		"selector":   "#nonexistent",
		"expression": "return el.textContent",
	})
	if !strings.Contains(errText, "not found") && !strings.Contains(errText, "matched no elements") {
		t.Errorf("evaluate with selector error = %q, want 'not found' or 'matched no elements'", errText)
	}
}

func TestEvaluateReturnsVariousTypes(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	tests := []struct {
		expr string
		want string
	}{
		{"42", "42"},
		{`"hello"`, `"hello"`},
		{"true", "true"},
		{"false", "false"},
		{"null", "null"},
		{"[1,2,3]", "[1,2,3]"},
		{`({a: 1})`, `{"a":1}`},
	}
	for _, tc := range tests {
		out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
			"tab":        tabID,
			"expression": tc.expr,
		})
		got := string(out.Result)
		if got != tc.want {
			t.Errorf("evaluate(%s) = %s, want %s", tc.expr, got, tc.want)
		}
	}
}

func TestEvaluateAwaitPromise(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "new Promise(resolve => setTimeout(() => resolve('resolved!'), 50))",
	})
	if !strings.Contains(string(out.Result), "resolved!") {
		t.Errorf("promise result = %s, want 'resolved!'", out.Result)
	}
}

func TestEvaluateAwaitPromiseFalse(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":           tabID,
		"expression":    "new Promise(resolve => resolve(42))",
		"await_promise": false,
	})
	// When not awaiting, should return an object descriptor, not the resolved value.
	if string(out.Result) == "42" {
		t.Error("await_promise=false should not resolve the promise to its value")
	}
}

func TestEvaluateUndefined(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "undefined",
	})
	if string(out.Result) != "null" {
		t.Errorf("undefined result = %s, want null (JSON serialization of undefined)", out.Result)
	}
}

func TestEvaluateOnSelectorSideEffect(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// Modify the element via the expression.
	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"selector":   "#title",
		"expression": "el.textContent = 'Modified'; return el.textContent;",
	})

	// Verify the modification.
	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('title').textContent",
	})
	if !strings.Contains(string(out.Result), "Modified") {
		t.Errorf("after evaluate with selector side effect, text = %s, want 'Modified'", out.Result)
	}
}

func TestEvaluateOnSelectorReturnTypes(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	tests := []struct {
		expr string
		want string
	}{
		{"return el.tagName.toLowerCase();", `"h1"`},
		{"return el.children.length;", "0"},
		{"return el.id === 'title';", "true"},
	}
	for _, tc := range tests {
		out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
			"tab":        tabID,
			"selector":   "#title",
			"expression": tc.expr,
		})
		if string(out.Result) != tc.want {
			t.Errorf("evaluate with selector(%s) = %s, want %s", tc.expr, out.Result, tc.want)
		}
	}
}

func TestEvaluateSyntaxError(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "if ({",
	})
	if errText == "" {
		t.Error("syntax error should return an error")
	}
}

func TestEvaluatePromiseRejection(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "evaluate", map[string]any{
		"tab":           tabID,
		"expression":    "Promise.reject(new Error('test rejection'))",
		"await_promise": true,
	})
	if !strings.Contains(errText, "test rejection") {
		t.Errorf("promise rejection error = %q, want to contain 'test rejection'", errText)
	}
}

func TestEvaluateOnSelectorExpressionThrows(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "evaluate", map[string]any{
		"tab":        tabID,
		"selector":   "#title",
		"expression": "throw new Error('element expression error')",
	})
	if !strings.Contains(errText, "element expression error") {
		t.Errorf("error = %q, want to contain 'element expression error'", errText)
	}
}

func TestEvaluateNestedObject(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `({a: {b: {c: [1,2,3]}}})`,
	})
	var parsed map[string]any
	if err := json.Unmarshal(out.Result, &parsed); err != nil {
		t.Fatalf("failed to parse nested result: %v (raw: %s)", err, out.Result)
	}
	a, ok := parsed["a"].(map[string]any)
	if !ok {
		t.Fatalf("missing or wrong type for 'a': %v", parsed)
	}
	b, ok := a["b"].(map[string]any)
	if !ok {
		t.Fatalf("missing or wrong type for 'b': %v", a)
	}
	c, ok := b["c"].([]any)
	if !ok || len(c) != 3 {
		t.Errorf("nested c = %v, want [1,2,3]", b["c"])
	}
}

func TestEvaluateOnSelectorElementProperties(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// Test classList.
	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"selector":   ".content",
		"expression": "return Array.from(el.classList);",
	})
	if !strings.Contains(string(out.Result), "content") {
		t.Errorf("classList = %s, want to contain 'content'", out.Result)
	}

	// Test getAttribute.
	out = callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"selector":   "#title",
		"expression": "return el.getAttribute('id');",
	})
	if string(out.Result) != `"title"` {
		t.Errorf("getAttribute('id') = %s, want \"title\"", out.Result)
	}
}

func TestEvaluateOnSelectorNoReturn(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"selector":   "#title",
		"expression": "el.textContent;", // No return statement.
	})
	// Without return, the IIFE returns undefined.
	if string(out.Result) != "null" {
		t.Errorf("no-return expression result = %s, want null (undefined)", out.Result)
	}
}

func TestEvaluateOnSelectorFirstMatch(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// .content matches two <p> elements. Should use the first one.
	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"selector":   ".content",
		"expression": "return el.textContent;",
	})
	if !strings.Contains(string(out.Result), "test paragraph") {
		t.Errorf("first match text = %s, want 'This is a test paragraph.'", out.Result)
	}
}

func TestEvaluateAwaitPromiseDefault(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	// Omit await_promise — default is true, so the Promise should be awaited.
	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "new Promise(function(resolve) { setTimeout(function() { resolve(99); }, 50); })",
	})
	if string(out.Result) != "99" {
		t.Errorf("default await_promise result = %s, want 99", out.Result)
	}
}

func TestEvaluateSelectorWithSpecialChars(t *testing.T) {
	tabID := navigateToFixture(t, "special-selectors.html")
	defer closeTab(t, tabID)

	// Test with a selector containing quotes so JS string escaping is exercised.
	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"selector":   "[data-value=\"test's\"]",
		"expression": "return el.textContent;",
	})
	if !strings.Contains(string(out.Result), "apostrophe") {
		t.Errorf("selector with apostrophe: result = %s, want to contain 'apostrophe'", out.Result)
	}
}

func TestEvaluateSelectorNotFoundMessage(t *testing.T) {
	tabID := navigateToFixture(t, "special-selectors.html")
	defer closeTab(t, tabID)

	// A selector that doesn't match should produce a clear error message
	// mentioning the selector.
	errText := callToolExpectErr(t, "evaluate", map[string]any{
		"tab":        tabID,
		"selector":   "#does-not-exist",
		"expression": "return el.textContent;",
	})
	if errText == "" {
		t.Error("expected error for non-existent selector")
	}
}

// TestEvaluateWithSelector verifies evaluating an expression scoped to a selector.
func TestEvaluateWithSelector(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	type evalResult struct {
		Result string `json:"result"`
	}
	result := callTool[evalResult](t, "evaluate", map[string]any{
		"tab":        tabID,
		"selector":   "h1",
		"expression": "return el.textContent",
	})
	if result.Result != "Hello World" {
		t.Fatalf("expected 'Hello World', got %q", result.Result)
	}
}

// TestEvaluateWithSelectorNotFound verifies error on missing selector.
func TestEvaluateWithSelectorNotFound(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "evaluate", map[string]any{
		"tab":        tabID,
		"selector":   "#nonexistent-xyz",
		"expression": "return el.textContent",
		"timeout":    500,
	})
	if !strings.Contains(errText, "not found") {
		t.Fatalf("expected 'not found' error, got: %s", errText)
	}
}

// TestEvaluateWithoutSelector verifies plain evaluate works without a selector.
func TestEvaluateWithoutSelector(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	type evalResult struct {
		Result float64 `json:"result"`
	}
	result := callTool[evalResult](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "2 + 2",
	})
	if result.Result != 4 {
		t.Fatalf("expected 4, got %v", result.Result)
	}
}
