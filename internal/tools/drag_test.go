package tools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDragAndDropHTML5AndPointerWithObservation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><html><head><style>
			.box { width: 100px; height: 60px; margin: 20px; display: inline-block; border: 1px solid #333; }
		</style></head><body>
			<div>
				<div id="src-html5" class="box" draggable="true">HTML5 Drag</div>
				<div id="dst-html5" class="box">HTML5 Drop</div>
				<div id="src-step1" class="box" draggable="true">Step1 Drag</div>
				<div id="dst-step1" class="box">Step1 Drop</div>
			</div>
			<div>
				<div id="src-ptr" class="box">Pointer Drag</div>
				<div id="dst-ptr" class="box">Pointer Drop</div>
			</div>
			<button id="after-btn">After</button>
			<div id="src-far" class="box" draggable="true" style="display:block; margin-top: 20px;">Far Drag</div>
			<div style="height: 1800px;"></div>
			<div id="dst-far" class="box" style="display:block;">Far Drop</div>
			<script>
				window.__html5Events = [];
				window.__droppedData = null;
				const srcH = document.getElementById('src-html5');
				const dstH = document.getElementById('dst-html5');
				srcH.addEventListener('dragstart', e => {
					e.dataTransfer.setData('text/plain', 'payload-42');
					window.__html5Events.push('dragstart');
				});
				dstH.addEventListener('dragover', e => {
					e.preventDefault();
					window.__html5Events.push('dragover');
				});
				dstH.addEventListener('drop', e => {
					e.preventDefault();
					window.__droppedData = e.dataTransfer.getData('text/plain');
					window.__html5Events.push('drop');
					const tag = document.createElement('span');
					tag.className = 'dropped';
					tag.textContent = window.__droppedData;
					dstH.appendChild(tag);
					console.log('dropped:' + window.__droppedData);
				});
				dstH.addEventListener('mouseup', () => {
					window.__html5Events.push('SPURIOUS_MOUSEUP');
				});

				window.__step1Data = null;
				document.getElementById('src-step1').addEventListener('dragstart', e => {
					e.dataTransfer.setData('text/plain', 'step1-ok');
				});
				document.getElementById('dst-step1').addEventListener('dragover', e => e.preventDefault());
				document.getElementById('dst-step1').addEventListener('drop', e => {
					e.preventDefault();
					window.__step1Data = e.dataTransfer.getData('text/plain');
				});

				window.__farData = null;
				document.getElementById('src-far').addEventListener('dragstart', e => {
					e.dataTransfer.setData('text/plain', 'far-99');
				});
				document.getElementById('dst-far').addEventListener('dragover', e => e.preventDefault());
				document.getElementById('dst-far').addEventListener('drop', e => {
					e.preventDefault();
					window.__farData = e.dataTransfer.getData('text/plain');
				});

				window.__ptrDown = false;
				window.__ptrMoves = 0;
				window.__ptrDone = false;
				const srcP = document.getElementById('src-ptr');
				const dstP = document.getElementById('dst-ptr');
				srcP.addEventListener('mousedown', e => {
					if (e.button === 0) window.__ptrDown = true;
				});
				window.addEventListener('mousemove', e => {
					if (window.__ptrDown && e.buttons === 1) window.__ptrMoves++;
				});
				dstP.addEventListener('mouseup', e => {
					if (window.__ptrDown && e.button === 0) {
						window.__ptrDown = false;
						window.__ptrDone = true;
					}
				});

				window.__afterClicked = false;
				document.getElementById('after-btn').addEventListener('click', () => {
					window.__afterClicked = true;
				});
			</script>
		</body></html>`))
	}))
	defer srv.Close()

	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)
	callTool[navigateOutput](t, "navigate", map[string]any{"tab": tabID, "url": srv.URL + "/"})

	// 1. HTML5 native drag-and-drop with inline observation window.
	out := callTool[actionOutput](t, "drag_and_drop", map[string]any{
		"tab":               tabID,
		"source_selector":   "#src-html5",
		"target_selector":   "#dst-html5",
		"steps":             6,
		"observe_window_ms": 200,
	})
	if out.Observed == nil || !out.Observed.HasAnyEffect || out.Observed.DOMMutations == 0 {
		t.Fatalf("HTML5 drag_and_drop observation = %+v, want HasAnyEffect=true DOMMutations>0", out.Observed)
	}
	evHTML5 := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `({data: window.__droppedData, events: window.__html5Events})`,
	})
	var html5Res struct {
		Data   string   `json:"data"`
		Events []string `json:"events"`
	}
	if err := json.Unmarshal(evHTML5.Result, &html5Res); err != nil {
		t.Fatalf("unmarshal html5 result: %v (raw=%s)", err, evHTML5.Result)
	}
	if html5Res.Data != "payload-42" || !strings.Contains(strings.Join(html5Res.Events, ","), "dragstart") || !strings.Contains(strings.Join(html5Res.Events, ","), "drop") {
		t.Fatalf("HTML5 drag result = %+v, want data='payload-42' with dragstart & drop", html5Res)
	}
	if strings.Contains(strings.Join(html5Res.Events, ","), "SPURIOUS_MOUSEUP") {
		t.Fatalf("HTML5 drop emitted spurious DOM mouseup on drop target: %+v", html5Res.Events)
	}

	// Verify HTML5 drag works with steps=1 (async Input.dragIntercepted).
	callTool[actionOutput](t, "drag_and_drop", map[string]any{
		"tab":             tabID,
		"source_selector": "#src-step1",
		"target_selector": "#dst-step1",
		"steps":           1,
	})
	if evS1 := callTool[evaluateOutput](t, "evaluate", map[string]any{"tab": tabID, "expression": `window.__step1Data`}); string(evS1.Result) != `"step1-ok"` {
		t.Fatalf("steps=1 HTML5 drag __step1Data = %s, want \"step1-ok\"", evS1.Result)
	}

	// Verify mouse state is clean after HTML5 drop by clicking a normal button.
	callTool[clickOutput](t, "click", map[string]any{"tab": tabID, "selector": "#after-btn"})
	evClick := callTool[evaluateOutput](t, "evaluate", map[string]any{"tab": tabID, "expression": `window.__afterClicked`})
	if string(evClick.Result) != "true" {
		t.Fatalf("post-drag click failed: __afterClicked = %s", evClick.Result)
	}

	// Verify HTML5 drag across >1 viewport height separation (1800px spacer).
	callTool[actionOutput](t, "drag_and_drop", map[string]any{
		"tab":             tabID,
		"source_selector": "#src-far",
		"target_selector": "#dst-far",
		"steps":           5,
	})
	if evFar := callTool[evaluateOutput](t, "evaluate", map[string]any{"tab": tabID, "expression": `window.__farData`}); string(evFar.Result) != `"far-99"` {
		t.Fatalf(">1-viewport HTML5 drag __farData = %s, want \"far-99\"", evFar.Result)
	}

	// 2. Pointer/mouse-based drag-and-drop (non-draggable element).
	callTool[actionOutput](t, "drag_and_drop", map[string]any{
		"tab":             tabID,
		"source_selector": "#src-ptr",
		"target_selector": "#dst-ptr",
		"steps":           8,
	})
	evPtr := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `({done: window.__ptrDone, moves: window.__ptrMoves})`,
	})
	var ptrRes struct {
		Done  bool `json:"done"`
		Moves int  `json:"moves"`
	}
	if err := json.Unmarshal(evPtr.Result, &ptrRes); err != nil {
		t.Fatalf("unmarshal pointer result: %v (raw=%s)", err, evPtr.Result)
	}
	if !ptrRes.Done || ptrRes.Moves < 8 {
		t.Fatalf("pointer drag result = %+v, want done=true moves>=8", ptrRes)
	}

	// 3. Validation errors.
	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"empty_source", map[string]any{"tab": tabID, "source_selector": "", "target_selector": "#dst-ptr"}, "source"},
		{"empty_target", map[string]any{"tab": tabID, "source_selector": "#src-ptr", "target_selector": ""}, "target"},
		{"negative_steps", map[string]any{"tab": tabID, "source_selector": "#src-ptr", "target_selector": "#dst-ptr", "steps": -1}, "steps"},
		{"too_many_steps", map[string]any{"tab": tabID, "source_selector": "#src-ptr", "target_selector": "#dst-ptr", "steps": 101}, "steps"},
		{"missing_source", map[string]any{"tab": tabID, "source_selector": "#nope", "target_selector": "#dst-ptr", "timeout": 300}, "nope"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if errText := callToolExpectErr(t, "drag_and_drop", tc.args); !strings.Contains(errText, tc.want) {
				t.Errorf("%s err = %q, want substring %q", tc.name, errText, tc.want)
			}
		})
	}
}
