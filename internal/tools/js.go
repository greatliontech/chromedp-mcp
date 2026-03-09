package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/thegrumpylion/chromedp-mcp/internal/browser"
)

// evalAwaitPromise is a chromedp.EvaluateOption that tells the CDP
// Runtime.evaluate call to await the resulting Promise before returning.
func evalAwaitPromise(p *runtime.EvaluateParams) *runtime.EvaluateParams {
	return p.WithAwaitPromise(true)
}

// EvaluateInput is the input for evaluate.
type EvaluateInput struct {
	SelectorInput
	Expression   string `json:"expression" jsonschema:"JavaScript expression to evaluate. When a selector is provided, the matched element is available as 'el'. Use 'return' to produce a value (e.g. 'return el.textContent')."`
	Selector     string `json:"selector,omitempty" jsonschema:"CSS selector. If provided, the first matched element is available as 'el' in the expression."`
	AwaitPromise *bool  `json:"await_promise,omitempty" jsonschema:"Wait for Promise to resolve (default true)"`
}

// EvaluateOutput is the output for evaluate.
type EvaluateOutput struct {
	Result json.RawMessage `json:"result"`
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

		tctx := t.Context()

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
				if (!el) throw new Error('selector %s matched no elements');
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
			return nil, EvaluateOutput{Result: data}, nil
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
		return nil, EvaluateOutput{Result: data}, nil
	})
}
