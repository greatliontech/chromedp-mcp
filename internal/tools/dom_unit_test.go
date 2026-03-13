package tools

import (
	"encoding/json"
	"testing"
)

// ---------------------------------------------------------------------------
// lenientAXValueString
// ---------------------------------------------------------------------------

func TestLenientAXValueString(t *testing.T) {
	tests := []struct {
		name string
		v    *axValue
		want string
	}{
		{
			name: "nil value",
			v:    nil,
			want: "",
		},
		{
			name: "nil Value field",
			v:    &axValue{Type: "string"},
			want: "",
		},
		{
			name: "string JSON",
			v:    &axValue{Type: "string", Value: json.RawMessage(`"hello"`)},
			want: "hello",
		},
		{
			name: "empty string JSON",
			v:    &axValue{Type: "string", Value: json.RawMessage(`""`)},
			want: "",
		},
		{
			name: "integer JSON falls back to raw",
			v:    &axValue{Type: "integer", Value: json.RawMessage(`42`)},
			want: "42",
		},
		{
			name: "boolean JSON falls back to raw",
			v:    &axValue{Type: "boolean", Value: json.RawMessage(`true`)},
			want: "true",
		},
		{
			name: "object JSON falls back to raw",
			v:    &axValue{Type: "object", Value: json.RawMessage(`{"k":"v"}`)},
			want: `{"k":"v"}`,
		},
		{
			name: "null JSON unmarshals to empty string",
			v:    &axValue{Type: "string", Value: json.RawMessage(`null`)},
			want: "", // json.Unmarshal(`null`, &s) succeeds with s=""
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := lenientAXValueString(tt.v)
			if got != tt.want {
				t.Errorf("lenientAXValueString() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// convertAXNode
// ---------------------------------------------------------------------------

// helper to build a quick axValue with a JSON string.
func strAXValue(s string) *axValue {
	b, _ := json.Marshal(s)
	return &axValue{Type: "string", Value: json.RawMessage(b)}
}

func TestConvertAXNode_SimpleNode(t *testing.T) {
	n := &axNode{
		NodeID: "1",
		Role:   strAXValue("button"),
		Name:   strAXValue("Submit"),
	}
	nodeMap := map[string]*axNode{"1": n}

	out := convertAXNode(n, nodeMap, true)
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
	n := &axNode{
		NodeID:      "1",
		Role:        strAXValue("textbox"),
		Name:        strAXValue("Email"),
		Value:       strAXValue("user@example.com"),
		Description: strAXValue("Enter your email address"),
		Properties: []*axProperty{
			{Name: "focused", Value: &axValue{Type: "boolean", Value: json.RawMessage(`true`)}},
			{Name: "required", Value: &axValue{Type: "boolean", Value: json.RawMessage(`true`)}},
		},
	}
	nodeMap := map[string]*axNode{"1": n}

	out := convertAXNode(n, nodeMap, false)
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
	n := &axNode{
		NodeID:  "1",
		Ignored: true,
	}
	nodeMap := map[string]*axNode{"1": n}

	out := convertAXNode(n, nodeMap, true)
	if out != nil {
		t.Errorf("expected nil for ignored node with no children, got %+v", out)
	}
}

func TestConvertAXNode_IgnoredNodeOneChild(t *testing.T) {
	// Ignored node with one interesting child → collapse to the child.
	child := &axNode{
		NodeID: "2",
		Role:   strAXValue("button"),
		Name:   strAXValue("OK"),
	}
	parent := &axNode{
		NodeID:   "1",
		Ignored:  true,
		ChildIDs: []string{"2"},
	}
	nodeMap := map[string]*axNode{"1": parent, "2": child}

	out := convertAXNode(parent, nodeMap, true)
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
	child1 := &axNode{NodeID: "2", Role: strAXValue("link"), Name: strAXValue("Home")}
	child2 := &axNode{NodeID: "3", Role: strAXValue("link"), Name: strAXValue("About")}
	parent := &axNode{
		NodeID:   "1",
		Ignored:  true,
		ChildIDs: []string{"2", "3"},
	}
	nodeMap := map[string]*axNode{"1": parent, "2": child1, "3": child2}

	out := convertAXNode(parent, nodeMap, true)
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
	n := &axNode{
		NodeID:  "1",
		Ignored: true,
		Role:    strAXValue("generic"),
	}
	nodeMap := map[string]*axNode{"1": n}

	out := convertAXNode(n, nodeMap, false)
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
	n := &axNode{
		NodeID: "1",
	}
	nodeMap := map[string]*axNode{"1": n}

	out := convertAXNode(n, nodeMap, true)
	if out != nil {
		t.Errorf("expected nil for uninteresting node without children, got %+v", out)
	}
}

func TestConvertAXNode_UninterestingNonIgnored_OneChild(t *testing.T) {
	// Non-ignored, uninteresting node with one interesting child → collapse.
	child := &axNode{NodeID: "2", Role: strAXValue("heading"), Name: strAXValue("Title")}
	parent := &axNode{
		NodeID:   "1",
		ChildIDs: []string{"2"},
	}
	nodeMap := map[string]*axNode{"1": parent, "2": child}

	out := convertAXNode(parent, nodeMap, true)
	if out == nil {
		t.Fatal("expected non-nil output (collapsed child)")
	}
	if out.Role != "heading" {
		t.Errorf("Role = %q, want %q (should be collapsed child)", out.Role, "heading")
	}
}

func TestConvertAXNode_UninterestingNonIgnored_MultipleChildren(t *testing.T) {
	// Non-ignored, uninteresting node with multiple interesting children → wrapper.
	child1 := &axNode{NodeID: "2", Role: strAXValue("link"), Name: strAXValue("A")}
	child2 := &axNode{NodeID: "3", Role: strAXValue("link"), Name: strAXValue("B")}
	parent := &axNode{
		NodeID:   "1",
		ChildIDs: []string{"2", "3"},
	}
	nodeMap := map[string]*axNode{"1": parent, "2": child1, "3": child2}

	out := convertAXNode(parent, nodeMap, true)
	if out == nil {
		t.Fatal("expected non-nil output (wrapper)")
	}
	if len(out.Children) != 2 {
		t.Fatalf("wrapper Children count = %d, want 2", len(out.Children))
	}
}

func TestConvertAXNode_WithChildren(t *testing.T) {
	// Interesting node with interesting children.
	child := &axNode{NodeID: "2", Role: strAXValue("listitem"), Name: strAXValue("Item 1")}
	parent := &axNode{
		NodeID:   "1",
		Role:     strAXValue("list"),
		Name:     strAXValue("Nav"),
		ChildIDs: []string{"2"},
	}
	nodeMap := map[string]*axNode{"1": parent, "2": child}

	out := convertAXNode(parent, nodeMap, true)
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
	parent := &axNode{
		NodeID:   "1",
		Role:     strAXValue("navigation"),
		Name:     strAXValue("Menu"),
		ChildIDs: []string{"999"},
	}
	nodeMap := map[string]*axNode{"1": parent}

	out := convertAXNode(parent, nodeMap, true)
	if out == nil {
		t.Fatal("expected non-nil output")
	}
	if len(out.Children) != 0 {
		t.Errorf("Children count = %d, want 0 (missing child ID)", len(out.Children))
	}
}

func TestConvertAXNode_PropertyWithNilValue(t *testing.T) {
	// Property with nil Value should be skipped.
	n := &axNode{
		NodeID: "1",
		Role:   strAXValue("button"),
		Name:   strAXValue("OK"),
		Properties: []*axProperty{
			{Name: "disabled", Value: nil},
			{Name: "focused", Value: &axValue{Type: "boolean", Value: json.RawMessage(`true`)}},
		},
	}
	nodeMap := map[string]*axNode{"1": n}

	out := convertAXNode(n, nodeMap, false)
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
	tree := buildAXTree(nil, nil, true)
	if tree != nil {
		t.Errorf("expected nil for empty input, got %+v", tree)
	}
}

func TestBuildAXTree_SingleRoot(t *testing.T) {
	root := &axNode{NodeID: "1", Role: strAXValue("WebArea"), Name: strAXValue("Page")}
	nodes := []*axNode{root}
	nodeMap := map[string]*axNode{"1": root}

	tree := buildAXTree(nodes, nodeMap, false)
	if len(tree) != 1 {
		t.Fatalf("tree length = %d, want 1", len(tree))
	}
	if tree[0].Role != "WebArea" {
		t.Errorf("root Role = %q, want %q", tree[0].Role, "WebArea")
	}
}

func TestBuildAXTree_ParentChild(t *testing.T) {
	child := &axNode{NodeID: "2", Role: strAXValue("button"), Name: strAXValue("OK")}
	root := &axNode{NodeID: "1", Role: strAXValue("WebArea"), Name: strAXValue("Page"), ChildIDs: []string{"2"}}
	nodes := []*axNode{root, child}
	nodeMap := map[string]*axNode{"1": root, "2": child}

	tree := buildAXTree(nodes, nodeMap, false)
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
	n1 := &axNode{NodeID: "1", Role: strAXValue("navigation"), Name: strAXValue("Nav")}
	n2 := &axNode{NodeID: "2", Role: strAXValue("main"), Name: strAXValue("Content")}
	nodes := []*axNode{n1, n2}
	nodeMap := map[string]*axNode{"1": n1, "2": n2}

	tree := buildAXTree(nodes, nodeMap, false)
	if len(tree) != 2 {
		t.Fatalf("tree length = %d, want 2", len(tree))
	}
}

func TestBuildAXTree_InterestingOnlyFilters(t *testing.T) {
	// Root is ignored with one interesting child → collapsed.
	child := &axNode{NodeID: "2", Role: strAXValue("heading"), Name: strAXValue("Title")}
	root := &axNode{NodeID: "1", Ignored: true, ChildIDs: []string{"2"}}
	nodes := []*axNode{root, child}
	nodeMap := map[string]*axNode{"1": root, "2": child}

	tree := buildAXTree(nodes, nodeMap, true)
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
	n1 := &axNode{NodeID: "1", Ignored: true}
	n2 := &axNode{NodeID: "2", Ignored: true}
	nodes := []*axNode{n1, n2}
	nodeMap := map[string]*axNode{"1": n1, "2": n2}

	tree := buildAXTree(nodes, nodeMap, true)
	if len(tree) != 0 {
		t.Errorf("tree length = %d, want 0", len(tree))
	}
}

func TestBuildAXTree_DeepNesting(t *testing.T) {
	// Root → ignored wrapper → interesting leaf. interestingOnly=true.
	leaf := &axNode{NodeID: "3", Role: strAXValue("button"), Name: strAXValue("Save")}
	wrapper := &axNode{NodeID: "2", Ignored: true, ChildIDs: []string{"3"}}
	root := &axNode{NodeID: "1", Role: strAXValue("WebArea"), Name: strAXValue("Page"), ChildIDs: []string{"2"}}
	nodes := []*axNode{root, wrapper, leaf}
	nodeMap := map[string]*axNode{"1": root, "2": wrapper, "3": leaf}

	tree := buildAXTree(nodes, nodeMap, true)
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
