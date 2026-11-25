package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/chromedp"
	"github.com/google/jsonschema-go/jsonschema"
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
		if input.Selector == "" {
			return nil, QueryOutput{}, fmt.Errorf("selector is required")
		}
		if input.Limit < 0 {
			return nil, QueryOutput{}, fmt.Errorf("limit must be >= 0, got %d", input.Limit)
		}
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, QueryOutput{}, err
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

		// Wait for at least one matching element within the selector timeout,
		// then run the snapshot evaluation on tctx so post-wait DOM reading
		// cannot trip selectorError with a misleading "not visible" message.
		if err := chromedp.Do(sctx, chromedp.WaitReady(chromedp.CSS(input.Selector))); err != nil {
			return nil, QueryOutput{}, selectorError(tctx, input.Selector, err)
		}

		// Snapshot all requested properties in a single renderer evaluation
		// rather than reading live cdp.Node.Attributes slices or re-querying
		// per-element FullXPath (which can race or hang if the DOM mutates).
		stylePropsJSON, err := json.Marshal(input.ComputedStyle)
		if err != nil {
			return nil, QueryOutput{}, err
		}
		js := fmt.Sprintf(`(function() {
			const all = document.querySelectorAll(%s);
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
		})()`, jsQuote(input.Selector), limit, includeAttrs, includeText, input.OuterHTML, string(stylePropsJSON), input.BBox)

		out, err := chromedp.Run(tctx, chromedp.Evaluate[QueryOutput](js))
		if err != nil {
			return nil, QueryOutput{}, err
		}
		if out.Elements == nil {
			out.Elements = []QueryOutputElement{}
		}
		return nil, out, nil
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
		Name:         "get_accessibility_tree",
		Description:  "Get the accessibility tree of the page. A compact, token-efficient representation of page structure. Uses CDP's accessibility domain directly.",
		OutputSchema: accessibilityTreeOutputSchema(),
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:   true,
			IdempotentHint: true,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GetAccessibilityTreeInput) (*mcp.CallToolResult, GetAccessibilityTreeOutput, error) {
		if input.Depth < 0 {
			return nil, GetAccessibilityTreeOutput{}, fmt.Errorf("depth must be >= 0, got %d", input.Depth)
		}
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, GetAccessibilityTreeOutput{}, err
		}

		interestingOnly := input.InterestingOnly == nil || *input.InterestingOnly

		var nodes []*accessibility.Node
		var rootBackendID cdp.BackendNodeID
		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()

		// Bound only selector resolution with selectorContext so slow full-tree
		// AX fetches cannot produce false "element exists but is not visible" errors.
		if input.Selector != "" {
			sctx, scancel := selectorContext(tctx, input.Timeout)
			domNodes, err := chromedp.Run(sctx, chromedp.Nodes(chromedp.CSS(input.Selector)))
			scancel()
			if err != nil {
				return nil, GetAccessibilityTreeOutput{}, selectorError(tctx, input.Selector, err)
			}
			if len(domNodes) == 0 {
				return nil, GetAccessibilityTreeOutput{}, fmt.Errorf("selector %q matched no elements", input.Selector)
			}
			rootBackendID = domNodes[0].BackendNodeID
		}

		err = chromedp.Do(tctx, chromedp.Func(func(ctx context.Context, tgt *chromedp.Target) error {
			if rootBackendID > 0 {
				// Fetch the full AX tree so we can root cleanly at the matched
				// element's subtree (including all descendants, excluding
				// unrelated ancestors/siblings) and enforce input.Depth.
				res, err := cdp.Call(ctx, tgt, accessibility.GetFullAXTree, accessibility.GetFullAXTreeParams{})
				if err != nil {
					return err
				}
				nodes = res.Nodes
				found := false
				for _, n := range nodes {
					if n.BackendDOMNodeID == rootBackendID {
						found = true
						break
					}
				}
				if !found {
					partial, err := cdp.Call(ctx, tgt, accessibility.GetPartialAXTree, accessibility.GetPartialAXTreeParams{
						BackendNodeID:  rootBackendID,
						FetchRelatives: new(false),
					})
					if err != nil {
						return err
					}
					nodes = partial.Nodes
					rootBackendID = 0
				}
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
			return nil, GetAccessibilityTreeOutput{}, err
		}

		// Build a lookup map for tree construction.
		nodeMap := make(map[accessibility.NodeID]*accessibility.Node, len(nodes))
		for _, n := range nodes {
			nodeMap[n.NodeID] = n
		}

		tree := buildAXTree(nodes, nodeMap, rootBackendID, input.Depth, interestingOnly)
		if tree == nil {
			tree = []AXNodeOutput{}
		}

		return nil, GetAccessibilityTreeOutput{Tree: tree}, nil
	})
}

// buildAXTree converts CDP accessibility nodes into the output format. When
// rootBackendID > 0, the tree is rooted at the AX node matching that DOM
// backend node ID. When maxDepth > 0, recursion stops at depth maxDepth below
// each root (matching CDP GetFullAXTree depth semantics: depth=1 includes the
// root and its direct children).
func buildAXTree(nodes []*accessibility.Node, nodeMap map[accessibility.NodeID]*accessibility.Node, rootBackendID cdp.BackendNodeID, maxDepth int, interestingOnly bool) []AXNodeOutput {
	if len(nodes) == 0 {
		return nil
	}

	var startNodes []*accessibility.Node
	if rootBackendID > 0 {
		for _, n := range nodes {
			if n.BackendDOMNodeID == rootBackendID {
				startNodes = append(startNodes, n)
				break
			}
		}
	} else {
		childSet := make(map[accessibility.NodeID]bool)
		for _, n := range nodes {
			for _, childID := range n.ChildIDs {
				childSet[childID] = true
			}
		}
		for _, n := range nodes {
			if !childSet[n.NodeID] {
				startNodes = append(startNodes, n)
			}
		}
	}

	visited := make(map[accessibility.NodeID]bool, len(nodes))
	var roots []AXNodeOutput
	for _, n := range startNodes {
		if out := convertAXNode(n, nodeMap, visited, 0, maxDepth, interestingOnly); out != nil {
			roots = append(roots, *out)
		}
	}
	return roots
}

func convertAXNode(n *accessibility.Node, nodeMap map[accessibility.NodeID]*accessibility.Node, visited map[accessibility.NodeID]bool, depth, maxDepth int, interestingOnly bool) *AXNodeOutput {
	if n == nil || visited[n.NodeID] {
		return nil
	}
	visited[n.NodeID] = true
	defer delete(visited, n.NodeID)

	if n.Ignored && interestingOnly {
		return collapseTransparentChildren(n, nodeMap, visited, depth, maxDepth, interestingOnly)
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
		return collapseTransparentChildren(n, nodeMap, visited, depth, maxDepth, interestingOnly)
	}

	// Process children unless maxDepth has been reached.
	if maxDepth == 0 || depth < maxDepth {
		for _, childID := range n.ChildIDs {
			if child, ok := nodeMap[childID]; ok {
				if c := convertAXNode(child, nodeMap, visited, depth+1, maxDepth, interestingOnly); c != nil {
					out.Children = append(out.Children, *c)
				}
			}
		}
	}

	return out
}

// isAnonymousAXWrapper reports whether n is a synthetic multi-child container
// emitted by collapseTransparentChildren (no role, name, value, description,
// or properties of its own).
func isAnonymousAXWrapper(n AXNodeOutput) bool {
	return n.Role == "" && n.Name == "" && n.Value == "" && n.Description == "" && len(n.Properties) == 0
}

// collapseTransparentChildren collapses an ignored or uninteresting wrapper
// node when interestingOnly is true. Single-child wrappers disappear so their
// child occupies the wrapper's depth (preserving nodes behind wrappers at
// depth == maxDepth). Nested anonymous wrappers are flattened, and multi-child
// wrappers emit an anonymous container whose descendants are trimmed to respect
// maxDepth without leaving empty {} ghost nodes.
func collapseTransparentChildren(n *accessibility.Node, nodeMap map[accessibility.NodeID]*accessibility.Node, visited map[accessibility.NodeID]bool, depth, maxDepth int, interestingOnly bool) *AXNodeOutput {
	if maxDepth > 0 && depth > maxDepth {
		return nil
	}
	var children []AXNodeOutput
	for _, childID := range n.ChildIDs {
		if child, ok := nodeMap[childID]; ok {
			if out := convertAXNode(child, nodeMap, visited, depth, maxDepth, interestingOnly); out != nil {
				if isAnonymousAXWrapper(*out) {
					children = append(children, out.Children...)
				} else {
					children = append(children, *out)
				}
			}
		}
	}
	if len(children) == 0 {
		return nil
	}
	if len(children) == 1 {
		return &children[0]
	}
	if maxDepth > 0 {
		rem := maxDepth - depth - 1
		if rem < 0 {
			rem = 0
		}
		children = pruneAXChildren(children, rem)
		if len(children) == 0 {
			return nil
		}
		if len(children) == 1 {
			return &children[0]
		}
	}
	return &AXNodeOutput{Children: children}
}

func pruneAXChildren(nodes []AXNodeOutput, remaining int) []AXNodeOutput {
	kept := make([]AXNodeOutput, 0, len(nodes))
	for _, n := range nodes {
		if remaining <= 0 {
			n.Children = nil
		} else {
			n.Children = pruneAXChildren(n.Children, remaining-1)
		}
		if isAnonymousAXWrapper(n) && len(n.Children) == 0 {
			continue
		}
		kept = append(kept, n)
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

// axValueString extracts a string from a CDP accessibility.Value.
func axValueString(v *accessibility.Value) string {
	if v == nil || len(v.Value) == 0 || string(v.Value) == "null" {
		return ""
	}
	if v.Value[0] == '"' {
		var s string
		if err := json.Unmarshal([]byte(v.Value), &s); err == nil {
			return s
		}
	}
	// Fall back to raw JSON.
	return string(v.Value)
}

// accessibilityTreeOutputSchema returns an explicit recursive JSON Schema for
// GetAccessibilityTreeOutput because AXNodeOutput references itself via
// Children, which jsonschema.For cannot infer automatically.
func accessibilityTreeOutputSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Defs: map[string]*jsonschema.Schema{
			"AXNode": {
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
					"role":        {Type: "string"},
					"name":        {Type: "string"},
					"value":       {Type: "string"},
					"description": {Type: "string"},
					"properties": {
						Type:                 "object",
						AdditionalProperties: &jsonschema.Schema{Type: "string"},
					},
					"children": {
						Type:  "array",
						Items: &jsonschema.Schema{Ref: "#/$defs/AXNode"},
					},
				},
			},
		},
		Properties: map[string]*jsonschema.Schema{
			"tree": {
				Type:  "array",
				Items: &jsonschema.Schema{Ref: "#/$defs/AXNode"},
			},
		},
		Required: []string{"tree"},
	}
}
