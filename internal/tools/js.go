package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/browser"
)

// evalAwaitPromise is a chromedp.EvaluateOption that tells the CDP
// Runtime.evaluate call to await the resulting Promise before returning.
func evalAwaitPromise(p *runtime.EvaluateParams) *runtime.EvaluateParams {
	return p.WithAwaitPromise(true)
}

// EvaluateInput is the input for evaluate.
type EvaluateInput struct {
	SelectorInput
	Expression     string `json:"expression" jsonschema:"JavaScript expression to evaluate. When a selector is provided, the matched element is available as 'el'. Use 'return' to produce a value (e.g. 'return el.textContent')."`
	Selector       string `json:"selector,omitempty" jsonschema:"CSS selector. If provided, the first matched element is available as 'el' in the expression."`
	AwaitPromise   *bool  `json:"await_promise,omitempty" jsonschema:"Wait for Promise to resolve (default true)"`
	MaxResultBytes int    `json:"max_result_bytes,omitempty" jsonschema:"If > 0, cap the serialized result at this many bytes. When the JSON encoding exceeds the cap, 'result' is replaced with the truncated prefix as a JSON string and 'truncated' / 'total_bytes' report the cut. Default 0 (no cap)."`
}

// EvaluateOutput is the output for evaluate.
type EvaluateOutput struct {
	// Result holds the JSON-encoded value returned by the expression.
	// When the response was truncated by max_result_bytes, this is a
	// JSON string containing the truncated prefix (not the original
	// JSON value), and Truncated/TotalBytes describe the cut.
	Result json.RawMessage `json:"result"`
	// Truncated is true when the serialized result was longer than the
	// caller's max_result_bytes and was cut.
	Truncated bool `json:"truncated,omitempty"`
	// TotalBytes is the size of the full JSON serialization before any
	// truncation. Set only when Truncated is true.
	TotalBytes int `json:"total_bytes,omitempty"`
}

func registerJSTools(s *mcp.Server, mgr *browser.Manager) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "evaluate",
		Description: "Execute JavaScript in the page context and return the result. If a selector is provided, the first matched element is available as 'el'. Use 'return' to produce a value (e.g. 'return el.textContent').",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input EvaluateInput) (*mcp.CallToolResult, any, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, nil, err
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()

		if input.Selector != "" {
			// Wait for the element using selectorContext, then execute
			// the expression with the element as the first argument.
			sctx, cancel := selectorContext(tctx, input.Timeout)
			defer cancel()

			// Wait for selector to appear in the DOM.
			if err := chromedp.Run(sctx, chromedp.WaitReady(input.Selector, chromedp.ByQuery)); err != nil {
				return nil, nil, selectorError(tctx, input.Selector, err)
			}

			// Wrap the expression in an IIFE that queries the selector and
			// passes the element as the first argument.
			js := fmt.Sprintf(`(function() {
			var el = document.querySelector(%q);
			if (!el) throw new Error('selector ' + %q + ' matched no elements');
			return (function(el) { %s })(el);
		})()`, input.Selector, input.Selector, input.Expression)

			var result interface{}
			evalOpts := []chromedp.EvaluateOption{chromedp.EvalAsValue}
			if input.AwaitPromise == nil || *input.AwaitPromise {
				evalOpts = append(evalOpts, evalAwaitPromise)
			}
			if err := chromedp.Run(tctx, chromedp.Evaluate(js, &result, evalOpts...)); err != nil {
				return nil, nil, err
			}

			data, err := json.Marshal(result)
			if err != nil {
				return nil, nil, err
			}
			return nil, capEvaluateResult(data, input.MaxResultBytes), nil
		}

		// No selector — evaluate the expression directly.
		var result interface{}
		evalOpts := []chromedp.EvaluateOption{chromedp.EvalAsValue}
		if input.AwaitPromise == nil || *input.AwaitPromise {
			evalOpts = append(evalOpts, evalAwaitPromise)
		}

		if err := chromedp.Run(tctx, chromedp.Evaluate(input.Expression, &result, evalOpts...)); err != nil {
			return nil, nil, err
		}

		data, err := json.Marshal(result)
		if err != nil {
			return nil, nil, err
		}
		return nil, capEvaluateResult(data, input.MaxResultBytes), nil
	})
}

// capEvaluateResult applies the max_result_bytes cap to the serialized
// evaluate result. When the cap fires the original Result (a JSON value
// of any type — array, object, string, etc.) is replaced with a JSON
// string containing the truncated prefix, so consumers always receive
// valid JSON. Truncated/TotalBytes carry the cut details.
//
// The byte cap may land mid-codepoint of a UTF-8 string. Without
// trimming, json.Marshal silently replaces invalid UTF-8 with U+FFFD —
// the LLM gets a corrupted prefix with no signal. Trim trailing bytes
// that fall inside a partial rune (max 3 iterations since UTF-8
// codepoints are at most 4 bytes).
func capEvaluateResult(data []byte, maxBytes int) EvaluateOutput {
	if maxBytes <= 0 || len(data) <= maxBytes {
		return EvaluateOutput{Result: data}
	}
	prefix := data[:maxBytes]
	for len(prefix) > 0 && !utf8.Valid(prefix) {
		prefix = prefix[:len(prefix)-1]
	}
	wrapped, _ := json.Marshal(string(prefix))
	return EvaluateOutput{
		Result:     wrapped,
		Truncated:  true,
		TotalBytes: len(data),
	}
}
