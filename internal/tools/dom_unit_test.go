package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/cdp"
	jsonv2 "github.com/chromedp/cdproto/cdp/jsonv2"
	"github.com/chromedp/cdproto/runtime"
)

func strAXValue(s string) *accessibility.Value {
	b, _ := json.Marshal(s)
	return &accessibility.Value{Type: accessibility.ValueTypeString, Value: jsonv2.Value(b)}
}

func boolAXValue(v bool) *accessibility.Value {
	raw := "false"
	if v {
		raw = "true"
	}
	return &accessibility.Value{Type: accessibility.ValueTypeBoolean, Value: jsonv2.Value(raw)}
}

func TestAXValueString(t *testing.T) {
	tests := []struct {
		name string
		v    *accessibility.Value
		want string
	}{
		{"nil value", nil, ""},
		{"empty Value", &accessibility.Value{Type: accessibility.ValueTypeString}, ""},
		{"string JSON", strAXValue("hello"), "hello"},
		{"empty string", strAXValue(""), ""},
		{"integer JSON", &accessibility.Value{Type: accessibility.ValueTypeInteger, Value: jsonv2.Value(`42`)}, "42"},
		{"boolean JSON", boolAXValue(true), "true"},
		{"null JSON", &accessibility.Value{Type: accessibility.ValueTypeString, Value: jsonv2.Value(`null`)}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := axValueString(tt.v); got != tt.want {
				t.Errorf("axValueString() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseShadowSelectorAndValidation(t *testing.T) {
	parts, isShadow, err := parseShadowSelector("div > button.save")
	if err != nil || isShadow || len(parts) != 1 || parts[0] != "div > button.save" {
		t.Fatalf("plain selector = (%v, %v, %v)", parts, isShadow, err)
	}

	parts, isShadow, err = parseShadowSelector("my-app >>> my-card >>> button.primary")
	if err != nil || !isShadow || len(parts) != 3 || parts[0] != "my-app" || parts[1] != "my-card" || parts[2] != "button.primary" {
		t.Fatalf("nested shadow selector = (%v, %v, %v)", parts, isShadow, err)
	}

	parts, isShadow, err = parseShadowSelector(`a[title="a >>> b"]`)
	if err != nil || isShadow || len(parts) != 1 || parts[0] != `a[title="a >>> b"]` {
		t.Fatalf("quoted >>> selector = (%v, %v, %v)", parts, isShadow, err)
	}
	parts, isShadow, err = parseShadowSelector(`div[data-x='x >>> y'] >>> span[data-y="z\" >>> w"]`)
	if err != nil || !isShadow || len(parts) != 2 || parts[0] != `div[data-x='x >>> y']` || parts[1] != `span[data-y="z\" >>> w"]` {
		t.Fatalf("mixed quoted + shadow selector = (%v, %v, %v)", parts, isShadow, err)
	}
	parts, isShadow, err = parseShadowSelector(`#a\"b >>> #foo\>>>bar`)
	if err != nil || !isShadow || len(parts) != 2 || parts[0] != `#a\"b` || parts[1] != `#foo\>>>bar` {
		t.Fatalf("escaped outside quotes = (%v, %v, %v)", parts, isShadow, err)
	}

	// formatExceptionDetails must never return empty string on throw null / throw "".
	if got := formatExceptionDetails(&runtime.ExceptionDetails{Text: "Uncaught", Exception: &runtime.RemoteObject{Value: jsonv2.Value(`null`)}}); got != "null" {
		t.Errorf("formatExceptionDetails(null) = %q, want \"null\"", got)
	}
	if got := formatExceptionDetails(&runtime.ExceptionDetails{Text: "Uncaught", Exception: &runtime.RemoteObject{Value: jsonv2.Value(`""`)}}); got != "Uncaught" {
		t.Errorf("formatExceptionDetails(\"\") = %q, want \"Uncaught\"", got)
	}
	if got := formatExceptionDetails(&runtime.ExceptionDetails{Text: "Uncaught", Exception: &runtime.RemoteObject{Value: jsonv2.Value(`"boom"`)}}); got != "boom" {
		t.Errorf("formatExceptionDetails(\"boom\") = %q, want \"boom\"", got)
	}

	for _, bad := range []string{">>> button", "host >>>", "a >>>   >>> b"} {
		if _, _, err := parseShadowSelector(bad); err == nil {
			t.Errorf("parseShadowSelector(%q) succeeded, want error", bad)
		}
	}

	if err := validateElementTarget("e1", "#btn", "", true); err == nil {
		t.Error("expected error when both ref and selector are set")
	}
	if err := validateElementTarget("e1", "", "frame-1", true); err == nil {
		t.Error("expected error when both ref and frame are set")
	}
	if err := validateElementTarget("", "", "", true); err == nil {
		t.Error("expected error when requireOne=true and both ref and selector empty")
	}
	if err := validateElementTarget("", "", "", false); err != nil {
		t.Errorf("validateElementTarget optional empty = %v, want nil", err)
	}
}

func TestCapDOMContent(t *testing.T) {
	out := capDOMContent("hello", 10)
	if out.Content != "hello" || out.Truncated || out.TotalBytes != 0 {
		t.Errorf("untruncated = %+v", out)
	}
	// Multi-byte UTF-8 boundary: "aé" is 3 bytes; maxBytes=2 must trim 'é'.
	out = capDOMContent("aé", 2)
	if out.Content != "a" || !out.Truncated || out.TotalBytes != 3 {
		t.Errorf("truncated UTF-8 = %+v", out)
	}
}

func TestRenderAXSnapshotOutlineRefsAndFilters(t *testing.T) {
	nodes := []*accessibility.Node{
		{
			NodeID:           "root",
			Role:             strAXValue("RootWebArea"),
			Name:             strAXValue("Checkout"),
			BackendDOMNodeID: 1,
			ChildIDs:         []accessibility.NodeID{"ignored", "generic", "heading"},
		},
		{
			NodeID:   "ignored",
			Ignored:  true,
			ChildIDs: []accessibility.NodeID{"btn"},
		},
		{
			NodeID:           "btn",
			Role:             strAXValue("button"),
			Name:             strAXValue("Pay Now"),
			BackendDOMNodeID: 10,
			ChildIDs:         []accessibility.NodeID{"btn-text"},
			Properties: []*accessibility.Property{
				{Name: "disabled", Value: boolAXValue(true)},
			},
		},
		{
			NodeID:           "btn-text",
			Role:             strAXValue("StaticText"),
			Name:             strAXValue("Pay Now"), // redundant with parent button name -> elided even with InlineTextBox child
			BackendDOMNodeID: 11,
			ChildIDs:         []accessibility.NodeID{"btn-itb"},
		},
		{
			NodeID: "btn-itb",
			Role:   strAXValue("InlineTextBox"),
			Name:   strAXValue("Pay Now"),
		},
		{
			NodeID:   "generic",
			Role:     strAXValue("generic"),
			ChildIDs: []accessibility.NodeID{"input"},
		},
		{
			NodeID:           "input",
			Role:             strAXValue("textbox"),
			Name:             strAXValue("Email"),
			Value:            strAXValue("a@b.com"),
			BackendDOMNodeID: 20,
			Properties: []*accessibility.Property{
				{Name: "required", Value: boolAXValue(true)},
			},
		},
		{
			NodeID:           "heading",
			Role:             strAXValue("heading"),
			Name:             strAXValue("Summary"),
			BackendDOMNodeID: 30,
			Properties: []*accessibility.Property{
				{Name: "level", Value: &accessibility.Value{Type: accessibility.ValueTypeInteger, Value: jsonv2.Value(`2`)}},
			},
		},
	}

	// Default full snapshot: collapses ignored + unnamed generic, elides redundant
	// StaticText, skips ref on RootWebArea, assigns sequential refs e1..e3.
	text, refs, truncated, _ := renderAXSnapshot(nodes, 0, cdp.FrameID("f1"), 0, false, 10_000)
	if truncated {
		t.Fatal("unexpected truncation")
	}
	if len(refs) != 3 {
		t.Fatalf("len(refs) = %d, want 3 (%+v)\n%s", len(refs), refs, text)
	}
	if refs["e1"].BackendNodeID != 10 || refs["e1"].FrameID != "f1" {
		t.Errorf("e1 = %+v, want BackendNodeID=10 FrameID=f1", refs["e1"])
	}
	if strings.Contains(text, "StaticText") {
		t.Errorf("redundant StaticText should be elided:\n%s", text)
	}
	if !strings.Contains(text, `- button "Pay Now" [ref=e1] disabled`) {
		t.Errorf("missing expected button line:\n%s", text)
	}
	if !strings.Contains(text, `- textbox "Email" [ref=e2] value="a@b.com" required`) {
		t.Errorf("missing expected textbox line:\n%s", text)
	}
	if !strings.Contains(text, `- heading "Summary" [ref=e3] level="2"`) {
		t.Errorf("missing expected heading line:\n%s", text)
	}

	// interactive_only=true keeps button and textbox, omits heading and RootWebArea.
	iText, iRefs, _, _ := renderAXSnapshot(nodes, 0, "", 0, true, 10_000)
	if len(iRefs) != 2 {
		t.Fatalf("interactive_only len(refs) = %d, want 2:\n%s", len(iRefs), iText)
	}
	if strings.Contains(iText, "heading") || strings.Contains(iText, "RootWebArea") {
		t.Errorf("interactive_only leaked non-interactive nodes:\n%s", iText)
	}

	// maxBytes truncation keeps only refs emitted before the cut.
	firstLineLen := len(strings.Split(iText, "\n")[0])
	tText, tRefs, tCut, tTotal := renderAXSnapshot(nodes, 0, "", 0, true, firstLineLen)
	if !tCut || tTotal <= firstLineLen {
		t.Fatalf("expected truncation at %d bytes: cut=%v total=%d text=%q", firstLineLen, tCut, tTotal, tText)
	}
	if len(tRefs) != 1 || tRefs["e1"].BackendNodeID != 10 {
		t.Errorf("truncated snapshot refs = %+v, want only e1", tRefs)
	}

	// Cyclic nodes must not recurse infinitely.
	cycA := &accessibility.Node{NodeID: "a", Role: strAXValue("group"), Name: strAXValue("A"), ChildIDs: []accessibility.NodeID{"b"}}
	cycB := &accessibility.Node{NodeID: "b", Role: strAXValue("group"), Name: strAXValue("B"), ChildIDs: []accessibility.NodeID{"a"}}
	cText, _, _, _ := renderAXSnapshot([]*accessibility.Node{cycA, cycB}, 0, "", 0, false, 10_000)
	if !strings.Contains(cText, `"A"`) || !strings.Contains(cText, `"B"`) {
		t.Errorf("cyclic render output = %q", cText)
	}

	// FrameID propagates down from ancestor RootWebArea to child elements.
	subNodes := []*accessibility.Node{
		{NodeID: "r", Role: strAXValue("RootWebArea"), FrameID: cdp.FrameID("sub-9"), ChildIDs: []accessibility.NodeID{"b"}},
		{NodeID: "b", Role: strAXValue("button"), Name: strAXValue("SubBtn"), BackendDOMNodeID: 77},
	}
	_, subRefs, _, _ := renderAXSnapshot(subNodes, 0, "", 0, false, 10_000)
	if subRefs["e1"].FrameID != "sub-9" {
		t.Errorf("inherited FrameID = %q, want sub-9", subRefs["e1"].FrameID)
	}

	// interactive_only respects structural maxDepth.
	deepNodes := []*accessibility.Node{
		{NodeID: "r", Role: strAXValue("RootWebArea"), ChildIDs: []accessibility.NodeID{"sec"}},
		{NodeID: "sec", Role: strAXValue("region"), Name: strAXValue("Box"), ChildIDs: []accessibility.NodeID{"deepBtn"}},
		{NodeID: "deepBtn", Role: strAXValue("button"), Name: strAXValue("Deep"), BackendDOMNodeID: 88},
	}
	_, d1Refs, _, _ := renderAXSnapshot(deepNodes, 0, "", 1, true, 10_000)
	if len(d1Refs) != 0 {
		t.Errorf("depth=1 interactive_only should exclude depth-2 button, got %+v", d1Refs)
	}
	_, d2Refs, _, _ := renderAXSnapshot(deepNodes, 0, "", 2, true, 10_000)
	if len(d2Refs) != 1 {
		t.Errorf("depth=2 interactive_only should include depth-2 button, got %+v", d2Refs)
	}
}
