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
	Ref            string `json:"ref,omitempty" jsonschema:"Element ref from snapshot (e.g. 'e4'). Available as 'el' in expression. Mutually exclusive with selector and frame."`
	Frame          string `json:"frame,omitempty" jsonschema:"Optional same-origin frame ID from list_frames."`
	Expression     string `json:"expression" jsonschema:"JavaScript expression. Without ref/selector, evaluated as an expression (no top-level 'return'). With ref or selector, runs as a function body with the matched element as 'el' — use 'return' for a value."`
	Selector       string `json:"selector,omitempty" jsonschema:"CSS selector (supports '>>>' for Shadow DOM). Matched element is available as 'el'."`
	AwaitPromise   *bool  `json:"await_promise,omitempty" jsonschema:"Wait for Promise to resolve (default true)."`
	MaxResultBytes int    `json:"max_result_bytes,omitempty" jsonschema:"If > 0, cap serialized result at this many bytes (default 0 = no cap)."`
}

// EvaluateOutput is the output for evaluate.
type EvaluateOutput struct {
	Result     json.RawMessage `json:"result"`
	Truncated  bool            `json:"truncated,omitempty"`
	TotalBytes int             `json:"total_bytes,omitempty"`
}

func registerJSTools(s *mcp.Server, mgr *browser.Manager) {
	mcp.AddTool(s, &mcp.Tool{
		Name:         "evaluate",
		Description:  "Execute JavaScript in the page (or a frame) and return the result. Without ref/selector, evaluates an expression directly. With ref or selector, runs as a function body with the matched element as 'el' (use 'return' to produce a value).",
		OutputSchema: evaluateOutputSchema(),
		Annotations:  mutatingAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input EvaluateInput) (*mcp.CallToolResult, EvaluateOutput, error) {
		if err := validateSelectorTimeout(input.Timeout); err != nil {
			return nil, EvaluateOutput{}, err
		}
		if input.MaxResultBytes < 0 {
			return nil, EvaluateOutput{}, fmt.Errorf("max_result_bytes must be >= 0, got %d", input.MaxResultBytes)
		}
		if err := validateElementTarget(input.Ref, input.Selector, input.Frame, false); err != nil {
			return nil, EvaluateOutput{}, err
		}
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, EvaluateOutput{}, err
		}

		awaitPromise := input.AwaitPromise == nil || *input.AwaitPromise
		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()

		var result any
		if input.Ref != "" || input.Selector != "" {
			// Unified element execution path for both ref and selector: binds
			// matched element as mutable 'el' with consistent 'this' semantics.
			// Newline before closing prevents a trailing '//' comment from
			// swallowing the closing brace.
			fnDecl := fmt.Sprintf("function() {\nreturn (function(el) {\n%s\n})(this);\n}", input.Expression)
			err = chromedp.Do(tctx, chromedp.Func(func(ctx context.Context, tgt *chromedp.Target) error {
				el, err := resolveElementObject(ctx, tgt, t, input.Ref, input.Selector, input.Frame, input.Timeout, false)
				if err != nil {
					return err
				}
				defer el.Release(ctx, tgt)
				result, err = callOnElement[any](ctx, tgt, el.ObjectID, fnDecl, awaitPromise)
				return err
			}))
		} else {
			uniqueCtxID, err2 := resolveFrameContext(t, input.Frame, false)
			if err2 != nil {
				return nil, EvaluateOutput{}, err2
			}
			var extraOpts []chromedp.EvaluateOption
			if awaitPromise {
				extraOpts = append(extraOpts, chromedp.EvalAwaitPromise)
			}
			evalOpts := evalOptsForFrame(uniqueCtxID, extraOpts...)
			result, err = chromedp.Run(tctx, chromedp.Evaluate[any](input.Expression, evalOpts...))
		}
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
// evaluate result. When the cap fires the original Result is replaced with a
// JSON string containing the truncated prefix so consumers always receive
// valid JSON.
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
