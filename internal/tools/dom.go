package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/chromedp/chromedp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/browser"
	"github.com/greatliontech/chromedp-mcp/internal/collector"
)

// defaultDOMMaxBytes bounds get_dom output so huge SPAs cannot flood context.
const defaultDOMMaxBytes = 65_536

// DOM format constants for get_dom.
const (
	domFormatText = "text"
	domFormatHTML = "html"
)

// queryInput is the input for query.
type queryInput struct {
	selectorInput
	Frame         string   `json:"frame,omitempty" jsonschema:"Optional same-origin frame ID from list_frames."`
	Selector      string   `json:"selector" jsonschema:"CSS selector (supports '>>>' to pierce open Shadow DOM, e.g. 'my-card >>> button')."`
	Limit         int      `json:"limit,omitempty" jsonschema:"Max elements to return (default 10)."`
	Attributes    *bool    `json:"attributes,omitempty" jsonschema:"Include element attributes (default true)."`
	ComputedStyle []string `json:"computed_style,omitempty" jsonschema:"CSS property names to include computed values for."`
	OuterHTML     bool     `json:"outer_html,omitempty" jsonschema:"Include outer HTML (default false)."`
	Text          *bool    `json:"text,omitempty" jsonschema:"Include text content (default true)."`
	BBox          bool     `json:"bbox,omitempty" jsonschema:"Include bounding box dimensions (default false)."`
}

// queryOutputElement is one element in query output.
type queryOutputElement struct {
	Index         int               `json:"index"`
	TagName       string            `json:"tag_name"`
	Attributes    map[string]string `json:"attributes,omitempty"`
	Text          string            `json:"text,omitempty"`
	OuterHTML     string            `json:"outer_html,omitempty"`
	ComputedStyle map[string]string `json:"computed_style,omitempty"`
	BBox          *boundingBox      `json:"bbox,omitempty"`
}

// boundingBox represents element dimensions.
type boundingBox struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// queryOutput is the output for query.
type queryOutput struct {
	Elements []queryOutputElement `json:"elements"`
	Total    int                  `json:"total"`
}

// getDOMInput is the input for get_dom.
type getDOMInput struct {
	selectorInput
	Format   string `json:"format" jsonschema:"Output format: 'text' (visible/DOM text) or 'html' (markup)."`
	Ref      string `json:"ref,omitempty" jsonschema:"Element ref from snapshot (e.g. 'e4'). Mutually exclusive with selector and frame."`
	Frame    string `json:"frame,omitempty" jsonschema:"Optional same-origin frame ID from list_frames."`
	Selector string `json:"selector,omitempty" jsonschema:"CSS selector (supports '>>>' for Shadow DOM). Defaults to 'body' for text or 'html' for html."`
	Outer    *bool  `json:"outer,omitempty" jsonschema:"For format='html': return outer HTML (default true) or inner HTML (false)."`
	Hidden   bool   `json:"hidden,omitempty" jsonschema:"For format='text': include hidden elements via textContent (default false = visible innerText)."`
	MaxBytes int    `json:"max_bytes,omitempty" jsonschema:"Max bytes to return (default 65536)."`
}

// getDOMOutput is the output for get_dom.
type getDOMOutput struct {
	Content    string `json:"content"`
	Truncated  bool   `json:"truncated,omitempty"`
	TotalBytes int    `json:"total_bytes,omitempty"`
}

func registerDOMTools(s *mcp.Server, mgr *browser.Manager) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "query",
		Description: "Query the DOM with a CSS selector (supports '>>>' to pierce open Shadow DOM) and return matching elements.",
		Annotations: readOnlyAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input queryInput) (*mcp.CallToolResult, queryOutput, error) {
		if input.Selector == "" {
			return nil, queryOutput{}, fmt.Errorf("selector is required")
		}
		if err := validateSelectorTimeout(input.Timeout); err != nil {
			return nil, queryOutput{}, err
		}
		if err := validateLimit(input.Limit); err != nil {
			return nil, queryOutput{}, err
		}
		findAllExpr, err := queryAllElementsJS(input.Selector)
		if err != nil {
			return nil, queryOutput{}, err
		}
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, queryOutput{}, err
		}
		uniqueCtxID, err := resolveFrameContext(t, input.Frame, true)
		if err != nil {
			return nil, queryOutput{}, err
		}

		limit := input.Limit
		if limit == 0 {
			limit = 10
		}
		includeAttrs := input.Attributes == nil || *input.Attributes
		includeText := input.Text == nil || *input.Text

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		sctx, scancel := selectorContext(tctx, input.Timeout)
		defer scancel()

		uniqueCtxID, err = waitForSelector(sctx, tctx, t, uniqueCtxID, input.Frame, input.Selector)
		if err != nil {
			return nil, queryOutput{}, err
		}

		stylePropsJSON, err := json.Marshal(input.ComputedStyle)
		if err != nil {
			return nil, queryOutput{}, err
		}
		js := fmt.Sprintf(`(function() {
			const all = %s;
			const limit = %d;
			const includeAttrs = %t;
			const includeText = %t;
			const includeOuter = %t;
			const styleProps = %s || [];
			const includeBBox = %t;
			const n = Math.min(all.length, limit);
			const elements = [];
			for (let i = 0; i < n; i++) {
				const el = all[i];
				const item = {
					index: i,
					tag_name: el.localName || el.tagName.toLowerCase(),
				};
				if (includeAttrs && el.attributes && el.attributes.length > 0) {
					const attrs = {};
					for (let j = 0; j < el.attributes.length; j++) {
						const a = el.attributes[j];
						attrs[a.name] = a.value;
					}
					item.attributes = attrs;
				}
				if (includeText) {
					item.text = el.textContent || "";
				}
				if (includeOuter) {
					item.outer_html = el.outerHTML || "";
				}
				if (styleProps.length > 0) {
					const cs = getComputedStyle(el);
					const out = {};
					for (let j = 0; j < styleProps.length; j++) {
						const prop = styleProps[j];
						const val = cs.getPropertyValue(prop);
						if (val !== "") {
							out[prop] = val;
						}
					}
					if (Object.keys(out).length > 0) {
						item.computed_style = out;
					}
				}
				if (includeBBox && el.getClientRects().length > 0) {
					const r = el.getBoundingClientRect();
					item.bbox = {x: r.x, y: r.y, width: r.width, height: r.height};
				}
				elements.push(item);
			}
			return {elements: elements, total: all.length};
		})()`, findAllExpr, limit, includeAttrs, includeText, input.OuterHTML, string(stylePropsJSON), input.BBox)

		out, err := chromedp.Run(tctx, chromedp.Evaluate[queryOutput](js, evalOptsForFrame(uniqueCtxID)...))
		if err != nil {
			return nil, queryOutput{}, err
		}
		if out.Elements == nil {
			out.Elements = []queryOutputElement{}
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_dom",
		Description: "Get text or HTML content of the page, a frame, or a specific element (by snapshot ref or CSS/shadow selector), bounded by max_bytes.",
		InputSchema: schemaWithEnums[getDOMInput](map[string][]string{
			"format": {domFormatText, domFormatHTML},
		}),
		Annotations: readOnlyAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input getDOMInput) (*mcp.CallToolResult, getDOMOutput, error) {
		if err := validateEnum("format", input.Format, false, []string{domFormatText, domFormatHTML}); err != nil {
			return nil, getDOMOutput{}, err
		}
		if err := validateSelectorTimeout(input.Timeout); err != nil {
			return nil, getDOMOutput{}, err
		}
		if input.MaxBytes < 0 {
			return nil, getDOMOutput{}, fmt.Errorf("max_bytes must be >= 0, got %d", input.MaxBytes)
		}
		if err := validateElementTarget(input.Ref, input.Selector, input.Frame, false); err != nil {
			return nil, getDOMOutput{}, err
		}

		maxBytes := input.MaxBytes
		if maxBytes == 0 {
			maxBytes = defaultDOMMaxBytes
		}

		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, getDOMOutput{}, err
		}

		var readFn string
		if input.Format == domFormatHTML {
			if input.Outer == nil || *input.Outer {
				readFn = `function() { return this.outerHTML || ""; }`
			} else {
				readFn = `function() { return this.innerHTML || ""; }`
			}
		} else {
			if input.Hidden {
				readFn = `function() { return this.textContent || ""; }`
			} else {
				// Match chromedp's js/text.js visibility check while gracefully
				// handling visible SVG/MathML elements (which lack HTMLElement.innerText)
				// and non-Element nodes (which lack getClientRects).
				readFn = `function() {
					const hasRects = typeof this.getClientRects === "function" && this.getClientRects().length > 0;
					const visible = Boolean(this.offsetWidth || this.offsetHeight || hasRects || this.nodeType === 9);
					if (!visible) return "";
					return (typeof this.innerText === "string" ? this.innerText : this.textContent) || "";
				}`
			}
		}

		selector := input.Selector
		if input.Ref == "" && selector == "" {
			if input.Format == domFormatHTML {
				selector = "html"
			} else {
				selector = "body"
			}
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()

		var content string
		err = chromedp.Do(tctx, chromedp.Func(func(ctx context.Context, tgt *chromedp.Target) error {
			el, err := resolveElementObject(ctx, tgt, t, input.Ref, selector, input.Frame, input.Timeout, false)
			if err != nil {
				return err
			}
			defer el.Release(ctx, tgt)
			content, err = callOnElement[string](ctx, tgt, el.ObjectID, readFn, false)
			return err
		}))
		if err != nil {
			return nil, getDOMOutput{}, err
		}

		return nil, capDOMContent(content, maxBytes), nil
	})
}

func capDOMContent(content string, maxBytes int) getDOMOutput {
	if maxBytes <= 0 || len(content) <= maxBytes {
		return getDOMOutput{Content: content}
	}
	trimmed, _ := collector.TruncateUTF8(content, maxBytes)
	return getDOMOutput{
		Content:    trimmed,
		Truncated:  true,
		TotalBytes: len(content),
	}
}
