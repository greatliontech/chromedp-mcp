package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/chromedp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/browser"
)

// QueryInput is the input for query.
type QueryInput struct {
	SelectorInput
	Selector      string   `json:"selector" jsonschema:"CSS selector"`
	Limit         int      `json:"limit,omitempty" jsonschema:"Max elements to return (default 10)"`
	Attributes    *bool    `json:"attributes,omitempty" jsonschema:"Include element attributes (default true)"`
	ComputedStyle []string `json:"computed_style,omitempty" jsonschema:"CSS property names to include computed values for"`
	OuterHTML     bool     `json:"outer_html,omitempty" jsonschema:"Include outer HTML (default false)"`
	Text          *bool    `json:"text,omitempty" jsonschema:"Include text content (default true)"`
	BBox          bool     `json:"bbox,omitempty" jsonschema:"Include bounding box dimensions (default false)"`
}

// QueryOutputElement is one element in query output.
type QueryOutputElement struct {
	Index         int               `json:"index"`
	TagName       string            `json:"tag_name"`
	Attributes    map[string]string `json:"attributes,omitempty"`
	Text          string            `json:"text,omitempty"`
	OuterHTML     string            `json:"outer_html,omitempty"`
	ComputedStyle map[string]string `json:"computed_style,omitempty"`
	BBox          *BoundingBox      `json:"bbox,omitempty"`
}

// BoundingBox represents element dimensions.
type BoundingBox struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// QueryOutput is the output for query.
type QueryOutput struct {
	Elements []QueryOutputElement `json:"elements"`
	Total    int                  `json:"total"`
}

// GetHTMLInput is the input for get_html.
type GetHTMLInput struct {
	SelectorInput
	Selector string `json:"selector,omitempty" jsonschema:"CSS selector to scope to a subtree. If omitted returns the full page."`
	Outer    *bool  `json:"outer,omitempty" jsonschema:"Return outer HTML (default true). If false returns inner HTML."`
}

// GetHTMLOutput is the output for get_html.
type GetHTMLOutput struct {
	HTML string `json:"html"`
}

// GetTextInput is the input for get_text.
type GetTextInput struct {
	SelectorInput
	Selector string `json:"selector,omitempty" jsonschema:"CSS selector. If omitted returns text of the entire page body."`
	Hidden   bool   `json:"hidden,omitempty" jsonschema:"Include text from hidden elements (default false). When false uses innerText (visible text only). When true uses textContent (all DOM text)."`
}

// GetTextOutput is the output for get_text.
type GetTextOutput struct {
	Text string `json:"text"`
}

// GetAccessibilityTreeInput is the input for get_accessibility_tree.
type GetAccessibilityTreeInput struct {
	SelectorInput
	Selector        string `json:"selector,omitempty" jsonschema:"CSS selector to scope to a subtree"`
	Depth           int    `json:"depth,omitempty" jsonschema:"Max tree depth (default unlimited)"`
	InterestingOnly *bool  `json:"interesting_only,omitempty" jsonschema:"Filter to only interesting nodes with a role name or value (default true)"`
}

// AXNodeOutput represents a simplified accessibility tree node.
type AXNodeOutput struct {
	Role        string            `json:"role,omitempty"`
	Name        string            `json:"name,omitempty"`
	Value       string            `json:"value,omitempty"`
	Description string            `json:"description,omitempty"`
	Properties  map[string]string `json:"properties,omitempty"`
	Children    []AXNodeOutput    `json:"children,omitempty"`
}

// GetAccessibilityTreeOutput is the output for get_accessibility_tree.
type GetAccessibilityTreeOutput struct {
	Tree []AXNodeOutput `json:"tree"`
}

func registerDOMTools(s *mcp.Server, mgr *browser.Manager) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "query",
		Description: "Query the DOM and return information about matching elements.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:   true,
			IdempotentHint: true,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input QueryInput) (*mcp.CallToolResult, QueryOutput, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, QueryOutput{}, err
		}

		limit := input.Limit
		if limit <= 0 {
			limit = 10
		}
		includeAttrs := input.Attributes == nil || *input.Attributes
		includeText := input.Text == nil || *input.Text

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		sctx, scancel := selectorContext(tctx, input.Timeout)
		defer scancel()
		nodes, err := chromedp.Run(sctx, chromedp.Nodes(chromedp.CSSAll(input.Selector)))
		if err != nil {
			return nil, QueryOutput{}, selectorError(tctx, input.Selector, err)
		}

		total := len(nodes)
		if len(nodes) > limit {
			nodes = nodes[:limit]
		}

		elements := make([]QueryOutputElement, 0, len(nodes))
		for i, node := range nodes {
			elem := QueryOutputElement{
				Index:   i,
				TagName: node.LocalName,
			}
			if includeAttrs && len(node.Attributes) > 0 {
				attrs := make(map[string]string)
				for j := 0; j+1 < len(node.Attributes); j += 2 {
					attrs[node.Attributes[j]] = node.Attributes[j+1]
				}
				elem.Attributes = attrs
			}
			if includeText {
				if text, err := chromedp.Run(tctx, chromedp.TextContent(chromedp.Search(node.FullXPath()))); err == nil {
					elem.Text = text
				}
			}
			if input.OuterHTML {
				if html, err := chromedp.Run(tctx, chromedp.OuterHTML(chromedp.Search(node.FullXPath()))); err == nil {
					elem.OuterHTML = html
				}
			}
			if len(input.ComputedStyle) > 0 {
				if styles, err := chromedp.Run(tctx, chromedp.ComputedStyle(chromedp.Search(node.FullXPath()))); err == nil {
					cs := make(map[string]string)
					wanted := make(map[string]bool)
					for _, p := range input.ComputedStyle {
						wanted[p] = true
					}
					for _, s := range styles {
						if wanted[s.Name] {
							cs[s.Name] = s.Value
						}
					}
					elem.ComputedStyle = cs
				}
			}
			if input.BBox {
				if model, err := chromedp.Run(tctx, chromedp.Dimensions(chromedp.Search(node.FullXPath()))); err == nil && model != nil {
					elem.BBox = &BoundingBox{
						X:      model.Content[0],
						Y:      model.Content[1],
						Width:  float64(model.Width),
						Height: float64(model.Height),
					}
				}
			}
			elements = append(elements, elem)
		}

		return nil, QueryOutput{Elements: elements, Total: total}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_html",
		Description: "Get the HTML content of the page or a subtree.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:   true,
			IdempotentHint: true,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GetHTMLInput) (*mcp.CallToolResult, GetHTMLOutput, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, GetHTMLOutput{}, err
		}

		selector := input.Selector
		if selector == "" {
			selector = "html"
		}

		outer := input.Outer == nil || *input.Outer
		var html string
		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		sctx, scancel := selectorContext(tctx, input.Timeout)
		defer scancel()
		if outer {
			html, err = chromedp.Run(sctx, chromedp.OuterHTML(chromedp.CSS(selector)))
		} else {
			html, err = chromedp.Run(sctx, chromedp.InnerHTML(chromedp.CSS(selector)))
		}
		if err != nil {
			return nil, GetHTMLOutput{}, selectorError(tctx, selector, err)
		}
		return nil, GetHTMLOutput{HTML: html}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_text",
		Description: "Get the visible text content of the page or an element.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:   true,
			IdempotentHint: true,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GetTextInput) (*mcp.CallToolResult, GetTextOutput, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, GetTextOutput{}, err
		}

		selector := input.Selector
		if selector == "" {
			selector = "body"
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		sctx, scancel := selectorContext(tctx, input.Timeout)
		defer scancel()
		var action chromedp.Action[string]
		if input.Hidden {
			action = chromedp.TextContent(chromedp.CSS(selector))
		} else {
			action = chromedp.Text(chromedp.CSS(selector))
		}
		text, err := chromedp.Run(sctx, action)
		if err != nil {
			return nil, GetTextOutput{}, selectorError(tctx, selector, err)
		}
		return nil, GetTextOutput{Text: text}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_accessibility_tree",
		Description: "Get the accessibility tree of the page. A compact, token-efficient representation of page structure. Uses CDP's accessibility domain directly.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:   true,
			IdempotentHint: true,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GetAccessibilityTreeInput) (*mcp.CallToolResult, any, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, nil, err
		}

		interestingOnly := input.InterestingOnly == nil || *input.InterestingOnly

		var nodes []*accessibility.Node
		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		runCtx := tctx
		if input.Selector != "" {
			var cancel context.CancelFunc
			runCtx, cancel = selectorContext(tctx, input.Timeout)
			defer cancel()
		}
		err = chromedp.Do(runCtx, chromedp.Func(func(ctx context.Context, tgt *chromedp.Target) error {
			if input.Selector != "" {
				// Get the DOM node ID for the selector first.
				domNodes, err := chromedp.Nodes(chromedp.CSS(input.Selector))(ctx, tgt)
				if err != nil {
					return err
				}
				if len(domNodes) == 0 {
					return fmt.Errorf("selector %q matched no elements", input.Selector)
				}
				res, err := cdp.Call(ctx, tgt, accessibility.GetPartialAXTree, accessibility.GetPartialAXTreeParams{
					BackendNodeID:  domNodes[0].BackendNodeID,
					FetchRelatives: new(true),
				})
				if err != nil {
					return err
				}
				nodes = res.Nodes
				return nil
			}
			var params accessibility.GetFullAXTreeParams
			if input.Depth > 0 {
				params.Depth = new(int64(input.Depth))
			}
			res, err := cdp.Call(ctx, tgt, accessibility.GetFullAXTree, params)
			if err != nil {
				return err
			}
			nodes = res.Nodes
			return nil
		}))
		if err != nil {
			if input.Selector != "" {
				return nil, nil, selectorError(tctx, input.Selector, err)
			}
			return nil, nil, err
		}

		// Build a lookup map for tree construction.
		nodeMap := make(map[accessibility.NodeID]*accessibility.Node, len(nodes))
		for _, n := range nodes {
			nodeMap[n.NodeID] = n
		}

		// Convert to output format.
		tree := buildAXTree(nodes, nodeMap, interestingOnly)

		return nil, GetAccessibilityTreeOutput{Tree: tree}, nil
	})
}

// buildAXTree converts CDP accessibility nodes into the output format.
func buildAXTree(nodes []*accessibility.Node, nodeMap map[accessibility.NodeID]*accessibility.Node, interestingOnly bool) []AXNodeOutput {
	if len(nodes) == 0 {
		return nil
	}

	// Find root nodes (those without a parent or whose parent is not in the map).
	childSet := make(map[accessibility.NodeID]bool)
	for _, n := range nodes {
		for _, childID := range n.ChildIDs {
			childSet[childID] = true
		}
	}

	var roots []AXNodeOutput
	for _, n := range nodes {
		if !childSet[n.NodeID] {
			out := convertAXNode(n, nodeMap, interestingOnly)
			if out != nil {
				roots = append(roots, *out)
			}
		}
	}
	return roots
}

func convertAXNode(n *accessibility.Node, nodeMap map[accessibility.NodeID]*accessibility.Node, interestingOnly bool) *AXNodeOutput {
	if n.Ignored && interestingOnly {
		// Still process children — ignored nodes may have interesting children.
		var children []AXNodeOutput
		for _, childID := range n.ChildIDs {
			if child, ok := nodeMap[childID]; ok {
				if out := convertAXNode(child, nodeMap, interestingOnly); out != nil {
					children = append(children, *out)
				}
			}
		}
		if len(children) == 1 {
			return &children[0]
		}
		if len(children) > 1 {
			return &AXNodeOutput{Children: children}
		}
		return nil
	}

	out := &AXNodeOutput{}
	if n.Role != nil {
		out.Role = axValueString(n.Role)
	}
	if n.Name != nil {
		out.Name = axValueString(n.Name)
	}
	if n.Value != nil {
		out.Value = axValueString(n.Value)
	}
	if n.Description != nil {
		out.Description = axValueString(n.Description)
	}

	// Extract interesting properties.
	if len(n.Properties) > 0 {
		props := make(map[string]string)
		for _, p := range n.Properties {
			if p.Value != nil {
				props[string(p.Name)] = axValueString(p.Value)
			}
		}
		if len(props) > 0 {
			out.Properties = props
		}
	}

	if interestingOnly && out.Role == "" && out.Name == "" && out.Value == "" {
		// Not interesting on its own, but may have interesting children.
		var children []AXNodeOutput
		for _, childID := range n.ChildIDs {
			if child, ok := nodeMap[childID]; ok {
				if c := convertAXNode(child, nodeMap, interestingOnly); c != nil {
					children = append(children, *c)
				}
			}
		}
		if len(children) == 0 {
			return nil
		}
		if len(children) == 1 {
			return &children[0]
		}
		return &AXNodeOutput{Children: children}
	}

	// Process children.
	for _, childID := range n.ChildIDs {
		if child, ok := nodeMap[childID]; ok {
			if c := convertAXNode(child, nodeMap, interestingOnly); c != nil {
				out.Children = append(out.Children, *c)
			}
		}
	}

	return out
}

// axValueString extracts a string from a CDP accessibility.Value.
func axValueString(v *accessibility.Value) string {
	if v == nil || len(v.Value) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal([]byte(v.Value), &s); err == nil {
		return s
	}
	// Fall back to raw JSON.
	return string(v.Value)
}
