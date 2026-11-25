package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/collector"
)

func TestClickRightDoubleClickDispatchesRightButton(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": `(function() {
			window.__events = [];
			var btn = document.createElement('button');
			btn.id = 'rt-dbl';
			btn.textContent = 'Right Double';
			btn.addEventListener('mousedown', function(e) {
				window.__events.push({button: e.button, detail: e.detail});
			});
			btn.addEventListener('contextmenu', function(e) { e.preventDefault(); });
			document.body.appendChild(btn);
		})()`,
	})

	callTool[ClickOutput](t, "click", map[string]any{
		"tab":         tabID,
		"selector":    "#rt-dbl",
		"button":      "right",
		"click_count": 2,
	})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `window.__events`,
	})
	var events []struct {
		Button int `json:"button"`
		Detail int `json:"detail"`
	}
	if err := json.Unmarshal(out.Result, &events); err != nil {
		t.Fatalf("unmarshal events: %v (raw: %s)", err, out.Result)
	}
	if len(events) != 2 {
		t.Fatalf("got %d mousedown events, want 2: %+v", len(events), events)
	}
	for i, ev := range events {
		if ev.Button != 2 {
			t.Errorf("event[%d].button = %d, want 2 (right button)", i, ev.Button)
		}
		if ev.Detail != i+1 {
			t.Errorf("event[%d].detail = %d, want %d", i, ev.Detail, i+1)
		}
	}
}

func TestClickOffscreenRightButtonScrollsIntoView(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": `(function() {
			window.__rightClicked = false;
			var spacer = document.createElement('div');
			spacer.style.height = '2500px';
			document.body.appendChild(spacer);
			var btn = document.createElement('button');
			btn.id = 'below-fold-btn';
			btn.textContent = 'Below Fold';
			btn.addEventListener('mousedown', function(e) {
				if (e.button === 2) window.__rightClicked = true;
			});
			btn.addEventListener('contextmenu', function(e) { e.preventDefault(); });
			document.body.appendChild(btn);
		})()`,
	})

	callTool[ClickOutput](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#below-fold-btn",
		"button":   "right",
	})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `window.__rightClicked`,
	})
	if string(out.Result) != "true" {
		t.Fatalf("offscreen right click did not reach target: __rightClicked = %s", out.Result)
	}
}

func TestClickSelfHidingAndSelfRemovingNoFalseWarning(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": `(function() {
			var hideBtn = document.createElement('button');
			hideBtn.id = 'self-hide';
			hideBtn.textContent = 'Hide Me';
			hideBtn.addEventListener('click', function() { hideBtn.style.display = 'none'; });
			document.body.appendChild(hideBtn);

			var rmBtn = document.createElement('button');
			rmBtn.id = 'self-remove';
			rmBtn.textContent = 'Remove Me';
			rmBtn.addEventListener('click', function() { rmBtn.remove(); });
			document.body.appendChild(rmBtn);
		})()`,
	})

	hideOut := callTool[ClickOutput](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#self-hide",
	})
	if hideOut.Warning != "" {
		t.Errorf("self-hiding click produced false-positive warning: %q", hideOut.Warning)
	}
	if hideOut.Visible {
		t.Errorf("expected post-click Visible=false for self-hiding button, got true")
	}

	rmOut := callTool[ClickOutput](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#self-remove",
	})
	if rmOut.Warning != "" {
		t.Errorf("self-removing click produced false-positive warning: %q", rmOut.Warning)
	}
}

func TestClickValidationRejectsInvalidButtonAndNegativeCount(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "click", map[string]any{
		"tab":      tabID,
		"selector": "h1",
		"button":   "side",
	})
	if !strings.Contains(errText, "button") || !strings.Contains(errText, "side") {
		t.Errorf("expected invalid button error, got: %s", errText)
	}

	errText = callToolExpectErr(t, "click", map[string]any{
		"tab":         tabID,
		"selector":    "h1",
		"click_count": -1,
	})
	if !strings.Contains(errText, "click_count") {
		t.Errorf("expected click_count validation error, got: %s", errText)
	}
}

func TestClickOpeningAlertDoesNotHang(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	tabObj, err := harness.mgr.ResolveTab("", tabID)
	if err != nil {
		t.Fatalf("ResolveTab: %v", err)
	}

	// Open an alert and wait until CDP confirms the dialog is active so
	// finalizeClickState is guaranteed to execute against a blocked renderer.
	dialogOpen := make(chan struct{}, 1)
	lctx, lcancel := context.WithCancel(tabObj.Context())
	defer lcancel()
	if err := chromedp.Listen(lctx, func(ev any) {
		if _, ok := ev.(*page.EventJavascriptDialogOpening); ok {
			select {
			case dialogOpen <- struct{}{}:
			default:
			}
		}
	}); err != nil {
		t.Fatalf("chromedp.Listen: %v", err)
	}

	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `setTimeout(function() { alert('hello'); }, 10); true`,
	})
	select {
	case <-dialogOpen:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for alert dialog to open")
	}
	defer handleDialog(t, tabID, map[string]any{"accept": true})

	pre := ClickOutput{PointerEvents: "auto", Visible: true}

	// Fast-path: when Dialog collector knows a dialog is open, finalizeClickState
	// returns immediately without stalling for the 500ms probe timeout.
	start := time.Now()
	fast := finalizeClickState(tabObj.Context(), tabObj.Dialog, "h1", pre)
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Errorf("finalizeClickState with open Dialog took %v, want immediate short-circuit", elapsed)
	}
	if !fast.Visible || fast.Warning != "" {
		t.Errorf("finalizeClickState fast-path = %+v, want Visible=true Warning=\"\"", fast)
	}

	// Defense-in-depth fallback: even if dialog state has not arrived yet (nil),
	// the 500ms timeout bounds probeClickTarget cleanly.
	start = time.Now()
	got := finalizeClickState(tabObj.Context(), nil, "h1", pre)
	elapsed := time.Since(start)
	if elapsed < 350*time.Millisecond || elapsed > 2500*time.Millisecond {
		t.Errorf("finalizeClickState fallback against open alert took %v, want bounded ~500ms", elapsed)
	}
	if !got.Visible || got.Warning != "" {
		t.Errorf("finalizeClickState fallback = %+v, want Visible=true Warning=\"\"", got)
	}
}

func TestClickDelayedVisibilityRefreshClearsStaleHiddenWarning(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": `(function() {
			window.__delayedClicked = false;
			var btn = document.createElement('button');
			btn.id = 'late-visible';
			btn.textContent = 'Appear Later';
			btn.style.display = 'none';
			btn.addEventListener('click', function() { window.__delayedClicked = true; });
			document.body.appendChild(btn);
			setTimeout(function() { btn.style.display = 'block'; }, 150);
		})()`,
	})

	out := callTool[ClickOutput](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#late-visible",
		"timeout":  2000,
	})
	if !out.Visible {
		t.Errorf("expected Visible=true after delayed reveal, got false")
	}
	if out.Warning != "" {
		t.Errorf("delayed-visibility click produced stale hidden warning: %q", out.Warning)
	}
	clicked := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `window.__delayedClicked`,
	})
	if string(clicked.Result) != "true" {
		t.Fatalf("expected click handler to fire, got %s", clicked.Result)
	}
}

func TestHoverOffscreenElementScrollsIntoView(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": `(function() {
			window.__hovered = false;
			var spacer = document.createElement('div');
			spacer.style.height = '2500px';
			document.body.appendChild(spacer);
			var target = document.createElement('div');
			target.id = 'below-fold-hover';
			target.style.width = '120px';
			target.style.height = '40px';
			target.textContent = 'Hover Me';
			target.addEventListener('mouseover', function() { window.__hovered = true; });
			document.body.appendChild(target);
		})()`,
	})

	callTool[ActionOutput](t, "hover", map[string]any{
		"tab":      tabID,
		"selector": "#below-fold-hover",
	})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `window.__hovered && document.querySelector('#below-fold-hover').matches(':hover')`,
	})
	if string(out.Result) != "true" {
		t.Fatalf("offscreen hover failed: result = %s", out.Result)
	}
}

func TestTypeDelayExceedsSelectorTimeout(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": `(function() {
			var inp = document.createElement('input');
			inp.id = 'slow-type';
			inp.value = 'old';
			document.body.appendChild(inp);
		})()`,
	})

	// 5 characters with 80ms inter-key delay (~320ms) exceeds timeout=200ms.
	// Typing must run on tctx after the selector wait so delay typing is not aborted.
	callTool[ActionOutput](t, "type", map[string]any{
		"tab":      tabID,
		"selector": "#slow-type",
		"text":     "abcde",
		"mode":     "replace",
		"delay":    80,
		"timeout":  200,
	})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `document.querySelector('#slow-type').value`,
	})
	if string(out.Result) != `"abcde"` {
		t.Fatalf("typed value = %s, want %q", out.Result, "abcde")
	}
}

func TestTypeReplaceContentEditableAndControlledInput(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": `(function() {
			var ce = document.createElement('div');
			ce.id = 'ce-box';
			ce.contentEditable = 'true';
			ce.textContent = 'existing content';
			document.body.appendChild(ce);

			// Simulate React's value tracker: an instance property overrides
			// el.value so plain el.value = '' does not fire the native setter.
			var inp = document.createElement('input');
			inp.id = 'controlled-inp';
			inp.value = 'stale';
			window.__inputEventValues = [];
			var nativeDesc = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value');
			Object.defineProperty(inp, 'value', {
				configurable: true,
				get: function() { return nativeDesc.get.call(this); },
				set: function(v) {
					// Deliberately ignore instance setter to prove native prototype setter is used.
					window.__instanceSetterCalled = true;
				}
			});
			inp.addEventListener('input', function() {
				window.__inputEventValues.push(nativeDesc.get.call(inp));
			});
			document.body.appendChild(inp);
		})()`,
	})

	callTool[ActionOutput](t, "type", map[string]any{
		"tab":      tabID,
		"selector": "#ce-box",
		"text":     "fresh",
		"mode":     "replace",
	})
	ceOut := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `document.querySelector('#ce-box').textContent`,
	})
	if string(ceOut.Result) != `"fresh"` {
		t.Errorf("contenteditable textContent = %s, want %q", ceOut.Result, "fresh")
	}

	callTool[ActionOutput](t, "type", map[string]any{
		"tab":      tabID,
		"selector": "#controlled-inp",
		"text":     "ok",
		"mode":     "replace",
	})
	inpOut := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": `({
			value: document.querySelector('#controlled-inp').value,
			instanceCalled: !!window.__instanceSetterCalled,
			events: window.__inputEventValues
		})`,
	})
	var state struct {
		Value          string   `json:"value"`
		InstanceCalled bool     `json:"instanceCalled"`
		Events         []string `json:"events"`
	}
	if err := json.Unmarshal(inpOut.Result, &state); err != nil {
		t.Fatalf("unmarshal controlled input state: %v", err)
	}
	if state.Value != "ok" {
		t.Errorf("controlled input value = %q, want %q", state.Value, "ok")
	}
	if state.InstanceCalled {
		t.Errorf("expected native prototype setter to be called, not overridden instance setter")
	}
	if len(state.Events) == 0 || state.Events[0] != "" {
		t.Errorf("expected first input event after clear to observe empty value, got %v", state.Events)
	}
}

func TestTypeNegativeDelayRejected(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "type", map[string]any{
		"tab":      tabID,
		"selector": "body",
		"text":     "hi",
		"mode":     "replace",
		"delay":    -5,
	})
	if !strings.Contains(errText, "delay") {
		t.Errorf("expected delay validation error, got: %s", errText)
	}
}

func TestTypeDetachedOnClearTimesOutCleanly(t *testing.T) {
	for _, delay := range []int{0, 20} {
		tabID := navigateToFixture(t, "page2.html")
		callTool[EvaluateOutput](t, "evaluate", map[string]any{
			"tab": tabID,
			"expression": `(function() {
				var inp = document.createElement('input');
				inp.id = 'vanish-inp';
				inp.value = 'initial';
				inp.addEventListener('input', function() { inp.remove(); });
				document.body.appendChild(inp);
			})()`,
		})

		start := time.Now()
		errText := callToolExpectErr(t, "type", map[string]any{
			"tab":      tabID,
			"selector": "#vanish-inp",
			"text":     "next",
			"mode":     "replace",
			"delay":    delay,
			"timeout":  300,
		})
		elapsed := time.Since(start)
		closeTab(t, tabID)

		if elapsed > 2500*time.Millisecond {
			t.Errorf("delay=%d: type on detached target waited %v, expected bounded ~300ms timeout", delay, elapsed)
		}
		if !strings.Contains(errText, "timed out") && !strings.Contains(errText, "not found") {
			t.Errorf("delay=%d: expected selector timeout error, got: %s", delay, errText)
		}
	}
}

func TestSelectOptionValidationBeforeSelectorWait(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	start := time.Now()
	errText := callToolExpectErr(t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#does-not-exist-at-all",
		"timeout":  5000,
	})
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Errorf("missing criteria waited %v for nonexistent selector instead of failing immediately", elapsed)
	}
	if !strings.Contains(errText, "exactly one") {
		t.Errorf("expected criteria validation error, got: %s", errText)
	}

	start = time.Now()
	errText = callToolExpectErr(t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#does-not-exist-at-all",
		"index":    -1,
		"timeout":  5000,
	})
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Errorf("negative index waited %v for nonexistent selector instead of failing immediately", elapsed)
	}
	if !strings.Contains(errText, "index out of range") {
		t.Errorf("expected negative index validation error, got: %s", errText)
	}
}

func TestSelectOptionEmptyLabelAndNonSelectElement(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": `(function() {
			var sel = document.createElement('select');
			sel.id = 'empty-label-sel';
			var o1 = document.createElement('option');
			o1.value = 'v1';
			o1.text = 'One';
			o1.selected = true;
			var o2 = document.createElement('option');
			o2.value = 'blank';
			o2.text = '';
			sel.appendChild(o1);
			sel.appendChild(o2);
			document.body.appendChild(sel);
		})()`,
	})

	callTool[ActionOutput](t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#empty-label-sel",
		"label":    "",
	})
	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `document.querySelector('#empty-label-sel').value`,
	})
	if string(out.Result) != `"blank"` {
		t.Errorf("selected value for empty label = %s, want %q", out.Result, "blank")
	}

	errText := callToolExpectErr(t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "h1",
		"value":    "x",
	})
	if !strings.Contains(errText, "not a <select>") {
		t.Errorf("expected non-<select> error, got: %s", errText)
	}
}

func TestPressKeyCtrlShortcutSuppressesLiteralChar(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": `(function() {
			window.__keyEvents = [];
			window.__keyPressCount = 0;
			var inp = document.createElement('input');
			inp.id = 'shortcut-inp';
			inp.value = 'hello';
			inp.addEventListener('keydown', function(e) {
				window.__keyEvents.push({key: e.key, ctrl: e.ctrlKey, meta: e.metaKey});
			});
			inp.addEventListener('keypress', function() {
				window.__keyPressCount++;
			});
			document.body.appendChild(inp);
			inp.focus();
		})()`,
	})

	callTool[ActionOutput](t, "press_key", map[string]any{
		"tab":       tabID,
		"key":       "a",
		"modifiers": []string{"ctrl"},
	})
	callTool[ActionOutput](t, "press_key", map[string]any{
		"tab":       tabID,
		"key":       "a",
		"modifiers": []string{"meta"},
	})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": `({
			value: document.querySelector('#shortcut-inp').value,
			keyPressCount: window.__keyPressCount,
			events: window.__keyEvents
		})`,
	})
	var state struct {
		Value         string `json:"value"`
		KeyPressCount int    `json:"keyPressCount"`
		Events        []struct {
			Key  string `json:"key"`
			Ctrl bool   `json:"ctrl"`
			Meta bool   `json:"meta"`
		} `json:"events"`
	}
	if err := json.Unmarshal(out.Result, &state); err != nil {
		t.Fatalf("unmarshal key state: %v", err)
	}
	if state.Value != "hello" {
		t.Errorf("Ctrl+A / Meta+A mutated input value to %q, want %q", state.Value, "hello")
	}
	if state.KeyPressCount != 0 {
		t.Errorf("Ctrl+A / Meta+A emitted %d keypress events, want 0 (KeyChar must be suppressed)", state.KeyPressCount)
	}
	if len(state.Events) != 2 || !state.Events[0].Ctrl || !state.Events[1].Meta {
		t.Errorf("expected Ctrl+A and Meta+A keydown events, got %+v", state.Events)
	}
}

func TestPressKeySpaceAndInsert(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": `(function() {
			window.__keys = [];
			var inp = document.createElement('input');
			inp.id = 'named-keys-inp';
			inp.addEventListener('keydown', function(e) { window.__keys.push(e.key); });
			document.body.appendChild(inp);
			inp.focus();
		})()`,
	})

	callTool[ActionOutput](t, "press_key", map[string]any{"tab": tabID, "key": "Space"})
	callTool[ActionOutput](t, "press_key", map[string]any{"tab": tabID, "key": "Insert"})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `({value: document.querySelector('#named-keys-inp').value, keys: window.__keys})`,
	})
	var state struct {
		Value string   `json:"value"`
		Keys  []string `json:"keys"`
	}
	if err := json.Unmarshal(out.Result, &state); err != nil {
		t.Fatalf("unmarshal named key state: %v", err)
	}
	if state.Value != " " {
		t.Errorf("Space value = %q, want %q", state.Value, " ")
	}
	if len(state.Keys) != 2 || state.Keys[0] != " " || state.Keys[1] != "Insert" {
		t.Errorf("keydown keys = %v, want [\" \" \"Insert\"]", state.Keys)
	}
}

func TestUploadFilesPathValidation(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "upload_files", map[string]any{
		"tab":      tabID,
		"selector": "input[type=file]",
		"paths":    []string{"relative/file.txt"},
	})
	if !strings.Contains(errText, "absolute") {
		t.Errorf("expected absolute path error, got: %s", errText)
	}

	errText = callToolExpectErr(t, "upload_files", map[string]any{
		"tab":      tabID,
		"selector": "input[type=file]",
		"paths":    []string{"/nonexistent/path/chromedp-mcp-missing.txt"},
	})
	if !strings.Contains(errText, "no such file") && !strings.Contains(errText, "upload path") {
		t.Errorf("expected missing file error, got: %s", errText)
	}

	dir := t.TempDir()
	errText = callToolExpectErr(t, "upload_files", map[string]any{
		"tab":      tabID,
		"selector": "input[type=file]",
		"paths":    []string{dir},
	})
	if !strings.Contains(errText, "directory") {
		t.Errorf("expected directory error, got: %s", errText)
	}
}

func TestHandleDialogPromptTextAndEmptyString(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	// Case 1: explicit prompt text is returned to window.prompt.
	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `setTimeout(function() { window.__promptVal = prompt('Name?', 'default'); }, 20); true`,
	})
	handleDialog(t, tabID, map[string]any{"accept": true, "text": "alice"})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `window.__promptVal`,
	})
	if string(out.Result) != `"alice"` {
		t.Errorf("prompt result = %s, want %q", out.Result, "alice")
	}

	// Case 2: explicit empty string submits "".
	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `setTimeout(function() { window.__promptEmpty = prompt('Name?', 'default'); }, 20); true`,
	})
	handleDialog(t, tabID, map[string]any{"accept": true, "text": ""})

	out = callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `window.__promptEmpty`,
	})
	if string(out.Result) != `""` {
		t.Errorf("empty text prompt result = %s, want %q", out.Result, "")
	}
}

func TestGetDialogAndHandleDialogPromptDefaultVsEmpty(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	waitDialogOpen := func(wantType string) (GetDialogOutput, string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			rawRes := callToolRaw(t, "get_dialog", map[string]any{"tab": tabID})
			if rawRes.IsError {
				t.Fatalf("get_dialog returned error: %+v", rawRes.Content)
			}
			var rawJSON string
			for _, c := range rawRes.Content {
				if tc, ok := c.(*mcp.TextContent); ok {
					rawJSON = tc.Text
					break
				}
			}
			var d GetDialogOutput
			if err := json.Unmarshal([]byte(rawJSON), &d); err != nil {
				t.Fatalf("unmarshal get_dialog: %v", err)
			}
			if d.Open {
				if d.Dialog == nil {
					t.Fatalf("get_dialog Open=true with nil Dialog: %s", rawJSON)
				}
				if d.Dialog.Type != wantType {
					t.Fatalf("get_dialog type = %q, want %q", d.Dialog.Type, wantType)
				}
				return d, rawJSON
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %q dialog to appear in get_dialog", wantType)
		return GetDialogOutput{}, ""
	}

	// Initially no dialog is open.
	initState := callTool[GetDialogOutput](t, "get_dialog", map[string]any{"tab": tabID})
	if initState.Open || initState.Dialog != nil {
		t.Fatalf("expected Open=false Dialog=nil initially, got %+v", initState)
	}

	// 1. Alert dialog with empty message verifies both message:"" and
	// has_browser_handler:false are preserved in serialized JSON (not dropped
	// by omitempty), and immediate post-handle_dialog get_dialog sees open=false
	// without polling.
	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `setTimeout(function() { alert(''); }, 20); true`,
	})
	d, rawJSON := waitDialogOpen("alert")
	if d.Dialog.Message != "" || !strings.HasSuffix(d.Dialog.URL, "/page2.html") || d.Dialog.OpenedAt.IsZero() {
		t.Errorf("unexpected alert dialog state: %+v", d.Dialog)
	}
	if !strings.Contains(rawJSON, `"message":""`) {
		t.Errorf("get_dialog JSON omitted empty message: %s", rawJSON)
	}
	if !strings.Contains(rawJSON, `"has_browser_handler":`) {
		t.Errorf("get_dialog JSON omitted has_browser_handler: %s", rawJSON)
	}
	// Verify false has_browser_handler and empty message are never dropped by omitempty.
	falseOut, err := json.Marshal(GetDialogOutput{
		Open: true,
		Dialog: &collector.DialogState{
			Type:              "alert",
			Message:           "",
			HasBrowserHandler: false,
		},
	})
	if err != nil {
		t.Fatalf("marshal GetDialogOutput: %v", err)
	}
	if !strings.Contains(string(falseOut), `"has_browser_handler":false`) || !strings.Contains(string(falseOut), `"message":""`) {
		t.Errorf("GetDialogOutput JSON omitted false/empty fields: %s", falseOut)
	}
	callTool[struct{}](t, "handle_dialog", map[string]any{"tab": tabID, "accept": true})
	// ClearIfSeq guarantees immediate follow-up get_dialog reports closed.
	if after := callTool[GetDialogOutput](t, "get_dialog", map[string]any{"tab": tabID}); after.Open {
		t.Fatalf("get_dialog immediately after handle_dialog still reported open: %+v", after)
	}

	// 2. Confirm dialog inspection + dismiss.
	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `setTimeout(function() { window.__confirm = confirm('proceed?'); }, 20); true`,
	})
	d, _ = waitDialogOpen("confirm")
	if d.Dialog.Message != "proceed?" {
		t.Errorf("confirm message = %q, want %q", d.Dialog.Message, "proceed?")
	}
	callTool[struct{}](t, "handle_dialog", map[string]any{"tab": tabID, "accept": false})
	if after := callTool[GetDialogOutput](t, "get_dialog", map[string]any{"tab": tabID}); after.Open {
		t.Fatalf("get_dialog immediately after confirm dismiss still reported open: %+v", after)
	}
	if res := callTool[EvaluateOutput](t, "evaluate", map[string]any{"tab": tabID, "expression": `window.__confirm`}); string(res.Result) != "false" {
		t.Errorf("dismissed confirm result = %s, want false", res.Result)
	}

	// 3. Prompt dialog with omitted text preserves DefaultPrompt.
	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `setTimeout(function() { window.__promptDef = prompt('Your name?', 'keep-default'); }, 20); true`,
	})
	d, _ = waitDialogOpen("prompt")
	if d.Dialog.Message != "Your name?" || d.Dialog.DefaultPrompt != "keep-default" {
		t.Errorf("prompt dialog state = %+v, want message='Your name?' default_prompt='keep-default'", d.Dialog)
	}
	callTool[struct{}](t, "handle_dialog", map[string]any{"tab": tabID, "accept": true})
	if after := callTool[GetDialogOutput](t, "get_dialog", map[string]any{"tab": tabID}); after.Open {
		t.Fatalf("get_dialog immediately after prompt accept still reported open: %+v", after)
	}
	if res := callTool[EvaluateOutput](t, "evaluate", map[string]any{"tab": tabID, "expression": `window.__promptDef`}); string(res.Result) != `"keep-default"` {
		t.Errorf("omitted text prompt result = %s, want %q", res.Result, "keep-default")
	}

	// 4. Prompt dialog with explicit empty string submits "".
	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `setTimeout(function() { window.__promptBlank = prompt('Your name?', 'keep-default'); }, 20); true`,
	})
	waitDialogOpen("prompt")
	callTool[struct{}](t, "handle_dialog", map[string]any{"tab": tabID, "accept": true, "text": ""})
	if after := callTool[GetDialogOutput](t, "get_dialog", map[string]any{"tab": tabID}); after.Open {
		t.Fatalf("get_dialog immediately after empty prompt accept still reported open: %+v", after)
	}
	if res := callTool[EvaluateOutput](t, "evaluate", map[string]any{"tab": tabID, "expression": `window.__promptBlank`}); string(res.Result) != `""` {
		t.Errorf("explicit empty text prompt result = %s, want %q", res.Result, "")
	}
}

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
	callTool[NavigateOutput](t, "navigate", map[string]any{"tab": tabID, "url": srv.URL + "/"})

	// 1. HTML5 native drag-and-drop with inline observation window.
	out := callTool[ActionOutput](t, "drag_and_drop", map[string]any{
		"tab":               tabID,
		"source":            "#src-html5",
		"target":            "#dst-html5",
		"steps":             6,
		"observe_window_ms": 200,
	})
	if out.Observed == nil || !out.Observed.HasAnyEffect || out.Observed.DOMMutations == 0 {
		t.Fatalf("HTML5 drag_and_drop observation = %+v, want HasAnyEffect=true DOMMutations>0", out.Observed)
	}
	evHTML5 := callTool[EvaluateOutput](t, "evaluate", map[string]any{
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
	callTool[ActionOutput](t, "drag_and_drop", map[string]any{
		"tab":    tabID,
		"source": "#src-step1",
		"target": "#dst-step1",
		"steps":  1,
	})
	if evS1 := callTool[EvaluateOutput](t, "evaluate", map[string]any{"tab": tabID, "expression": `window.__step1Data`}); string(evS1.Result) != `"step1-ok"` {
		t.Fatalf("steps=1 HTML5 drag __step1Data = %s, want \"step1-ok\"", evS1.Result)
	}

	// Verify mouse state is clean after HTML5 drop by clicking a normal button.
	callTool[ClickOutput](t, "click", map[string]any{"tab": tabID, "selector": "#after-btn"})
	evClick := callTool[EvaluateOutput](t, "evaluate", map[string]any{"tab": tabID, "expression": `window.__afterClicked`})
	if string(evClick.Result) != "true" {
		t.Fatalf("post-drag click failed: __afterClicked = %s", evClick.Result)
	}

	// Verify HTML5 drag across >1 viewport height separation (1800px spacer).
	callTool[ActionOutput](t, "drag_and_drop", map[string]any{
		"tab":    tabID,
		"source": "#src-far",
		"target": "#dst-far",
		"steps":  5,
	})
	if evFar := callTool[EvaluateOutput](t, "evaluate", map[string]any{"tab": tabID, "expression": `window.__farData`}); string(evFar.Result) != `"far-99"` {
		t.Fatalf(">1-viewport HTML5 drag __farData = %s, want \"far-99\"", evFar.Result)
	}

	// 2. Pointer/mouse-based drag-and-drop (non-draggable element).
	callTool[ActionOutput](t, "drag_and_drop", map[string]any{
		"tab":    tabID,
		"source": "#src-ptr",
		"target": "#dst-ptr",
		"steps":  8,
	})
	evPtr := callTool[EvaluateOutput](t, "evaluate", map[string]any{
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
		{"empty_source", map[string]any{"tab": tabID, "source": "", "target": "#dst-ptr"}, "source"},
		{"empty_target", map[string]any{"tab": tabID, "source": "#src-ptr", "target": ""}, "target"},
		{"negative_steps", map[string]any{"tab": tabID, "source": "#src-ptr", "target": "#dst-ptr", "steps": -1}, "steps"},
		{"too_many_steps", map[string]any{"tab": tabID, "source": "#src-ptr", "target": "#dst-ptr", "steps": 101}, "steps"},
		{"missing_source", map[string]any{"tab": tabID, "source": "#nope", "target": "#dst-ptr", "timeout": 300}, "nope"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if errText := callToolExpectErr(t, "drag_and_drop", tc.args); !strings.Contains(errText, tc.want) {
				t.Errorf("%s err = %q, want substring %q", tc.name, errText, tc.want)
			}
		})
	}
}
