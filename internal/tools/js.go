package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/chromedp/chromedp"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/browser"
	"github.com/greatliontech/chromedp-mcp/internal/collector"
)

// EvaluateInput is the input for evaluate.
type EvaluateInput struct {
	SelectorInput
	Frame          string `json:"frame,omitempty" jsonschema:"Optional frame ID from list_frames to target a same-origin iframe instead of the main frame."`
	Expression     string `json:"expression" jsonschema:"JavaScript expression to evaluate. Without a selector, evaluated directly as an expression (do not use top-level 'return'). When a selector is provided, runs as a function body with the matched element as 'el' — use 'return' to produce a value (e.g. 'return el.textContent')."`
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
		Name:         "evaluate",
		Description:  "Execute JavaScript in the page context and return the result. Without a selector, evaluates an expression directly. When a selector is provided, runs inside a function body with the first matched element as 'el' (use 'return' to produce a value, e.g. 'return el.textContent').",
		OutputSchema: evaluateOutputSchema(),
		Annotations:  mutatingAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input EvaluateInput) (*mcp.CallToolResult, EvaluateOutput, error) {
		if err := validateSelectorTimeout(input.Timeout); err != nil {
			return nil, EvaluateOutput{}, err
		}
		if input.MaxResultBytes < 0 {
			return nil, EvaluateOutput{}, fmt.Errorf("max_result_bytes must be >= 0, got %d", input.MaxResultBytes)
		}
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, EvaluateOutput{}, err
		}
		uniqueCtxID, err := resolveFrameContext(t, input.Frame, input.Selector != "")
		if err != nil {
			return nil, EvaluateOutput{}, err
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()

		expr := input.Expression
		if input.Selector != "" {
			// Wait for the element using selectorContext, then execute
			// the expression with the element as the first argument.
			sctx, cancel := selectorContext(tctx, input.Timeout)
			uniqueCtxID, err = waitForSelector(sctx, tctx, t, uniqueCtxID, input.Frame, input.Selector)
			cancel()
			if err != nil {
				return nil, EvaluateOutput{}, err
			}

			// Newline before closing inner function prevents a trailing '//'
			// comment in input.Expression from commenting out '})(el);'.
			expr = frameElementJS(input.Selector, fmt.Sprintf("return (function(el) {\n%s\n})(el);", input.Expression))
		}

		var extraOpts []chromedp.EvaluateOption
		if input.AwaitPromise == nil || *input.AwaitPromise {
			extraOpts = append(extraOpts, chromedp.EvalAwaitPromise)
		}
		evalOpts := evalOptsForFrame(uniqueCtxID, extraOpts...)

		result, err := chromedp.Run(tctx, chromedp.Evaluate[any](expr, evalOpts...))
		if err != nil {
			return nil, EvaluateOutput{}, err
		}

		data, err := json.Marshal(result)
		if err != nil {
			return nil, EvaluateOutput{}, err
		}
		return nil, capEvaluateResult(data, input.MaxResultBytes), nil
	})
}

// capEvaluateResult applies the max_result_bytes cap to the serialized
// evaluate result. When the cap fires the original Result (a JSON value
// of any type — array, object, string, etc.) is replaced with a JSON
// string containing the truncated prefix, so consumers always receive
// valid JSON. Truncated/TotalBytes carry the cut details.
func capEvaluateResult(data []byte, maxBytes int) EvaluateOutput {
	if maxBytes <= 0 || len(data) <= maxBytes {
		return EvaluateOutput{Result: data}
	}
	prefix, _ := collector.TruncateUTF8Bytes(data, maxBytes)
	wrapped, _ := json.Marshal(string(prefix))
	return EvaluateOutput{
		Result:     wrapped,
		Truncated:  true,
		TotalBytes: len(data),
	}
}

// evaluateOutputSchema returns an explicit OutputSchema for EvaluateOutput
// because Result is json.RawMessage ([]byte), which jsonschema.For would
// misinfer as a JSON array of integers rather than an arbitrary JSON value.
func evaluateOutputSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"result": {
				Description: "JSON-encoded value returned by the expression (any JSON type). When truncated, a JSON string containing the truncated prefix.",
			},
			"truncated": {
				Type:        "boolean",
				Description: "True when the serialized result exceeded max_result_bytes and was cut.",
			},
			"total_bytes": {
				Type:        "integer",
				Description: "Size in bytes of the full JSON serialization before truncation (present only when truncated).",
			},
		},
		Required: []string{"result"},
	}
}
