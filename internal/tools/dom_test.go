package tools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestQuery(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	out := callTool[queryOutput](t, "query", map[string]any{
		"tab":      tabID,
		"selector": ".item",
	})
	if out.Total != 3 || len(out.Elements) != 3 {
		t.Errorf("query total=%d len=%d, want 3", out.Total, len(out.Elements))
	}
	if len(out.Elements) > 0 && out.Elements[0].TagName != "li" {
		t.Errorf("first element tag = %q, want li", out.Elements[0].TagName)
	}
}

func TestGetDOMHTMLAndText(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	htmlOut := callTool[getDOMOutput](t, "get_dom", map[string]any{
		"tab":      tabID,
		"format":   "html",
		"selector": "#title",
	})
	if !strings.Contains(htmlOut.Content, "<h1") || !strings.Contains(htmlOut.Content, "Hello World") {
		t.Errorf("get_dom(html) = %q, want h1 with Hello World", htmlOut.Content)
	}

	innerOut := callTool[getDOMOutput](t, "get_dom", map[string]any{
		"tab":      tabID,
		"format":   "html",
		"selector": "#title",
		"outer":    false,
	})
	if strings.Contains(innerOut.Content, "<h1") || !strings.Contains(innerOut.Content, "Hello World") {
		t.Errorf("get_dom(html, outer=false) = %q", innerOut.Content)
	}

	textOut := callTool[getDOMOutput](t, "get_dom", map[string]any{
		"tab":      tabID,
		"format":   "text",
		"selector": "#title",
	})
	if strings.TrimSpace(textOut.Content) != "Hello World" {
		t.Errorf("get_dom(text) = %q, want Hello World", textOut.Content)
	}
}

func TestGetDOMDefaultsAndTruncation(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	htmlOut := callTool[getDOMOutput](t, "get_dom", map[string]any{
		"tab":    tabID,
		"format": "html",
	})
	if !strings.Contains(htmlOut.Content, "<html") || !strings.Contains(htmlOut.Content, "Page Two") {
		t.Errorf("get_dom(html) default = %q", htmlOut.Content)
	}

	textOut := callTool[getDOMOutput](t, "get_dom", map[string]any{
		"tab":    tabID,
		"format": "text",
	})
	if !strings.Contains(textOut.Content, "Page Two") {
		t.Errorf("get_dom(text) default = %q", textOut.Content)
	}

	// max_bytes cap truncates cleanly and reports TotalBytes.
	capped := callTool[getDOMOutput](t, "get_dom", map[string]any{
		"tab":       tabID,
		"format":    "html",
		"max_bytes": 20,
	})
	if !capped.Truncated || len(capped.Content) > 20 || capped.TotalBytes <= 20 {
		t.Errorf("get_dom capped = %+v, want Truncated=true len<=20 TotalBytes>20", capped)
	}

	// Validation: bad format, negative max_bytes, ref+selector mutual exclusion.
	if err := callToolExpectErr(t, "get_dom", map[string]any{"tab": tabID, "format": "xml"}); !strings.Contains(err, "format") {
		t.Errorf("invalid format err = %q", err)
	}
	if err := callToolExpectErr(t, "get_dom", map[string]any{"tab": tabID, "format": "text", "max_bytes": -1}); !strings.Contains(err, "max_bytes") {
		t.Errorf("negative max_bytes err = %q", err)
	}
	if err := callToolExpectErr(t, "get_dom", map[string]any{"tab": tabID, "format": "text", "ref": "e1", "selector": "h1"}); !strings.Contains(err, "mutually exclusive") {
		t.Errorf("ref+selector err = %q", err)
	}
}

func TestGetDOMMissingSelector(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	selectorTimeout(t, tabID, "get_dom",
		map[string]any{"format": "html"},
		map[string]any{"format": "html", "selector": "#title"},
	)
	selectorTimeout(t, tabID, "get_dom",
		map[string]any{"format": "text"},
		map[string]any{"format": "text", "selector": "#title"},
	)
}

func TestGetDOMTextPermutations(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	tests := []struct {
		name      string
		selector  string
		hidden    bool
		wantErr   bool
		wantEmpty bool
		wantText  string
	}{
		{"visible/hidden=false", "#title", false, false, false, "Hello World"},
		{"visible/hidden=true", "#title", true, false, false, "Hello World"},
		{"hidden-element/hidden=false", "#hidden-el", false, false, true, ""},
		{"hidden-element/hidden=true", "#hidden-el", true, false, false, "Hidden content"},
		{"first-match-hidden/hidden=false", ".mv-item", false, false, true, ""},
		{"first-match-hidden/hidden=true", ".mv-item", true, false, false, "Hidden paragraph"},
		{"non-existing/hidden=false", "#does-not-exist", false, true, false, ""},
		{"non-existing/hidden=true", "#does-not-exist", true, true, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := map[string]any{
				"tab":      tabID,
				"format":   "text",
				"selector": tt.selector,
			}
			if tt.hidden {
				args["hidden"] = true
			}
			if tt.wantErr {
				args["timeout"] = 500
				if errText := callToolExpectErr(t, "get_dom", args); errText == "" {
					t.Error("expected error for non-existing element")
				}
				return
			}
			out := callTool[getDOMOutput](t, "get_dom", args)
			if tt.wantEmpty {
				if out.Content != "" {
					t.Errorf("got %q, want empty", out.Content)
				}
				return
			}
			if !strings.Contains(out.Content, tt.wantText) {
				t.Errorf("got %q, want substring %q", out.Content, tt.wantText)
			}
		})
	}
}

func TestGetDOMHiddenBodyAndNested(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	visible := callTool[getDOMOutput](t, "get_dom", map[string]any{"tab": tabID, "format": "text", "hidden": false})
	if !strings.Contains(visible.Content, "Hello World") || strings.Contains(visible.Content, "Hidden content") {
		t.Errorf("visible body text = %q", visible.Content)
	}
	withHidden := callTool[getDOMOutput](t, "get_dom", map[string]any{"tab": tabID, "format": "text", "hidden": true})
	if !strings.Contains(withHidden.Content, "Hidden content") {
		t.Errorf("hidden=true body text missing hidden content: %q", withHidden.Content)
	}

	tab2 := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tab2)

	nested := callTool[getDOMOutput](t, "get_dom", map[string]any{"tab": tab2, "format": "text", "selector": "#nested-text"})
	for _, want := range []string{"Hello", "bold", "italic"} {
		if !strings.Contains(nested.Content, want) {
			t.Errorf("nested text missing %q: %q", want, nested.Content)
		}
	}
	empty := callTool[getDOMOutput](t, "get_dom", map[string]any{"tab": tab2, "format": "text", "selector": "#empty-el"})
	if empty.Content != "" {
		t.Errorf("empty element text = %q, want empty", empty.Content)
	}
	innerNested := callTool[getDOMOutput](t, "get_dom", map[string]any{"tab": tab2, "format": "html", "selector": "#nested-text", "outer": false})
	if strings.Contains(innerNested.Content, `<div id="nested-text"`) || !strings.Contains(innerNested.Content, "<span>") || !strings.Contains(innerNested.Content, "<strong>") {
		t.Errorf("inner nested HTML = %q", innerNested.Content)
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
	callTool[navigateOutput](t, "navigate", map[string]any{"tab": tabID, "url": fixtureURL("page2.html")})
	errText := callToolExpectErr(t, "evaluate", map[string]any{
		"tab":        tabID,
		"ref":        btnRef,
		"expression": `return el.id`,
	})
	if !strings.Contains(errText, "stale") {
		t.Errorf("evaluate(stale ref) err = %q, want mention of stale", errText)
	}
}

func TestQueryFeaturesAndMutationSafety(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	if err := callToolExpectErr(t, "query", map[string]any{"tab": tabID, "selector": ""}); !strings.Contains(err, "selector") {
		t.Errorf("empty selector err = %q", err)
	}
	if err := callToolExpectErr(t, "query", map[string]any{"tab": tabID, "selector": ".item", "limit": -1}); !strings.Contains(err, "limit") {
		t.Errorf("negative limit err = %q", err)
	}
	selectorTimeout(t, tabID, "query", nil, map[string]any{"selector": ".item"})

	limited := callTool[queryOutput](t, "query", map[string]any{"tab": tabID, "selector": ".item", "limit": 2})
	if limited.Total != 3 || len(limited.Elements) != 2 {
		t.Errorf("limit=2: total=%d len=%d", limited.Total, len(limited.Elements))
	}

	full := callTool[queryOutput](t, "query", map[string]any{
		"tab":            tabID,
		"selector":       "#title",
		"outer_html":     true,
		"bbox":           true,
		"computed_style": []string{"display", "font-family"},
	})
	if len(full.Elements) != 1 {
		t.Fatalf("len = %d, want 1", len(full.Elements))
	}
	e := full.Elements[0]
	if e.TagName != "h1" || !strings.Contains(e.OuterHTML, "<h1") || e.BBox == nil || e.BBox.Width <= 0 || e.ComputedStyle["display"] != "block" || e.Attributes["id"] != "title" {
		t.Errorf("query full fields = %+v", e)
	}

	noAttrs := callTool[queryOutput](t, "query", map[string]any{"tab": tabID, "selector": "#title", "attributes": false, "text": false})
	if len(noAttrs.Elements[0].Attributes) != 0 || noAttrs.Elements[0].Text != "" {
		t.Errorf("attributes/text disabled = %+v", noAttrs.Elements[0])
	}

	// Delayed element appears within selector timeout.
	tabDelayed := navigateToFixture(t, "delayed.html")
	defer closeTab(t, tabDelayed)
	delayed := callTool[queryOutput](t, "query", map[string]any{"tab": tabDelayed, "selector": "#delayed-element"})
	if delayed.Total != 1 || !strings.Contains(delayed.Elements[0].Text, "appeared") {
		t.Errorf("delayed query = %+v", delayed)
	}

	// Default limit=10 on 12 items + display:none partial bbox + mutation safety.
	tab2 := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tab2)
	defLim := callTool[queryOutput](t, "query", map[string]any{"tab": tab2, "selector": ".query-item", "limit": 0})
	if defLim.Total != 12 || len(defLim.Elements) != 10 {
		t.Errorf("default limit: total=%d len=%d, want 12/10", defLim.Total, len(defLim.Elements))
	}
	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tab2,
		"expression": `document.body.insertAdjacentHTML('beforeend', '<div id="hidden-q" style="display:none">Hidden</div>')`,
	})
	hiddenQ := callTool[queryOutput](t, "query", map[string]any{"tab": tab2, "selector": "#hidden-q", "outer_html": true, "bbox": true})
	if len(hiddenQ.Elements) != 1 || hiddenQ.Elements[0].OuterHTML == "" {
		t.Errorf("hidden query = %+v", hiddenQ)
	}
}
