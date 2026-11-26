package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/chromedp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/browser"
	"github.com/greatliontech/chromedp-mcp/internal/collector"
	"github.com/greatliontech/chromedp-mcp/internal/tab"
)

// defaultSnapshotMaxBytes bounds snapshot text output so huge pages cannot
// flood the LLM context window.
const defaultSnapshotMaxBytes = 50_000

// snapshotInput is the input for snapshot.
type snapshotInput struct {
	selectorInput
	Frame           string `json:"frame,omitempty" jsonschema:"Optional same-origin frame ID from list_frames to snapshot instead of the main frame."`
	Selector        string `json:"selector,omitempty" jsonschema:"Optional CSS selector (supports '>>>' for Shadow DOM) to scope the snapshot to a subtree."`
	Depth           int    `json:"depth,omitempty" jsonschema:"Max semantic tree depth (default 0 = unlimited)."`
	InteractiveOnly bool   `json:"interactive_only,omitempty" jsonschema:"Only include interactive/focusable controls (buttons, links, inputs, etc.)."`
	MaxBytes        int    `json:"max_bytes,omitempty" jsonschema:"Max bytes for the rendered snapshot text (default 50000)."`
}

// snapshotOutput is the output for snapshot.
type snapshotOutput struct {
	URL        string `json:"url"`
	Title      string `json:"title"`
	Snapshot   string `json:"snapshot"`
	RefCount   int    `json:"ref_count"`
	Truncated  bool   `json:"truncated,omitempty"`
	TotalBytes int    `json:"total_bytes,omitempty"`
}

func registerSnapshotTools(s *mcp.Server, mgr *browser.Manager) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "snapshot",
		Description: "Capture a compact accessibility snapshot of the page (or a same-origin frame/subtree) with short element refs ([ref=e1], [ref=e2], ...) usable in interaction tools, screenshot, get_dom, evaluate, and emulate(action='pseudo_state'). Each call replaces the tab's active ref table; refs pierce open Shadow DOM and automatically invalidate when their document navigates.",
		Annotations: readOnlyAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input snapshotInput) (*mcp.CallToolResult, snapshotOutput, error) {
		if err := validateSelectorTimeout(input.Timeout); err != nil {
			return nil, snapshotOutput{}, err
		}
		if input.Depth < 0 {
			return nil, snapshotOutput{}, fmt.Errorf("depth must be >= 0, got %d", input.Depth)
		}
		if input.MaxBytes < 0 {
			return nil, snapshotOutput{}, fmt.Errorf("max_bytes must be >= 0, got %d", input.MaxBytes)
		}
		if input.Selector != "" {
			if _, _, err := parseShadowSelector(input.Selector); err != nil {
				return nil, snapshotOutput{}, err
			}
		}

		maxBytes := input.MaxBytes
		if maxBytes == 0 {
			maxBytes = defaultSnapshotMaxBytes
		}

		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, snapshotOutput{}, err
		}
		// Accessibility.getFullAXTree runs on Blink's renderer thread and
		// stalls while a JS modal dialog is open; fail fast so callers can
		// inspect/dismiss it.
		if t.Dialog != nil && t.Dialog.Open() {
			return nil, snapshotOutput{}, fmt.Errorf("cannot capture snapshot while a JavaScript dialog is open — use dialog tool first")
		}
		if input.Frame != "" {
			if _, err := resolveFrameContext(t, input.Frame, input.Selector != ""); err != nil {
				return nil, snapshotOutput{}, err
			}
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()

		// Capture document generation BEFORE querying Chrome so a main-frame
		// or target-subframe navigation mid-snapshot cannot commit stale refs.
		genTok := t.SnapshotGeneration(cdp.FrameID(input.Frame))

		var (
			nodes         []*accessibility.Node
			rootBackendID cdp.BackendNodeID
		)
		err = chromedp.Do(tctx, chromedp.Func(func(ctx context.Context, tgt *chromedp.Target) error {
			if input.Selector != "" {
				el, err := resolveElementObject(ctx, tgt, t, "", input.Selector, input.Frame, input.Timeout, true)
				if err != nil {
					return err
				}
				rootBackendID = el.BackendNodeID
				el.Release(ctx, tgt)
				if rootBackendID == 0 {
					return fmt.Errorf("selector %q could not be resolved to a backend DOM node", input.Selector)
				}
			}

			// Always fetch full AX tree without CDP Depth so renderAXSnapshot
			// enforces semantic depth consistently after collapsing ignored and
			// unnamed generic wrapper nodes.
			params := accessibility.GetFullAXTreeParams{}
			if input.Frame != "" {
				params.FrameID = cdp.FrameID(input.Frame)
			}
			res, err := cdp.Call(ctx, tgt, accessibility.GetFullAXTree, params)
			if err != nil {
				return err
			}
			nodes = res.Nodes
			if rootBackendID > 0 {
				found := false
				for _, n := range nodes {
					if n != nil && n.BackendDOMNodeID == rootBackendID {
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
			}
			return nil
		}))
		if err != nil {
			return nil, snapshotOutput{}, err
		}

		rendered, refs, truncated, totalBytes := renderAXSnapshot(nodes, rootBackendID, cdp.FrameID(input.Frame), input.Depth, input.InteractiveOnly, maxBytes)
		if !t.SetSnapshotRefs(genTok, refs) {
			return nil, snapshotOutput{}, fmt.Errorf("document navigated during snapshot — retry snapshot")
		}

		url, title, _ := t.Info(ctx)
		out := snapshotOutput{
			URL:      url,
			Title:    title,
			Snapshot: rendered,
			RefCount: len(refs),
		}
		if truncated {
			out.Truncated = true
			out.TotalBytes = totalBytes
		}
		return nil, out, nil
	})
}

var interactiveAXRoles = map[string]bool{
	"button":           true,
	"checkbox":         true,
	"columnheader":     true,
	"combobox":         true,
	"gridcell":         true,
	"link":             true,
	"listbox":          true,
	"menu":             true,
	"menubar":          true,
	"menuitem":         true,
	"menuitemcheckbox": true,
	"menuitemradio":    true,
	"option":           true,
	"radio":            true,
	"rowheader":        true,
	"scrollbar":        true,
	"searchbox":        true,
	"slider":           true,
	"spinbutton":       true,
	"summary":          true,
	"switch":           true,
	"tab":              true,
	"textbox":          true,
	"treeitem":         true,
}

var nonRefAXRoles = map[string]bool{
	"":              true,
	"RootWebArea":   true,
	"StaticText":    true,
	"InlineTextBox": true,
	"LineBreak":     true,
	"none":          true,
	"presentation":  true,
}

func isInteractiveAXNode(role string, n *accessibility.Node) bool {
	if interactiveAXRoles[role] {
		return true
	}
	for _, p := range n.Properties {
		if p != nil && string(p.Name) == "focusable" && axValueString(p.Value) == "true" {
			return true
		}
	}
	return false
}

func hasOnlyTransparentTextChildren(n *accessibility.Node, nodeMap map[accessibility.NodeID]*accessibility.Node) bool {
	for _, cid := range n.ChildIDs {
		child := nodeMap[cid]
		if child == nil || child.Ignored {
			continue
		}
		if axValueString(child.Role) != "InlineTextBox" {
			return false
		}
	}
	return true
}

// renderAXSnapshot formats CDP accessibility nodes into a compact indented
// outline with sequential [ref=eN] element tags, capped at maxBytes. Only refs
// present in the non-truncated output are included in the returned ref map.
func renderAXSnapshot(
	nodes []*accessibility.Node,
	rootBackendID cdp.BackendNodeID,
	frameID cdp.FrameID,
	maxDepth int,
	interactiveOnly bool,
	maxBytes int,
) (string, map[string]tab.SnapshotRef, bool, int) {
	if len(nodes) == 0 {
		return "", nil, false, 0
	}

	nodeMap := make(map[accessibility.NodeID]*accessibility.Node, len(nodes))
	for _, n := range nodes {
		if n != nil {
			nodeMap[n.NodeID] = n
		}
	}

	var startNodes []*accessibility.Node
	if rootBackendID > 0 {
		var fallback *accessibility.Node
		for _, n := range nodes {
			if n == nil || n.BackendDOMNodeID != rootBackendID {
				continue
			}
			if !n.Ignored {
				startNodes = append(startNodes, n)
				break
			}
			if fallback == nil {
				fallback = n
			}
		}
		if len(startNodes) == 0 && fallback != nil {
			startNodes = append(startNodes, fallback)
		}
	} else {
		childSet := make(map[accessibility.NodeID]bool, len(nodes))
		for _, n := range nodes {
			if n == nil {
				continue
			}
			for _, cid := range n.ChildIDs {
				childSet[cid] = true
			}
		}
		for _, n := range nodes {
			if n != nil && !childSet[n.NodeID] {
				startNodes = append(startNodes, n)
			}
		}
		if len(startNodes) == 0 {
			for _, n := range nodes {
				if n != nil {
					startNodes = append(startNodes, n)
					break
				}
			}
		}
	}

	type renderedLine struct {
		text  string
		refID string
		ref   tab.SnapshotRef
	}
	var lines []renderedLine
	nextRef := 1
	visited := make(map[accessibility.NodeID]bool, len(nodes))

	var walk func(n *accessibility.Node, depth int, parentName string, curFrame cdp.FrameID)
	walk = func(n *accessibility.Node, depth int, parentName string, curFrame cdp.FrameID) {
		if n == nil || visited[n.NodeID] {
			return
		}
		visited[n.NodeID] = true
		defer delete(visited, n.NodeID)

		if n.FrameID != "" {
			curFrame = n.FrameID
		}

		role := axValueString(n.Role)
		name := axValueString(n.Name)
		val := axValueString(n.Value)
		desc := axValueString(n.Description)

		// Skip ignored nodes and pure transparent wrappers, collapsing depth so
		// their children sit at the current level.
		if n.Ignored || role == "InlineTextBox" || (role == "" && name == "" && val == "") || (role == "none" && name == "") || (role == "presentation" && name == "") {
			if maxDepth == 0 || depth <= maxDepth {
				for _, cid := range n.ChildIDs {
					walk(nodeMap[cid], depth, parentName, curFrame)
				}
			}
			return
		}

		// Elide redundant StaticText children repeating their parent's accessible name.
		if role == "StaticText" && strings.TrimSpace(name) == strings.TrimSpace(parentName) && parentName != "" && hasOnlyTransparentTextChildren(n, nodeMap) {
			return
		}

		// Collapse unnamed generic containers with no own semantic/interactive state.
		if role == "generic" && name == "" && val == "" && desc == "" && !isInteractiveAXNode(role, n) {
			if maxDepth == 0 || depth <= maxDepth {
				for _, cid := range n.ChildIDs {
					walk(nodeMap[cid], depth, parentName, curFrame)
				}
			}
			return
		}

		emit := !interactiveOnly || isInteractiveAXNode(role, n)
		// Always advance structural semantic depth for non-transparent nodes so
		// depth=N behaves consistently even with interactive_only=true.
		childDepth := depth + 1
		nextParentName := parentName
		if emit {
			var (
				refID string
				sref  tab.SnapshotRef
			)
			if n.BackendDOMNodeID > 0 && !nonRefAXRoles[role] {
				refID = fmt.Sprintf("e%d", nextRef)
				nextRef++
				sref = tab.SnapshotRef{
					BackendNodeID: n.BackendDOMNodeID,
					FrameID:       curFrame,
					Role:          role,
					Name:          name,
				}
			}

			indent := depth
			if interactiveOnly {
				indent = 0
			}
			line := formatAXLine(indent, role, name, val, desc, refID, n.Properties)
			lines = append(lines, renderedLine{text: line, refID: refID, ref: sref})
			if name != "" {
				nextParentName = name
			}
		}

		if maxDepth == 0 || childDepth <= maxDepth {
			for _, cid := range n.ChildIDs {
				walk(nodeMap[cid], childDepth, nextParentName, curFrame)
			}
		}
	}

	for _, root := range startNodes {
		walk(root, 0, "", frameID)
	}

	var (
		sb         strings.Builder
		refs       = make(map[string]tab.SnapshotRef)
		truncated  bool
		totalBytes int
	)
	for i, l := range lines {
		lineBytes := len(l.text)
		if i > 0 {
			lineBytes++ // newline
		}
		totalBytes += lineBytes
		if truncated {
			continue
		}
		if maxBytes > 0 && sb.Len()+lineBytes > maxBytes {
			truncated = true
			// If even the very first line exceeds maxBytes, emit a UTF-8 safe prefix.
			if sb.Len() == 0 {
				prefix, _ := collector.TruncateUTF8(l.text, maxBytes)
				sb.WriteString(prefix)
				if l.refID != "" && strings.Contains(prefix, "[ref="+l.refID+"]") {
					refs[l.refID] = l.ref
				}
			}
			continue
		}
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(l.text)
		if l.refID != "" {
			refs[l.refID] = l.ref
		}
	}

	if len(refs) == 0 {
		refs = nil
	}
	return sb.String(), refs, truncated, totalBytes
}

func formatAXLine(indent int, role, name, val, desc, refID string, props []*accessibility.Property) string {
	var b strings.Builder
	for i := 0; i < indent; i++ {
		b.WriteString("  ")
	}
	b.WriteString("- ")
	if role != "" {
		b.WriteString(role)
	} else {
		b.WriteString("node")
	}
	if name != "" {
		b.WriteByte(' ')
		b.WriteString(quoteCompact(name))
	}
	if refID != "" {
		b.WriteString(" [ref=")
		b.WriteString(refID)
		b.WriteByte(']')
	}
	if val != "" && val != name {
		b.WriteString(" value=")
		b.WriteString(quoteCompact(val))
	}
	for _, p := range props {
		if p == nil || p.Value == nil {
			continue
		}
		k := string(p.Name)
		v := axValueString(p.Value)
		switch k {
		case "checked", "pressed", "expanded", "selected", "disabled", "required", "readonly", "focused", "modal", "multiselectable", "invalid":
			if v == "true" {
				b.WriteByte(' ')
				b.WriteString(k)
			} else if v != "" && v != "false" {
				b.WriteByte(' ')
				b.WriteString(k)
				b.WriteByte('=')
				b.WriteString(v)
			}
		case "level", "url", "placeholder", "autocomplete":
			if v != "" {
				b.WriteByte(' ')
				b.WriteString(k)
				b.WriteByte('=')
				b.WriteString(quoteCompact(v))
			}
		}
	}
	if desc != "" && desc != name {
		b.WriteString(" desc=")
		b.WriteString(quoteCompact(desc))
	}
	return b.String()
}

func quoteCompact(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	b, _ := json.Marshal(s)
	return string(b)
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
	return string(v.Value)
}
