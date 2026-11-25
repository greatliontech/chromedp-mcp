package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/browser"
)

// probeClickTarget reads the click-relevant state of an element via JS:
// disabled (via the :disabled pseudo-class so <fieldset disabled> and
// <optgroup disabled> propagation are caught — the IDL `el.disabled`
// property only reflects the element's own attribute), aria-disabled,
// computed pointer-events, and a visibility heuristic.
//
// `document.querySelector` returns the first match — the same first-match
// semantics chromedp.Click uses via dom.QuerySelector, so the probed node
// is the same node the dispatch will target unless the DOM mutates
// between calls.
func probeClickTarget(ctx context.Context, selector string) (ClickOutput, bool, error) {
	js := fmt.Sprintf(`(function() {
		var el = document.querySelector(%s);
		if (!el) return null;
		var cs = window.getComputedStyle(el);
		var rect = el.getBoundingClientRect();
		return {
			disabled: el.matches(':disabled'),
			aria_disabled: el.getAttribute('aria-disabled') === 'true',
			pointer_events: cs.pointerEvents,
			visible: cs.display !== 'none' &&
				cs.visibility !== 'hidden' &&
				rect.width > 0 && rect.height > 0,
		};
	})()`, jsQuote(selector))

	type clickProbe struct {
		Disabled      bool   `json:"disabled"`
		AriaDisabled  bool   `json:"aria_disabled"`
		PointerEvents string `json:"pointer_events"`
		Visible       bool   `json:"visible"`
	}
	raw, err := chromedp.Run(ctx, chromedp.Evaluate[*clickProbe](js))
	if err != nil {
		return ClickOutput{}, false, fmt.Errorf("probe %q: %w", selector, err)
	}
	if raw == nil {
		return ClickOutput{}, false, nil
	}
	return ClickOutput{
		Disabled:      raw.Disabled,
		AriaDisabled:  raw.AriaDisabled,
		PointerEvents: raw.PointerEvents,
		Visible:       raw.Visible,
	}, true, nil
}

// clickWarning returns a pre-dispatch warning string when the target's state
// suggests the mouse event was likely swallowed (CSS pointer-events:none or
// hidden element). Disabled and aria-disabled error before reaching dispatch.
func clickWarning(state ClickOutput) string {
	switch {
	case state.PointerEvents == "none":
		return "element has pointer-events: none; the click was dispatched but the browser will not deliver mouse events to it"
	case !state.Visible:
		return "element is not visible (display:none, visibility:hidden, or zero-size); the click was dispatched but no handler is likely to fire"
	default:
		return ""
	}
}

// finalizeClickState re-probes the target after dispatch under a short bounded
// timeout (so a click that opens a JS alert/confirm/prompt dialog never hangs
// the click tool) to surface post-click state changes (e.g. a button that
// disabled itself in its handler), while preserving the pre-dispatch warning
// so a button that hides or removes itself on click does not trigger a false
// positive "element is not visible" warning.
func finalizeClickState(ctx context.Context, selector string, pre ClickOutput) ClickOutput {
	warning := clickWarning(pre)
	probeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	post, found, err := probeClickTarget(probeCtx, selector)
	if err != nil || !found {
		pre.Warning = warning
		return pre
	}
	post.Warning = warning
	return post
}

// scrollAndCenter scrolls node into the viewport (when needed) and returns its
// viewport-relative center coordinates via DOM.getContentQuads.
func scrollAndCenter(ctx context.Context, tgt *chromedp.Target, node *chromedp.Node) (x, y float64, err error) {
	// Best-effort scroll into view so elements below/above the fold receive
	// in-viewport coordinates for Input.dispatchMouseEvent. Ignore scroll errors
	// (e.g. non-laid-out nodes) and let GetContentQuads report the authoritative error.
	_, _ = cdp.Call(ctx, tgt, dom.ScrollIntoViewIfNeeded, dom.ScrollIntoViewIfNeededParams{
		BackendNodeID: node.BackendNodeID,
	})
	res, err := cdp.Call(ctx, tgt, dom.GetContentQuads, dom.GetContentQuadsParams{
		BackendNodeID: node.BackendNodeID,
	})
	if err != nil {
		return 0, 0, fmt.Errorf("getContentQuads: %w", err)
	}
	if len(res.Quads) == 0 {
		return 0, 0, fmt.Errorf("element has no content quads (not laid out)")
	}
	// Each quad is 8 floats: [x1,y1, x2,y2, x3,y3, x4,y4].
	// Compute the center from the first quad.
	q := res.Quads[0]
	if len(q) < 8 {
		return 0, 0, fmt.Errorf("unexpected quad length: %d", len(q))
	}
	cx := (q[0] + q[2] + q[4] + q[6]) / 4
	cy := (q[1] + q[3] + q[5] + q[7]) / 4
	return cx, cy, nil
}

// namedKeys maps key names used in the press_key MCP tool to the chromedp
// kb package rune constants. These runes are then passed to kb.Encode which
// generates the full CDP DispatchKeyEvent sequence with correct code,
// virtual key codes, and text fields.
var namedKeys map[string]rune

func init() {
	// Build the named key map from chromedp's kb constants. The constants
	// are UTF-8 encoded strings that may be multi-byte.
	entries := map[string]string{
		"Enter": kb.Enter, "Tab": kb.Tab, "Backspace": kb.Backspace,
		"Delete": kb.Delete, "Escape": kb.Escape, "Space": " ",
		"Insert": kb.Insert, "Control": kb.Control, "Shift": kb.Shift,
		"Alt": kb.Alt, "Meta": kb.Meta,
		"ArrowDown": kb.ArrowDown, "ArrowLeft": kb.ArrowLeft,
		"ArrowRight": kb.ArrowRight, "ArrowUp": kb.ArrowUp,
		"Home": kb.Home, "End": kb.End,
		"PageDown": kb.PageDown, "PageUp": kb.PageUp,
		"F1": kb.F1, "F2": kb.F2, "F3": kb.F3, "F4": kb.F4,
		"F5": kb.F5, "F6": kb.F6, "F7": kb.F7, "F8": kb.F8,
		"F9": kb.F9, "F10": kb.F10, "F11": kb.F11, "F12": kb.F12,
	}
	namedKeys = make(map[string]rune, len(entries))
	for name, s := range entries {
		r, _ := utf8.DecodeRuneInString(s)
		namedKeys[name] = r
	}
}

// ClickInput is the input for click.
type ClickInput struct {
	SelectorInput
	Selector        string `json:"selector" jsonschema:"CSS selector of the element to click"`
	Button          string `json:"button,omitempty" jsonschema:"Mouse button: left (default) right middle"`
	ClickCount      int    `json:"click_count,omitempty" jsonschema:"Number of clicks (default 1 use 2 for double-click)"`
	ObserveWindowMs int    `json:"observe_window_ms,omitempty" jsonschema:"If > 0, observe browser activity (network requests, DOM mutations, URL changes, console, JS errors) for this many ms after dispatch and return counts in 'observed'. Use to detect whether the click had any observable effect — has_any_effect=false means the page didn't react. Typical values: 200 to catch sync handlers + microtasks, 500–1000 to catch most network responses. Default 0 (no observation). Max 30000."`
}

// ClickOutput reports the post-resolution state of the click target so the
// LLM can detect cases where the click was dispatched but had no observable
// effect (display:none, pointer-events:none, off-screen). When `disabled`
// or `aria-disabled="true"` is set, click() returns an error instead of
// dispatching — see the click handler.
type ClickOutput struct {
	Disabled      bool   `json:"disabled"`
	AriaDisabled  bool   `json:"aria_disabled"`
	PointerEvents string `json:"pointer_events"`
	Visible       bool   `json:"visible"`
	// Warning is non-empty when the click likely had no effect (e.g.
	// pointer-events:none or visible=false). disabled/aria-disabled
	// produce errors rather than warnings, so they never appear here.
	Warning string `json:"warning,omitempty"`
	// Observed is populated when the caller passed observe_window_ms > 0.
	// Reports activity counts in the window starting at click dispatch.
	Observed *ObservedActivity `json:"observed,omitempty"`
}

// TypeInput is the input for type.
type TypeInput struct {
	SelectorInput
	Selector        string `json:"selector" jsonschema:"CSS selector of the input element"`
	Text            string `json:"text" jsonschema:"Text to type"`
	Mode            string `json:"mode" jsonschema:"How to combine the typed text with the field's existing value: 'replace' clears the field first, 'append' types after the existing value. Required."`
	Delay           int    `json:"delay,omitempty" jsonschema:"Delay between keystrokes in milliseconds (default 0)"`
	ObserveWindowMs int    `json:"observe_window_ms,omitempty" jsonschema:"If > 0, observe browser activity for this many ms after typing and return counts in 'observed'. Useful when typing triggers autocomplete / typeahead network requests. Default 0 (no observation). Max 30000."`
}

// ActionOutput carries the optional inline observation result from an
// action tool that doesn't otherwise return a typed value (type,
// press_key, submit_form, select_option, hover, focus, upload_files).
// Only Observed is populated when observe_window_ms > 0; otherwise the
// output is an empty object.
type ActionOutput struct {
	Observed *ObservedActivity `json:"observed,omitempty"`
}

// SelectOptionInput is the input for select_option.
type SelectOptionInput struct {
	SelectorInput
	Selector        string  `json:"selector" jsonschema:"CSS selector of the select element"`
	Value           *string `json:"value,omitempty" jsonschema:"Option value to select"`
	Label           *string `json:"label,omitempty" jsonschema:"Option visible text to select"`
	Index           *int    `json:"index,omitempty" jsonschema:"Option index to select"`
	ObserveWindowMs int     `json:"observe_window_ms,omitempty" jsonschema:"If > 0, observe browser activity for this many ms after the selection change fires and return counts in 'observed'. Useful when select onchange triggers cascading dropdown loads or form-level network requests. Default 0 (no observation). Max 30000."`
}

// SubmitFormInput is the input for submit_form.
type SubmitFormInput struct {
	SelectorInput
	Selector        string `json:"selector" jsonschema:"CSS selector of the form or an element within the form"`
	ObserveWindowMs int    `json:"observe_window_ms,omitempty" jsonschema:"If > 0, observe browser activity for this many ms after submit and return counts in 'observed'. Strongly recommended on submit_form: a form whose submit handler short-circuits (HTML5 validation, JS-side validation, framework guards) silently does nothing — non-zero observed counts confirm the submit landed. Default 0. Max 30000."`
}

// ScrollInput is the input for scroll.
type ScrollInput struct {
	SelectorInput
	Selector        string `json:"selector,omitempty" jsonschema:"CSS selector to scroll into view. If omitted scrolls the page."`
	X               int    `json:"x,omitempty" jsonschema:"Horizontal scroll offset in pixels"`
	Y               int    `json:"y,omitempty" jsonschema:"Vertical scroll offset in pixels"`
	ObserveWindowMs int    `json:"observe_window_ms,omitempty" jsonschema:"If > 0, observe browser activity for this many ms after scrolling and return counts in 'observed'. Useful for lazy-load and infinite-scroll detection. Default 0 (no observation). Max 30000."`
}

// ScrollOutput is the output for scroll, reporting the resulting scroll position.
type ScrollOutput struct {
	ScrollX  float64           `json:"scroll_x"`
	ScrollY  float64           `json:"scroll_y"`
	Observed *ObservedActivity `json:"observed,omitempty"`
}

// HoverInput is the input for hover.
type HoverInput struct {
	SelectorInput
	Selector        string `json:"selector" jsonschema:"CSS selector of the element to hover over"`
	ObserveWindowMs int    `json:"observe_window_ms,omitempty" jsonschema:"If > 0, observe browser activity for this many ms after the hover dispatch and return counts in 'observed'. Useful for tooltip/menu reveal detection. Default 0 (no observation). Max 30000."`
}

// FocusInput is the input for focus.
type FocusInput struct {
	SelectorInput
	Selector        string `json:"selector" jsonschema:"CSS selector of the element to focus"`
	ObserveWindowMs int    `json:"observe_window_ms,omitempty" jsonschema:"If > 0, observe browser activity for this many ms after focusing and return counts in 'observed'. Useful when focus triggers validation reveal or autofill. Default 0. Max 30000."`
}

// PressKeyInput is the input for press_key.
type PressKeyInput struct {
	TabInput
	Key             string   `json:"key" jsonschema:"Key to press (e.g. Enter Tab Escape ArrowDown)"`
	Modifiers       []string `json:"modifiers,omitempty" jsonschema:"Modifier keys: ctrl shift alt meta"`
	ObserveWindowMs int      `json:"observe_window_ms,omitempty" jsonschema:"If > 0, observe browser activity for this many ms after the key event and return counts in 'observed'. Useful for Enter (submit detection), Escape (modal dismiss), arrow navigation. Default 0. Max 30000."`
}

// UploadFilesInput is the input for upload_files.
type UploadFilesInput struct {
	SelectorInput
	Selector        string   `json:"selector" jsonschema:"CSS selector of the file input element"`
	Paths           []string `json:"paths" jsonschema:"Absolute file paths to set"`
	ObserveWindowMs int      `json:"observe_window_ms,omitempty" jsonschema:"If > 0, observe browser activity for this many ms after the input.files change fires and return counts in 'observed'. Useful when an onchange handler kicks off the actual upload XHR. Default 0. Max 30000."`
}

// HandleDialogInput is the input for handle_dialog.
type HandleDialogInput struct {
	TabInput
	Accept bool   `json:"accept" jsonschema:"Accept or dismiss the dialog"`
	Text   string `json:"text,omitempty" jsonschema:"Text to enter in a prompt dialog"`
}

func registerInteractionTools(s *mcp.Server, mgr *browser.Manager) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "click",
		Description: "Click an element by CSS selector. Returns the post-resolution state of the target (disabled, aria_disabled, pointer_events, visible) plus a warning string when the click likely had no observable effect (e.g. pointer-events:none, hidden). Errors out before dispatching when the target has the 'disabled' attribute or aria-disabled='true', since clicking a disabled element is almost always a bug in the caller's plan. Pass observe_window_ms > 0 to also observe browser activity (network/DOM/URL/console/JS errors) for that many ms after dispatch — use this to detect silent no-ops where the click landed but the handler short-circuited.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, inp ClickInput) (*mcp.CallToolResult, ClickOutput, error) {
		if err := validateObserveWindow(inp.ObserveWindowMs); err != nil {
			return nil, ClickOutput{}, err
		}
		switch inp.Button {
		case "", "left", "right", "middle":
		default:
			return nil, ClickOutput{}, fmt.Errorf("invalid button %q: must be \"left\", \"right\", or \"middle\"", inp.Button)
		}
		if inp.ClickCount < 0 {
			return nil, ClickOutput{}, fmt.Errorf("click_count must be >= 0, got %d", inp.ClickCount)
		}
		t, err := mgr.ResolveTab("", inp.Tab)
		if err != nil {
			return nil, ClickOutput{}, err
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		sctx, cancel := selectorContext(tctx, inp.Timeout)
		defer cancel()

		// Wait for the selector to appear and probe the target's state
		// before dispatching. The probe covers all click code paths so
		// the disabled-error and warning-on-no-effect contracts are
		// uniform across left/right/middle/double clicks.
		if err := chromedp.Do(sctx, chromedp.WaitReady(chromedp.CSS(inp.Selector))); err != nil {
			return nil, ClickOutput{}, selectorError(tctx, inp.Selector, err)
		}
		state, _, err := probeClickTarget(tctx, inp.Selector)
		if err != nil {
			return nil, ClickOutput{}, err
		}
		if state.Disabled {
			return nil, state, fmt.Errorf("element %q is disabled; click was not dispatched", inp.Selector)
		}
		if state.AriaDisabled {
			return nil, state, fmt.Errorf("element %q has aria-disabled=\"true\"; click was not dispatched", inp.Selector)
		}

		// Wait for VISIBILITY before opening the window. The probe above
		// only waited for the node to exist, but every dispatch path below
		// waits for it to be visible. Leaving that wait inside the window
		// would attribute everything a page does while the element is still
		// invisible to a click that has not been dispatched yet.
		//
		// This runs after the disabled/aria-disabled checks so those still
		// produce their crisp errors rather than a visibility timeout.
		if err := chromedp.Do(sctx, chromedp.WaitVisible(chromedp.CSS(inp.Selector))); err != nil {
			return nil, state, selectorError(tctx, inp.Selector, err)
		}
		// Refresh pre-dispatch state if the element was not yet visible at the
		// earlier probe (e.g. layout settled during WaitVisible) so pre-dispatch
		// warnings and disabled checks reflect settled state.
		if !state.Visible {
			if settled, found, perr := probeClickTarget(tctx, inp.Selector); perr == nil && found {
				state = settled
				if state.Disabled {
					return nil, state, fmt.Errorf("element %q is disabled; click was not dispatched", inp.Selector)
				}
				if state.AriaDisabled {
					return nil, state, fmt.Errorf("element %q has aria-disabled=\"true\"; click was not dispatched", inp.Selector)
				}
			}
		}

		isLeft := inp.Button == "" || inp.Button == "left"
		// Handle non-left buttons and click_count > 2 via CDP DispatchMouseEvent
		// BEFORE the left double-click fast path so right/middle double-clicks
		// dispatch the requested button rather than a left double-click.
		if !isLeft || inp.ClickCount > 2 {
			// Resolve the target node under a fresh selector timeout BEFORE
			// opening the observation window so a slow WaitVisible + re-probe
			// cannot starve node lookup and lookup is not blamed on the click.
			nodeCtx, ncancel := selectorContext(tctx, inp.Timeout)
			nodes, err := chromedp.Run(nodeCtx, chromedp.Nodes(chromedp.CSS(inp.Selector), chromedp.NodeVisible))
			ncancel()
			if err != nil {
				return nil, state, selectorError(tctx, inp.Selector, err)
			}
			if len(nodes) == 0 {
				return nil, state, fmt.Errorf("selector %q matched no elements", inp.Selector)
			}

			var button input.MouseButton
			switch inp.Button {
			case "right":
				button = input.MouseButtonRight
			case "middle":
				button = input.MouseButtonMiddle
			default:
				button = input.MouseButtonLeft
			}
			clickCount := inp.ClickCount
			if clickCount == 0 {
				clickCount = 1
			}

			obs := openObservation(tctx, t, inp.ObserveWindowMs)
			if err := chromedp.Do(tctx, chromedp.Func(func(ctx context.Context, tgt *chromedp.Target) error {
				cx, cy, err := scrollAndCenter(ctx, tgt, nodes[0])
				if err != nil {
					return err
				}
				for i := 0; i < clickCount; i++ {
					cc := int64(i + 1)
					if _, err := cdp.Call(ctx, tgt, input.DispatchMouseEvent, input.DispatchMouseEventParams{
						Type:       chromedp.MousePressed,
						X:          cx,
						Y:          cy,
						Button:     button,
						ClickCount: cc,
					}); err != nil {
						return err
					}
					if _, err := cdp.Call(ctx, tgt, input.DispatchMouseEvent, input.DispatchMouseEventParams{
						Type:       chromedp.MouseReleased,
						X:          cx,
						Y:          cy,
						Button:     button,
						ClickCount: cc,
					}); err != nil {
						return err
					}
				}
				return nil
			})); err != nil {
				return nil, state, err
			}
			out := finalizeClickState(tctx, inp.Selector, state)
			out.Observed = obs.close(tctx)
			return nil, out, nil
		}

		// Open the window BEFORE dispatch so handler effects — sync
		// handlers, microtasks fired during dispatch — are caught. Give
		// dispatch a fresh selector timeout so a slow WaitVisible + probes +
		// openObservation cannot starve it while still bounding missing nodes.
		obs := openObservation(tctx, t, inp.ObserveWindowMs)
		dispatchCtx, dcancel := selectorContext(tctx, inp.Timeout)
		defer dcancel()

		if inp.ClickCount == 2 {
			if err := chromedp.Do(dispatchCtx, chromedp.DoubleClick(chromedp.CSS(inp.Selector))); err != nil {
				return nil, state, selectorError(tctx, inp.Selector, err)
			}
			out := finalizeClickState(tctx, inp.Selector, state)
			out.Observed = obs.close(tctx)
			return nil, out, nil
		}

		if err := chromedp.Do(dispatchCtx, chromedp.Click(chromedp.CSS(inp.Selector))); err != nil {
			return nil, state, selectorError(tctx, inp.Selector, err)
		}
		out := finalizeClickState(tctx, inp.Selector, state)
		out.Observed = obs.close(tctx)
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "type",
		Description: "Type text into an element matching a CSS selector. 'mode' must be 'replace' (clear the field first — the correct choice for almost every form interaction, since real pages pre-populate fields with placeholders, geocoded defaults, or previous values) or 'append' (type after the existing value — rare; e.g. for contenteditable mid-composition).",
		InputSchema: modeSchemaFor[TypeInput](TypeModeReplace, TypeModeAppend),
	}, func(ctx context.Context, req *mcp.CallToolRequest, inp TypeInput) (*mcp.CallToolResult, ActionOutput, error) {
		if err := validateMode(inp.Mode, TypeModeReplace, TypeModeAppend); err != nil {
			return nil, ActionOutput{}, err
		}
		if err := validateObserveWindow(inp.ObserveWindowMs); err != nil {
			return nil, ActionOutput{}, err
		}
		if inp.Delay < 0 {
			return nil, ActionOutput{}, fmt.Errorf("delay must be >= 0, got %d", inp.Delay)
		}
		t, err := mgr.ResolveTab("", inp.Tab)
		if err != nil {
			return nil, ActionOutput{}, err
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()

		var clearAction chromedp.Action[chromedp.Void]
		if inp.Mode == TypeModeReplace {
			// Clear inputs/textareas via the native prototype value setter so
			// framework controlled inputs (e.g. React) detect the change when
			// 'input' fires, and clear contenteditable elements via textContent.
			clearJS := fmt.Sprintf(`(function() {
				var el = document.querySelector(%s);
				if (!el) return;
				if (el.isContentEditable) {
					el.textContent = '';
					el.dispatchEvent(new Event('input', {bubbles: true}));
					return;
				}
				var proto = el instanceof HTMLTextAreaElement
					? HTMLTextAreaElement.prototype
					: (el instanceof HTMLInputElement ? HTMLInputElement.prototype : null);
				var desc = proto && Object.getOwnPropertyDescriptor(proto, 'value');
				if (desc && desc.set) {
					desc.set.call(el, '');
				} else {
					el.value = '';
				}
				el.dispatchEvent(new Event('input', {bubbles: true}));
			})()`, jsQuote(inp.Selector))
			clearAction = chromedp.Evaluate[chromedp.Void](clearJS)
		}

		sctx, cancel := selectorContext(tctx, inp.Timeout)
		defer cancel()
		// Wait for the field BEFORE opening the window, so the wait for a
		// still-loading page to produce it is not counted as the type's
		// doing. The pre-wait must match the wait the dispatch performs
		// internally: SendKeys waits NodeVisible; Focus waits NodeReady.
		var preWait chromedp.Action[chromedp.Void]
		if inp.Delay > 0 {
			preWait = chromedp.WaitReady(chromedp.CSS(inp.Selector))
		} else {
			preWait = chromedp.WaitVisible(chromedp.CSS(inp.Selector))
		}
		if err := chromedp.Do(sctx, preWait); err != nil {
			return nil, ActionOutput{}, selectorError(tctx, inp.Selector, err)
		}
		obs := openObservation(tctx, t, inp.ObserveWindowMs)

		// Bound selector-bearing actions (clear + Focus / SendKeys) with a
		// fresh selector timeout so a page handler hiding or removing the
		// target on clear cannot hang indefinitely on tctx. For delay > 0, run
		// only the KeyEvent + Sleep loop on tctx so long strings are never
		// aborted by the selector timeout.
		dispatchCtx, dcancel := selectorContext(tctx, inp.Timeout)
		defer dcancel()
		if clearAction != nil {
			if err := chromedp.Do(dispatchCtx, clearAction); err != nil {
				return nil, ActionOutput{}, err
			}
		}
		if inp.Delay > 0 {
			if err := chromedp.Do(dispatchCtx, chromedp.Focus(chromedp.CSS(inp.Selector))); err != nil {
				return nil, ActionOutput{}, selectorError(tctx, inp.Selector, err)
			}
			var keySteps []chromedp.Action[chromedp.Void]
			runes := []rune(inp.Text)
			for i, ch := range runes {
				keySteps = append(keySteps, chromedp.KeyEvent(string(ch)))
				if i < len(runes)-1 {
					keySteps = append(keySteps, chromedp.Sleep(time.Duration(inp.Delay)*time.Millisecond))
				}
			}
			if len(keySteps) > 0 {
				if err := chromedp.Do(tctx, keySteps...); err != nil {
					return nil, ActionOutput{}, err
				}
			}
		} else {
			if err := chromedp.Do(dispatchCtx, chromedp.SendKeys(chromedp.CSS(inp.Selector), inp.Text)); err != nil {
				return nil, ActionOutput{}, selectorError(tctx, inp.Selector, err)
			}
		}
		return nil, ActionOutput{Observed: obs.close(tctx)}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "select_option",
		Description: "Select an option from a <select> element. Exactly one of value, label, or index must be provided.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, inp SelectOptionInput) (*mcp.CallToolResult, ActionOutput, error) {
		if err := validateObserveWindow(inp.ObserveWindowMs); err != nil {
			return nil, ActionOutput{}, err
		}
		// Validate selection criteria BEFORE waiting on the selector so
		// invalid calls fail immediately.
		criteria := 0
		if inp.Value != nil {
			criteria++
		}
		if inp.Label != nil {
			criteria++
		}
		if inp.Index != nil {
			criteria++
		}
		if criteria == 0 {
			return nil, ActionOutput{}, fmt.Errorf("exactly one of value, label, or index must be provided")
		}
		if criteria > 1 {
			return nil, ActionOutput{}, fmt.Errorf("exactly one of value, label, or index must be provided, not multiple")
		}
		if inp.Index != nil && *inp.Index < 0 {
			return nil, ActionOutput{}, fmt.Errorf("index out of range: must be >= 0, got %d", *inp.Index)
		}
		t, err := mgr.ResolveTab("", inp.Tab)
		if err != nil {
			return nil, ActionOutput{}, err
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()

		// Wait for the selector to appear in the DOM before running JS.
		sctx, cancel := selectorContext(tctx, inp.Timeout)
		defer cancel()
		if err := chromedp.Do(sctx, chromedp.WaitReady(chromedp.CSS(inp.Selector))); err != nil {
			return nil, ActionOutput{}, selectorError(tctx, inp.Selector, err)
		}

		// Build a single JS snippet to select by value, label, or index.
		// Verify the matched element is an HTMLSelectElement (handles both HTML
		// and XHTML documents) and use option.selected = true so <select multiple>
		// elements are handled correctly.
		var selectBody string
		switch {
		case inp.Value != nil:
			qVal := jsQuote(*inp.Value)
			selectBody = fmt.Sprintf(`
				var found = false;
				for (var i = 0; i < sel.options.length; i++) {
					if (sel.options[i].value === %s) { sel.options[i].selected = true; found = true; break; }
				}
				if (!found) throw new Error('no option with value ' + %s);`, qVal, qVal)
		case inp.Label != nil:
			qLabel := jsQuote(*inp.Label)
			selectBody = fmt.Sprintf(`
				var found = false;
				for (var i = 0; i < sel.options.length; i++) {
					if (sel.options[i].text === %s) { sel.options[i].selected = true; found = true; break; }
				}
				if (!found) throw new Error('no option with label ' + %s);`, qLabel, qLabel)
		case inp.Index != nil:
			selectBody = fmt.Sprintf(`
				if (%d < 0 || %d >= sel.options.length) throw new Error('index out of range');
				sel.options[%d].selected = true;`, *inp.Index, *inp.Index, *inp.Index)
		}
		js := fmt.Sprintf(`(function() {
			var sel = document.querySelector(%s);
			if (!sel) throw new Error('element not found');
			if (!(sel instanceof HTMLSelectElement)) throw new Error('element matched by selector is not a <select>');%s
			sel.dispatchEvent(new Event('change', {bubbles: true}));
		})()`, jsQuote(inp.Selector), selectBody)

		obs := openObservation(tctx, t, inp.ObserveWindowMs)
		if err := chromedp.Do(tctx, chromedp.Evaluate[chromedp.Void](js)); err != nil {
			return nil, ActionOutput{}, err
		}
		return nil, ActionOutput{Observed: obs.close(tctx)}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "submit_form",
		Description: "Submit a form by CSS selector. Fires the submit event so JS handlers can intercept it. Pass observe_window_ms > 0 to detect submits that fire the event but short-circuit (HTML5 validation, JS validation, framework guards) — non-zero observed counts confirm the submit landed.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, inp SubmitFormInput) (*mcp.CallToolResult, ActionOutput, error) {
		if err := validateObserveWindow(inp.ObserveWindowMs); err != nil {
			return nil, ActionOutput{}, err
		}
		t, err := mgr.ResolveTab("", inp.Tab)
		if err != nil {
			return nil, ActionOutput{}, err
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()

		// Wait for the selector to appear in the DOM before running JS.
		sctx, cancel := selectorContext(tctx, inp.Timeout)
		defer cancel()
		if err := chromedp.Do(sctx, chromedp.WaitReady(chromedp.CSS(inp.Selector))); err != nil {
			return nil, ActionOutput{}, selectorError(tctx, inp.Selector, err)
		}

		// Use requestSubmit() which fires the submit event (unlike the
		// native .submit() method used by chromedp.Submit). This allows
		// JS submit handlers and e.preventDefault() to work correctly.
		js := fmt.Sprintf(`(function() {
			var el = document.querySelector(%s);
			if (!el) throw new Error('element not found');
			var form = el.nodeName === 'FORM' ? el : el.form || el.closest('form');
			if (!form) throw new Error('no form found for selector');
			form.requestSubmit();
		})()`, jsQuote(inp.Selector))
		obs := openObservation(tctx, t, inp.ObserveWindowMs)
		if err := chromedp.Do(tctx, chromedp.Evaluate[chromedp.Void](js)); err != nil {
			return nil, ActionOutput{}, err
		}
		return nil, ActionOutput{Observed: obs.close(tctx)}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "scroll",
		Description: "Scroll a page or scroll an element into view.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, inp ScrollInput) (*mcp.CallToolResult, ScrollOutput, error) {
		if err := validateObserveWindow(inp.ObserveWindowMs); err != nil {
			return nil, ScrollOutput{}, err
		}
		t, err := mgr.ResolveTab("", inp.Tab)
		if err != nil {
			return nil, ScrollOutput{}, err
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		var obs *observation
		if inp.Selector != "" {
			sctx, cancel := selectorContext(tctx, inp.Timeout)
			defer cancel()
			// Wait for the target BEFORE opening the window, so the wait is
			// not blamed on the scroll. WaitReady, not WaitVisible: it must
			// match chromedp.ScrollIntoView's own NodeReady wait —
			// requiring visibility here would reject elements scroll
			// previously accepted.
			if err := chromedp.Do(sctx, chromedp.WaitReady(chromedp.CSS(inp.Selector))); err != nil {
				return nil, ScrollOutput{}, selectorError(tctx, inp.Selector, err)
			}
			obs = openObservation(tctx, t, inp.ObserveWindowMs)
			dispatchCtx, dcancel := selectorContext(tctx, inp.Timeout)
			defer dcancel()
			err = chromedp.Do(dispatchCtx, chromedp.ScrollIntoView(chromedp.CSS(inp.Selector)))
			if err != nil {
				return nil, ScrollOutput{}, selectorError(tctx, inp.Selector, err)
			}
		} else {
			obs = openObservation(tctx, t, inp.ObserveWindowMs)
			js := fmt.Sprintf("window.scrollBy(%d, %d)", inp.X, inp.Y)
			err = chromedp.Do(tctx, chromedp.Evaluate[chromedp.Void](js))
		}
		if err != nil {
			return nil, ScrollOutput{}, err
		}

		// Report the resulting scroll position via page.GetLayoutMetrics.
		metrics, err := chromedp.Call(tctx, page.GetLayoutMetrics, cdp.Empty{})
		if err != nil {
			return nil, ScrollOutput{}, err
		}
		var out ScrollOutput
		if metrics.CSSVisualViewport != nil {
			out.ScrollX = metrics.CSSVisualViewport.PageX
			out.ScrollY = metrics.CSSVisualViewport.PageY
		}
		out.Observed = obs.close(tctx)
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "hover",
		Description: "Hover over an element by CSS selector.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, inp HoverInput) (*mcp.CallToolResult, ActionOutput, error) {
		if err := validateObserveWindow(inp.ObserveWindowMs); err != nil {
			return nil, ActionOutput{}, err
		}
		t, err := mgr.ResolveTab("", inp.Tab)
		if err != nil {
			return nil, ActionOutput{}, err
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		sctx, cancel := selectorContext(tctx, inp.Timeout)
		defer cancel()

		// Wait for the element, then get its center via CDP
		// DOM.getContentQuads. Dispatch a CDP mouseMoved event to
		// trigger the browser's native :hover CSS state.
		nodes, err := chromedp.Run(sctx, chromedp.Nodes(chromedp.CSS(inp.Selector)))
		if err != nil {
			return nil, ActionOutput{}, selectorError(tctx, inp.Selector, err)
		}
		if len(nodes) == 0 {
			return nil, ActionOutput{}, fmt.Errorf("selector %q matched no elements", inp.Selector)
		}

		obs := openObservation(tctx, t, inp.ObserveWindowMs)
		if err := chromedp.Do(tctx, chromedp.Func(func(ctx context.Context, tgt *chromedp.Target) error {
			cx, cy, err := scrollAndCenter(ctx, tgt, nodes[0])
			if err != nil {
				return err
			}
			_, err = cdp.Call(ctx, tgt, input.DispatchMouseEvent, input.DispatchMouseEventParams{
				Type: chromedp.MouseMoved,
				X:    cx,
				Y:    cy,
			})
			return err
		})); err != nil {
			return nil, ActionOutput{}, err
		}
		return nil, ActionOutput{Observed: obs.close(tctx)}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "focus",
		Description: "Focus an element by CSS selector.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, inp FocusInput) (*mcp.CallToolResult, ActionOutput, error) {
		if err := validateObserveWindow(inp.ObserveWindowMs); err != nil {
			return nil, ActionOutput{}, err
		}
		t, err := mgr.ResolveTab("", inp.Tab)
		if err != nil {
			return nil, ActionOutput{}, err
		}
		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		sctx, cancel := selectorContext(tctx, inp.Timeout)
		defer cancel()
		// Wait for the element BEFORE opening the window, so the wait is not
		// counted as the focus's doing. WaitReady, not WaitVisible: it must
		// match chromedp.Focus's own NodeReady wait — requiring visibility
		// here would reject elements focus previously accepted.
		if err := chromedp.Do(sctx, chromedp.WaitReady(chromedp.CSS(inp.Selector))); err != nil {
			return nil, ActionOutput{}, selectorError(tctx, inp.Selector, err)
		}
		obs := openObservation(tctx, t, inp.ObserveWindowMs)
		dispatchCtx, dcancel := selectorContext(tctx, inp.Timeout)
		defer dcancel()
		if err := chromedp.Do(dispatchCtx, chromedp.Focus(chromedp.CSS(inp.Selector))); err != nil {
			return nil, ActionOutput{}, selectorError(tctx, inp.Selector, err)
		}
		return nil, ActionOutput{Observed: obs.close(tctx)}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "press_key",
		Description: "Press a keyboard key, optionally with modifiers. Pass observe_window_ms > 0 to detect Enter (form submit), Escape (modal dismiss), or arrow-key handler effects.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, inp PressKeyInput) (*mcp.CallToolResult, ActionOutput, error) {
		if err := validateObserveWindow(inp.ObserveWindowMs); err != nil {
			return nil, ActionOutput{}, err
		}
		t, err := mgr.ResolveTab("", inp.Tab)
		if err != nil {
			return nil, ActionOutput{}, err
		}

		var modifiers chromedp.Modifier
		for _, m := range inp.Modifiers {
			switch m {
			case "ctrl":
				modifiers |= chromedp.ModifierCtrl
			case "shift":
				modifiers |= chromedp.ModifierShift
			case "alt":
				modifiers |= chromedp.ModifierAlt
			case "meta":
				modifiers |= chromedp.ModifierMeta
			default:
				return nil, ActionOutput{}, fmt.Errorf("unknown modifier %q: must be ctrl, shift, alt, or meta", m)
			}
		}

		// Resolve the key to a rune for chromedp.KeyEvent. Named keys (Enter,
		// Tab, ArrowDown, etc.) map to special chromedp kb runes. Single
		// characters map directly.
		var r rune
		if mapped, ok := namedKeys[inp.Key]; ok {
			r = mapped
		} else if len(inp.Key) == 1 {
			r = rune(inp.Key[0])
		} else {
			// Try decoding as a single UTF-8 rune.
			decoded, size := utf8.DecodeRuneInString(inp.Key)
			if decoded == utf8.RuneError || size != len(inp.Key) {
				return nil, ActionOutput{}, fmt.Errorf("unknown key: %q", inp.Key)
			}
			r = decoded
		}

		// chromedp.KeyEvent encodes the full CDP KeyDown/KeyChar/KeyUp sequence
		// and omits KeyChar whenever Ctrl, Alt, or Meta is held so shortcuts
		// (e.g. Ctrl+A, Meta+A) do not also type a literal character.
		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		var opts []chromedp.KeyOption
		if modifiers != 0 {
			opts = append(opts, chromedp.KeyModifiers(modifiers))
		}
		obs := openObservation(tctx, t, inp.ObserveWindowMs)
		if err := chromedp.Do(tctx, chromedp.KeyEvent(string(r), opts...)); err != nil {
			return nil, ActionOutput{}, err
		}
		return nil, ActionOutput{Observed: obs.close(tctx)}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "upload_files",
		Description: "Set files on a file input element. Pass observe_window_ms > 0 to catch the upload XHR that an onchange handler typically fires.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, inp UploadFilesInput) (*mcp.CallToolResult, ActionOutput, error) {
		if err := validateObserveWindow(inp.ObserveWindowMs); err != nil {
			return nil, ActionOutput{}, err
		}
		b, t, err := mgr.ResolveTabWithBrowser("", inp.Tab)
		if err != nil {
			return nil, ActionOutput{}, err
		}
		for _, p := range inp.Paths {
			if p == "" {
				return nil, ActionOutput{}, fmt.Errorf("upload path must not be empty")
			}
			// Only validate against the local filesystem when this server
			// launched Chrome locally; connected browsers may run on a remote
			// host or container with a different filesystem.
			if b.Mode == browser.ModeLaunch {
				if !filepath.IsAbs(p) {
					return nil, ActionOutput{}, fmt.Errorf("upload path %q must be an absolute path", p)
				}
				fi, err := os.Stat(p)
				if err != nil {
					return nil, ActionOutput{}, fmt.Errorf("upload path %q: %w", p, err)
				}
				if fi.IsDir() {
					return nil, ActionOutput{}, fmt.Errorf("upload path %q is a directory, not a file", p)
				}
			}
		}
		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		sctx, cancel := selectorContext(tctx, inp.Timeout)
		defer cancel()
		// Wait for the input BEFORE opening the window — SetUploadFiles
		// waits internally, and that wait must not be inside the window.
		if err := chromedp.Do(sctx, chromedp.WaitReady(chromedp.CSS(inp.Selector))); err != nil {
			return nil, ActionOutput{}, selectorError(tctx, inp.Selector, err)
		}
		obs := openObservation(tctx, t, inp.ObserveWindowMs)
		dispatchCtx, dcancel := selectorContext(tctx, inp.Timeout)
		defer dcancel()
		if err := chromedp.Do(dispatchCtx, chromedp.SetUploadFiles(chromedp.CSS(inp.Selector), inp.Paths)); err != nil {
			return nil, ActionOutput{}, selectorError(tctx, inp.Selector, err)
		}
		return nil, ActionOutput{Observed: obs.close(tctx)}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "handle_dialog",
		Description: "Handle a JavaScript dialog (alert, confirm, prompt, beforeunload).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, inp HandleDialogInput) (*mcp.CallToolResult, struct{}, error) {
		t, err := mgr.ResolveTab("", inp.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		if _, err := chromedp.Call(tctx, page.HandleJavaScriptDialog, page.HandleJavaScriptDialogParams{
			Accept:     inp.Accept,
			PromptText: inp.Text,
		}); err != nil {
			return nil, struct{}{}, err
		}
		return nil, struct{}{}, nil
	})
}
