package tools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/cdp"
	jsonv2 "github.com/chromedp/cdproto/cdp/jsonv2"
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

func TestSnapshotStructureScopeDepthAndRefs(t *testing.T) {
	tabID := navigateToFixture(t, "accessibility.html")
	defer closeTab(t, tabID)

	if err := callToolExpectErr(t, "snapshot", map[string]any{"tab": tabID, "depth": -1}); !strings.Contains(err, "depth") {
		t.Errorf("negative depth err = %q", err)
	}
	if err := callToolExpectErr(t, "snapshot", map[string]any{"tab": tabID, "max_bytes": -1}); !strings.Contains(err, "max_bytes") {
		t.Errorf("negative max_bytes err = %q", err)
	}
	if err := callToolExpectErr(t, "snapshot", map[string]any{"tab": tabID, "selector": "#missing-xyz", "timeout": 400}); err == "" {
		t.Error("expected error for missing snapshot selector")
	}

	full := callTool[snapshotOutput](t, "snapshot", map[string]any{"tab": tabID})
	if full.Snapshot == "" || full.RefCount == 0 {
		t.Fatalf("empty full snapshot: %+v", full)
	}
	if !strings.Contains(full.URL, "accessibility.html") {
		t.Errorf("snapshot URL = %q, want accessibility.html", full.URL)
	}

	// Interactive-only filters down to strictly fewer lines/refs.
	inter := callTool[snapshotOutput](t, "snapshot", map[string]any{"tab": tabID, "interactive_only": true})
	if inter.RefCount == 0 || inter.RefCount >= full.RefCount {
		t.Errorf("interactive_only refs=%d vs full=%d", inter.RefCount, full.RefCount)
	}

	// Scoped selector="nav" includes nav descendants and excludes unrelated siblings.
	scoped := callTool[snapshotOutput](t, "snapshot", map[string]any{"tab": tabID, "selector": "nav"})
	if !strings.Contains(scoped.Snapshot, "Main navigation") || !strings.Contains(scoped.Snapshot, "Home") {
		t.Fatalf("scoped nav snapshot missing expected items:\n%s", scoped.Snapshot)
	}
	if strings.Contains(scoped.Snapshot, "Contact form") || strings.Contains(scoped.Snapshot, "Section One") {
		t.Errorf("scoped nav snapshot leaked siblings:\n%s", scoped.Snapshot)
	}

	// Depth=1 on nav is non-empty and strictly smaller than unlimited nav.
	shallow := callTool[snapshotOutput](t, "snapshot", map[string]any{"tab": tabID, "selector": "nav", "depth": 1})
	if shallow.Snapshot == "" || len(shallow.Snapshot) >= len(scoped.Snapshot) {
		t.Errorf("depth=1 nav (%dB) should be non-empty and smaller than unlimited (%dB): %q", len(shallow.Snapshot), len(scoped.Snapshot), shallow.Snapshot)
	}

	// Depth=1 WITHOUT selector still includes semantic children behind generic wrappers.
	topDepth1 := callTool[snapshotOutput](t, "snapshot", map[string]any{"tab": tabID, "depth": 1})
	if topDepth1.RefCount == 0 || len(topDepth1.Snapshot) >= len(full.Snapshot) {
		t.Errorf("depth=1 full-page snapshot should be non-empty with refs and smaller than full: refs=%d len=%d/%d\n%s", topDepth1.RefCount, len(topDepth1.Snapshot), len(full.Snapshot), topDepth1.Snapshot)
	}

	// Truncation with small max_bytes.
	capped := callTool[snapshotOutput](t, "snapshot", map[string]any{"tab": tabID, "max_bytes": 120})
	if !capped.Truncated || len(capped.Snapshot) > 120 || capped.TotalBytes <= 120 {
		t.Errorf("capped snapshot = %+v", capped)
	}

	// snapshot must fail fast when a JS dialog is open rather than hanging.
	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `setTimeout(() => alert('blocking'), 10); true`,
	})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		d := callTool[dialogOutput](t, "dialog", map[string]any{"tab": tabID, "action": "get"})
		if d.Open {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	dlgStart := time.Now()
	if err := callToolExpectErr(t, "snapshot", map[string]any{"tab": tabID}); !strings.Contains(err, "dialog") {
		t.Errorf("snapshot while dialog open err = %q, want dialog error", err)
	}
	if time.Since(dlgStart) > 2*time.Second {
		t.Errorf("snapshot while dialog open did not fail fast (%v)", time.Since(dlgStart))
	}
	callTool[dialogOutput](t, "dialog", map[string]any{"tab": tabID, "action": "accept"})
}

func TestShadowDOMPiercingAndSnapshotRefsEndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html>
<html>
<head><title>Shadow Demo</title></head>
<body>
  <div id="host-a"></div>
  <div id="host-b"></div>
  <div id="closed-host"></div>
  <svg id="svg-box" width="120" height="30"><text y="20">Hello SVG</text></svg>
  <script>
    const ra = document.getElementById('host-a').attachShadow({mode: 'open'});
    ra.innerHTML = '<div class="card"><span class="label">Card A</span><div class="inner"></div></div>';
    const rInner = ra.querySelector('.inner').attachShadow({mode: 'open'});
    rInner.innerHTML = '<button id="deep-btn" data-role="secret">Deep Action</button>';

    const rb = document.getElementById('host-b').attachShadow({mode: 'open'});
    rb.innerHTML = '<div class="card"><span class="label">Card B</span></div>';

    const rc = document.getElementById('closed-host').attachShadow({mode: 'closed'});
    rc.innerHTML = '<button id="closed-btn">Closed</button>';
  </script>
</body>
</html>`))
	}))
	defer srv.Close()

	tabOut := callTool[tabNewOutput](t, "tab_new", map[string]any{"url": srv.URL})
	defer closeTab(t, tabOut.TabID)
	tabID := tabOut.TabID

	// query with >>> pierces single and nested Shadow DOM across multiple hosts.
	qCards := callTool[queryOutput](t, "query", map[string]any{
		"tab":      tabID,
		"selector": "div >>> .label",
	})
	if qCards.Total != 2 {
		t.Fatalf("query 'div >>> .label' total = %d, want 2 (%+v)", qCards.Total, qCards)
	}
	qDeep := callTool[queryOutput](t, "query", map[string]any{
		"tab":      tabID,
		"selector": "#host-a >>> .inner >>> #deep-btn",
	})
	if qDeep.Total != 1 || strings.TrimSpace(qDeep.Elements[0].Text) != "Deep Action" {
		t.Fatalf("nested shadow query = %+v", qDeep)
	}

	// get_dom and evaluate work through >>> shadow selectors.
	domDeep := callTool[getDOMOutput](t, "get_dom", map[string]any{
		"tab":      tabID,
		"format":   "text",
		"selector": "#host-a >>> .inner >>> #deep-btn",
	})
	if strings.TrimSpace(domDeep.Content) != "Deep Action" {
		t.Errorf("get_dom shadow text = %q, want Deep Action", domDeep.Content)
	}
	evalShadow := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"selector":   "#host-a >>> .inner >>> #deep-btn",
		"expression": `return el.getAttribute('data-role')`,
	})
	if string(evalShadow.Result) != `"secret"` {
		t.Errorf("evaluate shadow = %s, want \"secret\"", evalShadow.Result)
	}

	// Invalid CSS in a later >>> segment fails fast even when host doesn't match.
	badStart := time.Now()
	if errText := callToolExpectErr(t, "query", map[string]any{
		"tab":      tabID,
		"selector": "#missing-host >>> [=bad]",
		"timeout":  2000,
	}); errText == "" || time.Since(badStart) > 1500*time.Millisecond {
		t.Errorf("invalid CSS after missing shadow host took %v (err=%q), want fast failure", time.Since(badStart), errText)
	}

	// Closed shadow root is skipped cleanly (times out as not found, no crash).
	if errText := callToolExpectErr(t, "query", map[string]any{
		"tab":      tabID,
		"selector": "#closed-host >>> #closed-btn",
		"timeout":  200,
	}); errText == "" {
		t.Error("expected closed shadow root query to error as not found")
	}

	// Visible SVG element returns text under get_dom(format=text, hidden=false).
	svgText := callTool[getDOMOutput](t, "get_dom", map[string]any{
		"tab":      tabID,
		"format":   "text",
		"selector": "#svg-box",
	})
	if !strings.Contains(svgText.Content, "Hello SVG") {
		t.Errorf("get_dom(text) on SVG = %q, want Hello SVG", svgText.Content)
	}

	// snapshot(selector=...) supports >>> shadow piercing too.
	scopedShadowSnap := callTool[snapshotOutput](t, "snapshot", map[string]any{
		"tab":      tabID,
		"selector": "#host-a >>> .inner",
	})
	if !strings.Contains(scopedShadowSnap.Snapshot, "Deep Action") || strings.Contains(scopedShadowSnap.Snapshot, "Card B") {
		t.Errorf("scoped shadow snapshot = %q", scopedShadowSnap.Snapshot)
	}

	// snapshot discovers the button inside nested Shadow DOM and assigns a ref
	// usable directly in get_dom(ref) and evaluate(ref).
	snap := callTool[snapshotOutput](t, "snapshot", map[string]any{
		"tab":              tabID,
		"interactive_only": true,
	})
	re := regexp.MustCompile(`button "Deep Action" \[ref=(e\d+)\]`)
	m := re.FindStringSubmatch(snap.Snapshot)
	if len(m) != 2 {
		t.Fatalf("snapshot did not include Deep Action button with ref:\n%s", snap.Snapshot)
	}
	btnRef := m[1]

	refDOM := callTool[getDOMOutput](t, "get_dom", map[string]any{
		"tab":    tabID,
		"format": "html",
		"ref":    btnRef,
	})
	if !strings.Contains(refDOM.Content, `data-role="secret"`) {
		t.Errorf("get_dom(ref=%s) = %q, want data-role=\"secret\"", btnRef, refDOM.Content)
	}

	refEval := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"ref":        btnRef,
		"expression": `return Promise.resolve(el.id + ':' + el.textContent); // trailing comment`,
	})
	var gotStr string
	if err := json.Unmarshal(refEval.Result, &gotStr); err != nil || gotStr != "deep-btn:Deep Action" {
		t.Errorf("evaluate(ref=%s) = %s (err=%v), want \"deep-btn:Deep Action\"", btnRef, refEval.Result, err)
	}

	// evaluate(ref) handles undefined, primitives, reassignable el, and thrown primitive exceptions.
	if out := callTool[evaluateOutput](t, "evaluate", map[string]any{"tab": tabID, "ref": btnRef, "expression": `el = null; return 0;`}); string(out.Result) != "0" {
		t.Errorf("evaluate(ref, 0) = %s, want 0", out.Result)
	}
	if out := callTool[evaluateOutput](t, "evaluate", map[string]any{"tab": tabID, "ref": btnRef, "expression": `return;`}); string(out.Result) != "null" {
		t.Errorf("evaluate(ref, undefined) = %s, want null", out.Result)
	}
	if errText := callToolExpectErr(t, "evaluate", map[string]any{"tab": tabID, "ref": btnRef, "expression": `throw "boom"`}); !strings.Contains(errText, "boom") {
		t.Errorf("evaluate(ref, throw primitive) err = %q, want boom", errText)
	}

	// Removing the element from DOM makes its ref fail cleanly with detached error.
	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"ref":        btnRef,
		"expression": `el.remove(); return true;`,
	})
	if errText := callToolExpectErr(t, "get_dom", map[string]any{"tab": tabID, "format": "text", "ref": btnRef}); !strings.Contains(errText, "no longer attached") {
		t.Errorf("detached ref err = %q, want no longer attached", errText)
	}

	// Navigating invalidates refs with a clear stale message.
	callTool[navigateOutput](t, "navigate", map[string]any{"tab": tabID, "url": fixtureURL(t, "page2.html")})
	errText := callToolExpectErr(t, "evaluate", map[string]any{
		"tab":        tabID,
		"ref":        btnRef,
		"expression": `return el.id`,
	})
	if !strings.Contains(errText, "stale") {
		t.Errorf("evaluate(stale ref) err = %q, want mention of stale", errText)
	}
}
