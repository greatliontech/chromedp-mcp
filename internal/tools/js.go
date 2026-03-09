package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/chromedp/chromedp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/thegrumpylion/chromedp-mcp/internal/browser"
)

// EvaluateInput is the input for evaluate.
type EvaluateInput struct {
	TabInput
	Expression   string `json:"expression" jsonschema:"JavaScript expression to evaluate"`
	AwaitPromise *bool  `json:"await_promise,omitempty" jsonschema:"Wait for Promise to resolve (default true)"`
}

// EvaluateOutput is the output for evaluate.
type EvaluateOutput struct {
	Result json.RawMessage `json:"result"`
}

// EvaluateOnSelectorInput is the input for evaluate_on_selector.
type EvaluateOnSelectorInput struct {
	TabInput
	Selector   string `json:"selector" jsonschema:"CSS selector"`
	Expression string `json:"expression" jsonschema:"JavaScript function body. The matched element is passed as the first argument."`
}

// EvaluateOnSelectorOutput is the output for evaluate_on_selector.
type EvaluateOnSelectorOutput struct {
	Result json.RawMessage `json:"result"`
}

func registerJSTools(s *mcp.Server, mgr *browser.Manager) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "evaluate",
		Description: "Execute JavaScript in the page context and return the result.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input EvaluateInput) (*mcp.CallToolResult, any, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, nil, err
		}

		var result interface{}
		evalOpts := []chromedp.EvaluateOption{}
		if input.AwaitPromise == nil || *input.AwaitPromise {
			evalOpts = append(evalOpts, chromedp.EvalAsValue)
		}

		if err := chromedp.Run(t.Context(), chromedp.Evaluate(input.Expression, &result, evalOpts...)); err != nil {
			return nil, nil, err
		}

		data, err := json.Marshal(result)
		if err != nil {
			return nil, nil, err
		}
		return nil, EvaluateOutput{Result: data}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "evaluate_on_selector",
		Description: "Execute JavaScript with the first element matching a selector as the first argument.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input EvaluateOnSelectorInput) (*mcp.CallToolResult, any, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, nil, err
		}

		// Wrap the expression in an IIFE that queries the selector and
		// passes the element as the first argument.
		js := fmt.Sprintf(`(function() {
			var el = document.querySelector(%q);
			if (!el) throw new Error('selector %s matched no elements');
			return (function(el) { %s })(el);
		})()`, input.Selector, input.Selector, input.Expression)

		var result interface{}
		if err := chromedp.Run(t.Context(), chromedp.Evaluate(js, &result, chromedp.EvalAsValue)); err != nil {
			return nil, nil, err
		}

		data, err := json.Marshal(result)
		if err != nil {
			return nil, nil, err
		}
		return nil, EvaluateOnSelectorOutput{Result: data}, nil
	})
}
