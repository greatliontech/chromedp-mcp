package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/browser"
	"github.com/greatliontech/chromedp-mcp/internal/collector"
)

const probeClickStateJS = `function() {
	const win = (this.ownerDocument && this.ownerDocument.defaultView) || window;
	const cs = win.getComputedStyle(this);
	const rect = typeof this.getBoundingClientRect === 'function' ? this.getBoundingClientRect() : {width: 0, height: 0};
	return {
		disabled: typeof this.matches === 'function' && this.matches(':disabled'),
		aria_disabled: typeof this.getAttribute === 'function' && this.getAttribute('aria-disabled') === 'true',
		pointer_events: cs.pointerEvents,
		visible: cs.display !== 'none' &&
			cs.visibility !== 'hidden' &&
			rect.width > 0 && rect.height > 0,
	};
}`

func probeClickObject(ctx context.Context, tgt *chromedp.Target, oid runtime.RemoteObjectID) (ClickOutput, error) {
	type clickProbe struct {
		Disabled      bool   `json:"disabled"`
		AriaDisabled  bool   `json:"aria_disabled"`
		PointerEvents string `json:"pointer_events"`
		Visible       bool   `json:"visible"`
	}
	raw, err := callOnElement[clickProbe](ctx, tgt, oid, probeClickStateJS, false)
	if err != nil {
		return ClickOutput{}, err
	}
	return ClickOutput{
		Disabled:      raw.Disabled,
		AriaDisabled:  raw.AriaDisabled,
		PointerEvents: raw.PointerEvents,
		Visible:       raw.Visible,
	}, nil
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

// finalizeClickObject re-probes the target element after click dispatch under
// a short bounded timeout (short-circuiting when a JS dialog is open so clicks
// opening alerts never stall), preserving the pre-dispatch warning.
func finalizeClickObject(ctx context.Context, tgt *chromedp.Target, dialog *collector.Dialog, oid runtime.RemoteObjectID, pre ClickOutput) ClickOutput {
	warning := clickWarning(pre)
	if dialog != nil && dialog.Open() {
		pre.Warning = warning
		return pre
	}
	probeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	connected, err := callOnElement[bool](probeCtx, tgt, oid, `function() { return Boolean(this && this.isConnected); }`, false)
	if err != nil || !connected {
		pre.Warning = warning
		return pre
	}
	post, err := probeClickObject(probeCtx, tgt, oid)
	if err != nil {
		pre.Warning = warning
		return pre
	}
	post.Warning = warning
	return post
}

// namedKeys maps key names used in press_key to chromedp kb rune constants.
var namedKeys map[string]rune

func init() {
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
	Ref             string   `json:"ref,omitempty" jsonschema:"Element ref from snapshot (e.g. 'e4')."`
	Frame           string   `json:"frame,omitempty" jsonschema:"Optional same-origin frame ID from list_frames."`
	Selector        string   `json:"selector,omitempty" jsonschema:"CSS/shadow ('>>>') selector of the element to click."`
	X               *float64 `json:"x,omitempty" jsonschema:"Viewport X coordinate to click (requires y; mutually exclusive with ref/selector)."`
	Y               *float64 `json:"y,omitempty" jsonschema:"Viewport Y coordinate to click (requires x; mutually exclusive with ref/selector)."`
	Button          string   `json:"button,omitempty" jsonschema:"Mouse button: left (default), right, middle."`
	ClickCount      int      `json:"click_count,omitempty" jsonschema:"Number of clicks (default 1; use 2 for double-click)."`
	Modifiers       []string `json:"modifiers,omitempty" jsonschema:"Optional modifier keys: ctrl, shift, alt, meta."`
	ObserveWindowMs int      `json:"observe_window_ms,omitempty" jsonschema:"Observe activity for N ms after action (0=off, max 30000)."`
}

// ClickOutput reports target state and optional warning/observation after click.
type ClickOutput struct {
	Disabled      bool              `json:"disabled"`
	AriaDisabled  bool              `json:"aria_disabled"`
	PointerEvents string            `json:"pointer_events"`
	Visible       bool              `json:"visible"`
	Warning       string            `json:"warning,omitempty"`
	Observed      *ObservedActivity `json:"observed,omitempty"`
}

// TypeInput is the input for type.
type TypeInput struct {
	SelectorInput
	Ref             string `json:"ref,omitempty" jsonschema:"Element ref from snapshot."`
	Frame           string `json:"frame,omitempty" jsonschema:"Optional same-origin frame ID from list_frames."`
	Selector        string `json:"selector,omitempty" jsonschema:"CSS/shadow ('>>>') selector of the input element."`
	Text            string `json:"text" jsonschema:"Text to type."`
	Mode            string `json:"mode" jsonschema:"'replace' (clear field first) or 'append' (type at end of existing value). Required."`
	Delay           int    `json:"delay,omitempty" jsonschema:"Delay between keystrokes in ms (default 0)."`
	Submit          bool   `json:"submit,omitempty" jsonschema:"Press Enter after typing (default false)."`
	ObserveWindowMs int    `json:"observe_window_ms,omitempty" jsonschema:"Observe activity for N ms after action (0=off, max 30000)."`
}

// ActionOutput carries the optional inline observation result from action tools.
type ActionOutput struct {
	Observed *ObservedActivity `json:"observed,omitempty"`
}

// SelectOptionInput is the input for select_option.
type SelectOptionInput struct {
	SelectorInput
	Ref             string   `json:"ref,omitempty" jsonschema:"Element ref from snapshot."`
	Frame           string   `json:"frame,omitempty" jsonschema:"Optional same-origin frame ID from list_frames."`
	Selector        string   `json:"selector,omitempty" jsonschema:"CSS/shadow ('>>>') selector of the <select> element."`
	Values          []string `json:"values,omitempty" jsonschema:"Option values to select (on <select multiple>, replaces selection; [] clears all)."`
	Labels          []string `json:"labels,omitempty" jsonschema:"Option visible texts to select (on <select multiple>, replaces selection; [] clears all)."`
	Indexes         []int    `json:"indexes,omitempty" jsonschema:"Option 0-based indexes to select (on <select multiple>, replaces selection; [] clears all)."`
	ObserveWindowMs int      `json:"observe_window_ms,omitempty" jsonschema:"Observe activity for N ms after action (0=off, max 30000)."`
}

// ScrollInput is the input for scroll.
type ScrollInput struct {
	SelectorInput
	Ref             string `json:"ref,omitempty" jsonschema:"Element ref from snapshot to scroll into view."`
	Frame           string `json:"frame,omitempty" jsonschema:"Optional same-origin frame ID from list_frames."`
	Selector        string `json:"selector,omitempty" jsonschema:"CSS/shadow ('>>>') selector to scroll into view. If omitted scrolls window by (x, y)."`
	X               int    `json:"x,omitempty" jsonschema:"Horizontal scroll delta in pixels (when scrolling window)."`
	Y               int    `json:"y,omitempty" jsonschema:"Vertical scroll delta in pixels (when scrolling window)."`
	ObserveWindowMs int    `json:"observe_window_ms,omitempty" jsonschema:"Observe activity for N ms after action (0=off, max 30000)."`
}

// ScrollOutput reports the resulting scroll position.
type ScrollOutput struct {
	ScrollX  float64           `json:"scroll_x"`
	ScrollY  float64           `json:"scroll_y"`
	Observed *ObservedActivity `json:"observed,omitempty"`
}

// HoverInput is the input for hover.
type HoverInput struct {
	SelectorInput
	Ref             string   `json:"ref,omitempty" jsonschema:"Element ref from snapshot."`
	Frame           string   `json:"frame,omitempty" jsonschema:"Optional same-origin frame ID from list_frames."`
	Selector        string   `json:"selector,omitempty" jsonschema:"CSS/shadow ('>>>') selector of the element to hover over."`
	X               *float64 `json:"x,omitempty" jsonschema:"Viewport X coordinate to move pointer to (requires y)."`
	Y               *float64 `json:"y,omitempty" jsonschema:"Viewport Y coordinate to move pointer to (requires x)."`
	ObserveWindowMs int      `json:"observe_window_ms,omitempty" jsonschema:"Observe activity for N ms after action (0=off, max 30000)."`
}

// PressKeyInput is the input for press_key.
type PressKeyInput struct {
	SelectorInput
	Ref             string   `json:"ref,omitempty" jsonschema:"Optional element ref from snapshot to focus before pressing the key."`
	Frame           string   `json:"frame,omitempty" jsonschema:"Optional same-origin frame ID for selector."`
	Selector        string   `json:"selector,omitempty" jsonschema:"Optional CSS/shadow ('>>>') selector to focus before pressing the key."`
	Key             string   `json:"key" jsonschema:"Key to press (e.g. Enter, Tab, Escape, ArrowDown, a)."`
	Modifiers       []string `json:"modifiers,omitempty" jsonschema:"Optional modifier keys: ctrl, shift, alt, meta."`
	ObserveWindowMs int      `json:"observe_window_ms,omitempty" jsonschema:"Observe activity for N ms after action (0=off, max 30000)."`
}

// UploadFilesInput is the input for upload_files.
type UploadFilesInput struct {
	SelectorInput
	Ref             string   `json:"ref,omitempty" jsonschema:"Element ref from snapshot."`
	Frame           string   `json:"frame,omitempty" jsonschema:"Optional same-origin frame ID from list_frames."`
	Selector        string   `json:"selector,omitempty" jsonschema:"CSS/shadow ('>>>') selector of the file input element."`
	Paths           []string `json:"paths" jsonschema:"Non-empty file paths to set (validated as local files on launched browsers; passed directly on connected browsers)."`
	ObserveWindowMs int      `json:"observe_window_ms,omitempty" jsonschema:"Observe activity for N ms after action (0=off, max 30000)."`
}

var (
	clickButtonValues      = []string{"left", "right", "middle"}
	pressKeyModifierValues = []string{"ctrl", "shift", "alt", "meta"}
)

func registerInteractionTools(s *mcp.Server, mgr *browser.Manager) {
	registerClickTool(s, mgr)
	registerTypeTool(s, mgr)
	registerSelectOptionTool(s, mgr)
	registerScrollTool(s, mgr)
	registerHoverTool(s, mgr)
	registerPressKeyTool(s, mgr)
	registerUploadFilesTool(s, mgr)
}

func registerClickTool(s *mcp.Server, mgr *browser.Manager) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "click",
		Description: "Click an element (by snapshot ref or CSS/shadow selector) or viewport (x, y) coordinates. Errors before dispatch on disabled/aria-disabled elements; warns on pointer-events:none or visibility:hidden.",
		InputSchema: schemaWithEnums[ClickInput](map[string][]string{
			"button":    clickButtonValues,
			"modifiers": pressKeyModifierValues,
		}),
		Annotations: mutatingAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, inp ClickInput) (*mcp.CallToolResult, ClickOutput, error) {
		if err := validatePointerTarget(inp.Ref, inp.Selector, inp.Frame, inp.X, inp.Y, true); err != nil {
			return nil, ClickOutput{}, err
		}
		if err := validateSelectorTimeout(inp.Timeout); err != nil {
			return nil, ClickOutput{}, err
		}
		if err := validateObserveWindow(inp.ObserveWindowMs); err != nil {
			return nil, ClickOutput{}, err
		}
		if err := validateEnum("button", inp.Button, true, clickButtonValues); err != nil {
			return nil, ClickOutput{}, err
		}
		if inp.ClickCount < 0 || inp.ClickCount > 10 {
			return nil, ClickOutput{}, fmt.Errorf("click_count must be between 0 and 10, got %d", inp.ClickCount)
		}
		modifiers, err := parseModifiers(inp.Modifiers)
		if err != nil {
			return nil, ClickOutput{}, err
		}
		t, err := mgr.ResolveTab("", inp.Tab)
		if err != nil {
			return nil, ClickOutput{}, err
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

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()

		dispatchAt := func(ctx context.Context, tgt *chromedp.Target, x, y float64) error {
			return runDialogInterruptible(ctx, t.Dialog, func(dctx context.Context) error {
				for i := 0; i < clickCount; i++ {
					cc := int64(i + 1)
					if _, err := cdp.Call(dctx, tgt, input.DispatchMouseEvent, input.DispatchMouseEventParams{
						Type:       chromedp.MousePressed,
						X:          x,
						Y:          y,
						Button:     button,
						Modifiers:  int64(modifiers),
						ClickCount: cc,
					}); err != nil {
						return err
					}
					if _, err := cdp.Call(dctx, tgt, input.DispatchMouseEvent, input.DispatchMouseEventParams{
						Type:       chromedp.MouseReleased,
						X:          x,
						Y:          y,
						Button:     button,
						Modifiers:  int64(modifiers),
						ClickCount: cc,
					}); err != nil {
						return err
					}
					if t.Dialog != nil && t.Dialog.Open() {
						return nil
					}
				}
				return nil
			})
		}

		if inp.X != nil {
			obs := openObservation(tctx, t, inp.ObserveWindowMs)
			if err := chromedp.Do(tctx, chromedp.Func(func(ctx context.Context, tgt *chromedp.Target) error {
				return dispatchAt(ctx, tgt, *inp.X, *inp.Y)
			})); err != nil {
				return nil, ClickOutput{}, err
			}
			return nil, ClickOutput{Visible: true, Observed: obs.close(tctx)}, nil
		}

		var (
			out ClickOutput
			obs *observation
		)
		err = chromedp.Do(tctx, chromedp.Func(func(ctx context.Context, tgt *chromedp.Target) error {
			el, err := resolveElementObject(ctx, tgt, t, inp.Ref, inp.Selector, inp.Frame, inp.Timeout, true)
			if err != nil {
				return err
			}
			defer el.Release(ctx, tgt)

			state, err := probeClickObject(ctx, tgt, el.ObjectID)
			if err != nil {
				return fmt.Errorf("probe %s: %w", el.Label, err)
			}
			out = state
			if err := checkClickEnabled(el.Label, state); err != nil {
				return err
			}

			// Wait for a layout box BEFORE opening the observation window so
			// pre-click waiting is not attributed to the click.
			if err := waitForElementLayoutBox(ctx, tgt, el, inp.Timeout); err != nil {
				return err
			}
			if !state.Visible {
				if settled, perr := probeClickObject(ctx, tgt, el.ObjectID); perr == nil {
					state = settled
					out = state
					if err := checkClickEnabled(el.Label, state); err != nil {
						return err
					}
				}
			}

			obs = openObservation(ctx, t, inp.ObserveWindowMs)
			cx, cy, err := scrollAndCenterBackendNode(ctx, tgt, el.BackendNodeID)
			if err != nil {
				return err
			}
			if err := dispatchAt(ctx, tgt, cx, cy); err != nil {
				return err
			}
			out = finalizeClickObject(ctx, tgt, t.Dialog, el.ObjectID, state)
			return nil
		}))
		if err != nil {
			return nil, out, err
		}
		out.Observed = obs.close(tctx)
		return nil, out, nil
	})
}

func checkClickEnabled(label string, state ClickOutput) error {
	if state.Disabled {
		return fmt.Errorf("element %s is disabled; click was not dispatched", label)
	}
	if state.AriaDisabled {
		return fmt.Errorf("element %s has aria-disabled=\"true\"; click was not dispatched", label)
	}
	return nil
}

const clearInputObjectJS = `function() {
	const win = (this.ownerDocument && this.ownerDocument.defaultView) || window;
	if (this.isContentEditable) {
		this.textContent = '';
		this.dispatchEvent(new win.Event('input', {bubbles: true}));
		return Boolean(this.isConnected);
	}
	const proto = this instanceof win.HTMLTextAreaElement
		? win.HTMLTextAreaElement.prototype
		: (this instanceof win.HTMLInputElement ? win.HTMLInputElement.prototype : null);
	const desc = proto && Object.getOwnPropertyDescriptor(proto, 'value');
	if (desc && desc.set) {
		desc.set.call(this, '');
	} else {
		this.value = '';
	}
	this.dispatchEvent(new win.Event('input', {bubbles: true}));
	return Boolean(this.isConnected);
}`

const moveCaretToEndJS = `function() {
	if (this.isContentEditable) {
		const doc = this.ownerDocument || document;
		const r = doc.createRange();
		r.selectNodeContents(this);
		r.collapse(false);
		const root = this.getRootNode();
		const sel = (root && typeof root.getSelection === 'function' ? root.getSelection() : null) || doc.getSelection();
		if (sel) { sel.removeAllRanges(); sel.addRange(r); }
		return;
	}
	if (typeof this.value === 'string') {
		if (typeof this.setSelectionRange === 'function') {
			try {
				this.setSelectionRange(this.value.length, this.value.length);
				return;
			} catch (_) {
				// Input types like "email" and "number" throw InvalidStateError on
				// setSelectionRange; reassigning value moves Blink's caret to end
				// without firing an input event.
			}
		}
		const v = this.value;
		this.value = '';
		this.value = v;
	}
}`

func registerTypeTool(s *mcp.Server, mgr *browser.Manager) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "type",
		Description: "Type text into an input element (by snapshot ref or CSS/shadow selector). 'mode' must be 'replace' (clear first) or 'append' (type at end). Set submit=true to press Enter afterward.",
		InputSchema: modeSchemaFor[TypeInput](TypeModeReplace, TypeModeAppend),
		Annotations: mutatingAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, inp TypeInput) (*mcp.CallToolResult, ActionOutput, error) {
		if err := validateMode(inp.Mode, TypeModeReplace, TypeModeAppend); err != nil {
			return nil, ActionOutput{}, err
		}
		if err := validateElementTarget(inp.Ref, inp.Selector, inp.Frame, true); err != nil {
			return nil, ActionOutput{}, err
		}
		if err := validateSelectorTimeout(inp.Timeout); err != nil {
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

		var obs *observation
		err = chromedp.Do(tctx, chromedp.Func(func(ctx context.Context, tgt *chromedp.Target) error {
			el, err := resolveElementObject(ctx, tgt, t, inp.Ref, inp.Selector, inp.Frame, inp.Timeout, true)
			if err != nil {
				return err
			}
			defer el.Release(ctx, tgt)

			if err := waitForElementLayoutBox(ctx, tgt, el, inp.Timeout); err != nil {
				return err
			}

			obs = openObservation(ctx, t, inp.ObserveWindowMs)
			// Focus before clearing so disabled/non-focusable elements error
			// without mutating their value or typing into a previously focused element.
			if err := focusResolvedElement(ctx, tgt, el); err != nil {
				return err
			}
			if inp.Mode == TypeModeReplace {
				stillConnected, err := callOnElement[bool](ctx, tgt, el.ObjectID, clearInputObjectJS, false)
				if err != nil {
					return err
				}
				if !stillConnected {
					if inp.Ref != "" {
						return fmt.Errorf("%s was detached from the DOM after clearing", el.Label)
					}
					// The input listener may have remounted the element; re-resolve within timeout.
					el.Release(ctx, tgt)
					el, err = resolveElementObject(ctx, tgt, t, "", inp.Selector, inp.Frame, inp.Timeout, true)
					if err != nil {
						return err
					}
					defer el.Release(ctx, tgt)
					if err := waitForElementLayoutBox(ctx, tgt, el, inp.Timeout); err != nil {
						return err
					}
				}
				if err := focusResolvedElement(ctx, tgt, el); err != nil {
					return err
				}
			} else if inp.Mode == TypeModeAppend {
				_, _ = callOnElement[any](ctx, tgt, el.ObjectID, moveCaretToEndJS, false)
			}
			// Release handle before keystroke dispatch so a key/submit handler
			// opening a modal dialog cannot stall deferred ReleaseObject.
			el.Release(ctx, tgt)

			return runDialogInterruptible(ctx, t.Dialog, func(dctx context.Context) error {
				if inp.Delay > 0 {
					runes := []rune(inp.Text)
					for i, ch := range runes {
						if _, err := chromedp.KeyEvent(string(ch))(dctx, tgt); err != nil {
							return err
						}
						if i < len(runes)-1 {
							timer := time.NewTimer(time.Duration(inp.Delay) * time.Millisecond)
							select {
							case <-timer.C:
							case <-dctx.Done():
								timer.Stop()
								return dctx.Err()
							}
						}
					}
				} else if inp.Text != "" {
					if _, err := chromedp.KeyEvent(inp.Text)(dctx, tgt); err != nil {
						return err
					}
				}
				if inp.Submit {
					if _, err := chromedp.KeyEvent(kb.Enter)(dctx, tgt); err != nil {
						return err
					}
				}
				return nil
			})
		}))
		if err != nil {
			return nil, ActionOutput{}, err
		}
		return nil, ActionOutput{Observed: obs.close(tctx)}, nil
	})
}

func registerSelectOptionTool(s *mcp.Server, mgr *browser.Manager) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "select_option",
		Description: "Select option(s) on a <select> element (by snapshot ref or CSS/shadow selector). Provide exactly one of values, labels, or indexes; on <select multiple>, replaces selection (pass [] to deselect all).",
		Annotations: mutatingAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, inp SelectOptionInput) (*mcp.CallToolResult, ActionOutput, error) {
		if err := validateElementTarget(inp.Ref, inp.Selector, inp.Frame, true); err != nil {
			return nil, ActionOutput{}, err
		}
		if err := validateSelectorTimeout(inp.Timeout); err != nil {
			return nil, ActionOutput{}, err
		}
		if err := validateObserveWindow(inp.ObserveWindowMs); err != nil {
			return nil, ActionOutput{}, err
		}

		criteria := 0
		var mode string
		var items any
		if inp.Values != nil {
			criteria++
			mode = "values"
			items = inp.Values
		}
		if inp.Labels != nil {
			criteria++
			mode = "labels"
			items = inp.Labels
		}
		if inp.Indexes != nil {
			criteria++
			mode = "indexes"
			for _, idx := range inp.Indexes {
				if idx < 0 {
					return nil, ActionOutput{}, fmt.Errorf("index out of range: must be >= 0, got %d", idx)
				}
			}
			items = inp.Indexes
		}
		if criteria != 1 {
			return nil, ActionOutput{}, fmt.Errorf("exactly one of values, labels, or indexes must be provided")
		}

		t, err := mgr.ResolveTab("", inp.Tab)
		if err != nil {
			return nil, ActionOutput{}, err
		}

		// Wrap mode and items inside a non-empty JSON object so encoding/json/v2's
		// omitempty tag on runtime.CallArgument.Value never drops an empty items array [].
		const selectOptionJS = `function(req) {
			const win = (this.ownerDocument && this.ownerDocument.defaultView) || window;
			if (!(this instanceof win.HTMLSelectElement)) {
				throw new Error('matched element is not a <select>');
			}
			const mode = req.mode;
			const items = req.items;
			if (!this.multiple && items.length !== 1) {
				throw new Error('single <select> requires exactly one option, got ' + items.length);
			}
			const chosen = new Set();
			for (let k = 0; k < items.length; k++) {
				const target = items[k];
				let matchedIdx = -1;
				if (mode === 'indexes') {
					if (target < 0 || target >= this.options.length) {
						throw new Error('index out of range: ' + target);
					}
					matchedIdx = target;
				} else {
					for (let i = 0; i < this.options.length; i++) {
						const v = mode === 'values' ? this.options[i].value : this.options[i].text;
						if (v === target) { matchedIdx = i; break; }
					}
					if (matchedIdx < 0) {
						throw new Error('no option with ' + mode.slice(0, -1) + ' ' + JSON.stringify(target));
					}
				}
				chosen.add(matchedIdx);
			}
			for (let i = 0; i < this.options.length; i++) {
				this.options[i].selected = chosen.has(i);
			}
			this.dispatchEvent(new win.Event('input', {bubbles: true}));
			this.dispatchEvent(new win.Event('change', {bubbles: true}));
		}`
		reqJSON, _ := json.Marshal(struct {
			Mode  string `json:"mode"`
			Items any    `json:"items"`
		}{Mode: mode, Items: items})

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()

		var obs *observation
		err = chromedp.Do(tctx, chromedp.Func(func(ctx context.Context, tgt *chromedp.Target) error {
			el, err := resolveElementObject(ctx, tgt, t, inp.Ref, inp.Selector, inp.Frame, inp.Timeout, false)
			if err != nil {
				return err
			}
			defer el.Release(ctx, tgt)

			obs = openObservation(ctx, t, inp.ObserveWindowMs)
			return runDialogInterruptible(ctx, t.Dialog, func(dctx context.Context) error {
				_, err := callOnElement[any](dctx, tgt, el.ObjectID, selectOptionJS, false,
					&runtime.CallArgument{Value: reqJSON},
				)
				return err
			})
		}))
		if err != nil {
			return nil, ActionOutput{}, err
		}
		return nil, ActionOutput{Observed: obs.close(tctx)}, nil
	})
}

func registerScrollTool(s *mcp.Server, mgr *browser.Manager) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "scroll",
		Description: "Scroll an element into view (by snapshot ref or CSS/shadow selector) or scroll the window by (x, y) pixels.",
		Annotations: mutatingAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, inp ScrollInput) (*mcp.CallToolResult, ScrollOutput, error) {
		if err := validateSelectorTimeout(inp.Timeout); err != nil {
			return nil, ScrollOutput{}, err
		}
		if err := validateObserveWindow(inp.ObserveWindowMs); err != nil {
			return nil, ScrollOutput{}, err
		}
		if err := validateElementTarget(inp.Ref, inp.Selector, inp.Frame, false); err != nil {
			return nil, ScrollOutput{}, err
		}
		t, err := mgr.ResolveTab("", inp.Tab)
		if err != nil {
			return nil, ScrollOutput{}, err
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()

		var (
			obs          *observation
			out          ScrollOutput
			haveFramePos bool
		)
		if inp.Ref != "" || inp.Selector != "" {
			err = chromedp.Do(tctx, chromedp.Func(func(ctx context.Context, tgt *chromedp.Target) error {
				el, err := resolveElementObject(ctx, tgt, t, inp.Ref, inp.Selector, inp.Frame, inp.Timeout, true)
				if err != nil {
					return err
				}
				defer el.Release(ctx, tgt)

				obs = openObservation(ctx, t, inp.ObserveWindowMs)
				scrolled := false
				if el.BackendNodeID > 0 {
					if _, serr := cdp.Call(ctx, tgt, dom.ScrollIntoViewIfNeeded, dom.ScrollIntoViewIfNeededParams{
						BackendNodeID: el.BackendNodeID,
					}); serr == nil {
						scrolled = true
					}
				}
				if !scrolled {
					if _, err := callOnElement[any](ctx, tgt, el.ObjectID, `function() { if (typeof this.scrollIntoView === 'function') this.scrollIntoView({block: 'center', inline: 'center'}); }`, false); err != nil {
						return err
					}
				}
				if el.FrameID != "" {
					pos, err := callOnElement[ScrollOutput](ctx, tgt, el.ObjectID, `function() {
						const win = (this.ownerDocument && this.ownerDocument.defaultView) || window;
						return {scroll_x: win.scrollX, scroll_y: win.scrollY};
					}`, false)
					if err != nil {
						return err
					}
					out = pos
					haveFramePos = true
				}
				return nil
			}))
		} else {
			uniqueCtxID, err2 := resolveFrameContext(t, inp.Frame, false)
			if err2 != nil {
				return nil, ScrollOutput{}, err2
			}
			obs = openObservation(tctx, t, inp.ObserveWindowMs)
			js := fmt.Sprintf("window.scrollBy(%d, %d)", inp.X, inp.Y)
			err = chromedp.Do(tctx, chromedp.Evaluate[chromedp.Void](js, evalOptsForFrame(uniqueCtxID)...))
			if err == nil && inp.Frame != "" {
				pos, perr := chromedp.Run(tctx, chromedp.Evaluate[ScrollOutput](`({scroll_x: window.scrollX, scroll_y: window.scrollY})`, evalOptsForFrame(uniqueCtxID)...))
				if perr != nil {
					return nil, ScrollOutput{}, perr
				}
				out = pos
				haveFramePos = true
			}
		}
		if err != nil {
			return nil, ScrollOutput{}, err
		}

		if !haveFramePos {
			metrics, err := chromedp.Call(tctx, page.GetLayoutMetrics, cdp.Empty{})
			if err != nil {
				return nil, ScrollOutput{}, err
			}
			if metrics.CSSVisualViewport != nil {
				out.ScrollX = metrics.CSSVisualViewport.PageX
				out.ScrollY = metrics.CSSVisualViewport.PageY
			}
		}
		out.Observed = obs.close(tctx)
		return nil, out, nil
	})
}

func registerHoverTool(s *mcp.Server, mgr *browser.Manager) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "hover",
		Description: "Move the pointer over an element (by snapshot ref or CSS/shadow selector) or viewport (x, y) coordinates.",
		Annotations: mutatingAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, inp HoverInput) (*mcp.CallToolResult, ActionOutput, error) {
		if err := validatePointerTarget(inp.Ref, inp.Selector, inp.Frame, inp.X, inp.Y, true); err != nil {
			return nil, ActionOutput{}, err
		}
		if err := validateSelectorTimeout(inp.Timeout); err != nil {
			return nil, ActionOutput{}, err
		}
		if err := validateObserveWindow(inp.ObserveWindowMs); err != nil {
			return nil, ActionOutput{}, err
		}
		t, err := mgr.ResolveTab("", inp.Tab)
		if err != nil {
			return nil, ActionOutput{}, err
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()

		var obs *observation
		err = chromedp.Do(tctx, chromedp.Func(func(ctx context.Context, tgt *chromedp.Target) error {
			var cx, cy float64
			if inp.X != nil {
				obs = openObservation(ctx, t, inp.ObserveWindowMs)
				cx, cy = *inp.X, *inp.Y
			} else {
				el, err := resolveElementObject(ctx, tgt, t, inp.Ref, inp.Selector, inp.Frame, inp.Timeout, true)
				if err != nil {
					return err
				}
				defer el.Release(ctx, tgt)

				obs = openObservation(ctx, t, inp.ObserveWindowMs)
				cx, cy, err = scrollAndCenterBackendNode(ctx, tgt, el.BackendNodeID)
				if err != nil {
					return err
				}
			}
			_, err := cdp.Call(ctx, tgt, input.DispatchMouseEvent, input.DispatchMouseEventParams{
				Type: chromedp.MouseMoved,
				X:    cx,
				Y:    cy,
			})
			return err
		}))
		if err != nil {
			return nil, ActionOutput{}, err
		}
		return nil, ActionOutput{Observed: obs.close(tctx)}, nil
	})
}

func registerPressKeyTool(s *mcp.Server, mgr *browser.Manager) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "press_key",
		Description: "Press a keyboard key (optionally with modifiers), optionally focusing a target element (ref or CSS/shadow selector) first.",
		InputSchema: schemaWithEnums[PressKeyInput](map[string][]string{
			"modifiers": pressKeyModifierValues,
		}),
		Annotations: mutatingAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, inp PressKeyInput) (*mcp.CallToolResult, ActionOutput, error) {
		if err := validateSelectorTimeout(inp.Timeout); err != nil {
			return nil, ActionOutput{}, err
		}
		if err := validateObserveWindow(inp.ObserveWindowMs); err != nil {
			return nil, ActionOutput{}, err
		}
		if err := validateElementTarget(inp.Ref, inp.Selector, inp.Frame, false); err != nil {
			return nil, ActionOutput{}, err
		}
		if inp.Frame != "" && inp.Selector == "" {
			return nil, ActionOutput{}, fmt.Errorf("frame requires selector")
		}
		modifiers, err := parseModifiers(inp.Modifiers)
		if err != nil {
			return nil, ActionOutput{}, err
		}

		var r rune
		if mapped, ok := namedKeys[inp.Key]; ok {
			r = mapped
		} else if len(inp.Key) == 1 {
			r = rune(inp.Key[0])
		} else {
			decoded, size := utf8.DecodeRuneInString(inp.Key)
			if decoded == utf8.RuneError || size != len(inp.Key) {
				return nil, ActionOutput{}, fmt.Errorf("unknown key: %q", inp.Key)
			}
			r = decoded
		}

		t, err := mgr.ResolveTab("", inp.Tab)
		if err != nil {
			return nil, ActionOutput{}, err
		}
		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()

		var opts []chromedp.KeyOption
		if modifiers != 0 {
			opts = append(opts, chromedp.KeyModifiers(modifiers))
		}

		var obs *observation
		err = chromedp.Do(tctx, chromedp.Func(func(ctx context.Context, tgt *chromedp.Target) error {
			if inp.Ref != "" || inp.Selector != "" {
				el, err := resolveElementObject(ctx, tgt, t, inp.Ref, inp.Selector, inp.Frame, inp.Timeout, true)
				if err != nil {
					return err
				}
				defer el.Release(ctx, tgt)
				obs = openObservation(ctx, t, inp.ObserveWindowMs)
				if err := focusResolvedElement(ctx, tgt, el); err != nil {
					return err
				}
				// Release before KeyEvent so a keydown handler opening a modal
				// dialog cannot stall deferred ReleaseObject.
				el.Release(ctx, tgt)
			} else {
				obs = openObservation(ctx, t, inp.ObserveWindowMs)
			}
			return runDialogInterruptible(ctx, t.Dialog, func(dctx context.Context) error {
				_, err := chromedp.KeyEvent(string(r), opts...)(dctx, tgt)
				return err
			})
		}))
		if err != nil {
			return nil, ActionOutput{}, err
		}
		return nil, ActionOutput{Observed: obs.close(tctx)}, nil
	})
}

func registerUploadFilesTool(s *mcp.Server, mgr *browser.Manager) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "upload_files",
		Description: "Set files on a file input element (by snapshot ref or CSS/shadow selector).",
		Annotations: mutatingAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, inp UploadFilesInput) (*mcp.CallToolResult, ActionOutput, error) {
		if err := validateElementTarget(inp.Ref, inp.Selector, inp.Frame, true); err != nil {
			return nil, ActionOutput{}, err
		}
		if err := validateSelectorTimeout(inp.Timeout); err != nil {
			return nil, ActionOutput{}, err
		}
		if err := validateObserveWindow(inp.ObserveWindowMs); err != nil {
			return nil, ActionOutput{}, err
		}
		if len(inp.Paths) == 0 {
			return nil, ActionOutput{}, fmt.Errorf("paths must not be empty")
		}
		for _, p := range inp.Paths {
			if p == "" {
				return nil, ActionOutput{}, fmt.Errorf("upload path must not be empty")
			}
		}
		b, t, err := mgr.ResolveTabWithBrowser("", inp.Tab)
		if err != nil {
			return nil, ActionOutput{}, err
		}
		// Only validate against the local filesystem when this server
		// launched Chrome locally; connected browsers may run on a remote
		// host or container with a different filesystem.
		if b.Mode == browser.ModeLaunch {
			for _, p := range inp.Paths {
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

		var obs *observation
		err = chromedp.Do(tctx, chromedp.Func(func(ctx context.Context, tgt *chromedp.Target) error {
			el, err := resolveElementObject(ctx, tgt, t, inp.Ref, inp.Selector, inp.Frame, inp.Timeout, true)
			if err != nil {
				return err
			}
			defer el.Release(ctx, tgt)
			if el.BackendNodeID == 0 {
				return fmt.Errorf("file input %s could not be resolved to a backend DOM node", el.Label)
			}
			obs = openObservation(ctx, t, inp.ObserveWindowMs)
			_, err = cdp.Call(ctx, tgt, dom.SetFileInputFiles, dom.SetFileInputFilesParams{
				Files:         inp.Paths,
				BackendNodeID: el.BackendNodeID,
			})
			return err
		}))
		if err != nil {
			return nil, ActionOutput{}, err
		}
		return nil, ActionOutput{Observed: obs.close(tctx)}, nil
	})
}
