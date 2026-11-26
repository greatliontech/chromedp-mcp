package tools

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

	callTool[evaluateOutput](t, "evaluate", map[string]any{
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

	callTool[clickOutput](t, "click", map[string]any{
		"tab":         tabID,
		"selector":    "#rt-dbl",
		"button":      "right",
		"click_count": 2,
	})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
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

	callTool[evaluateOutput](t, "evaluate", map[string]any{
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

	callTool[clickOutput](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#below-fold-btn",
		"button":   "right",
	})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
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

	callTool[evaluateOutput](t, "evaluate", map[string]any{
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

	hideOut := callTool[clickOutput](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#self-hide",
	})
	if hideOut.Warning != "" {
		t.Errorf("self-hiding click produced false-positive warning: %q", hideOut.Warning)
	}
	if hideOut.Visible {
		t.Errorf("expected post-click Visible=false for self-hiding button, got true")
	}

	rmOut := callTool[clickOutput](t, "click", map[string]any{
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

	// Resolve h1's RemoteObjectID before opening the alert so
	// finalizeClickObject executes against a blocked renderer.
	var h1Obj *resolvedElement
	var tgtRef *chromedp.Target
	if err := chromedp.Do(tabObj.Context(), chromedp.Func(func(ctx context.Context, tgt *chromedp.Target) error {
		tgtRef = tgt
		var rerr error
		h1Obj, rerr = resolveElementObject(ctx, tgt, tabObj, "", "h1", "", 1000, false)
		return rerr
	})); err != nil {
		t.Fatalf("resolve h1: %v", err)
	}

	// Open an alert and wait until CDP confirms the dialog is active so
	// finalizeClickObject is guaranteed to execute against a blocked renderer.
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

	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `setTimeout(function() { alert('hello'); }, 10); true`,
	})
	select {
	case <-dialogOpen:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for alert dialog to open")
	}
	defer func() {
		handleDialog(t, tabID, map[string]any{"action": "accept"})
		h1Obj.Release(tabObj.Context(), tgtRef)
	}()

	pre := clickOutput{PointerEvents: "auto", Visible: true}

	// Fast-path: when Dialog collector knows a dialog is open, finalizeClickObject
	// returns immediately without stalling for the 500ms probe timeout.
	start := time.Now()
	fast := finalizeClickObject(tabObj.Context(), tgtRef, tabObj.Dialog, h1Obj.ObjectID, pre)
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Errorf("finalizeClickObject with open Dialog took %v, want immediate short-circuit", elapsed)
	}
	if !fast.Visible || fast.Warning != "" {
		t.Errorf("finalizeClickObject fast-path = %+v, want Visible=true Warning=\"\"", fast)
	}

	// Defense-in-depth fallback: even if dialog state has not arrived yet (nil),
	// the 500ms timeout bounds probeClickObject cleanly.
	start = time.Now()
	got := finalizeClickObject(tabObj.Context(), tgtRef, nil, h1Obj.ObjectID, pre)
	elapsed := time.Since(start)
	if elapsed < 350*time.Millisecond || elapsed > 2500*time.Millisecond {
		t.Errorf("finalizeClickObject fallback against open alert took %v, want bounded ~500ms", elapsed)
	}
	if !got.Visible || got.Warning != "" {
		t.Errorf("finalizeClickObject fallback = %+v, want Visible=true Warning=\"\"", got)
	}

	// Dismiss first alert and verify an end-to-end click that triggers a
	// synchronous alert returns promptly without stalling on el.Release (2s).
	handleDialog(t, tabID, map[string]any{"action": "accept"})
	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": `(function() {
			var b = document.createElement('button');
			b.id = 'sync-alert-btn';
			b.textContent = 'Alert';
			b.addEventListener('click', function() { alert('sync-from-click'); });
			document.body.appendChild(b);
		})()`,
	})
	start = time.Now()
	callTool[clickOutput](t, "click", map[string]any{"tab": tabID, "selector": "#sync-alert-btn"})
	clickElapsed := time.Since(start)
	if clickElapsed > 1400*time.Millisecond {
		t.Errorf("end-to-end click opening sync alert took %v, want fast return without 2s ReleaseObject stall", clickElapsed)
	}
	if d := callTool[dialogOutput](t, "dialog", map[string]any{"tab": tabID, "action": "get"}); !d.Open || d.Dialog == nil || d.Dialog.Message != "sync-from-click" {
		t.Errorf("dialog after sync-alert click = %+v, want Open=true Message='sync-from-click'", d)
	}
}

func TestClickDelayedVisibilityRefreshClearsStaleHiddenWarning(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	callTool[evaluateOutput](t, "evaluate", map[string]any{
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

	out := callTool[clickOutput](t, "click", map[string]any{
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
	clicked := callTool[evaluateOutput](t, "evaluate", map[string]any{
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

	callTool[evaluateOutput](t, "evaluate", map[string]any{
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

	callTool[actionOutput](t, "hover", map[string]any{
		"tab":      tabID,
		"selector": "#below-fold-hover",
	})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
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

	callTool[evaluateOutput](t, "evaluate", map[string]any{
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
	callTool[actionOutput](t, "type", map[string]any{
		"tab":      tabID,
		"selector": "#slow-type",
		"text":     "abcde",
		"mode":     "replace",
		"delay":    80,
		"timeout":  200,
	})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
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

	callTool[evaluateOutput](t, "evaluate", map[string]any{
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

	callTool[actionOutput](t, "type", map[string]any{
		"tab":      tabID,
		"selector": "#ce-box",
		"text":     "fresh",
		"mode":     "replace",
	})
	ceOut := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `document.querySelector('#ce-box').textContent`,
	})
	if string(ceOut.Result) != `"fresh"` {
		t.Errorf("contenteditable textContent = %s, want %q", ceOut.Result, "fresh")
	}

	callTool[actionOutput](t, "type", map[string]any{
		"tab":      tabID,
		"selector": "#controlled-inp",
		"text":     "ok",
		"mode":     "replace",
	})
	inpOut := callTool[evaluateOutput](t, "evaluate", map[string]any{
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
		callTool[evaluateOutput](t, "evaluate", map[string]any{
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

	// Ref target detached on clear fails fast with detached error.
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)
	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": `(function() {
			var inp = document.createElement('input');
			inp.id = 'vanish-ref';
			inp.setAttribute('aria-label', 'Vanish Ref');
			inp.value = 'old';
			inp.addEventListener('input', function() { inp.remove(); });
			document.body.appendChild(inp);
		})()`,
	})
	snap := callTool[snapshotOutput](t, "snapshot", map[string]any{"tab": tabID})
	vRef := findSnapshotRef(t, snap.Snapshot, `"Vanish Ref"`)
	if errText := callToolExpectErr(t, "type", map[string]any{
		"tab":  tabID,
		"ref":  vRef,
		"text": "next",
		"mode": "replace",
	}); !strings.Contains(errText, "detached") {
		t.Errorf("type(ref) detached on clear err = %q, want 'detached'", errText)
	}

	// Selector target remounted on clear succeeds on the replacement node.
	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": `(function() {
			var inp = document.createElement('input');
			inp.id = 'remount-inp';
			inp.value = 'old';
			inp.addEventListener('input', function() {
				inp.remove();
				var fresh = document.createElement('input');
				fresh.id = 'remount-inp';
				document.body.appendChild(fresh);
			});
			document.body.appendChild(inp);
		})()`,
	})
	callTool[actionOutput](t, "type", map[string]any{
		"tab":      tabID,
		"selector": "#remount-inp",
		"text":     "reborn",
		"mode":     "replace",
	})
	remountVal := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `document.getElementById('remount-inp').value`,
	})
	if string(remountVal.Result) != `"reborn"` {
		t.Errorf("remounted input value = %s, want \"reborn\"", remountVal.Result)
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
		"indexes":  []int{-1},
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

	callTool[evaluateOutput](t, "evaluate", map[string]any{
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

	callTool[actionOutput](t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#empty-label-sel",
		"labels":   []string{""},
	})
	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `document.querySelector('#empty-label-sel').value`,
	})
	if string(out.Result) != `"blank"` {
		t.Errorf("selected value for empty label = %s, want %q", out.Result, "blank")
	}

	errText := callToolExpectErr(t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "h1",
		"values":   []string{"x"},
	})
	if !strings.Contains(errText, "not a <select>") {
		t.Errorf("expected non-<select> error, got: %s", errText)
	}
}

func TestPressKeyCtrlShortcutSuppressesLiteralChar(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	callTool[evaluateOutput](t, "evaluate", map[string]any{
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

	callTool[actionOutput](t, "press_key", map[string]any{
		"tab":       tabID,
		"key":       "a",
		"modifiers": []string{"ctrl"},
	})
	callTool[actionOutput](t, "press_key", map[string]any{
		"tab":       tabID,
		"key":       "a",
		"modifiers": []string{"meta"},
	})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
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

	callTool[evaluateOutput](t, "evaluate", map[string]any{
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

	callTool[actionOutput](t, "press_key", map[string]any{"tab": tabID, "key": "Space"})
	callTool[actionOutput](t, "press_key", map[string]any{"tab": tabID, "key": "Insert"})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
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
	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `setTimeout(function() { window.__promptVal = prompt('Name?', 'default'); }, 20); true`,
	})
	handleDialog(t, tabID, map[string]any{"action": "accept", "text": "alice"})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `window.__promptVal`,
	})
	if string(out.Result) != `"alice"` {
		t.Errorf("prompt result = %s, want %q", out.Result, "alice")
	}

	// Case 2: explicit empty string submits "".
	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `setTimeout(function() { window.__promptEmpty = prompt('Name?', 'default'); }, 20); true`,
	})
	handleDialog(t, tabID, map[string]any{"action": "accept", "text": ""})

	out = callTool[evaluateOutput](t, "evaluate", map[string]any{
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

	waitDialogOpen := func(wantType string) (dialogOutput, string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			rawRes := callToolRaw(t, "dialog", map[string]any{"tab": tabID, "action": "get"})
			if rawRes.IsError {
				t.Fatalf("dialog(get) returned error: %+v", rawRes.Content)
			}
			var rawJSON string
			for _, c := range rawRes.Content {
				if tc, ok := c.(*mcp.TextContent); ok {
					rawJSON = tc.Text
					break
				}
			}
			var d dialogOutput
			if err := json.Unmarshal([]byte(rawJSON), &d); err != nil {
				t.Fatalf("unmarshal dialog(get): %v", err)
			}
			if d.Open {
				if d.Dialog == nil {
					t.Fatalf("dialog(get) Open=true with nil Dialog: %s", rawJSON)
				}
				if d.Dialog.Type != wantType {
					t.Fatalf("dialog(get) type = %q, want %q", d.Dialog.Type, wantType)
				}
				return d, rawJSON
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %q dialog to appear in dialog(get)", wantType)
		return dialogOutput{}, ""
	}

	// Initially no dialog is open.
	initState := callTool[dialogOutput](t, "dialog", map[string]any{"tab": tabID, "action": "get"})
	if initState.Open || initState.Dialog != nil {
		t.Fatalf("expected Open=false Dialog=nil initially, got %+v", initState)
	}

	// 1. Alert dialog with empty message verifies both message:"" and
	// has_browser_handler:false are preserved in serialized JSON (not dropped
	// by omitempty), and immediate post-accept dialog(get) sees open=false
	// without polling.
	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `setTimeout(function() { alert(''); }, 20); true`,
	})
	d, rawJSON := waitDialogOpen("alert")
	if d.Dialog.Message != "" || !strings.HasSuffix(d.Dialog.URL, "/page2.html") || d.Dialog.OpenedAt.IsZero() {
		t.Errorf("unexpected alert dialog state: %+v", d.Dialog)
	}
	if !strings.Contains(rawJSON, `"message":""`) {
		t.Errorf("dialog(get) JSON omitted empty message: %s", rawJSON)
	}
	if !strings.Contains(rawJSON, `"has_browser_handler":`) {
		t.Errorf("dialog(get) JSON omitted has_browser_handler: %s", rawJSON)
	}
	// Verify false has_browser_handler and empty message are never dropped by omitempty.
	falseOut, err := json.Marshal(dialogOutput{
		Open: true,
		Dialog: &collector.DialogState{
			Type:              "alert",
			Message:           "",
			HasBrowserHandler: false,
		},
	})
	if err != nil {
		t.Fatalf("marshal DialogOutput: %v", err)
	}
	if !strings.Contains(string(falseOut), `"has_browser_handler":false`) || !strings.Contains(string(falseOut), `"message":""`) {
		t.Errorf("DialogOutput JSON omitted false/empty fields: %s", falseOut)
	}
	accOut := callTool[dialogOutput](t, "dialog", map[string]any{"tab": tabID, "action": "accept"})
	if accOut.Open {
		t.Fatalf("dialog(accept) immediately reported open: %+v", accOut)
	}
	// ClearIfSeq guarantees immediate follow-up dialog(get) reports closed.
	if after := callTool[dialogOutput](t, "dialog", map[string]any{"tab": tabID, "action": "get"}); after.Open {
		t.Fatalf("dialog(get) immediately after accept still reported open: %+v", after)
	}

	// 2. Confirm dialog inspection + dismiss.
	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `setTimeout(function() { window.__confirm = confirm('proceed?'); }, 20); true`,
	})
	d, _ = waitDialogOpen("confirm")
	if d.Dialog.Message != "proceed?" {
		t.Errorf("confirm message = %q, want %q", d.Dialog.Message, "proceed?")
	}
	callTool[dialogOutput](t, "dialog", map[string]any{"tab": tabID, "action": "dismiss"})
	if after := callTool[dialogOutput](t, "dialog", map[string]any{"tab": tabID, "action": "get"}); after.Open {
		t.Fatalf("dialog(get) immediately after confirm dismiss still reported open: %+v", after)
	}
	if res := callTool[evaluateOutput](t, "evaluate", map[string]any{"tab": tabID, "expression": `window.__confirm`}); string(res.Result) != "false" {
		t.Errorf("dismissed confirm result = %s, want false", res.Result)
	}

	// 3. Prompt dialog with omitted text preserves DefaultPrompt.
	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `setTimeout(function() { window.__promptDef = prompt('Your name?', 'keep-default'); }, 20); true`,
	})
	d, _ = waitDialogOpen("prompt")
	if d.Dialog.Message != "Your name?" || d.Dialog.DefaultPrompt != "keep-default" {
		t.Errorf("prompt dialog state = %+v, want message='Your name?' default_prompt='keep-default'", d.Dialog)
	}
	callTool[dialogOutput](t, "dialog", map[string]any{"tab": tabID, "action": "accept"})
	if after := callTool[dialogOutput](t, "dialog", map[string]any{"tab": tabID, "action": "get"}); after.Open {
		t.Fatalf("dialog(get) immediately after prompt accept still reported open: %+v", after)
	}
	if res := callTool[evaluateOutput](t, "evaluate", map[string]any{"tab": tabID, "expression": `window.__promptDef`}); string(res.Result) != `"keep-default"` {
		t.Errorf("omitted text prompt result = %s, want %q", res.Result, "keep-default")
	}

	// 4. Prompt dialog with explicit empty string submits "".
	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `setTimeout(function() { window.__promptBlank = prompt('Your name?', 'keep-default'); }, 20); true`,
	})
	waitDialogOpen("prompt")
	callTool[dialogOutput](t, "dialog", map[string]any{"tab": tabID, "action": "accept", "text": ""})
	if after := callTool[dialogOutput](t, "dialog", map[string]any{"tab": tabID, "action": "get"}); after.Open {
		t.Fatalf("dialog(get) immediately after empty prompt accept still reported open: %+v", after)
	}
	if res := callTool[evaluateOutput](t, "evaluate", map[string]any{"tab": tabID, "expression": `window.__promptBlank`}); string(res.Result) != `""` {
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

func TestClickAndType(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// Click the target.
	callTool[struct{}](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#click-target",
	})

	// Verify click was registered.
	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('click-count').textContent",
	})
	if string(out.Result) != `"1"` {
		t.Errorf("click count = %s, want \"1\"", out.Result)
	}

	// Type into the input.
	callTool[struct{}](t, "type", map[string]any{
		"tab":      tabID,
		"selector": "#type-target",
		"text":     "hello world",
		"mode":     "replace",
	})

	// Verify typed text.
	out = callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('type-target').value",
	})
	if string(out.Result) != `"hello world"` {
		t.Errorf("typed text = %s, want \"hello world\"", out.Result)
	}
}

func TestFormInteraction(t *testing.T) {
	tabID := navigateToFixture(t, "forms.html")
	defer closeTab(t, tabID)

	// Type a name.
	callTool[struct{}](t, "type", map[string]any{
		"tab":      tabID,
		"selector": "#name",
		"text":     "Alice",
		"mode":     "replace",
	})

	// Select an option.
	callTool[struct{}](t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#color",
		"values":   []string{"blue"},
	})

	// Verify selection.
	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('color').value",
	})
	if string(out.Result) != `"blue"` {
		t.Errorf("select value = %s, want \"blue\"", out.Result)
	}

	// Submit form via press_key Enter on focused form input.
	callTool[struct{}](t, "press_key", map[string]any{
		"tab":      tabID,
		"selector": "#name",
		"key":      "Enter",
	})

	// Wait for result to appear.
	callTool[struct{}](t, "wait_for", map[string]any{
		"tab":      tabID,
		"selector": "#result",
	})

	out = callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('result').textContent",
	})
	if !strings.Contains(string(out.Result), "Alice") {
		t.Errorf("form result = %s, want to contain 'Alice'", out.Result)
	}
}

func TestScrollIntoView(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "scroll", map[string]any{
		"tab":      tabID,
		"selector": "#scroll-marker",
	})

	// Verify the element is in view.
	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": `(function() {
			var el = document.getElementById('scroll-marker');
			var rect = el.getBoundingClientRect();
			return rect.top >= 0 && rect.top < window.innerHeight;
		})()`,
	})
	if string(out.Result) != "true" {
		t.Error("scroll-marker not in view after scroll")
	}
}

func TestPressKey(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "press_key", map[string]any{
		"tab": tabID,
		"key": "a",
	})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('key-output').textContent",
	})
	if !strings.Contains(string(out.Result), "a") {
		t.Errorf("key output = %s, want to contain 'a'", out.Result)
	}
}

func TestHover(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "hover", map[string]any{
		"tab":      tabID,
		"selector": "#hover-target",
	})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('hover-target').textContent",
	})
	if !strings.Contains(string(out.Result), "Hovered") {
		t.Errorf("hover target text = %s, want to contain 'Hovered'", out.Result)
	}
}

func TestPressKeyFocusesSelector(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "press_key", map[string]any{
		"tab":      tabID,
		"selector": "#type-target",
		"key":      "x",
	})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "({active: document.activeElement.id, val: document.getElementById('type-target').value})",
	})
	if !strings.Contains(string(out.Result), `"active":"type-target"`) || !strings.Contains(string(out.Result), `"val":"x"`) {
		t.Errorf("press_key with selector state = %s, want active=type-target and val=x", out.Result)
	}
}

func TestTypeWithClear(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// Type initial text.
	callTool[struct{}](t, "type", map[string]any{
		"tab":      tabID,
		"selector": "#type-target",
		"text":     "initial",
		"mode":     "replace",
	})

	// Type again with clear.
	callTool[struct{}](t, "type", map[string]any{
		"tab":      tabID,
		"selector": "#type-target",
		"text":     "replaced",
		"mode":     "replace",
	})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('type-target').value",
	})
	if !strings.Contains(string(out.Result), "replaced") {
		t.Errorf("typed text = %s, want 'replaced'", out.Result)
	}
	if strings.Contains(string(out.Result), "initial") {
		t.Errorf("typed text = %s, should not contain 'initial' after clear", out.Result)
	}
}

// TestTypeAppendPreservesExisting verifies mode=append concatenates
// onto the field's current value instead of clearing it. Without this,
// the only test coverage of the append branch was the validateMode
// table — a refactor that inverted the mode check would silently change
// behaviour but still pass the existing suite.
func TestTypeAppendPreservesExisting(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "type", map[string]any{
		"tab":      tabID,
		"selector": "#type-target",
		"text":     "abc",
		"mode":     "replace",
	})
	callTool[struct{}](t, "type", map[string]any{
		"tab":      tabID,
		"selector": "#type-target",
		"text":     "def",
		"mode":     "append",
	})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('type-target').value",
	})
	if string(out.Result) != `"abcdef"` {
		t.Errorf("after append, value = %s, want \"abcdef\"", out.Result)
	}
}

func TestPressKeyWithModifiers(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "press_key", map[string]any{
		"tab":       tabID,
		"key":       "a",
		"modifiers": []string{"ctrl"},
	})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('key-output').textContent",
	})
	if !strings.Contains(string(out.Result), "Ctrl") {
		t.Errorf("key output = %s, want to contain 'Ctrl'", out.Result)
	}
}

func TestSelectOptionByLabel(t *testing.T) {
	tabID := navigateToFixture(t, "forms.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#color",
		"labels":   []string{"Green"},
	})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('color').value",
	})
	if string(out.Result) != `"green"` {
		t.Errorf("select value = %s, want \"green\"", out.Result)
	}
}

func TestSelectOptionByIndex(t *testing.T) {
	tabID := navigateToFixture(t, "forms.html")
	defer closeTab(t, tabID)

	idx := 2 // "Blue" is index 2 (Red=0, Green=1, Blue=2)
	callTool[struct{}](t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#color",
		"indexes":  []int{idx},
	})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('color').value",
	})
	if string(out.Result) != `"blue"` {
		t.Errorf("select value = %s, want \"blue\"", out.Result)
	}
}

func TestSelectOptionValidation(t *testing.T) {
	tabID := navigateToFixture(t, "forms.html")
	defer closeTab(t, tabID)

	// No values, labels, or indexes provided → error.
	errText := callToolExpectErr(t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#color",
	})
	if !strings.Contains(errText, "exactly one") {
		t.Errorf("expected validation error, got: %s", errText)
	}
}

func TestHandleDialog(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	// Set up a dialog handler before triggering the dialog.
	// Chrome auto-dismisses dialogs unless we handle them. We need to
	// trigger the dialog and handle it in the right order. The simplest
	// approach: trigger an alert via evaluate (which blocks until dialog
	// is dismissed), but do so from a separate goroutine.
	done := make(chan struct{})
	go func() {
		defer close(done)
		// This will block until the dialog is handled.
		callToolRaw(t, "evaluate", map[string]any{
			"tab":        tabID,
			"expression": "alert('test dialog')",
		})
	}()

	// Handle the dialog (retries until dialog appears).
	handleDialog(t, tabID, map[string]any{"action": "accept"})

	// Wait for the evaluate to complete.
	select {
	case <-done:
		// Success — dialog was handled and evaluate returned.
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for dialog handling to complete")
	}
}

func TestScrollByOffset(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "scroll", map[string]any{
		"tab": tabID,
		"y":   500,
	})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "window.scrollY > 0",
	})
	if string(out.Result) != "true" {
		t.Errorf("after scroll by y=500, scrollY > 0 = %s, want true", out.Result)
	}
}

func TestClickMissingSelector(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	selectorTimeout(t, tabID, "click",
		nil, // no extra args for invalid
		map[string]any{"selector": "#click-target"}, // valid
	)

	// Verify the valid click actually worked.
	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('click-count').textContent",
	})
	if !strings.Contains(string(out.Result), "1") {
		t.Errorf("after valid click, click count = %s, want to contain '1'", out.Result)
	}
}

func TestDoubleClickMissingSelector(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	selectorTimeout(t, tabID, "click",
		map[string]any{"click_count": 2},                              // invalid
		map[string]any{"selector": "#click-target", "click_count": 2}, // valid
	)
}

func TestTypeMissingSelector(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	selectorTimeout(t, tabID, "type",
		map[string]any{"text": "hello", "mode": "replace"},                               // invalid
		map[string]any{"selector": "#type-target", "text": "test123", "mode": "replace"}, // valid
	)

	// Verify the valid type actually worked.
	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('type-target').value",
	})
	if !strings.Contains(string(out.Result), "test123") {
		t.Errorf("after valid type, value = %s, want to contain 'test123'", out.Result)
	}
}

func TestTypeWithClearMissingSelector(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	selectorTimeout(t, tabID, "type",
		map[string]any{"text": "hello", "mode": "replace"},                               // invalid
		map[string]any{"selector": "#type-target", "text": "cleared", "mode": "replace"}, // valid
	)
}

func TestPressKeyMissingSelector(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	selectorTimeout(t, tabID, "press_key",
		map[string]any{"key": "a"},                             // invalid
		map[string]any{"selector": "#type-target", "key": "a"}, // valid
	)

	// Verify focus actually moved.
	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.activeElement.id",
	})
	if !strings.Contains(string(out.Result), "type-target") {
		t.Errorf("after valid press_key with selector, activeElement = %s, want 'type-target'", out.Result)
	}
}

func TestScrollIntoViewMissingSelector(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	selectorTimeout(t, tabID, "scroll",
		nil, // invalid
		map[string]any{"selector": "#scroll-marker"}, // valid
	)
}

func TestUploadFilesMissingSelector(t *testing.T) {
	tabID := navigateToFixture(t, "forms.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "upload_files", map[string]any{
		"tab":      tabID,
		"selector": "#nonexistent-file-input",
		"paths":    []string{"/dev/null"},
		"timeout":  500,
	})
	if errText == "" {
		t.Error("upload_files with missing selector should return an error")
	}
}

func TestHoverMissingSelector(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "hover", map[string]any{
		"tab":      tabID,
		"selector": "#nonexistent-hover",
		"timeout":  1000,
	})
	if errText == "" {
		t.Error("hover on missing selector should return an error")
	}
}

func TestSelectOptionMissingSelector(t *testing.T) {
	tabID := navigateToFixture(t, "forms.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#nonexistent-select",
		"values":   []string{"red"},
	})
	if errText == "" {
		t.Error("select_option with missing selector should return an error")
	}
}

func TestClickDelayedElement(t *testing.T) {
	tabID := navigateToFixture(t, "delayed.html")
	defer closeTab(t, tabID)

	// #delayed-element appears after 500ms. With default 5s timeout,
	// chromedp should poll and find it.
	callTool[struct{}](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#delayed-element",
	})
	// If we get here without error, the element was found after it appeared.
}

func TestClickRightButton(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// Set up a contextmenu listener.
	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": `document.getElementById('click-target').addEventListener('contextmenu', function(e) {
			e.preventDefault();
			this.textContent = 'Right-clicked';
		})`,
	})

	callTool[struct{}](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#click-target",
		"button":   "right",
	})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('click-target').textContent",
	})
	// The JS dispatch sends mousedown/mouseup/click with button=2 but not
	// contextmenu. The click event listener should fire (it fires for any button).
	if !strings.Contains(string(out.Result), "Clicked") && !strings.Contains(string(out.Result), "Right-clicked") {
		t.Errorf("right-click result = %s, want evidence of click or right-click", out.Result)
	}
}

func TestClickDoubleClickBehaviour(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// Set up a dblclick listener.
	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": `document.getElementById('click-target').addEventListener('dblclick', function() {
			document.getElementById('click-count').textContent = 'dblclick';
		})`,
	})

	callTool[struct{}](t, "click", map[string]any{
		"tab":         tabID,
		"selector":    "#click-target",
		"click_count": 2,
	})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('click-count').textContent",
	})
	if string(out.Result) != `"dblclick"` {
		t.Errorf("double-click result = %s, want \"dblclick\"", out.Result)
	}
}

func TestTypeWithDelay(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	start := time.Now()
	callTool[struct{}](t, "type", map[string]any{
		"tab":      tabID,
		"selector": "#type-target",
		"text":     "abc",
		"delay":    50,
		"mode":     "replace",
	})
	elapsed := time.Since(start)

	// With 3 characters and 50ms delay each, should take at least ~150ms.
	if elapsed < 100*time.Millisecond {
		t.Errorf("type with delay took %v, expected at least 100ms for 3 chars at 50ms delay", elapsed)
	}

	// Verify the text was actually typed.
	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('type-target').value",
	})
	if !strings.Contains(string(out.Result), "abc") {
		t.Errorf("typed text = %s, want to contain 'abc'", out.Result)
	}
}

func TestUploadFilesSuccess(t *testing.T) {
	tabID := navigateToFixture(t, "forms.html")
	defer closeTab(t, tabID)

	// Create a temp file to upload.
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "test-upload.txt")
	if err := os.WriteFile(tmpFile, []byte("upload content"), 0644); err != nil {
		t.Fatalf("create temp file: %v", err)
	}

	callTool[struct{}](t, "upload_files", map[string]any{
		"tab":      tabID,
		"selector": "#file-input",
		"paths":    []string{tmpFile},
	})

	// Verify the file input now has a file.
	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('file-input').files.length",
	})
	if string(out.Result) != "1" {
		t.Errorf("file input files.length = %s, want 1", out.Result)
	}

	out = callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('file-input').files[0].name",
	})
	if !strings.Contains(string(out.Result), "test-upload.txt") {
		t.Errorf("file name = %s, want 'test-upload.txt'", out.Result)
	}
}

func TestHandleDialogDismiss(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	done := make(chan string, 1)
	go func() {
		out := callTool[evaluateOutput](t, "evaluate", map[string]any{
			"tab":        tabID,
			"expression": "confirm('Are you sure?')",
		})
		done <- string(out.Result)
	}()

	handleDialog(t, tabID, map[string]any{"action": "dismiss"})

	select {
	case result := <-done:
		if result != "false" {
			t.Errorf("confirm() after dismiss = %s, want false", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for confirm dialog")
	}
}

func TestHandleDialogConfirmAccept(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	done := make(chan string, 1)
	go func() {
		out := callTool[evaluateOutput](t, "evaluate", map[string]any{
			"tab":        tabID,
			"expression": "confirm('Are you sure?')",
		})
		done <- string(out.Result)
	}()

	handleDialog(t, tabID, map[string]any{"action": "accept"})

	select {
	case result := <-done:
		if result != "true" {
			t.Errorf("confirm() after accept = %s, want true", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for confirm dialog")
	}
}

func TestHandleDialogPrompt(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	done := make(chan string, 1)
	go func() {
		out := callTool[evaluateOutput](t, "evaluate", map[string]any{
			"tab":        tabID,
			"expression": "prompt('Enter name:')",
		})
		done <- string(out.Result)
	}()

	handleDialog(t, tabID, map[string]any{"action": "accept", "text": "Alice"})

	select {
	case result := <-done:
		if !strings.Contains(result, "Alice") {
			t.Errorf("prompt() result = %s, want 'Alice'", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for prompt dialog")
	}
}

func TestPressKeyEnter(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "press_key", map[string]any{
		"tab": tabID,
		"key": "Enter",
	})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('key-output').textContent",
	})
	if !strings.Contains(string(out.Result), "Enter") {
		t.Errorf("key output = %s, want to contain 'Enter'", out.Result)
	}
}

func TestPressKeyMultiModifier(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "press_key", map[string]any{
		"tab":       tabID,
		"key":       "a",
		"modifiers": []string{"ctrl", "shift"},
	})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('key-output').textContent",
	})
	result := string(out.Result)
	if !strings.Contains(result, "Ctrl") || !strings.Contains(result, "Shift") {
		t.Errorf("key output = %s, want to contain both 'Ctrl' and 'Shift'", result)
	}
}

func TestTypeSubmitSubmitsForm(t *testing.T) {
	tabID := navigateToFixture(t, "forms.html")
	defer closeTab(t, tabID)

	// Type a name and submit in a single call with submit=true.
	callTool[struct{}](t, "type", map[string]any{
		"tab":      tabID,
		"selector": "#name",
		"text":     "Bob",
		"mode":     "replace",
		"submit":   true,
	})

	// Wait for result to appear.
	callTool[struct{}](t, "wait_for", map[string]any{
		"tab":      tabID,
		"selector": "#result",
	})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('result').textContent",
	})
	if !strings.Contains(string(out.Result), "Bob") {
		t.Errorf("form result = %s, want to contain 'Bob'", out.Result)
	}
}

func TestSelectOptionMultipleCriteria(t *testing.T) {
	tabID := navigateToFixture(t, "forms.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#color",
		"values":   []string{"red"},
		"labels":   []string{"Green"},
	})
	if !strings.Contains(errText, "exactly one") {
		t.Errorf("expected multiple criteria error, got: %s", errText)
	}
}

func TestClickMiddleButton(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#click-target",
		"button":   "middle",
	})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('middle-click-output').textContent",
	})
	if !strings.Contains(string(out.Result), "middle-click") {
		t.Errorf("middle-click output = %s, want 'middle-click'", out.Result)
	}
}

func TestClickTripleClick(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "click", map[string]any{
		"tab":         tabID,
		"selector":    "#click-target",
		"click_count": 3,
	})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('triple-click-output').textContent",
	})
	if !strings.Contains(string(out.Result), "triple-click") {
		t.Errorf("triple-click output = %s, want 'triple-click'", out.Result)
	}
}

func TestScrollHorizontal(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "scroll", map[string]any{
		"tab": tabID,
		"x":   300,
	})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "window.scrollX > 0",
	})
	if string(out.Result) != "true" {
		t.Errorf("after scroll x=300, scrollX > 0 = %s, want true", out.Result)
	}
}

func TestScrollBothAxes(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "scroll", map[string]any{
		"tab": tabID,
		"x":   200,
		"y":   400,
	})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "window.scrollX > 0 && window.scrollY > 0",
	})
	if string(out.Result) != "true" {
		t.Errorf("after scroll x=200,y=400, both axes should be > 0, got %s", out.Result)
	}
}

func TestTypeWithClearOnEmptyField(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// Type into an empty field with clear=true.
	callTool[struct{}](t, "type", map[string]any{
		"tab":      tabID,
		"selector": "#type-target",
		"text":     "fresh",
		"mode":     "replace",
	})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('type-target').value",
	})
	if string(out.Result) != `"fresh"` {
		t.Errorf("typed text = %s, want \"fresh\"", out.Result)
	}
}

func TestPressKeyTab(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "press_key", map[string]any{
		"tab": tabID,
		"key": "Tab",
	})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('key-output').textContent",
	})
	if !strings.Contains(string(out.Result), "Tab") {
		t.Errorf("key output = %s, want to contain 'Tab'", out.Result)
	}
}

func TestPressKeyEscape(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "press_key", map[string]any{
		"tab": tabID,
		"key": "Escape",
	})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('key-output').textContent",
	})
	if !strings.Contains(string(out.Result), "Escape") {
		t.Errorf("key output = %s, want to contain 'Escape'", out.Result)
	}
}

func TestPressKeyArrowDown(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "press_key", map[string]any{
		"tab": tabID,
		"key": "ArrowDown",
	})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('key-output').textContent",
	})
	if !strings.Contains(string(out.Result), "ArrowDown") {
		t.Errorf("key output = %s, want to contain 'ArrowDown'", out.Result)
	}
}

func TestPressKeyAltModifier(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "press_key", map[string]any{
		"tab":       tabID,
		"key":       "a",
		"modifiers": []string{"alt"},
	})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('key-output').textContent",
	})
	if !strings.Contains(string(out.Result), "Alt") {
		t.Errorf("key output = %s, want to contain 'Alt'", out.Result)
	}
}

func TestSelectOptionByValueVerification(t *testing.T) {
	tabID := navigateToFixture(t, "forms.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#color",
		"values":   []string{"green"},
	})

	// Verify value.
	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('color').value",
	})
	if string(out.Result) != `"green"` {
		t.Errorf("value = %s, want \"green\"", out.Result)
	}

	// Verify selectedIndex.
	out = callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('color').selectedIndex",
	})
	if string(out.Result) != "1" {
		t.Errorf("selectedIndex = %s, want 1", out.Result)
	}
}

func TestSelectOptionFiresChangeEvent(t *testing.T) {
	tabID := navigateToFixture(t, "forms.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#color",
		"values":   []string{"blue"},
	})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('change-output').textContent",
	})
	if !strings.Contains(string(out.Result), "changed:blue") {
		t.Errorf("change event output = %s, want to contain 'changed:blue'", out.Result)
	}
}

func TestSelectOptionByIndexZero(t *testing.T) {
	tabID := navigateToFixture(t, "forms.html")
	defer closeTab(t, tabID)

	// First select blue (index 2), then select index 0 (red).
	callTool[struct{}](t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#color",
		"values":   []string{"blue"},
	})
	callTool[struct{}](t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#color",
		"indexes":  []int{0},
	})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('color').value",
	})
	if string(out.Result) != `"red"` {
		t.Errorf("value after index=0 = %s, want \"red\"", out.Result)
	}
}

func TestUploadMultipleFiles(t *testing.T) {
	tabID := navigateToFixture(t, "forms.html")
	defer closeTab(t, tabID)

	tmpDir := t.TempDir()
	file1 := filepath.Join(tmpDir, "file1.txt")
	file2 := filepath.Join(tmpDir, "file2.txt")
	os.WriteFile(file1, []byte("content1"), 0644)
	os.WriteFile(file2, []byte("content2"), 0644)

	// Make the file input accept multiple files.
	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('file-input').multiple = true",
	})

	callTool[struct{}](t, "upload_files", map[string]any{
		"tab":      tabID,
		"selector": "#file-input",
		"paths":    []string{file1, file2},
	})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('file-input').files.length",
	})
	if string(out.Result) != "2" {
		t.Errorf("files.length = %s, want 2", out.Result)
	}
}

func TestHandleDialogTextOnAlert(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	done := make(chan struct{}, 1)
	go func() {
		callToolRaw(t, "evaluate", map[string]any{
			"tab":        tabID,
			"expression": "alert('test alert')",
		})
		done <- struct{}{}
	}()

	// Pass text to an alert dialog — should be ignored, no error.
	handleDialog(t, tabID, map[string]any{"action": "accept", "text": "ignored text"})

	select {
	case <-done:
		// Success.
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for alert handling")
	}
}

func TestPressKeyFocusNonFocusableElement(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// #title is an h1 — not focusable. press_key must return an error rather
	// than hanging or dispatching the key to an unrelated focused element.
	errText := callToolExpectErr(t, "press_key", map[string]any{
		"tab":      tabID,
		"selector": "#title",
		"key":      "Tab",
	})
	if !strings.Contains(strings.ToLower(errText), "focus") {
		t.Errorf("press_key on non-focusable element err = %q, want focus error", errText)
	}
}

func TestPressKeyIntoFocusedInput(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// Focus the input and press 'x' in one call.
	callTool[struct{}](t, "press_key", map[string]any{
		"tab":      tabID,
		"selector": "#type-target",
		"key":      "x",
	})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('type-target').value",
	})
	if string(out.Result) != `"x"` {
		t.Errorf("input value after press_key(selector, 'x') = %s, want \"x\"", out.Result)
	}
}

func TestTypeUnicodeCharacters(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "type", map[string]any{
		"tab":      tabID,
		"selector": "#type-target",
		"text":     "café",
		"mode":     "replace",
	})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('type-target').value",
	})
	if !strings.Contains(string(out.Result), "caf") {
		t.Errorf("typed text = %s, want to contain 'caf'", out.Result)
	}
}

func TestTypeWithDelayAndClear(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// Type initial.
	callTool[struct{}](t, "type", map[string]any{
		"tab":      tabID,
		"selector": "#type-target",
		"text":     "old",
		"mode":     "replace",
	})

	// Type with delay + clear.
	callTool[struct{}](t, "type", map[string]any{
		"tab":      tabID,
		"selector": "#type-target",
		"text":     "new",
		"mode":     "replace",
		"delay":    20,
	})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('type-target').value",
	})
	if strings.Contains(string(out.Result), "old") {
		t.Errorf("after clear+delay, text should not contain 'old': %s", out.Result)
	}
	if !strings.Contains(string(out.Result), "new") {
		t.Errorf("after clear+delay, text should contain 'new': %s", out.Result)
	}
}

func TestTypeIntoTextarea(t *testing.T) {
	tabID := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "type", map[string]any{
		"tab":      tabID,
		"selector": "#textarea-target",
		"text":     "multi\nline\ntext",
		"mode":     "replace",
	})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('textarea-target').value",
	})
	if !strings.Contains(string(out.Result), "multi") {
		t.Errorf("textarea value = %s, want to contain 'multi'", out.Result)
	}
}

func TestTypeEmptyText(t *testing.T) {
	tabID := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tabID)

	// Type empty string — should not error.
	callTool[struct{}](t, "type", map[string]any{
		"tab":      tabID,
		"selector": "#textarea-target",
		"text":     "",
		"mode":     "replace",
	})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('textarea-target').value",
	})
	if string(out.Result) != `""` {
		t.Errorf("after typing empty string, value = %s, want empty", out.Result)
	}
}

func TestClickDisabledButton(t *testing.T) {
	tabID := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tabID)

	// Click on a disabled button must error rather than silently
	// dispatching into the void.
	errText := callToolExpectErr(t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#disabled-btn",
	})
	if !strings.Contains(errText, "disabled") {
		t.Errorf("click on disabled button: error %q should mention 'disabled'", errText)
	}

	// And the page-side handler must not have fired (defense against an
	// implementation that errored after dispatching).
	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('disabled-output').textContent",
	})
	if strings.Contains(string(out.Result), "clicked-disabled") {
		t.Error("click event fired despite the pre-click error")
	}
}

func TestClickTriggersNavigation(t *testing.T) {
	tabID := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tabID)

	// Click the link that navigates to page2.html.
	callTool[struct{}](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#nav-link",
	})

	// Poll until the navigation completes and document.title changes.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		result := callToolRaw(t, "evaluate", map[string]any{
			"tab":        tabID,
			"expression": "document.title",
		})
		if !result.IsError {
			if text := contentText(result); strings.Contains(text, "Page 2") {
				break
			}
			// Also check structured content.
			if result.StructuredContent != nil {
				b, _ := json.Marshal(result.StructuredContent)
				if strings.Contains(string(b), "Page 2") {
					break
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.title",
	})
	if !strings.Contains(string(out.Result), "Page 2") {
		t.Errorf("after click navigation, title = %s, want 'Page 2'", out.Result)
	}
}

func TestSelectOptionIndexOutOfRange(t *testing.T) {
	tabID := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tabID)

	// Select index 999 — out of range. Should return an error.
	errText := callToolExpectErr(t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#big-select",
		"indexes":  []int{999},
	})
	if !strings.Contains(strings.ToLower(errText), "out of range") {
		t.Errorf("expected 'out of range' error, got: %s", errText)
	}
}

func TestSelectOptionLabelNotFound(t *testing.T) {
	tabID := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tabID)

	// Try to select by a label that doesn't exist — should error.
	errText := callToolExpectErr(t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#big-select",
		"labels":   []string{"NonexistentLabel"},
	})
	if !strings.Contains(strings.ToLower(errText), "no option with label") {
		t.Errorf("expected 'no option with label' error, got: %s", errText)
	}
}

func TestSelectOptionAllThreeCriteria(t *testing.T) {
	tabID := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#big-select",
		"values":   []string{"a"},
		"labels":   []string{"Alpha"},
		"indexes":  []int{0},
	})
	if !strings.Contains(errText, "exactly one") {
		t.Errorf("error = %q, want validation error about multiple criteria", errText)
	}
}

func TestScrollNegativeOffset(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// First scroll down.
	callTool[struct{}](t, "scroll", map[string]any{
		"tab": tabID,
		"y":   500,
	})

	out1 := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "window.scrollY",
	})

	// Now scroll up with negative offset.
	callTool[struct{}](t, "scroll", map[string]any{
		"tab": tabID,
		"y":   -200,
	})

	out2 := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "window.scrollY",
	})

	// scrollY should have decreased.
	var y1, y2 float64
	json.Unmarshal(out1.Result, &y1)
	json.Unmarshal(out2.Result, &y2)
	if y2 >= y1 {
		t.Errorf("after negative scroll, scrollY = %f, expected < %f", y2, y1)
	}
}

func TestScrollSelectorIgnoresXY(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// With both selector and x/y, the implementation takes the selector path
	// (ScrollIntoView) and ignores x/y. Should not error.
	callTool[struct{}](t, "scroll", map[string]any{
		"tab":      tabID,
		"selector": "#scroll-marker",
		"x":        100,
		"y":        100,
	})

	// The marker should now be in view. Verify scrollY > 0.
	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "window.scrollY > 0",
	})
	if string(out.Result) != "true" {
		t.Error("after scroll-into-view with x/y set, page should have scrolled")
	}
}

func TestHandleDialogDismissConfirm(t *testing.T) {
	tabID := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tabID)

	done := make(chan struct{}, 1)
	go func() {
		callToolRaw(t, "evaluate", map[string]any{
			"tab":        tabID,
			"expression": "document.getElementById('confirm-result').textContent = String(confirm('Sure?'))",
		})
		done <- struct{}{}
	}()

	handleDialog(t, tabID, map[string]any{"action": "dismiss"})

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for confirm dialog")
	}

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('confirm-result').textContent",
	})
	if !strings.Contains(string(out.Result), "false") {
		t.Errorf("dismissed confirm result = %s, want 'false'", out.Result)
	}
}

func TestHandleDialogDismissPrompt(t *testing.T) {
	tabID := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tabID)

	done := make(chan struct{}, 1)
	go func() {
		callToolRaw(t, "evaluate", map[string]any{
			"tab":        tabID,
			"expression": "document.getElementById('prompt-result').textContent = String(prompt('Name:'))",
		})
		done <- struct{}{}
	}()

	handleDialog(t, tabID, map[string]any{"action": "dismiss"})

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for prompt dialog")
	}

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('prompt-result').textContent",
	})
	if !strings.Contains(string(out.Result), "null") {
		t.Errorf("dismissed prompt result = %s, want 'null'", out.Result)
	}
}

func TestPressKeyMetaModifier(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "press_key", map[string]any{
		"tab":       tabID,
		"key":       "a",
		"modifiers": []string{"meta"},
	})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('key-output').textContent",
	})
	if !strings.Contains(string(out.Result), "Meta") {
		t.Errorf("key output = %s, want to contain 'Meta'", out.Result)
	}
}

func TestUploadFilesEmptyPathsRejected(t *testing.T) {
	tabID := navigateToFixture(t, "forms.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "upload_files", map[string]any{
		"tab":      tabID,
		"selector": "#file-input",
		"paths":    []string{},
	})
	if !strings.Contains(errText, "paths must not be empty") {
		t.Errorf("upload_files empty paths err = %q, want 'paths must not be empty'", errText)
	}
}

func TestHoverTriggersCSSHover(t *testing.T) {
	tabID := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tabID)

	// Get the background color BEFORE hover.
	bgBefore := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "getComputedStyle(document.getElementById('hover-target')).backgroundColor",
	})

	// Hover over the element.
	callTool[struct{}](t, "hover", map[string]any{
		"tab":      tabID,
		"selector": "#hover-target",
	})

	// Verify JS mouseover event fired (CDP mouseMoved triggers native events).
	eval := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('hover-output').textContent",
	})
	if string(eval.Result) != `"hovered"` {
		t.Errorf("JS mouseover event not fired: got %s", eval.Result)
	}

	// Check background color AFTER hover — CSS :hover SHOULD activate
	// because we use CDP Input.dispatchMouseEvent (mouseMoved).
	bgAfter := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "getComputedStyle(document.getElementById('hover-target')).backgroundColor",
	})

	if string(bgBefore.Result) == string(bgAfter.Result) {
		t.Errorf("CSS :hover not activated: background before=%s, after=%s (should change to yellow)",
			bgBefore.Result, bgAfter.Result)
	}
}

func TestSelectOptionMultipleSelect(t *testing.T) {
	tabID := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tabID)

	// Select multiple options in a single call.
	callTool[struct{}](t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#multi-select",
		"values":   []string{"red", "green"},
	})
	eval := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "Array.from(document.getElementById('multi-select').selectedOptions).map(o => o.value).join(',')",
	})
	if string(eval.Result) != `"red,green"` {
		t.Errorf("selected options = %s, want \"red,green\"", eval.Result)
	}

	// Subsequent call replaces selection rather than accumulating.
	callTool[struct{}](t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#multi-select",
		"values":   []string{"blue"},
	})
	eval2 := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "Array.from(document.getElementById('multi-select').selectedOptions).map(o => o.value).join(',')",
	})
	if string(eval2.Result) != `"blue"` {
		t.Errorf("after replace, selected = %s, want \"blue\"", eval2.Result)
	}

	// Passing empty slice deselects all options on <select multiple>.
	callTool[struct{}](t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#multi-select",
		"values":   []string{},
	})
	evalEmpty := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('multi-select').selectedOptions.length",
	})
	if string(evalEmpty.Result) != "0" {
		t.Errorf("after [] deselect, selectedOptions.length = %s, want 0", evalEmpty.Result)
	}

	// Single <select> rejects 0 or >1 items.
	if errText := callToolExpectErr(t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#big-select",
		"values":   []string{},
	}); !strings.Contains(errText, "single <select> requires exactly one") {
		t.Errorf("single select empty err = %q", errText)
	}
	if errText := callToolExpectErr(t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#big-select",
		"values":   []string{"a", "b"},
	}); !strings.Contains(errText, "single <select> requires exactly one") {
		t.Errorf("single select multi err = %q", errText)
	}

	// Verify change event fired.
	eval3 := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('multi-select-output').textContent",
	})
	if !strings.Contains(string(eval3.Result), "multi:") {
		t.Errorf("change event not fired: %s", eval3.Result)
	}
}

func TestSelectOptionMultipleByLabel(t *testing.T) {
	tabID := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#multi-select",
		"labels":   []string{"Blue"},
	})

	eval := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('multi-select').value",
	})
	if string(eval.Result) != `"blue"` {
		t.Errorf("selected by label = %s, want \"blue\"", eval.Result)
	}
}

func TestSelectOptionMultipleByIndex(t *testing.T) {
	tabID := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tabID)

	idx := 2 // "Blue"
	callTool[struct{}](t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#multi-select",
		"indexes":  []int{idx},
	})

	eval := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('multi-select').value",
	})
	if string(eval.Result) != `"blue"` {
		t.Errorf("selected by index = %s, want \"blue\"", eval.Result)
	}
}

func TestSelectOptionByValueNonexistent(t *testing.T) {
	tabID := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tabID)

	// Set a value that doesn't match any option — should error.
	errText := callToolExpectErr(t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#big-select",
		"values":   []string{"nonexistent"},
	})
	if !strings.Contains(strings.ToLower(errText), "no option with value") {
		t.Errorf("expected 'no option with value' error, got: %s", errText)
	}
}

func TestClickHiddenElement(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// #hidden-btn is display:none. click should fail with a clear message
	// that the element exists but is not visible.
	errText := callToolExpectErr(t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#hidden-btn",
		"timeout":  500,
	})
	if errText == "" {
		t.Fatal("click on hidden element should error")
	}
	if !strings.Contains(errText, "not visible") {
		t.Errorf("error should mention 'not visible', got: %s", errText)
	}
}

func TestClickRightHiddenElement(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#hidden-btn",
		"button":   "right",
		"timeout":  500,
	})
	if errText == "" {
		t.Fatal("right-click on hidden element should error")
	}
	if !strings.Contains(errText, "not visible") {
		t.Errorf("error should mention 'not visible', got: %s", errText)
	}
}

func TestClickNonExistentElement(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#does-not-exist",
		"timeout":  500,
	})
	if errText == "" {
		t.Fatal("click on non-existent element should error")
	}
	if !strings.Contains(errText, "not found") {
		t.Errorf("error should mention 'not found', got: %s", errText)
	}
}

func TestClickZeroSizeElement(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// chromedp clicks zero-size elements without error (they exist in DOM,
	// just have no visual dimensions). This matches browser behavior —
	// you can programmatically click hidden inputs.
	callTool[struct{}](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#zero-size-input",
	})
}

func TestClickToggleRevealsElement(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// The wiki-search-input is initially hidden. Clicking the toggle reveals it.
	callTool[struct{}](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#wiki-search-toggle",
	})

	// Now the input should be visible and clickable.
	callTool[struct{}](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#wiki-search-input",
	})

	// Verify the input is focused.
	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.activeElement.id",
	})
	if string(out.Result) != `"wiki-search-input"` {
		t.Errorf("active element = %s, want wiki-search-input", out.Result)
	}
}

func TestPressEnterSubmitsForm(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// Type into search input.
	callTool[struct{}](t, "type", map[string]any{
		"tab":      tabID,
		"selector": "#search-input",
		"text":     "test query",
		"mode":     "replace",
	})

	// Press Enter — should trigger form submission.
	callTool[struct{}](t, "press_key", map[string]any{
		"tab": tabID,
		"key": "Enter",
	})

	// Wait for the submit handler to update the DOM.
	callTool[struct{}](t, "wait_for", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('search-submitted').textContent !== ''",
		"timeout":    3000,
	})

	// Check that the form's submit handler was called.
	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('search-submitted').textContent",
	})
	if string(out.Result) != `"submitted:test query"` {
		t.Errorf("form submit result = %s, want 'submitted:test query'", out.Result)
	}
}

func TestPressEnterAfterFocus(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// Type using press_key character by character, focusing on the first key.
	for i, ch := range "hello" {
		args := map[string]any{"tab": tabID, "key": string(ch)}
		if i == 0 {
			args["selector"] = "#search-input"
		}
		callTool[struct{}](t, "press_key", args)
	}

	// Press Enter.
	callTool[struct{}](t, "press_key", map[string]any{
		"tab": tabID,
		"key": "Enter",
	})

	// Wait for the submit handler to update the DOM.
	callTool[struct{}](t, "wait_for", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('search-submitted').textContent !== ''",
		"timeout":    3000,
	})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('search-submitted').textContent",
	})
	if string(out.Result) != `"submitted:hello"` {
		t.Errorf("form submit after focus+type = %s, want 'submitted:hello'", out.Result)
	}
}

// TestPressKeyUTF8Character verifies that a multi-byte UTF-8 character
// (e.g. an emoji or accented char) can be pressed via the UTF-8 decode path.
func TestPressKeyUTF8Character(t *testing.T) {
	tabID := navigateToFixture(t, "forms.html")
	defer closeTab(t, tabID)

	// Focus the input and press an accented character (é) which is multi-byte UTF-8.
	callTool[struct{}](t, "press_key", map[string]any{
		"tab":      tabID,
		"selector": "#name",
		"key":      "é",
	})

	// Verify the character was typed.
	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `document.querySelector('#name').value`,
	})
	var val string
	if err := json.Unmarshal(out.Result, &val); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if val != "é" {
		t.Errorf("input value = %q, want %q", val, "é")
	}
}

func TestPressKeyInvalidModifier(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "press_key", map[string]any{
		"tab":       tabID,
		"key":       "a",
		"modifiers": []string{"invalid_mod"},
	})
	if !strings.Contains(errText, "modifiers") || !strings.Contains(errText, "invalid_mod") {
		t.Errorf("error = %q, want to mention modifiers and 'invalid_mod'", errText)
	}
}

func TestPressKeyMultipleInvalidModifiers(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// Mix valid and invalid modifiers — the first invalid one should trigger error.
	errText := callToolExpectErr(t, "press_key", map[string]any{
		"tab":       tabID,
		"key":       "a",
		"modifiers": []string{"ctrl", "superkey"},
	})
	if !strings.Contains(errText, "modifiers") {
		t.Errorf("error = %q, want to mention 'modifiers'", errText)
	}
	if !strings.Contains(errText, "superkey") {
		t.Errorf("error = %q, want to mention the invalid modifier 'superkey'", errText)
	}
}

func TestPressKeyValidModifiersStillWork(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// Verify all valid modifiers still work after the validation was added.
	for _, mod := range []string{"ctrl", "shift", "alt", "meta"} {
		t.Run(mod, func(t *testing.T) {
			callTool[struct{}](t, "press_key", map[string]any{
				"tab":       tabID,
				"key":       "a",
				"modifiers": []string{mod},
			})
		})
	}
}

func TestPressKeyUnknownKeyName(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "press_key", map[string]any{
		"tab": tabID,
		"key": "NonExistentKey",
	})
	if !strings.Contains(errText, "unknown key") {
		t.Errorf("error = %q, want to contain 'unknown key'", errText)
	}
}

func TestSelectOptionEmptyValue(t *testing.T) {
	tabID := navigateToFixture(t, "forms.html")
	defer closeTab(t, tabID)

	// First select "low" so we have a non-empty selection.
	callTool[struct{}](t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#priority",
		"values":   []string{"low"},
	})

	// Verify it was set to "low".
	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('priority').value",
	})
	if string(out.Result) != `"low"` {
		t.Fatalf("value after selecting low = %s, want \"low\"", out.Result)
	}

	// Now select the empty-value option (the placeholder).
	callTool[struct{}](t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#priority",
		"values":   []string{""},
	})

	// Verify it was set back to "".
	out = callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('priority').value",
	})
	if string(out.Result) != `""` {
		t.Errorf("value after selecting empty = %s, want \"\"", out.Result)
	}
}

func TestPressKeyInsertsCharacter(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// Press individual keys, focusing the input on the first key.
	for i, key := range []string{"a", "b", "c"} {
		args := map[string]any{"tab": tabID, "key": key}
		if i == 0 {
			args["selector"] = "#type-target"
		}
		callTool[struct{}](t, "press_key", args)
	}

	// Verify the input has the typed characters.
	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('type-target').value",
	})
	if string(out.Result) != `"abc"` {
		t.Errorf("input value after press_key = %s, want \"abc\"", out.Result)
	}
}

// TestSelectOptionSelectorTimeout verifies select_option times out cleanly.
func TestSelectOptionSelectorTimeout(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "select_option", map[string]any{
		"tab":      tabID,
		"selector": "#nonexistent-select",
		"values":   []string{"x"},
		"timeout":  500,
	})
	if !strings.Contains(errText, "not found") {
		t.Fatalf("expected 'not found' error, got: %s", errText)
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

// findSnapshotRef extracts the [ref=eN] token from the first snapshot line
// containing needle.
func findSnapshotRef(t *testing.T, snap, needle string) string {
	t.Helper()
	for line := range strings.SplitSeq(snap, "\n") {
		if !strings.Contains(line, needle) {
			continue
		}
		idx := strings.Index(line, "[ref=")
		if idx < 0 {
			continue
		}
		rest := line[idx+len("[ref="):]
		end := strings.IndexByte(rest, ']')
		if end > 0 {
			return rest[:end]
		}
	}
	t.Fatalf("could not find [ref=...] on line containing %q in snapshot:\n%s", needle, snap)
	return ""
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

func TestInteractionViewportCoordinatesAndDialogObserveWindow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ping" {
			_, _ = w.Write([]byte("pong"))
			return
		}
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

	// dialog(action="accept", observe_window_ms=300) observes post-dialog fetch.
	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `setTimeout(() => { if (confirm('fetch?')) fetch('/ping'); }, 15); true`,
	})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if callTool[dialogOutput](t, "dialog", map[string]any{"tab": tabID, "action": "get"}).Open {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	dlgOut := callTool[dialogOutput](t, "dialog", map[string]any{
		"tab":               tabID,
		"action":            "accept",
		"observe_window_ms": 300,
	})
	if dlgOut.Open {
		t.Errorf("dialog(accept) still Open=true")
	}
	if dlgOut.Observed == nil || !dlgOut.Observed.HasAnyEffect || dlgOut.Observed.NetworkRequests < 1 {
		t.Errorf("dialog(accept, observe_window_ms) Observed = %+v, want HasAnyEffect=true NetworkRequests>=1", dlgOut.Observed)
	}

	// Chained dialog opening during observe_window_ms must be reflected in Open/Dialog.
	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `setTimeout(() => { if (confirm('first')) setTimeout(() => alert('second'), 30); }, 15); true`,
	})
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if callTool[dialogOutput](t, "dialog", map[string]any{"tab": tabID, "action": "get"}).Open {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	chained := callTool[dialogOutput](t, "dialog", map[string]any{
		"tab":               tabID,
		"action":            "accept",
		"observe_window_ms": 200,
	})
	if !chained.Open || chained.Dialog == nil || chained.Dialog.Message != "second" {
		t.Errorf("chained dialog during observe_window_ms = %+v, want Open=true Message='second'", chained)
	}
	handleDialog(t, tabID, map[string]any{"action": "accept"})
}

func TestTypeAndPressKeyRejectNonFocusableAndCaretEmailShadow(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": `(function() {
			document.body.innerHTML =
				'<input id="active-inp" value="keep">' +
				'<input id="disabled-inp" value="untouched" disabled>' +
				'<div id="plain-div">not focusable</div>' +
				'<input id="email-inp" type="email" value="a@b.com">' +
				'<div id="ce-host"></div>' +
				'<button id="vanish-hidden" style="display:none">Hidden</button>';
			const sr = document.getElementById('ce-host').attachShadow({mode: 'open'});
			sr.innerHTML = '<div id="ce" contenteditable="true">hello</div>';
			document.getElementById('active-inp').focus();
		})()`,
	})

	// Non-focusable targets must error without clearing their value or typing into previously focused inputs.
	if errText := callToolExpectErr(t, "type", map[string]any{
		"tab":      tabID,
		"selector": "#disabled-inp",
		"text":     "secret",
		"mode":     "replace",
	}); !strings.Contains(strings.ToLower(errText), "focus") {
		t.Errorf("type on disabled input err = %q, want focus error", errText)
	}
	if errText := callToolExpectErr(t, "press_key", map[string]any{
		"tab":      tabID,
		"selector": "#plain-div",
		"key":      "Z",
	}); !strings.Contains(strings.ToLower(errText), "focus") {
		t.Errorf("press_key on plain div err = %q, want focus error", errText)
	}
	guardCheck := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `({active: document.getElementById('active-inp').value, disabled: document.getElementById('disabled-inp').value})`,
	})
	if string(guardCheck.Result) != `{"active":"keep","disabled":"untouched"}` {
		t.Errorf("non-focusable target mutated wrong element: %s", guardCheck.Result)
	}

	// type(append) on <input type="email"> and Shadow DOM contenteditable appends at end.
	callTool[actionOutput](t, "type", map[string]any{
		"tab":      tabID,
		"selector": "#email-inp",
		"text":     ".uk",
		"mode":     "append",
	})
	emailVal := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `document.getElementById('email-inp').value`,
	})
	if string(emailVal.Result) != `"a@b.com.uk"` {
		t.Errorf("email append value = %s, want \"a@b.com.uk\"", emailVal.Result)
	}

	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": `(function() {
			const ce = document.getElementById('ce-host').shadowRoot.getElementById('ce');
			ce.focus();
			const r = document.createRange();
			r.setStart(ce.firstChild, 0);
			r.collapse(true);
			const sr = ce.getRootNode();
			const sel = (sr.getSelection ? sr.getSelection() : document.getSelection());
			if (sel) { sel.removeAllRanges(); sel.addRange(r); }
		})()`,
	})
	callTool[actionOutput](t, "type", map[string]any{
		"tab":      tabID,
		"selector": "#ce-host >>> #ce",
		"text":     " world",
		"mode":     "append",
	})
	ceText := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `document.getElementById('ce-host').shadowRoot.getElementById('ce').textContent`,
	})
	if string(ceText.Result) != `"hello world"` {
		t.Errorf("shadow contenteditable append = %s, want \"hello world\"", ceText.Result)
	}

	// Clicking a display:none element removed mid-wait fails fast with a detached error.
	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `setTimeout(() => document.getElementById('vanish-hidden').remove(), 60); true`,
	})
	start := time.Now()
	errText := callToolExpectErr(t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#vanish-hidden",
		"timeout":  4000,
	})
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Errorf("click on detached hidden element took %v, want fast failure", elapsed)
	}
	if !strings.Contains(errText, "detached") {
		t.Errorf("click on detached hidden element err = %q, want 'detached'", errText)
	}

	// drag_and_drop whose drop handler opens a synchronous alert returns promptly with dialog open.
	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": `(function() {
			document.body.innerHTML =
				'<div id="d-src" draggable="true" style="position:fixed;left:20px;top:20px;width:60px;height:40px;border:1px solid #000;">S</div>' +
				'<div id="d-dst" style="position:fixed;left:140px;top:20px;width:60px;height:40px;border:1px solid #000;">D</div>';
			document.getElementById('d-src').addEventListener('dragstart', e => e.dataTransfer.setData('text/plain', 'dlg'));
			document.getElementById('d-dst').addEventListener('dragover', e => e.preventDefault());
			document.getElementById('d-dst').addEventListener('drop', e => {
				e.preventDefault();
				alert('drop-alert');
			});
		})()`,
	})
	dragStart := time.Now()
	callTool[actionOutput](t, "drag_and_drop", map[string]any{
		"tab":             tabID,
		"source_selector": "#d-src",
		"target_selector": "#d-dst",
		"steps":           3,
	})
	if elapsed := time.Since(dragStart); elapsed > 2*time.Second {
		t.Errorf("drag_and_drop opening sync alert took %v, want prompt return", elapsed)
	}
	defer handleDialog(t, tabID, map[string]any{"action": "accept"})
	if d := callTool[dialogOutput](t, "dialog", map[string]any{"tab": tabID, "action": "get"}); !d.Open || d.Dialog == nil || d.Dialog.Message != "drop-alert" {
		t.Errorf("dialog after drop-alert = %+v, want Open=true Message='drop-alert'", d)
	}
}
