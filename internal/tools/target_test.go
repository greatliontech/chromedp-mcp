package tools

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	jsonv2 "github.com/chromedp/cdproto/cdp/jsonv2"
	"github.com/chromedp/cdproto/runtime"
)

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

func TestValidatePointerTargetUnit(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	if err := validatePointerTarget("", "", "", f(10), f(20), true); err != nil {
		t.Errorf("valid (x,y) failed: %v", err)
	}
	if err := validatePointerTarget("", "", "", f(10), nil, true); err == nil {
		t.Error("x without y should error")
	}
	if err := validatePointerTarget("", "", "", nil, f(20), true); err == nil {
		t.Error("y without x should error")
	}
	for _, bad := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if err := validatePointerTarget("", "", "", f(bad), f(0), true); err == nil {
			t.Errorf("non-finite x=%v should error", bad)
		}
		if err := validatePointerTarget("", "", "", f(0), f(bad), true); err == nil {
			t.Errorf("non-finite y=%v should error", bad)
		}
	}
	if err := validatePointerTarget("e1", "", "", f(1), f(2), true); err == nil {
		t.Error("coords + ref should error")
	}
	if err := validatePointerTarget("", "#a", "", f(1), f(2), true); err == nil {
		t.Error("coords + selector should error")
	}
	if err := validatePointerTarget("", "", "f1", f(1), f(2), true); err == nil {
		t.Error("coords + frame should error")
	}
	if err := validatePointerTarget("", "", "", nil, nil, true); err == nil {
		t.Error("requireOne with empty target should error")
	}
	if err := validatePointerTarget("", "", "", nil, nil, false); err != nil {
		t.Errorf("requireOne=false with empty target failed: %v", err)
	}
}

func TestInteractionWithSnapshotRefs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><html><head><style>
			.box { width: 90px; height: 50px; display: inline-block; border: 1px solid #333; margin: 10px; }
		</style></head><body>
			<button id="act-btn">Run Action</button>
			<form id="ref-form">
				<input id="ref-inp" aria-label="Search Box" value="start">
				<button type="submit">Send</button>
			</form>
			<select id="ref-sel" aria-label="Tags" multiple>
				<option value="go">Go</option>
				<option value="rust">Rust</option>
				<option value="zig">Zig</option>
			</select>
			<button id="hover-btn">Hover Me</button>
			<input id="ref-file" type="file" aria-label="Upload File">
			<div id="drag-src" role="button" aria-label="Drag Source" class="box" draggable="true">Src</div>
			<div id="drag-dst" role="region" aria-label="Drop Zone" class="box">Dst</div>
			<div style="height: 1500px;"></div>
			<button id="far-btn">Bottom Target</button>
			<script>
				window.__clicks = [];
				document.getElementById('act-btn').addEventListener('click', e => {
					window.__clicks.push({shift: e.shiftKey, ctrl: e.ctrlKey});
				});
				window.__submittedVal = '';
				document.getElementById('ref-form').addEventListener('submit', e => {
					e.preventDefault();
					window.__submittedVal = document.getElementById('ref-inp').value;
				});
				window.__hovered = false;
				document.getElementById('hover-btn').addEventListener('mouseover', () => { window.__hovered = true; });
				window.__dropped = '';
				document.getElementById('drag-src').addEventListener('dragstart', e => e.dataTransfer.setData('text/plain', 'via-ref'));
				document.getElementById('drag-dst').addEventListener('dragover', e => e.preventDefault());
				document.getElementById('drag-dst').addEventListener('drop', e => {
					e.preventDefault();
					window.__dropped = e.dataTransfer.getData('text/plain');
				});
			</script>
		</body></html>`))
	}))
	defer srv.Close()

	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)
	callTool[navigateOutput](t, "navigate", map[string]any{"tab": tabID, "url": srv.URL + "/"})

	snap := callTool[snapshotOutput](t, "snapshot", map[string]any{"tab": tabID})
	btnRef := findSnapshotRef(t, snap.Snapshot, `"Run Action"`)
	inpRef := findSnapshotRef(t, snap.Snapshot, `"Search Box"`)
	selRef := findSnapshotRef(t, snap.Snapshot, `"Tags"`)
	hoverRef := findSnapshotRef(t, snap.Snapshot, `"Hover Me"`)
	fileRef := findSnapshotRef(t, snap.Snapshot, `"Upload File"`)
	srcRef := findSnapshotRef(t, snap.Snapshot, `"Drag Source"`)
	dstRef := findSnapshotRef(t, snap.Snapshot, `"Drop Zone"`)
	farRef := findSnapshotRef(t, snap.Snapshot, `"Bottom Target"`)

	// 1. click(ref) with modifiers.
	callTool[clickOutput](t, "click", map[string]any{
		"tab":       tabID,
		"ref":       btnRef,
		"modifiers": []string{"shift"},
	})
	clicks := callTool[evaluateOutput](t, "evaluate", map[string]any{"tab": tabID, "expression": `window.__clicks`})
	if !strings.Contains(string(clicks.Result), `"shift":true`) {
		t.Errorf("click(ref, shift) = %s, want shift=true", clicks.Result)
	}

	// 2. type(ref, append) moves caret to end even if caret was placed at 0,
	//    then type(ref, replace, submit=true) submits the form.
	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `(function(){ var i = document.getElementById('ref-inp'); i.focus(); i.setSelectionRange(0, 0); })()`,
	})
	callTool[actionOutput](t, "type", map[string]any{
		"tab":  tabID,
		"ref":  inpRef,
		"text": "-end",
		"mode": "append",
	})
	val := callTool[evaluateOutput](t, "evaluate", map[string]any{"tab": tabID, "expression": `document.getElementById('ref-inp').value`})
	if string(val.Result) != `"start-end"` {
		t.Errorf("type(ref, append) value = %s, want \"start-end\"", val.Result)
	}
	callTool[actionOutput](t, "type", map[string]any{
		"tab":    tabID,
		"ref":    inpRef,
		"text":   "final",
		"mode":   "replace",
		"submit": true,
	})
	sub := callTool[evaluateOutput](t, "evaluate", map[string]any{"tab": tabID, "expression": `window.__submittedVal`})
	if string(sub.Result) != `"final"` {
		t.Errorf("type(ref, submit=true) submitted = %s, want \"final\"", sub.Result)
	}

	// 3. select_option(ref) multi-select + deselect all.
	callTool[actionOutput](t, "select_option", map[string]any{
		"tab":    tabID,
		"ref":    selRef,
		"values": []string{"go", "zig"},
	})
	selOut := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `Array.from(document.getElementById('ref-sel').selectedOptions).map(o => o.value).join(',')`,
	})
	if string(selOut.Result) != `"go,zig"` {
		t.Errorf("select_option(ref) = %s, want \"go,zig\"", selOut.Result)
	}
	callTool[actionOutput](t, "select_option", map[string]any{
		"tab":    tabID,
		"ref":    selRef,
		"values": []string{},
	})
	selEmpty := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `document.getElementById('ref-sel').selectedOptions.length`,
	})
	if string(selEmpty.Result) != "0" {
		t.Errorf("select_option(ref, []) length = %s, want 0", selEmpty.Result)
	}

	// 4. hover(ref).
	callTool[actionOutput](t, "hover", map[string]any{"tab": tabID, "ref": hoverRef})
	hov := callTool[evaluateOutput](t, "evaluate", map[string]any{"tab": tabID, "expression": `window.__hovered`})
	if string(hov.Result) != "true" {
		t.Errorf("hover(ref) __hovered = %s, want true", hov.Result)
	}

	// 5. press_key(ref).
	callTool[actionOutput](t, "press_key", map[string]any{"tab": tabID, "ref": inpRef, "key": "!"})
	val2 := callTool[evaluateOutput](t, "evaluate", map[string]any{"tab": tabID, "expression": `document.getElementById('ref-inp').value`})
	if !strings.Contains(string(val2.Result), "!") {
		t.Errorf("press_key(ref) value = %s, want to contain '!'", val2.Result)
	}

	// 6. upload_files(ref).
	tmpFile := filepath.Join(t.TempDir(), "via-ref.txt")
	if err := os.WriteFile(tmpFile, []byte("ok"), 0o644); err != nil {
		t.Fatalf("write tmpFile: %v", err)
	}
	callTool[actionOutput](t, "upload_files", map[string]any{
		"tab":   tabID,
		"ref":   fileRef,
		"paths": []string{tmpFile},
	})
	fName := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `document.getElementById('ref-file').files[0].name`,
	})
	if string(fName.Result) != `"via-ref.txt"` {
		t.Errorf("upload_files(ref) filename = %s, want \"via-ref.txt\"", fName.Result)
	}

	// 7. drag_and_drop(source_ref, target_ref).
	callTool[actionOutput](t, "drag_and_drop", map[string]any{
		"tab":        tabID,
		"source_ref": srcRef,
		"target_ref": dstRef,
		"steps":      5,
	})
	dropVal := callTool[evaluateOutput](t, "evaluate", map[string]any{"tab": tabID, "expression": `window.__dropped`})
	if string(dropVal.Result) != `"via-ref"` {
		t.Errorf("drag_and_drop(refs) = %s, want \"via-ref\"", dropVal.Result)
	}

	// 8. scroll(ref).
	scr := callTool[scrollOutput](t, "scroll", map[string]any{"tab": tabID, "ref": farRef})
	if scr.ScrollY <= 0 {
		t.Errorf("scroll(ref) ScrollY = %v, want > 0", scr.ScrollY)
	}

	// 9. Detached ref fails cleanly with snapshot guidance.
	callTool[evaluateOutput](t, "evaluate", map[string]any{"tab": tabID, "expression": `document.getElementById('act-btn').remove(); true`})
	if errText := callToolExpectErr(t, "click", map[string]any{"tab": tabID, "ref": btnRef}); !strings.Contains(errText, "snapshot") {
		t.Errorf("detached ref click err = %q, want snapshot guidance", errText)
	}
}

func TestInteractionShadowDOMAndSameOriginIframe(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/child.html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><html><body>
			<button id="if-btn">Frame Button</button>
			<input id="if-inp" aria-label="Frame Input" value="old">
			<select id="if-sel" aria-label="Frame Select">
				<option value="x">X</option>
				<option value="y">Y</option>
			</select>
			<div style="height: 1200px;"></div>
			<button id="if-bottom">Frame Bottom</button>
			<script>
				window.__ifClicked = 0;
				document.getElementById('if-btn').addEventListener('click', () => { window.__ifClicked++; });
			</script>
		</body></html>`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><html><body>
			<div id="outer-host"></div>
			<iframe id="child-frame" src="/child.html" style="width: 400px; height: 250px; border: 1px solid #000;"></iframe>
			<script>
				window.__shadowClicked = 0;
				window.__shadowHovered = false;
				window.__shadowDropped = '';
				const outer = document.getElementById('outer-host').attachShadow({mode: 'open'});
				outer.innerHTML = '<div id="inner-host"></div>';
				const inner = outer.getElementById('inner-host').attachShadow({mode: 'open'});
				inner.innerHTML =
					'<button id="s-btn">Shadow Click</button>' +
					'<input id="s-inp" value="init">' +
					'<select id="s-sel"><option value="one">One</option><option value="two">Two</option></select>' +
					'<input id="s-file" type="file">' +
					'<div id="s-src" draggable="true" style="width:60px;height:30px;display:inline-block;border:1px solid #000;">S</div>' +
					'<div id="s-dst" style="width:60px;height:30px;display:inline-block;border:1px solid #000;">D</div>';
				inner.getElementById('s-btn').addEventListener('click', () => { window.__shadowClicked++; });
				inner.getElementById('s-btn').addEventListener('mouseover', () => { window.__shadowHovered = true; });
				inner.getElementById('s-src').addEventListener('dragstart', e => e.dataTransfer.setData('text/plain', 'shadow-drop'));
				inner.getElementById('s-dst').addEventListener('dragover', e => e.preventDefault());
				inner.getElementById('s-dst').addEventListener('drop', e => {
					e.preventDefault();
					window.__shadowDropped = e.dataTransfer.getData('text/plain');
				});
			</script>
		</body></html>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)
	callTool[navigateOutput](t, "navigate", map[string]any{"tab": tabID, "url": srv.URL + "/"})

	// Wait for iframe load.
	callTool[struct{}](t, "wait_for", map[string]any{
		"tab":        tabID,
		"expression": `document.getElementById('child-frame').contentDocument && document.getElementById('child-frame').contentDocument.getElementById('if-btn') !== null`,
	})

	// Nested open Shadow DOM interactions via '>>>'.
	callTool[clickOutput](t, "click", map[string]any{"tab": tabID, "selector": "#outer-host >>> #inner-host >>> #s-btn"})
	callTool[actionOutput](t, "hover", map[string]any{"tab": tabID, "selector": "#outer-host >>> #inner-host >>> #s-btn"})
	callTool[actionOutput](t, "type", map[string]any{
		"tab":      tabID,
		"selector": "#outer-host >>> #inner-host >>> #s-inp",
		"text":     "shadow-typed",
		"mode":     "replace",
	})
	callTool[actionOutput](t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#outer-host >>> #inner-host >>> #s-sel",
		"values":   []string{"two"},
	})
	tmpFile := filepath.Join(t.TempDir(), "shadow.txt")
	if err := os.WriteFile(tmpFile, []byte("s"), 0o644); err != nil {
		t.Fatalf("write tmpFile: %v", err)
	}
	callTool[actionOutput](t, "upload_files", map[string]any{
		"tab":      tabID,
		"selector": "#outer-host >>> #inner-host >>> #s-file",
		"paths":    []string{tmpFile},
	})
	callTool[actionOutput](t, "drag_and_drop", map[string]any{
		"tab":             tabID,
		"source_selector": "#outer-host >>> #inner-host >>> #s-src",
		"target_selector": "#outer-host >>> #inner-host >>> #s-dst",
		"steps":           4,
	})

	shadowCheck := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": `(function() {
			const inner = document.getElementById('outer-host').shadowRoot.getElementById('inner-host').shadowRoot;
			return {
				clicks: window.__shadowClicked,
				hovered: window.__shadowHovered,
				val: inner.getElementById('s-inp').value,
				sel: inner.getElementById('s-sel').value,
				file: inner.getElementById('s-file').files[0]?.name || '',
				drop: window.__shadowDropped
			};
		})()`,
	})
	var sh struct {
		Clicks  int    `json:"clicks"`
		Hovered bool   `json:"hovered"`
		Val     string `json:"val"`
		Sel     string `json:"sel"`
		File    string `json:"file"`
		Drop    string `json:"drop"`
	}
	if err := json.Unmarshal(shadowCheck.Result, &sh); err != nil {
		t.Fatalf("unmarshal shadowCheck: %v (raw=%s)", err, shadowCheck.Result)
	}
	if sh.Clicks != 1 || !sh.Hovered || sh.Val != "shadow-typed" || sh.Sel != "two" || sh.File != "shadow.txt" || sh.Drop != "shadow-drop" {
		t.Errorf("shadow DOM state = %+v, want clicks=1 hovered=true val='shadow-typed' sel='two' file='shadow.txt' drop='shadow-drop'", sh)
	}

	// Same-origin iframe interactions via frame + selector AND via snapshot(frame) ref.
	frames := callTool[listFramesOutput](t, "list_frames", map[string]any{"tab": tabID})
	var childFrameID string
	for _, fr := range frames.Frames {
		if strings.HasSuffix(fr.URL, "/child.html") {
			childFrameID = fr.ID
			break
		}
	}
	if childFrameID == "" {
		t.Fatalf("child frame not found in %+v", frames.Frames)
	}

	callTool[clickOutput](t, "click", map[string]any{
		"tab":      tabID,
		"frame":    childFrameID,
		"selector": "#if-btn",
	})
	callTool[actionOutput](t, "type", map[string]any{
		"tab":      tabID,
		"frame":    childFrameID,
		"selector": "#if-inp",
		"text":     "in-frame",
		"mode":     "replace",
	})
	callTool[actionOutput](t, "select_option", map[string]any{
		"tab":      tabID,
		"frame":    childFrameID,
		"selector": "#if-sel",
		"values":   []string{"y"},
	})
	scrFrame := callTool[scrollOutput](t, "scroll", map[string]any{
		"tab":   tabID,
		"frame": childFrameID,
		"y":     200,
	})
	if scrFrame.ScrollY <= 0 {
		t.Errorf("scroll(frame, y=200) ScrollY = %v, want > 0", scrFrame.ScrollY)
	}

	// Now take a subframe snapshot, click the button via ref, and scroll to
	// #if-bottom via ref (must report subframe scroll_y > 0, not main-frame 0).
	fSnap := callTool[snapshotOutput](t, "snapshot", map[string]any{"tab": tabID, "frame": childFrameID})
	ifBtnRef := findSnapshotRef(t, fSnap.Snapshot, `"Frame Button"`)
	ifBottomRef := findSnapshotRef(t, fSnap.Snapshot, `"Frame Bottom"`)
	callTool[clickOutput](t, "click", map[string]any{"tab": tabID, "ref": ifBtnRef})
	scrSubRef := callTool[scrollOutput](t, "scroll", map[string]any{"tab": tabID, "ref": ifBottomRef})
	if scrSubRef.ScrollY <= 500 {
		t.Errorf("scroll(ref=<subframe bottom>) ScrollY = %v, want > 500 (subframe scroll, not main-frame 0)", scrSubRef.ScrollY)
	}

	ifCheck := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":   tabID,
		"frame": childFrameID,
		"expression": `({
			clicks: window.__ifClicked,
			val: document.getElementById('if-inp').value,
			sel: document.getElementById('if-sel').value
		})`,
	})
	var ifr struct {
		Clicks int    `json:"clicks"`
		Val    string `json:"val"`
		Sel    string `json:"sel"`
	}
	if err := json.Unmarshal(ifCheck.Result, &ifr); err != nil {
		t.Fatalf("unmarshal ifCheck: %v", err)
	}
	if ifr.Clicks != 2 || ifr.Val != "in-frame" || ifr.Sel != "y" {
		t.Errorf("iframe state = %+v, want clicks=2 val='in-frame' sel='y'", ifr)
	}
}

func TestInteractionViewportCoordinates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><html><body style="margin:0;">
			<button id="xy-btn" style="position:fixed;left:40px;top:40px;width:80px;height:40px;">XY</button>
			<div id="xy-src" draggable="true" style="position:fixed;left:40px;top:120px;width:80px;height:40px;border:1px solid #000;">Src</div>
			<div id="xy-dst" style="position:fixed;left:200px;top:120px;width:80px;height:40px;border:1px solid #000;">Dst</div>
			<script>
				window.__xyClick = null;
				window.__xyHover = false;
				window.__xyDrop = '';
				const btn = document.getElementById('xy-btn');
				btn.addEventListener('click', e => { window.__xyClick = {shift: e.shiftKey, ctrl: e.ctrlKey}; });
				btn.addEventListener('mouseover', () => { window.__xyHover = true; });
				document.getElementById('xy-src').addEventListener('dragstart', e => e.dataTransfer.setData('text/plain', 'xy-ok'));
				document.getElementById('xy-dst').addEventListener('dragover', e => e.preventDefault());
				document.getElementById('xy-dst').addEventListener('drop', e => {
					e.preventDefault();
					window.__xyDrop = e.dataTransfer.getData('text/plain');
				});
			</script>
		</body></html>`))
	}))
	defer srv.Close()

	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)
	callTool[navigateOutput](t, "navigate", map[string]any{"tab": tabID, "url": srv.URL + "/"})

	// Hover + Click by raw viewport coordinates with modifiers.
	callTool[actionOutput](t, "hover", map[string]any{"tab": tabID, "x": 80.0, "y": 60.0})
	callTool[clickOutput](t, "click", map[string]any{
		"tab":       tabID,
		"x":         80.0,
		"y":         60.0,
		"modifiers": []string{"shift", "ctrl"},
	})
	callTool[actionOutput](t, "drag_and_drop", map[string]any{
		"tab":      tabID,
		"source_x": 80.0,
		"source_y": 140.0,
		"target_x": 240.0,
		"target_y": 140.0,
		"steps":    4,
	})
	ev := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `({click: window.__xyClick, hover: window.__xyHover, drop: window.__xyDrop})`,
	})
	if !strings.Contains(string(ev.Result), `"shift":true`) ||
		!strings.Contains(string(ev.Result), `"ctrl":true`) ||
		!strings.Contains(string(ev.Result), `"hover":true`) ||
		!strings.Contains(string(ev.Result), `"drop":"xy-ok"`) {
		t.Errorf("coordinate interaction state = %s", ev.Result)
	}
}
