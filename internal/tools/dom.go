package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/css"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/chromedp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/thegrumpylion/chromedp-mcp/internal/browser"
)

// QueryInput is the input for query.
type QueryInput struct {
	TabInput
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
	TabInput
	Selector string `json:"selector,omitempty" jsonschema:"CSS selector to scope to a subtree. If omitted returns the full page."`
	Outer    *bool  `json:"outer,omitempty" jsonschema:"Return outer HTML (default true). If false returns inner HTML."`
}

// GetHTMLOutput is the output for get_html.
type GetHTMLOutput struct {
	HTML string `json:"html"`
}

// GetTextInput is the input for get_text.
type GetTextInput struct {
	TabInput
	Selector string `json:"selector,omitempty" jsonschema:"CSS selector. If omitted returns text of the entire page body."`
}

// GetTextOutput is the output for get_text.
type GetTextOutput struct {
	Text string `json:"text"`
}

// GetAccessibilityTreeInput is the input for get_accessibility_tree.
type GetAccessibilityTreeInput struct {
	TabInput
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

		var nodes []*cdp.Node
		sctx, scancel := selectorContext(t.Context(), input.Timeout)
		defer scancel()
		if err := chromedp.Run(sctx, chromedp.Nodes(input.Selector, &nodes, chromedp.ByQueryAll)); err != nil {
			return nil, QueryOutput{}, err
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
				var text string
				if err := chromedp.Run(t.Context(), chromedp.TextContent(node.FullXPath(), &text, chromedp.BySearch)); err == nil {
					elem.Text = text
				}
			}
			if input.OuterHTML {
				var html string
				if err := chromedp.Run(t.Context(), chromedp.OuterHTML(node.FullXPath(), &html, chromedp.BySearch)); err == nil {
					elem.OuterHTML = html
				}
			}
			if len(input.ComputedStyle) > 0 {
				var styles []*css.ComputedStyleProperty
				if err := chromedp.Run(t.Context(), chromedp.ComputedStyle(node.FullXPath(), &styles, chromedp.BySearch)); err == nil {
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
				var model *dom.BoxModel
				if err := chromedp.Run(t.Context(), chromedp.Dimensions(node.FullXPath(), &model, chromedp.BySearch)); err == nil && model != nil {
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
		sctx, scancel := selectorContext(t.Context(), input.Timeout)
		defer scancel()
		if outer {
			err = chromedp.Run(sctx, chromedp.OuterHTML(selector, &html, chromedp.ByQuery))
		} else {
			err = chromedp.Run(sctx, chromedp.InnerHTML(selector, &html, chromedp.ByQuery))
		}
		if err != nil {
			return nil, GetHTMLOutput{}, err
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

		var text string
		sctx, scancel := selectorContext(t.Context(), input.Timeout)
		defer scancel()
		if err := chromedp.Run(sctx, chromedp.Text(selector, &text, chromedp.NodeVisible, chromedp.ByQuery)); err != nil {
			return nil, GetTextOutput{}, err
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

		var axNodes []*accessibility.Node
		tctx := t.Context()
		if input.Selector != "" {
			var cancel context.CancelFunc
			tctx, cancel = selectorContext(tctx, input.Timeout)
			defer cancel()
		}
		err = chromedp.Run(tctx, chromedp.ActionFunc(func(ctx context.Context) error {
			if input.Selector != "" {
				// Get the DOM node ID for the selector first.
				var nodes []*cdp.Node
				if err := chromedp.Run(ctx, chromedp.Nodes(input.Selector, &nodes, chromedp.ByQuery)); err != nil {
					return err
				}
				if len(nodes) == 0 {
					return fmt.Errorf("selector %q matched no elements", input.Selector)
				}
				params := accessibility.GetPartialAXTree().WithBackendNodeID(nodes[0].BackendNodeID)
				if input.Depth > 0 {
					params = params.WithFetchRelatives(true)
				}
				var err error
				axNodes, err = params.Do(ctx)
				return err
			}
			params := accessibility.GetFullAXTree()
			if input.Depth > 0 {
				params = params.WithDepth(int64(input.Depth))
			}
			var err error
			axNodes, err = params.Do(ctx)
			return err
		}))
		if err != nil {
			return nil, nil, err
		}

		// Build a lookup map for tree construction.
		nodeMap := make(map[accessibility.NodeID]*accessibility.Node)
		for _, n := range axNodes {
			nodeMap[n.NodeID] = n
		}

		// Convert to output format.
		tree := buildAXTree(axNodes, nodeMap, interestingOnly)

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

// axValueString extracts a string from an accessibility.Value.
func axValueString(v *accessibility.Value) string {
	if v == nil || v.Value == nil {
		return ""
	}
	var s string
	if err := json.Unmarshal(v.Value, &s); err == nil {
		return s
	}
	// Fall back to raw JSON.
	return string(v.Value)
}
