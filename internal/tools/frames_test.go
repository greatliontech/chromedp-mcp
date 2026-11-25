package tools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestListFramesAndSameOriginTargeting(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/top.html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><html><body>
			<script>window.__who = 'top';</script>
			<h1 id="heading">Top Heading</h1>
			<div id="top-only">Only Top</div>
			<iframe name="child-frame" src="/child.html#sec1" width="400" height="300"></iframe>
		</body></html>`))
	})
	mux.HandleFunc("/child.html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><html><body>
			<script>window.__who = 'child';</script>
			<h1 id="heading" style="color: rgb(255, 0, 0)">Child Heading</h1>
			<p id="secret" style="display:none">Hidden Child</p>
			<div style="display:none"><span id="hidden-ancestor">Inside Hidden</span></div>
			<p id="vis-hidden" style="visibility:hidden">Invisible Text</p>
			<ul class="list"><li class="row" data-k="1" style="color: rgb(0, 128, 0)">One</li><li class="row" data-k="2">Two</li></ul>
			<iframe name="grandchild-frame" src="/grandchild.html" width="200" height="100"></iframe>
		</body></html>`))
	})
	mux.HandleFunc("/child2.html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><html><body>
			<script>window.__who = 'child2';</script>
			<h2 id="after-nav">Navigated Child</h2>
		</body></html>`))
	})
	mux.HandleFunc("/grandchild.html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><html><body>
			<script>window.__who = 'grandchild';</script>
			<span id="leaf">Grandchild Leaf</span>
		</body></html>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)
	callTool[NavigateOutput](t, "navigate", map[string]any{
		"tab": tabID,
		"url": srv.URL + "/top.html",
	})

	// Wait until all 3 frames are listed and reachable.
	var list ListFramesOutput
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		list = callTool[ListFramesOutput](t, "list_frames", map[string]any{"tab": tabID})
		reachableCount := 0
		for _, f := range list.Frames {
			if f.Reachable {
				reachableCount++
			}
		}
		if len(list.Frames) == 3 && reachableCount == 3 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if len(list.Frames) != 3 {
		t.Fatalf("list_frames len = %d, want 3: %+v", len(list.Frames), list.Frames)
	}

	var mainF, childF, grandF FrameInfo
	for _, f := range list.Frames {
		switch {
		case f.IsMain:
			mainF = f
		case f.Name == "child-frame":
			childF = f
		case f.Name == "grandchild-frame":
			grandF = f
		}
	}
	if mainF.ID == "" || !mainF.Reachable || mainF.ParentID != "" {
		t.Fatalf("unexpected main frame: %+v", mainF)
	}
	if childF.ID == "" || !childF.Reachable || childF.ParentID != mainF.ID || !strings.HasSuffix(childF.URL, "/child.html#sec1") {
		t.Fatalf("unexpected child frame: %+v", childF)
	}
	if grandF.ID == "" || !grandF.Reachable || grandF.ParentID != childF.ID || !strings.HasSuffix(grandF.URL, "/grandchild.html") {
		t.Fatalf("unexpected grandchild frame: %+v", grandF)
	}

	// evaluate: top (default + explicit main frame), child, grandchild, selector, promise, truncation.
	for _, args := range []map[string]any{
		{"tab": tabID, "expression": "window.__who"},
		{"tab": tabID, "frame": mainF.ID, "expression": "window.__who"},
	} {
		ev := callTool[EvaluateOutput](t, "evaluate", args)
		if string(ev.Result) != `"top"` {
			t.Errorf("top evaluate(%v) = %s, want %q", args, ev.Result, "top")
		}
	}
	evChild := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"frame":      childF.ID,
		"expression": "window.__who",
	})
	if string(evChild.Result) != `"child"` {
		t.Errorf("child evaluate = %s, want %q", evChild.Result, "child")
	}
	evSel := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"frame":      childF.ID,
		"selector":   "#heading",
		"expression": "return el.textContent",
	})
	if string(evSel.Result) != `"Child Heading"` {
		t.Errorf("child selector evaluate = %s, want %q", evSel.Result, "Child Heading")
	}
	evPromise := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"frame":      childF.ID,
		"expression": "Promise.resolve('async-child')",
	})
	if string(evPromise.Result) != `"async-child"` {
		t.Errorf("child promise evaluate = %s, want %q", evPromise.Result, "async-child")
	}
	evNoAwait := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":           tabID,
		"frame":         childF.ID,
		"expression":    "Promise.resolve('async-child')",
		"await_promise": false,
	})
	if string(evNoAwait.Result) != `{}` {
		t.Errorf("child await_promise=false evaluate = %s, want {}", evNoAwait.Result)
	}
	evTrunc := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":              tabID,
		"frame":            childF.ID,
		"expression":       "'0123456789abcdef'",
		"max_result_bytes": 5,
	})
	if !evTrunc.Truncated || evTrunc.TotalBytes <= 5 {
		t.Errorf("child truncated evaluate = %+v, want Truncated=true TotalBytes>5", evTrunc)
	}
	evGrand := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"frame":      grandF.ID,
		"selector":   "#leaf",
		"expression": "return el.textContent",
	})
	if string(evGrand.Result) != `"Grandchild Leaf"` {
		t.Errorf("grandchild evaluate = %s, want %q", evGrand.Result, "Grandchild Leaf")
	}

	// query inside child frame (including computed_style, attributes, outer_html, bbox).
	qOut := callTool[QueryOutput](t, "query", map[string]any{
		"tab":            tabID,
		"frame":          childF.ID,
		"selector":       ".row",
		"limit":          1,
		"attributes":     true,
		"computed_style": []string{"color"},
		"outer_html":     true,
		"bbox":           true,
	})
	if qOut.Total != 2 || len(qOut.Elements) != 1 {
		t.Fatalf("child query = %+v, want Total=2 len=1", qOut)
	}
	el := qOut.Elements[0]
	if el.TagName != "li" || el.Attributes["data-k"] != "1" || el.Text != "One" || !strings.Contains(el.OuterHTML, `data-k="1"`) || el.BBox == nil || el.ComputedStyle["color"] != "rgb(0, 128, 0)" {
		t.Errorf("unexpected child query element: %+v", el)
	}

	// Frame isolation: #top-only exists only in top frame and must NOT resolve in child frame.
	if errText := callToolExpectErr(t, "query", map[string]any{
		"tab":      tabID,
		"frame":    childF.ID,
		"selector": "#top-only",
		"timeout":  200,
	}); !strings.Contains(errText, childF.ID) {
		t.Errorf("expected frame-scoped timeout error for #top-only in child frame, got %q", errText)
	}

	// Invalid CSS selector in frame must fail fast (not wait for 5s timeout).
	start := time.Now()
	errText := callToolExpectErr(t, "query", map[string]any{
		"tab":      tabID,
		"frame":    childF.ID,
		"selector": "[[",
		"timeout":  5000,
	})
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Errorf("invalid selector in frame took %v (err=%q), want fast failure", elapsed, errText)
	}

	// get_html inside child frame: full default, outer=true, outer=false.
	fullHTML := callTool[GetHTMLOutput](t, "get_html", map[string]any{
		"tab":   tabID,
		"frame": childF.ID,
	})
	if !strings.Contains(fullHTML.HTML, "Child Heading") || strings.Contains(fullHTML.HTML, "Top Heading") {
		t.Errorf("child full get_html = %q", fullHTML.HTML)
	}
	outerHTML := callTool[GetHTMLOutput](t, "get_html", map[string]any{
		"tab":      tabID,
		"frame":    childF.ID,
		"selector": "#heading",
		"outer":    true,
	})
	if !strings.HasPrefix(outerHTML.HTML, `<h1 id="heading"`) || !strings.HasSuffix(outerHTML.HTML, `>Child Heading</h1>`) {
		t.Errorf("child outer get_html = %q", outerHTML.HTML)
	}
	innerHTML := callTool[GetHTMLOutput](t, "get_html", map[string]any{
		"tab":      tabID,
		"frame":    childF.ID,
		"selector": "#heading",
		"outer":    false,
	})
	if innerHTML.HTML != "Child Heading" {
		t.Errorf("child inner get_html = %q, want %q", innerHTML.HTML, "Child Heading")
	}
	// get_html isolation: #top-only does not exist in child frame.
	if errText := callToolExpectErr(t, "get_html", map[string]any{
		"tab":      tabID,
		"frame":    childF.ID,
		"selector": "#top-only",
		"timeout":  150,
	}); !strings.Contains(errText, childF.ID) {
		t.Errorf("expected frame-scoped get_html timeout error, got %q", errText)
	}

	// get_text inside child frame: body default, display:none, display:none ancestor, visibility:hidden.
	bodyText := callTool[GetTextOutput](t, "get_text", map[string]any{
		"tab":   tabID,
		"frame": childF.ID,
	})
	if !strings.Contains(bodyText.Text, "Child Heading") || strings.Contains(bodyText.Text, "Top Heading") || strings.Contains(bodyText.Text, "Hidden Child") {
		t.Errorf("child body get_text = %q", bodyText.Text)
	}
	for _, tc := range []struct {
		selector string
		wantOff  string
		wantOn   string
	}{
		{"#secret", "", "Hidden Child"},
		{"#hidden-ancestor", "", "Inside Hidden"},
		{"#vis-hidden", "", "Invisible Text"},
	} {
		off := callTool[GetTextOutput](t, "get_text", map[string]any{
			"tab": tabID, "frame": childF.ID, "selector": tc.selector, "hidden": false,
		})
		if strings.TrimSpace(off.Text) != tc.wantOff {
			t.Errorf("child get_text(%s, hidden=false) = %q, want %q", tc.selector, off.Text, tc.wantOff)
		}
		on := callTool[GetTextOutput](t, "get_text", map[string]any{
			"tab": tabID, "frame": childF.ID, "selector": tc.selector, "hidden": true,
		})
		if on.Text != tc.wantOn {
			t.Errorf("child get_text(%s, hidden=true) = %q, want %q", tc.selector, on.Text, tc.wantOn)
		}
	}
	// get_text isolation: #top-only does not exist in child frame.
	if errText := callToolExpectErr(t, "get_text", map[string]any{
		"tab":      tabID,
		"frame":    childF.ID,
		"selector": "#top-only",
		"timeout":  150,
	}); !strings.Contains(errText, childF.ID) {
		t.Errorf("expected frame-scoped get_text timeout error, got %q", errText)
	}

	// Dynamic same-origin iframe navigation: frame ID persists, grandchild disappears,
	// and waitForSelector resolves #after-nav in the navigated child frame.
	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `document.querySelector('iframe[name="child-frame"]').src = '/child2.html'; true`,
	})
	navText := callTool[GetTextOutput](t, "get_text", map[string]any{
		"tab":      tabID,
		"frame":    childF.ID,
		"selector": "#after-nav",
		"timeout":  3000,
	})
	if navText.Text != "Navigated Child" {
		t.Errorf("post-nav child get_text = %q, want %q", navText.Text, "Navigated Child")
	}
	afterNavFrames := callTool[ListFramesOutput](t, "list_frames", map[string]any{"tab": tabID})
	if len(afterNavFrames.Frames) != 2 {
		t.Errorf("after child nav to frameless child2.html, list_frames len = %d, want 2: %+v", len(afterNavFrames.Frames), afterNavFrames.Frames)
	}
}

func TestListFramesOOPIFAndUnknownFrameErrors(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/xorigin.html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><html><body><p>Cross-Origin</p></body></html>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	crossURL := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1) + "/xorigin.html"

	mux.HandleFunc("/host.html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><html><body><h1>Host</h1><iframe src="` + crossURL + `"></iframe></body></html>`))
	})

	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)
	callTool[NavigateOutput](t, "navigate", map[string]any{
		"tab": tabID,
		"url": srv.URL + "/host.html",
	})

	// Wait until the cross-origin OOPIF appears in list_frames with reachable=false.
	var mainID string
	var oopif FrameInfo
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		list := callTool[ListFramesOutput](t, "list_frames", map[string]any{"tab": tabID})
		mainID = ""
		oopif = FrameInfo{}
		for _, f := range list.Frames {
			if f.IsMain {
				mainID = f.ID
			} else if !f.Reachable {
				oopif = f
			}
		}
		if mainID != "" && oopif.ID != "" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if oopif.ID == "" {
		raw := callToolRaw(t, "list_frames", map[string]any{"tab": tabID})
		b, _ := json.Marshal(raw)
		t.Fatalf("expected cross-origin OOPIF with reachable=false in list_frames, got: %s", b)
	}
	if oopif.ParentID != mainID {
		t.Errorf("OOPIF parent_id = %q, want main frame %q", oopif.ParentID, mainID)
	}
	oopifID := oopif.ID

	// All 4 frame-capable tools must reject the OOPIF cleanly with an out-of-process error,
	// and reject unknown frame IDs with a not-found error mentioning list_frames.
	toolCalls := []struct {
		name string
		args func(frame string) map[string]any
	}{
		{"evaluate", func(f string) map[string]any { return map[string]any{"tab": tabID, "frame": f, "expression": "1+1"} }},
		{"query", func(f string) map[string]any { return map[string]any{"tab": tabID, "frame": f, "selector": "body"} }},
		{"get_html", func(f string) map[string]any { return map[string]any{"tab": tabID, "frame": f} }},
		{"get_text", func(f string) map[string]any { return map[string]any{"tab": tabID, "frame": f} }},
	}
	for _, tc := range toolCalls {
		errOOPIF := callToolExpectErr(t, tc.name, tc.args(oopifID))
		if !strings.Contains(errOOPIF, "out-of-process") {
			t.Errorf("%s(oopif) err = %q, want mention of out-of-process", tc.name, errOOPIF)
		}
		errUnknown := callToolExpectErr(t, tc.name, tc.args("nonexistent-frame"))
		if !strings.Contains(errUnknown, "not found") || !strings.Contains(errUnknown, "list_frames") {
			t.Errorf("%s(unknown) err = %q, want mention of not found and list_frames", tc.name, errUnknown)
		}
	}
}

func TestListFramesFailsFastWhenDialogOpen(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `setTimeout(function() { alert('blocks-frames'); }, 10); true`,
	})
	defer handleDialog(t, tabID, map[string]any{"accept": true})

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if callTool[GetDialogOutput](t, "get_dialog", map[string]any{"tab": tabID}).Open {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if !callTool[GetDialogOutput](t, "get_dialog", map[string]any{"tab": tabID}).Open {
		t.Fatal("timed out waiting for alert dialog")
	}

	start := time.Now()
	errText := callToolExpectErr(t, "list_frames", map[string]any{"tab": tabID})
	elapsed := time.Since(start)
	if elapsed > 500*time.Millisecond {
		t.Fatalf("list_frames with open dialog took %v, want fast failure", elapsed)
	}
	if !strings.Contains(errText, "get_dialog") || !strings.Contains(errText, "handle_dialog") {
		t.Errorf("list_frames open-dialog err = %q, want guidance to get_dialog/handle_dialog", errText)
	}
}
