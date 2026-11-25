package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/cdp"
	jsonv2 "github.com/chromedp/cdproto/cdp/jsonv2"
)

// ---------------------------------------------------------------------------
// axValueString
// ---------------------------------------------------------------------------

func TestAXValueString(t *testing.T) {
	tests := []struct {
		name string
		v    *accessibility.Value
		want string
	}{
		{
			name: "nil value",
			v:    nil,
			want: "",
		},
		{
			name: "nil Value field",
			v:    &accessibility.Value{Type: accessibility.ValueTypeString},
			want: "",
		},
		{
			name: "string JSON",
			v:    &accessibility.Value{Type: accessibility.ValueTypeString, Value: jsonv2.Value(`"hello"`)},
			want: "hello",
		},
		{
			name: "empty string JSON",
			v:    &accessibility.Value{Type: accessibility.ValueTypeString, Value: jsonv2.Value(`""`)},
			want: "",
		},
		{
			name: "integer JSON falls back to raw",
			v:    &accessibility.Value{Type: accessibility.ValueTypeInteger, Value: jsonv2.Value(`42`)},
			want: "42",
		},
		{
			name: "boolean JSON falls back to raw",
			v:    &accessibility.Value{Type: accessibility.ValueTypeBoolean, Value: jsonv2.Value(`true`)},
			want: "true",
		},
		{
			name: "object JSON falls back to raw",
			v:    &accessibility.Value{Type: "object", Value: jsonv2.Value(`{"k":"v"}`)},
			want: `{"k":"v"}`,
		},
		{
			name: "null JSON unmarshals to empty string",
			v:    &accessibility.Value{Type: accessibility.ValueTypeString, Value: jsonv2.Value(`null`)},
			want: "", // json.Unmarshal(`null`, &s) succeeds with s=""
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := axValueString(tt.v)
			if got != tt.want {
				t.Errorf("axValueString() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// convertAXNode
// ---------------------------------------------------------------------------

// helper to build a quick accessibility.Value with a JSON string.
func strAXValue(s string) *accessibility.Value {
	b, _ := json.Marshal(s)
	return &accessibility.Value{Type: accessibility.ValueTypeString, Value: jsonv2.Value(b)}
}

func TestConvertAXNode_SimpleNode(t *testing.T) {
	n := &accessibility.Node{
		NodeID: "1",
		Role:   strAXValue("button"),
		Name:   strAXValue("Submit"),
	}
	nodeMap := map[accessibility.NodeID]*accessibility.Node{"1": n}

	out := convertAXNode(n, nodeMap, map[accessibility.NodeID]bool{}, 0, 0, true)
	if out == nil {
		t.Fatal("expected non-nil output")
	}
	if out.Role != "button" {
		t.Errorf("Role = %q, want %q", out.Role, "button")
	}
	if out.Name != "Submit" {
		t.Errorf("Name = %q, want %q", out.Name, "Submit")
	}
}

func TestConvertAXNode_AllFields(t *testing.T) {
	n := &accessibility.Node{
		NodeID:      "1",
		Role:        strAXValue("textbox"),
		Name:        strAXValue("Email"),
		Value:       strAXValue("user@example.com"),
		Description: strAXValue("Enter your email address"),
		Properties: []*accessibility.Property{
			{Name: accessibility.PropertyNameFocused, Value: &accessibility.Value{Type: accessibility.ValueTypeBoolean, Value: jsonv2.Value(`true`)}},
			{Name: accessibility.PropertyNameRequired, Value: &accessibility.Value{Type: accessibility.ValueTypeBoolean, Value: jsonv2.Value(`true`)}},
		},
	}
	nodeMap := map[accessibility.NodeID]*accessibility.Node{"1": n}

	out := convertAXNode(n, nodeMap, map[accessibility.NodeID]bool{}, 0, 0, false)
	if out == nil {
		t.Fatal("expected non-nil output")
	}
	if out.Role != "textbox" {
		t.Errorf("Role = %q, want %q", out.Role, "textbox")
	}
	if out.Name != "Email" {
		t.Errorf("Name = %q, want %q", out.Name, "Email")
	}
	if out.Value != "user@example.com" {
		t.Errorf("Value = %q, want %q", out.Value, "user@example.com")
	}
	if out.Description != "Enter your email address" {
		t.Errorf("Description = %q, want %q", out.Description, "Enter your email address")
	}
	if len(out.Properties) != 2 {
		t.Fatalf("Properties count = %d, want 2", len(out.Properties))
	}
	if out.Properties["focused"] != "true" {
		t.Errorf("Properties[focused] = %q, want %q", out.Properties["focused"], "true")
	}
	if out.Properties["required"] != "true" {
		t.Errorf("Properties[required] = %q, want %q", out.Properties["required"], "true")
	}
}

func TestConvertAXNode_IgnoredNodeNoChildren(t *testing.T) {
	// Ignored node with no children and interestingOnly=true → nil
	n := &accessibility.Node{
		NodeID:  "1",
		Ignored: true,
	}
	nodeMap := map[accessibility.NodeID]*accessibility.Node{"1": n}

	out := convertAXNode(n, nodeMap, map[accessibility.NodeID]bool{}, 0, 0, true)
	if out != nil {
		t.Errorf("expected nil for ignored node with no children, got %+v", out)
	}
}

func TestConvertAXNode_IgnoredNodeOneChild(t *testing.T) {
	// Ignored node with one interesting child → collapse to the child.
	child := &accessibility.Node{
		NodeID: "2",
		Role:   strAXValue("button"),
		Name:   strAXValue("OK"),
	}
	parent := &accessibility.Node{
		NodeID:   "1",
		Ignored:  true,
		ChildIDs: []accessibility.NodeID{"2"},
	}
	nodeMap := map[accessibility.NodeID]*accessibility.Node{"1": parent, "2": child}

	out := convertAXNode(parent, nodeMap, map[accessibility.NodeID]bool{}, 0, 0, true)
	if out == nil {
		t.Fatal("expected non-nil output (collapsed child)")
	}
	// Should be the child directly, not wrapped.
	if out.Role != "button" {
		t.Errorf("Role = %q, want %q (should be collapsed child)", out.Role, "button")
	}
	if out.Name != "OK" {
		t.Errorf("Name = %q, want %q", out.Name, "OK")
	}
	if len(out.Children) != 0 {
		t.Errorf("Children count = %d, want 0 (collapsed single child)", len(out.Children))
	}
}

func TestConvertAXNode_IgnoredNodeMultipleChildren(t *testing.T) {
	// Ignored node with multiple interesting children → wrapper with children.
	child1 := &accessibility.Node{NodeID: "2", Role: strAXValue("link"), Name: strAXValue("Home")}
	child2 := &accessibility.Node{NodeID: "3", Role: strAXValue("link"), Name: strAXValue("About")}
	parent := &accessibility.Node{
		NodeID:   "1",
		Ignored:  true,
		ChildIDs: []accessibility.NodeID{"2", "3"},
	}
	nodeMap := map[accessibility.NodeID]*accessibility.Node{"1": parent, "2": child1, "3": child2}

	out := convertAXNode(parent, nodeMap, map[accessibility.NodeID]bool{}, 0, 0, true)
	if out == nil {
		t.Fatal("expected non-nil output (wrapper)")
	}
	// Wrapper should have no role/name but two children.
	if out.Role != "" {
		t.Errorf("wrapper Role = %q, want empty", out.Role)
	}
	if len(out.Children) != 2 {
		t.Fatalf("wrapper Children count = %d, want 2", len(out.Children))
	}
	if out.Children[0].Name != "Home" {
		t.Errorf("child[0] Name = %q, want %q", out.Children[0].Name, "Home")
	}
	if out.Children[1].Name != "About" {
		t.Errorf("child[1] Name = %q, want %q", out.Children[1].Name, "About")
	}
}

func TestConvertAXNode_IgnoredNodeNotFiltered_InterestingOnlyFalse(t *testing.T) {
	// With interestingOnly=false, ignored nodes are NOT skipped.
	n := &accessibility.Node{
		NodeID:  "1",
		Ignored: true,
		Role:    strAXValue("generic"),
	}
	nodeMap := map[accessibility.NodeID]*accessibility.Node{"1": n}

	out := convertAXNode(n, nodeMap, map[accessibility.NodeID]bool{}, 0, 0, false)
	if out == nil {
		t.Fatal("expected non-nil output (interestingOnly=false)")
	}
	if out.Role != "generic" {
		t.Errorf("Role = %q, want %q", out.Role, "generic")
	}
}

func TestConvertAXNode_UninterestingNonIgnored_NoRoleNameValue(t *testing.T) {
	// Non-ignored node with no role, name, or value, interestingOnly=true.
	// Should be skipped (returns nil) if it has no interesting children.
	n := &accessibility.Node{
		NodeID: "1",
	}
	nodeMap := map[accessibility.NodeID]*accessibility.Node{"1": n}

	out := convertAXNode(n, nodeMap, map[accessibility.NodeID]bool{}, 0, 0, true)
	if out != nil {
		t.Errorf("expected nil for uninteresting node without children, got %+v", out)
	}
}

func TestConvertAXNode_UninterestingNonIgnored_OneChild(t *testing.T) {
	// Non-ignored, uninteresting node with one interesting child → collapse.
	child := &accessibility.Node{NodeID: "2", Role: strAXValue("heading"), Name: strAXValue("Title")}
	parent := &accessibility.Node{
		NodeID:   "1",
		ChildIDs: []accessibility.NodeID{"2"},
	}
	nodeMap := map[accessibility.NodeID]*accessibility.Node{"1": parent, "2": child}

	out := convertAXNode(parent, nodeMap, map[accessibility.NodeID]bool{}, 0, 0, true)
	if out == nil {
		t.Fatal("expected non-nil output (collapsed child)")
	}
	if out.Role != "heading" {
		t.Errorf("Role = %q, want %q (should be collapsed child)", out.Role, "heading")
	}
}

func TestConvertAXNode_UninterestingNonIgnored_MultipleChildren(t *testing.T) {
	// Non-ignored, uninteresting node with multiple interesting children → wrapper.
	child1 := &accessibility.Node{NodeID: "2", Role: strAXValue("link"), Name: strAXValue("A")}
	child2 := &accessibility.Node{NodeID: "3", Role: strAXValue("link"), Name: strAXValue("B")}
	parent := &accessibility.Node{
		NodeID:   "1",
		ChildIDs: []accessibility.NodeID{"2", "3"},
	}
	nodeMap := map[accessibility.NodeID]*accessibility.Node{"1": parent, "2": child1, "3": child2}

	out := convertAXNode(parent, nodeMap, map[accessibility.NodeID]bool{}, 0, 0, true)
	if out == nil {
		t.Fatal("expected non-nil output (wrapper)")
	}
	if len(out.Children) != 2 {
		t.Fatalf("wrapper Children count = %d, want 2", len(out.Children))
	}
}

func TestConvertAXNode_WithChildren(t *testing.T) {
	// Interesting node with interesting children.
	child := &accessibility.Node{NodeID: "2", Role: strAXValue("listitem"), Name: strAXValue("Item 1")}
	parent := &accessibility.Node{
		NodeID:   "1",
		Role:     strAXValue("list"),
		Name:     strAXValue("Nav"),
		ChildIDs: []accessibility.NodeID{"2"},
	}
	nodeMap := map[accessibility.NodeID]*accessibility.Node{"1": parent, "2": child}

	out := convertAXNode(parent, nodeMap, map[accessibility.NodeID]bool{}, 0, 0, true)
	if out == nil {
		t.Fatal("expected non-nil output")
	}
	if out.Role != "list" {
		t.Errorf("Role = %q, want %q", out.Role, "list")
	}
	if len(out.Children) != 1 {
		t.Fatalf("Children count = %d, want 1", len(out.Children))
	}
	if out.Children[0].Role != "listitem" {
		t.Errorf("child Role = %q, want %q", out.Children[0].Role, "listitem")
	}
}

func TestConvertAXNode_ChildNotInMap(t *testing.T) {
	// Child ID references a node not in the map → skip silently.
	parent := &accessibility.Node{
		NodeID:   "1",
		Role:     strAXValue("navigation"),
		Name:     strAXValue("Menu"),
		ChildIDs: []accessibility.NodeID{"999"},
	}
	nodeMap := map[accessibility.NodeID]*accessibility.Node{"1": parent}

	out := convertAXNode(parent, nodeMap, map[accessibility.NodeID]bool{}, 0, 0, true)
	if out == nil {
		t.Fatal("expected non-nil output")
	}
	if len(out.Children) != 0 {
		t.Errorf("Children count = %d, want 0 (missing child ID)", len(out.Children))
	}
}

func TestConvertAXNode_PropertyWithNilValue(t *testing.T) {
	// Property with nil Value should be skipped.
	n := &accessibility.Node{
		NodeID: "1",
		Role:   strAXValue("button"),
		Name:   strAXValue("OK"),
		Properties: []*accessibility.Property{
			{Name: accessibility.PropertyNameDisabled, Value: nil},
			{Name: accessibility.PropertyNameFocused, Value: &accessibility.Value{Type: accessibility.ValueTypeBoolean, Value: jsonv2.Value(`true`)}},
		},
	}
	nodeMap := map[accessibility.NodeID]*accessibility.Node{"1": n}

	out := convertAXNode(n, nodeMap, map[accessibility.NodeID]bool{}, 0, 0, false)
	if out == nil {
		t.Fatal("expected non-nil output")
	}
	if len(out.Properties) != 1 {
		t.Fatalf("Properties count = %d, want 1 (nil value skipped)", len(out.Properties))
	}
	if out.Properties["focused"] != "true" {
		t.Errorf("Properties[focused] = %q, want %q", out.Properties["focused"], "true")
	}
}

// ---------------------------------------------------------------------------
// buildAXTree
// ---------------------------------------------------------------------------

func TestBuildAXTree_EmptyInput(t *testing.T) {
	tree := buildAXTree(nil, nil, 0, 0, true)
	if tree != nil {
		t.Errorf("expected nil for empty input, got %+v", tree)
	}
}

func TestBuildAXTree_SingleRoot(t *testing.T) {
	root := &accessibility.Node{NodeID: "1", Role: strAXValue("WebArea"), Name: strAXValue("Page")}
	nodes := []*accessibility.Node{root}
	nodeMap := map[accessibility.NodeID]*accessibility.Node{"1": root}

	tree := buildAXTree(nodes, nodeMap, 0, 0, false)
	if len(tree) != 1 {
		t.Fatalf("tree length = %d, want 1", len(tree))
	}
	if tree[0].Role != "WebArea" {
		t.Errorf("root Role = %q, want %q", tree[0].Role, "WebArea")
	}
}

func TestBuildAXTree_ParentChild(t *testing.T) {
	child := &accessibility.Node{NodeID: "2", Role: strAXValue("button"), Name: strAXValue("OK")}
	root := &accessibility.Node{NodeID: "1", Role: strAXValue("WebArea"), Name: strAXValue("Page"), ChildIDs: []accessibility.NodeID{"2"}}
	nodes := []*accessibility.Node{root, child}
	nodeMap := map[accessibility.NodeID]*accessibility.Node{"1": root, "2": child}

	tree := buildAXTree(nodes, nodeMap, 0, 0, false)
	if len(tree) != 1 {
		t.Fatalf("tree length = %d, want 1 (root only)", len(tree))
	}
	if len(tree[0].Children) != 1 {
		t.Fatalf("root children = %d, want 1", len(tree[0].Children))
	}
	if tree[0].Children[0].Role != "button" {
		t.Errorf("child Role = %q, want %q", tree[0].Children[0].Role, "button")
	}
}

func TestBuildAXTree_MultipleRoots(t *testing.T) {
	// Two nodes, neither is a child of the other → both are roots.
	n1 := &accessibility.Node{NodeID: "1", Role: strAXValue("navigation"), Name: strAXValue("Nav")}
	n2 := &accessibility.Node{NodeID: "2", Role: strAXValue("main"), Name: strAXValue("Content")}
	nodes := []*accessibility.Node{n1, n2}
	nodeMap := map[accessibility.NodeID]*accessibility.Node{"1": n1, "2": n2}

	tree := buildAXTree(nodes, nodeMap, 0, 0, false)
	if len(tree) != 2 {
		t.Fatalf("tree length = %d, want 2", len(tree))
	}
}

func TestBuildAXTree_InterestingOnlyFilters(t *testing.T) {
	// Root is ignored with one interesting child → collapsed.
	child := &accessibility.Node{NodeID: "2", Role: strAXValue("heading"), Name: strAXValue("Title")}
	root := &accessibility.Node{NodeID: "1", Ignored: true, ChildIDs: []accessibility.NodeID{"2"}}
	nodes := []*accessibility.Node{root, child}
	nodeMap := map[accessibility.NodeID]*accessibility.Node{"1": root, "2": child}

	tree := buildAXTree(nodes, nodeMap, 0, 0, true)
	if len(tree) != 1 {
		t.Fatalf("tree length = %d, want 1", len(tree))
	}
	// Should be the child (collapsed from ignored parent).
	if tree[0].Role != "heading" {
		t.Errorf("root Role = %q, want %q (collapsed from ignored parent)", tree[0].Role, "heading")
	}
}

func TestBuildAXTree_AllIgnoredNoInteresting(t *testing.T) {
	// All nodes are ignored with no interesting children → empty tree.
	n1 := &accessibility.Node{NodeID: "1", Ignored: true}
	n2 := &accessibility.Node{NodeID: "2", Ignored: true}
	nodes := []*accessibility.Node{n1, n2}
	nodeMap := map[accessibility.NodeID]*accessibility.Node{"1": n1, "2": n2}

	tree := buildAXTree(nodes, nodeMap, 0, 0, true)
	if len(tree) != 0 {
		t.Errorf("tree length = %d, want 0", len(tree))
	}
}

func TestBuildAXTree_DeepNesting(t *testing.T) {
	// Root → ignored wrapper → interesting leaf. interestingOnly=true.
	leaf := &accessibility.Node{NodeID: "3", Role: strAXValue("button"), Name: strAXValue("Save")}
	wrapper := &accessibility.Node{NodeID: "2", Ignored: true, ChildIDs: []accessibility.NodeID{"3"}}
	root := &accessibility.Node{NodeID: "1", Role: strAXValue("WebArea"), Name: strAXValue("Page"), ChildIDs: []accessibility.NodeID{"2"}}
	nodes := []*accessibility.Node{root, wrapper, leaf}
	nodeMap := map[accessibility.NodeID]*accessibility.Node{"1": root, "2": wrapper, "3": leaf}

	tree := buildAXTree(nodes, nodeMap, 0, 0, true)
	if len(tree) != 1 {
		t.Fatalf("tree length = %d, want 1", len(tree))
	}
	if tree[0].Role != "WebArea" {
		t.Errorf("root Role = %q, want %q", tree[0].Role, "WebArea")
	}
	// The ignored wrapper should be collapsed, so root's child is the leaf directly.
	if len(tree[0].Children) != 1 {
		t.Fatalf("root children = %d, want 1", len(tree[0].Children))
	}
	if tree[0].Children[0].Role != "button" {
		t.Errorf("child Role = %q, want %q (ignored wrapper collapsed)", tree[0].Children[0].Role, "button")
	}
	if tree[0].Children[0].Name != "Save" {
		t.Errorf("child Name = %q, want %q", tree[0].Children[0].Name, "Save")
	}
}

func TestBuildAXTree_SubtreeRootAndMaxDepth(t *testing.T) {
	grandchild := &accessibility.Node{NodeID: "4", Role: strAXValue("StaticText"), Name: strAXValue("Deep")}
	child := &accessibility.Node{NodeID: "3", Role: strAXValue("link"), Name: strAXValue("Docs"), ChildIDs: []accessibility.NodeID{"4"}}
	nav := &accessibility.Node{NodeID: "2", BackendDOMNodeID: cdp.BackendNodeID(42), Role: strAXValue("navigation"), Name: strAXValue("Nav"), ChildIDs: []accessibility.NodeID{"3"}}
	sibling := &accessibility.Node{NodeID: "5", Role: strAXValue("main"), Name: strAXValue("Main")}
	root := &accessibility.Node{NodeID: "1", Role: strAXValue("RootWebArea"), ChildIDs: []accessibility.NodeID{"2", "5"}}
	nodes := []*accessibility.Node{root, nav, child, grandchild, sibling}
	nodeMap := map[accessibility.NodeID]*accessibility.Node{"1": root, "2": nav, "3": child, "4": grandchild, "5": sibling}

	// Rooted at nav (backendID=42) with depth=1: includes nav and link, excludes sibling & grandchild.
	tree := buildAXTree(nodes, nodeMap, cdp.BackendNodeID(42), 1, true)
	if len(tree) != 1 || tree[0].Role != "navigation" {
		t.Fatalf("subtree root = %+v, want single navigation root", tree)
	}
	if len(tree[0].Children) != 1 || tree[0].Children[0].Role != "link" {
		t.Fatalf("nav children = %+v, want [link]", tree[0].Children)
	}
	if len(tree[0].Children[0].Children) != 0 {
		t.Errorf("depth=1 leaked grandchildren: %+v", tree[0].Children[0].Children)
	}

	// Cycle guard: cyclic ChildIDs must terminate without infinite recursion.
	nA := &accessibility.Node{NodeID: "a", Role: strAXValue("group"), ChildIDs: []accessibility.NodeID{"b"}}
	nB := &accessibility.Node{NodeID: "b", Role: strAXValue("group"), ChildIDs: []accessibility.NodeID{"a"}}
	cycled := convertAXNode(nA, map[accessibility.NodeID]*accessibility.Node{"a": nA, "b": nB}, map[accessibility.NodeID]bool{}, 0, 0, true)
	if cycled == nil || len(cycled.Children) != 1 || len(cycled.Children[0].Children) != 0 {
		t.Errorf("cycle guard failed: %+v", cycled)
	}

	// Ignored wrapper at depth == maxDepth must collapse and preserve interesting leaf
	// (while still pruning the leaf's own children beyond maxDepth).
	deepText := &accessibility.Node{NodeID: "40", Role: strAXValue("StaticText"), Name: strAXValue("Save")}
	btn := &accessibility.Node{NodeID: "30", Role: strAXValue("button"), Name: strAXValue("Save"), ChildIDs: []accessibility.NodeID{"40"}}
	wrap := &accessibility.Node{NodeID: "20", Ignored: true, ChildIDs: []accessibility.NodeID{"30"}}
	navWrap := &accessibility.Node{NodeID: "10", Role: strAXValue("navigation"), ChildIDs: []accessibility.NodeID{"20"}}
	wrapNodes := []*accessibility.Node{navWrap, wrap, btn, deepText}
	wrapMap := map[accessibility.NodeID]*accessibility.Node{"10": navWrap, "20": wrap, "30": btn, "40": deepText}
	gotWrap := buildAXTree(wrapNodes, wrapMap, 0, 1, true)
	if len(gotWrap) != 1 || len(gotWrap[0].Children) != 1 || gotWrap[0].Children[0].Role != "button" {
		t.Fatalf("ignored wrapper at depth=maxDepth dropped button: %+v", gotWrap)
	}
	if len(gotWrap[0].Children[0].Children) != 0 {
		t.Errorf("leaf children beyond maxDepth should be pruned: %+v", gotWrap[0].Children[0].Children)
	}

	// Multi-child ignored wrapper at depth=0 with maxDepth=1 must prune grandchildren.
	grandA := &accessibility.Node{NodeID: "gA", Role: strAXValue("StaticText"), Name: strAXValue("A")}
	linkA := &accessibility.Node{NodeID: "lA", Role: strAXValue("link"), Name: strAXValue("A"), ChildIDs: []accessibility.NodeID{"gA"}}
	linkB := &accessibility.Node{NodeID: "lB", Role: strAXValue("link"), Name: strAXValue("B")}
	rootWrap := &accessibility.Node{NodeID: "rw", Ignored: true, ChildIDs: []accessibility.NodeID{"lA", "lB"}}
	multiTree := buildAXTree(
		[]*accessibility.Node{rootWrap, linkA, grandA, linkB},
		map[accessibility.NodeID]*accessibility.Node{"rw": rootWrap, "lA": linkA, "gA": grandA, "lB": linkB},
		0, 1, true,
	)
	if len(multiTree) != 1 || len(multiTree[0].Children) != 2 {
		t.Fatalf("multi-child root wrapper = %+v, want 1 wrapper with 2 children", multiTree)
	}
	if len(multiTree[0].Children[0].Children) != 0 {
		t.Errorf("multi-child wrapper with maxDepth=1 leaked grandchildren: %+v", multiTree[0].Children[0].Children)
	}

	// Nested multi-child ignored wrappers at maxDepth=1 must flatten without emitting empty {} ghost nodes.
	linkC := &accessibility.Node{NodeID: "lC", Role: strAXValue("link"), Name: strAXValue("C")}
	innerWrap := &accessibility.Node{NodeID: "iw", Ignored: true, ChildIDs: []accessibility.NodeID{"lB", "lC"}}
	outerWrap := &accessibility.Node{NodeID: "ow", Ignored: true, ChildIDs: []accessibility.NodeID{"lA", "iw"}}
	nestedTree := buildAXTree(
		[]*accessibility.Node{outerWrap, linkA, grandA, innerWrap, linkB, linkC},
		map[accessibility.NodeID]*accessibility.Node{"ow": outerWrap, "lA": linkA, "gA": grandA, "iw": innerWrap, "lB": linkB, "lC": linkC},
		0, 1, true,
	)
	if len(nestedTree) != 1 || len(nestedTree[0].Children) != 3 {
		t.Fatalf("nested multi-child wrapper = %+v, want 1 wrapper with 3 flattened links", nestedTree)
	}
	for i, c := range nestedTree[0].Children {
		if c.Role != "link" || len(c.Children) != 0 {
			t.Errorf("nested child[%d] = %+v, want leaf link", i, c)
		}
	}

	// Anonymous multi-child wrapper beneath an interesting child of a multi-child root at maxDepth=2
	// must be dropped rather than pruned into an empty {} ghost node.
	btnB := &accessibility.Node{NodeID: "bB", Role: strAXValue("button"), Name: strAXValue("B")}
	btnC := &accessibility.Node{NodeID: "bC", Role: strAXValue("button"), Name: strAXValue("C")}
	subWrap := &accessibility.Node{NodeID: "sw", Ignored: true, ChildIDs: []accessibility.NodeID{"bB", "bC"}}
	region := &accessibility.Node{NodeID: "reg", Role: strAXValue("region"), Name: strAXValue("Sec"), ChildIDs: []accessibility.NodeID{"sw"}}
	topWrap := &accessibility.Node{NodeID: "tw", Ignored: true, ChildIDs: []accessibility.NodeID{"lA", "reg"}}
	prunedTree := buildAXTree(
		[]*accessibility.Node{topWrap, linkA, region, subWrap, btnB, btnC},
		map[accessibility.NodeID]*accessibility.Node{"tw": topWrap, "lA": linkA, "reg": region, "sw": subWrap, "bB": btnB, "bC": btnC},
		0, 2, true,
	)
	if len(prunedTree) != 1 || len(prunedTree[0].Children) != 2 {
		t.Fatalf("prunedTree = %+v, want 1 wrapper with [link, region]", prunedTree)
	}
	if len(prunedTree[0].Children[1].Children) != 0 {
		t.Errorf("region leaked empty ghost wrapper after pruning: %+v", prunedTree[0].Children[1].Children)
	}
}

func TestGetAccessibilityTreeSelectorSubtreeDepthAndValidation(t *testing.T) {
	tabID := navigateToFixture(t, "accessibility.html")
	defer closeTab(t, tabID)

	if err := callToolExpectErr(t, "get_accessibility_tree", map[string]any{
		"tab":   tabID,
		"depth": -1,
	}); !strings.Contains(err, "depth") {
		t.Errorf("negative depth err = %q, want depth error", err)
	}

	// Selector="nav" must scope to the <nav> subtree (include Home/About/Contact, exclude Contact form).
	fullNav := callTool[GetAccessibilityTreeOutput](t, "get_accessibility_tree", map[string]any{
		"tab":      tabID,
		"selector": "nav",
	})
	rawFull, _ := json.Marshal(fullNav)
	if !strings.Contains(string(rawFull), "Main navigation") || !strings.Contains(string(rawFull), "Home") {
		t.Fatalf("selector='nav' missing nav descendants: %s", rawFull)
	}
	if strings.Contains(string(rawFull), "Contact form") || strings.Contains(string(rawFull), "Section One") {
		t.Errorf("selector='nav' leaked unrelated page siblings: %s", rawFull)
	}

	// Selector="nav" with depth=1 must be strictly shallower than unlimited depth.
	shallowNav := callTool[GetAccessibilityTreeOutput](t, "get_accessibility_tree", map[string]any{
		"tab":      tabID,
		"selector": "nav",
		"depth":    1,
	})
	rawShallow, _ := json.Marshal(shallowNav)
	if len(rawShallow) >= len(rawFull) {
		t.Errorf("depth=1 partial AX tree (%d bytes) not smaller than unlimited (%d bytes): %s", len(rawShallow), len(rawFull), rawShallow)
	}
}

func TestQueryValidationAndMutationSafety(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	if err := callToolExpectErr(t, "query", map[string]any{"tab": tabID, "selector": ""}); !strings.Contains(err, "selector") {
		t.Errorf("empty selector err = %q", err)
	}
	if err := callToolExpectErr(t, "query", map[string]any{"tab": tabID, "selector": ".item", "limit": -1}); !strings.Contains(err, "limit") {
		t.Errorf("negative limit err = %q", err)
	}

	// Rapidly mutate attributes and remove/re-add items while querying: must never hang or race.
	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": `(() => {
			let n = 0;
			window.__mutTimer = setInterval(() => {
				const items = document.querySelectorAll('.item');
				items.forEach(el => el.setAttribute('data-tick', String(++n)));
			}, 5);
			return true;
		})()`,
	})
	defer callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `clearInterval(window.__mutTimer)`,
	})

	for i := 0; i < 5; i++ {
		out := callTool[QueryOutput](t, "query", map[string]any{
			"tab":            tabID,
			"selector":       ".item",
			"outer_html":     true,
			"computed_style": []string{"display"},
			"bbox":           true,
		})
		if out.Total != 3 || len(out.Elements) != 3 {
			t.Fatalf("iter %d: total=%d len=%d, want 3", i, out.Total, len(out.Elements))
		}
		if out.Elements[0].BBox == nil || out.Elements[0].ComputedStyle["display"] == "" {
			t.Fatalf("iter %d: missing bbox/computed_style: %+v", i, out.Elements[0])
		}
	}
}
