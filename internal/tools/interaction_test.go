package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
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

	tabObj, err := ensureHarness(t).mgr.ResolveTab("", tabID)
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

// TestClickAriaDisabledRejected verifies clicking an aria-disabled="true"
// element errors before dispatching, even though the DOM would otherwise
// fire the click handler. This is precisely the case where the browser
// itself doesn't block the click but the LLM's plan is almost certainly
// wrong.
func TestClickAriaDisabledRejected(t *testing.T) {
	tabID := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#aria-disabled-btn",
	})
	if !strings.Contains(errText, "aria-disabled") {
		t.Errorf("error %q should mention 'aria-disabled'", errText)
	}

	// Page-side handler must not have fired.
	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('aria-disabled-output').textContent",
	})
	if strings.Contains(string(out.Result), "clicked-aria-disabled") {
		t.Error("aria-disabled click handler fired despite the pre-click error")
	}
}

// TestClickPointerEventsNoneWarning verifies a click on an element with
// CSS pointer-events:none succeeds (the dispatch is valid) but the
// returned state carries a warning so the LLM can spot the no-effect.
func TestClickPointerEventsNoneWarning(t *testing.T) {
	tabID := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tabID)

	out := callTool[clickOutput](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#pointer-events-none-btn",
	})
	if out.PointerEvents != "none" {
		t.Errorf("PointerEvents = %q, want 'none'", out.PointerEvents)
	}
	if out.Disabled || out.AriaDisabled {
		t.Errorf("disabled flags should be false; got disabled=%v aria_disabled=%v", out.Disabled, out.AriaDisabled)
	}
	if !strings.Contains(out.Warning, "pointer-events") {
		t.Errorf("Warning %q should mention 'pointer-events'", out.Warning)
	}

	// Page-side handler must not fire because the browser doesn't deliver
	// mouse events to pointer-events:none elements.
	state := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('pointer-events-none-output').textContent",
	})
	if strings.Contains(string(state.Result), "clicked-pointer-events-none") {
		t.Errorf("pointer-events:none handler fired; expected the click to be swallowed")
	}
}

// TestClickDisplayNoneErrors verifies a click on a display:none element
// times out via chromedp's NodeVisible wait — the LLM gets a clear
// failure signal even though the warning path doesn't fire (the dispatch
// never happens).
func TestClickDisplayNoneErrors(t *testing.T) {
	tabID := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#hidden-btn",
		"timeout":  500,
	})
	if errText == "" {
		t.Error("click on display:none element should error")
	}
}

// TestClickInvisibleWarning verifies the visible=false warning path on
// an element with visibility:hidden — it has a layout box (so chromedp's
// NodeVisible can find it) but the user cannot see it. The click is
// dispatched and the page-side handler does fire, but the warning
// surfaces the no-effect-from-the-user's-POV state.
func TestClickInvisibleWarning(t *testing.T) {
	tabID := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tabID)

	out := callTool[clickOutput](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#invisible-btn",
	})
	if out.Visible {
		t.Error("Visible should be false on visibility:hidden element")
	}
	if !strings.Contains(out.Warning, "not visible") {
		t.Errorf("Warning %q should mention 'not visible'", out.Warning)
	}
}

// TestClickFieldsetDisabledRejected verifies an input nested inside a
// disabled fieldset is rejected pre-dispatch. The input itself has no
// `disabled` attribute, so a probe based on `el.disabled` would miss
// this — the implementation must use `el.matches(':disabled')`.
func TestClickFieldsetDisabledRejected(t *testing.T) {
	tabID := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#fieldset-input",
	})
	if !strings.Contains(errText, "disabled") {
		t.Errorf("error %q should mention 'disabled' (fieldset propagation)", errText)
	}

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('fieldset-input-output').textContent",
	})
	if strings.Contains(string(out.Result), "clicked-fieldset-input") {
		t.Error("fieldset-disabled input handler fired despite the pre-click error")
	}
}

// TestClickNormalButtonReturnsCleanState verifies a successful click on
// a regular button returns visible=true, disabled=false, no warning.
func TestClickNormalButtonReturnsCleanState(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	out := callTool[clickOutput](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#click-target",
	})
	if out.Disabled || out.AriaDisabled {
		t.Errorf("normal button should not be disabled; got disabled=%v aria_disabled=%v",
			out.Disabled, out.AriaDisabled)
	}
	if !out.Visible {
		t.Error("normal button should be visible=true")
	}
	if out.PointerEvents != "auto" {
		t.Errorf("default pointer-events = %q, want 'auto'", out.PointerEvents)
	}
	if out.Warning != "" {
		t.Errorf("Warning = %q on a clean click, want empty", out.Warning)
	}
}

// ===========================================================================
// Scroll: offset scroll returns position
// ===========================================================================

func TestScrollOffsetReturnsPosition(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	out := callTool[scrollOutput](t, "scroll", map[string]any{
		"tab": tabID,
		"y":   500,
	})
	if out.ScrollY < 1 {
		t.Errorf("after scroll y=500, ScrollY = %f, want > 0", out.ScrollY)
	}
	if out.ScrollX != 0 {
		t.Errorf("after scroll y=500, ScrollX = %f, want 0", out.ScrollX)
	}
}

// ===========================================================================
// Scroll: negative offset returns decreased position
// ===========================================================================

func TestScrollNegativeReturnsPosition(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// Scroll down first.
	out1 := callTool[scrollOutput](t, "scroll", map[string]any{
		"tab": tabID,
		"y":   500,
	})

	// Scroll up.
	out2 := callTool[scrollOutput](t, "scroll", map[string]any{
		"tab": tabID,
		"y":   -200,
	})

	if out2.ScrollY >= out1.ScrollY {
		t.Errorf("after negative scroll, ScrollY = %f, expected < %f", out2.ScrollY, out1.ScrollY)
	}
	if out2.ScrollY < 0 {
		t.Errorf("ScrollY should not be negative, got %f", out2.ScrollY)
	}
}

// ===========================================================================
// Scroll: clamping at top boundary
// ===========================================================================

func TestScrollClampedAtTop(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// Scroll down a bit.
	callTool[scrollOutput](t, "scroll", map[string]any{
		"tab": tabID,
		"y":   100,
	})

	// Scroll up by more than current position — should clamp at 0.
	out := callTool[scrollOutput](t, "scroll", map[string]any{
		"tab": tabID,
		"y":   -9999,
	})
	if out.ScrollY != 0 {
		t.Errorf("after scrolling past top, ScrollY = %f, want 0", out.ScrollY)
	}
}

// ===========================================================================
// Scroll: horizontal scroll returns position
// ===========================================================================

func TestScrollHorizontalReturnsPosition(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	out := callTool[scrollOutput](t, "scroll", map[string]any{
		"tab": tabID,
		"x":   300,
	})
	if out.ScrollX < 1 {
		t.Errorf("after scroll x=300, ScrollX = %f, want > 0", out.ScrollX)
	}
}

// ===========================================================================
// Scroll: scroll into view returns position
// ===========================================================================

func TestScrollIntoViewReturnsPosition(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// The #scroll-marker is at margin-top: 2000px, so scrolling into view
	// should result in a non-zero scrollY.
	out := callTool[scrollOutput](t, "scroll", map[string]any{
		"tab":      tabID,
		"selector": "#scroll-marker",
	})
	if out.ScrollY < 1 {
		t.Errorf("after scroll into view of #scroll-marker, ScrollY = %f, want > 0", out.ScrollY)
	}
}

// ===========================================================================
// Scroll: both axes returns correct positions
// ===========================================================================

func TestScrollBothAxesReturnsPositions(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	out := callTool[scrollOutput](t, "scroll", map[string]any{
		"tab": tabID,
		"x":   200,
		"y":   400,
	})
	if out.ScrollX < 1 {
		t.Errorf("after scroll x=200,y=400, ScrollX = %f, want > 0", out.ScrollX)
	}
	if out.ScrollY < 1 {
		t.Errorf("after scroll x=200,y=400, ScrollY = %f, want > 0", out.ScrollY)
	}
}

// ---------------------------------------------------------------------------
// Scroll position: verify values are approximately correct
// ---------------------------------------------------------------------------

func TestScrollPositionApproximatelyCorrect(t *testing.T) {
	tabID := navigateToFixture(t, "cdp-coords.html")
	defer closeTab(t, tabID)

	out := callTool[scrollOutput](t, "scroll", map[string]any{
		"tab": tabID,
		"y":   500,
	})
	// page.GetLayoutMetrics CSSVisualViewport.PageY should be ~500.
	if math.Abs(out.ScrollY-500) > 5 {
		t.Errorf("scroll y=500: ScrollY = %f, want ~500 (±5)", out.ScrollY)
	}
	if out.ScrollX != 0 {
		t.Errorf("scroll y=500: ScrollX = %f, want 0", out.ScrollX)
	}
}

func TestScrollPositionHorizontalApproximatelyCorrect(t *testing.T) {
	tabID := navigateToFixture(t, "cdp-coords.html")
	defer closeTab(t, tabID)

	out := callTool[scrollOutput](t, "scroll", map[string]any{
		"tab": tabID,
		"x":   300,
	})
	if math.Abs(out.ScrollX-300) > 5 {
		t.Errorf("scroll x=300: ScrollX = %f, want ~300 (±5)", out.ScrollX)
	}
	if out.ScrollY != 0 {
		t.Errorf("scroll x=300: ScrollY = %f, want 0", out.ScrollY)
	}
}

func TestScrollPositionBothAxesApproximatelyCorrect(t *testing.T) {
	tabID := navigateToFixture(t, "cdp-coords.html")
	defer closeTab(t, tabID)

	out := callTool[scrollOutput](t, "scroll", map[string]any{
		"tab": tabID,
		"x":   200,
		"y":   400,
	})
	if math.Abs(out.ScrollX-200) > 5 {
		t.Errorf("scroll x=200,y=400: ScrollX = %f, want ~200 (±5)", out.ScrollX)
	}
	if math.Abs(out.ScrollY-400) > 5 {
		t.Errorf("scroll x=200,y=400: ScrollY = %f, want ~400 (±5)", out.ScrollY)
	}
}

func TestScrollPositionIncrementalAccumulates(t *testing.T) {
	tabID := navigateToFixture(t, "cdp-coords.html")
	defer closeTab(t, tabID)

	// Scroll 300, then 200 more. Total should be ~500.
	callTool[scrollOutput](t, "scroll", map[string]any{
		"tab": tabID,
		"y":   300,
	})
	out := callTool[scrollOutput](t, "scroll", map[string]any{
		"tab": tabID,
		"y":   200,
	})
	if math.Abs(out.ScrollY-500) > 5 {
		t.Errorf("incremental scroll 300+200: ScrollY = %f, want ~500 (±5)", out.ScrollY)
	}
}

func TestScrollIntoViewPositionMatchesElement(t *testing.T) {
	tabID := navigateToFixture(t, "cdp-coords.html")
	defer closeTab(t, tabID)

	// #bottom-box is at top: 3000px. After scroll-into-view, ScrollY should
	// be close to the element's position. The exact value depends on viewport
	// height — the element should be near the bottom of the viewport, so
	// ScrollY ≈ elementTop - viewportHeight + elementHeight.
	// With default viewport 1080px: ~3000 - 1080 + 100 = ~2020.
	// Allow generous tolerance since viewport may vary.
	out := callTool[scrollOutput](t, "scroll", map[string]any{
		"tab":      tabID,
		"selector": "#bottom-box",
	})
	if out.ScrollY < 1500 || out.ScrollY > 3100 {
		t.Errorf("scroll into view #bottom-box (top:3000px): ScrollY = %f, expected 1500..3100", out.ScrollY)
	}
}

// ---------------------------------------------------------------------------
// Hover: coordinate accuracy via clientX/clientY
// ---------------------------------------------------------------------------

// parseCoordOutput parses "elementID:x,y" format from the fixture's output divs.
func parseCoordOutput(t *testing.T, raw string) (id string, x, y int) {
	t.Helper()
	parts := strings.SplitN(raw, ":", 2)
	if len(parts) != 2 {
		t.Fatalf("unexpected coord output format: %q", raw)
	}
	id = parts[0]
	_, err := fmt.Sscanf(parts[1], "%d,%d", &x, &y)
	if err != nil {
		t.Fatalf("parsing coordinates from %q: %v", raw, err)
	}
	return
}

func TestHoverCoordinateAccuracy(t *testing.T) {
	tabID := navigateToFixture(t, "cdp-coords.html")
	defer closeTab(t, tabID)

	// #top-box is at (50,50) with size 100x80, so center is (100, 90).
	callTool[actionOutput](t, "hover", map[string]any{
		"tab":      tabID,
		"selector": "#top-box",
	})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('hover-output').textContent",
	})
	raw := strings.Trim(string(out.Result), "\"")
	if raw == "" {
		t.Fatal("hover did not trigger mousemove event on #top-box")
	}
	id, cx, cy := parseCoordOutput(t, raw)
	if id != "top-box" {
		t.Errorf("hover hit element %q, want 'top-box'", id)
	}
	// Center of #top-box: (50+50, 50+40) = (100, 90). Allow ±5 tolerance.
	if math.Abs(float64(cx)-100) > 5 {
		t.Errorf("hover clientX = %d, want ~100 (center of 50..150)", cx)
	}
	if math.Abs(float64(cy)-90) > 5 {
		t.Errorf("hover clientY = %d, want ~90 (center of 50..130)", cy)
	}
}

func TestHoverOnScrolledPage(t *testing.T) {
	tabID := navigateToFixture(t, "cdp-coords.html")
	defer closeTab(t, tabID)

	// Scroll #bottom-box into view first. It's at top:3000px, left:200px, 150x100.
	callTool[scrollOutput](t, "scroll", map[string]any{
		"tab":      tabID,
		"selector": "#bottom-box",
	})

	// Now hover on it. GetContentQuads returns viewport-relative coords,
	// so this should work correctly even after scrolling.
	callTool[actionOutput](t, "hover", map[string]any{
		"tab":      tabID,
		"selector": "#bottom-box",
	})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('hover-output').textContent",
	})
	raw := strings.Trim(string(out.Result), "\"")
	if raw == "" {
		t.Fatal("hover did not trigger mousemove on #bottom-box after scroll")
	}
	id, _, _ := parseCoordOutput(t, raw)
	if id != "bottom-box" {
		t.Errorf("hover after scroll hit %q, want 'bottom-box'", id)
	}
}

// ---------------------------------------------------------------------------
// Right-click: coordinate accuracy and scrolled page
// ---------------------------------------------------------------------------

func TestRightClickCoordinateAccuracy(t *testing.T) {
	tabID := navigateToFixture(t, "cdp-coords.html")
	defer closeTab(t, tabID)

	// Right-click #top-box. Center is (100, 90).
	callTool[clickOutput](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#top-box",
		"button":   "right",
	})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('right-click-output').textContent",
	})
	raw := strings.Trim(string(out.Result), "\"")
	if raw == "" {
		t.Fatal("right-click did not trigger mousedown(button=2) on #top-box")
	}
	id, cx, cy := parseCoordOutput(t, raw)
	if id != "top-box" {
		t.Errorf("right-click hit %q, want 'top-box'", id)
	}
	if math.Abs(float64(cx)-100) > 5 {
		t.Errorf("right-click clientX = %d, want ~100", cx)
	}
	if math.Abs(float64(cy)-90) > 5 {
		t.Errorf("right-click clientY = %d, want ~90", cy)
	}
}

func TestRightClickOnScrolledPage(t *testing.T) {
	tabID := navigateToFixture(t, "cdp-coords.html")
	defer closeTab(t, tabID)

	// Scroll to #bottom-box and right-click it.
	callTool[scrollOutput](t, "scroll", map[string]any{
		"tab":      tabID,
		"selector": "#bottom-box",
	})

	callTool[clickOutput](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#bottom-box",
		"button":   "right",
	})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('right-click-output').textContent",
	})
	raw := strings.Trim(string(out.Result), "\"")
	if raw == "" {
		t.Fatal("right-click did not trigger mousedown on #bottom-box after scroll")
	}
	id, _, _ := parseCoordOutput(t, raw)
	if id != "bottom-box" {
		t.Errorf("right-click after scroll hit %q, want 'bottom-box'", id)
	}
}

func TestMiddleClickOnScrolledPage(t *testing.T) {
	tabID := navigateToFixture(t, "cdp-coords.html")
	defer closeTab(t, tabID)

	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": `(function() {
			var el = document.getElementById('bottom-box');
			el.addEventListener('mousedown', function(e) {
				if (e.button === 1) { el.dataset.middleClick = 'yes'; }
			});
		})()`,
	})

	// Scroll to #bottom-box and middle-click it.
	callTool[scrollOutput](t, "scroll", map[string]any{
		"tab":      tabID,
		"selector": "#bottom-box",
	})

	callTool[clickOutput](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#bottom-box",
		"button":   "middle",
	})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `document.getElementById('bottom-box').dataset.middleClick || ''`,
	})
	if string(out.Result) != `"yes"` {
		t.Errorf("middle click on scrolled #bottom-box did not fire mousedown(button=1); got %s", out.Result)
	}
}

// ---------------------------------------------------------------------------
// Scroll position: cross-check CDP GetLayoutMetrics vs JS window.scrollX/Y
// ---------------------------------------------------------------------------

func TestScrollPositionMatchesJS(t *testing.T) {
	tabID := navigateToFixture(t, "cdp-coords.html")
	defer closeTab(t, tabID)

	// Scroll to a known position.
	out := callTool[scrollOutput](t, "scroll", map[string]any{
		"tab": tabID,
		"x":   150,
		"y":   750,
	})

	// Cross-check against JS window.scrollX/scrollY.
	var jsPos struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
	}
	jsOut := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "({x: window.scrollX, y: window.scrollY})",
	})
	if err := json.Unmarshal(jsOut.Result, &jsPos); err != nil {
		t.Fatalf("parsing JS scroll position: %v (raw: %s)", err, jsOut.Result)
	}

	if math.Abs(out.ScrollX-jsPos.X) > 1 {
		t.Errorf("ScrollX mismatch: CDP=%f, JS=%f", out.ScrollX, jsPos.X)
	}
	if math.Abs(out.ScrollY-jsPos.Y) > 1 {
		t.Errorf("ScrollY mismatch: CDP=%f, JS=%f", out.ScrollY, jsPos.Y)
	}
}
