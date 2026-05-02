package tools

import (
	"strings"
	"testing"
)

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
	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
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

	out := callTool[ClickOutput](t, "click", map[string]any{
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
	state := callTool[EvaluateOutput](t, "evaluate", map[string]any{
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

	out := callTool[ClickOutput](t, "click", map[string]any{
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

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
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

	out := callTool[ClickOutput](t, "click", map[string]any{
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
