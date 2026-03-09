package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/thegrumpylion/chromedp-mcp/internal/browser"
)

// ClickInput is the input for click.
type ClickInput struct {
	TabInput
	Selector   string `json:"selector" jsonschema:"CSS selector of the element to click"`
	Button     string `json:"button,omitempty" jsonschema:"Mouse button: left (default) right middle"`
	ClickCount int    `json:"click_count,omitempty" jsonschema:"Number of clicks (default 1 use 2 for double-click)"`
}

// TypeInput is the input for type.
type TypeInput struct {
	TabInput
	Selector string `json:"selector" jsonschema:"CSS selector of the input element"`
	Text     string `json:"text" jsonschema:"Text to type"`
	Clear    bool   `json:"clear,omitempty" jsonschema:"Clear the field before typing (default false)"`
	Delay    int    `json:"delay,omitempty" jsonschema:"Delay between keystrokes in milliseconds (default 0)"`
}

// SelectOptionInput is the input for select_option.
type SelectOptionInput struct {
	TabInput
	Selector string  `json:"selector" jsonschema:"CSS selector of the select element"`
	Value    *string `json:"value,omitempty" jsonschema:"Option value to select"`
	Label    string  `json:"label,omitempty" jsonschema:"Option visible text to select"`
	Index    *int    `json:"index,omitempty" jsonschema:"Option index to select"`
}

// SubmitFormInput is the input for submit_form.
type SubmitFormInput struct {
	TabInput
	Selector string `json:"selector" jsonschema:"CSS selector of the form or an element within the form"`
}

// ScrollInput is the input for scroll.
type ScrollInput struct {
	TabInput
	Selector string `json:"selector,omitempty" jsonschema:"CSS selector to scroll into view. If omitted scrolls the page."`
	X        int    `json:"x,omitempty" jsonschema:"Horizontal scroll offset in pixels"`
	Y        int    `json:"y,omitempty" jsonschema:"Vertical scroll offset in pixels"`
}

// HoverInput is the input for hover.
type HoverInput struct {
	TabInput
	Selector string `json:"selector" jsonschema:"CSS selector of the element to hover over"`
}

// FocusInput is the input for focus.
type FocusInput struct {
	TabInput
	Selector string `json:"selector" jsonschema:"CSS selector of the element to focus"`
}

// PressKeyInput is the input for press_key.
type PressKeyInput struct {
	TabInput
	Key       string   `json:"key" jsonschema:"Key to press (e.g. Enter Tab Escape ArrowDown)"`
	Modifiers []string `json:"modifiers,omitempty" jsonschema:"Modifier keys: ctrl shift alt meta"`
}

// UploadFilesInput is the input for upload_files.
type UploadFilesInput struct {
	TabInput
	Selector string   `json:"selector" jsonschema:"CSS selector of the file input element"`
	Paths    []string `json:"paths" jsonschema:"Absolute file paths to set"`
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
		Description: "Click an element by CSS selector.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, inp ClickInput) (*mcp.CallToolResult, struct{}, error) {
		t, err := mgr.ResolveTab("", inp.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}

		tctx := t.Context()

		if inp.ClickCount == 2 {
			sctx, cancel := selectorContext(tctx, inp.Timeout)
			defer cancel()
			return nil, struct{}{}, chromedp.Run(sctx, chromedp.DoubleClick(inp.Selector, chromedp.ByQuery))
		}

		// For non-standard buttons or click counts, use JS dispatch.
		if inp.Button == "right" || inp.Button == "middle" || inp.ClickCount > 2 {
			button := 0
			switch inp.Button {
			case "right":
				button = 2
			case "middle":
				button = 1
			}
			clickCount := inp.ClickCount
			if clickCount <= 0 {
				clickCount = 1
			}
			js := fmt.Sprintf(`(function() {
				var el = document.querySelector(%q);
				if (!el) throw new Error('element not found');
				var rect = el.getBoundingClientRect();
				var x = rect.x + rect.width/2, y = rect.y + rect.height/2;
				for (var i = 0; i < %d; i++) {
					var detail = i + 1;
					el.dispatchEvent(new MouseEvent('mousedown', {bubbles:true, button:%d, detail:detail, clientX:x, clientY:y}));
					el.dispatchEvent(new MouseEvent('mouseup', {bubbles:true, button:%d, detail:detail, clientX:x, clientY:y}));
					el.dispatchEvent(new MouseEvent('click', {bubbles:true, button:%d, detail:detail, clientX:x, clientY:y}));
				}
			})()`, inp.Selector, clickCount, button, button, button)
			var res interface{}
			if err := chromedp.Run(tctx, chromedp.Evaluate(js, &res)); err != nil {
				return nil, struct{}{}, err
			}
			return nil, struct{}{}, nil
		}

		sctx, cancel := selectorContext(tctx, inp.Timeout)
		defer cancel()
		return nil, struct{}{}, chromedp.Run(sctx, chromedp.Click(inp.Selector, chromedp.ByQuery))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "type",
		Description: "Type text into an element matching a CSS selector.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, inp TypeInput) (*mcp.CallToolResult, struct{}, error) {
		t, err := mgr.ResolveTab("", inp.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}

		tctx := t.Context()

		var actions chromedp.Tasks
		if inp.Clear {
			// Use JS to clear the field value. chromedp.Clear only
			// resets the HTML attribute, not the JS property, which
			// means typed text may not actually be removed.
			clearJS := fmt.Sprintf(`(function() {
				var el = document.querySelector(%q);
				if (el) { el.value = ''; el.dispatchEvent(new Event('input', {bubbles:true})); }
			})()`, inp.Selector)
			actions = append(actions, chromedp.Evaluate(clearJS, nil))
		}
		if inp.Delay > 0 {
			// Type character by character with delay.
			actions = append(actions, chromedp.Focus(inp.Selector, chromedp.ByQuery))
			for _, ch := range inp.Text {
				actions = append(actions, chromedp.KeyEvent(string(ch)))
				actions = append(actions, chromedp.Sleep(time.Duration(inp.Delay)*time.Millisecond))
			}
		} else {
			actions = append(actions, chromedp.SendKeys(inp.Selector, inp.Text, chromedp.ByQuery))
		}

		sctx, cancel := selectorContext(tctx, inp.Timeout)
		defer cancel()
		if err := chromedp.Run(sctx, actions); err != nil {
			return nil, struct{}{}, err
		}
		return nil, struct{}{}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "select_option",
		Description: "Select an option from a <select> element. Exactly one of value, label, or index must be provided.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, inp SelectOptionInput) (*mcp.CallToolResult, struct{}, error) {
		t, err := mgr.ResolveTab("", inp.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}

		// Validate that exactly one selection criterion is provided.
		criteria := 0
		if inp.Value != nil {
			criteria++
		}
		if inp.Label != "" {
			criteria++
		}
		if inp.Index != nil {
			criteria++
		}
		if criteria > 1 {
			return nil, struct{}{}, fmt.Errorf("exactly one of value, label, or index must be provided, not multiple")
		}

		// Build a JS snippet to select by the appropriate attribute.
		// Use option.selected = true instead of el.value = X so that
		// <select multiple> elements are handled correctly.
		var js string
		if inp.Value != nil {
			js = fmt.Sprintf(`(function() {
				var sel = document.querySelector(%q);
				if (!sel) throw new Error('element not found');
				var found = false;
				for (var i = 0; i < sel.options.length; i++) {
					if (sel.options[i].value === %q) { sel.options[i].selected = true; found = true; break; }
				}
				if (!found) throw new Error('no option with value ' + %q);
				sel.dispatchEvent(new Event('change', {bubbles: true}));
			})()`, inp.Selector, *inp.Value, *inp.Value)
		} else if inp.Label != "" {
			js = fmt.Sprintf(`(function() {
				var sel = document.querySelector(%q);
				if (!sel) throw new Error('element not found');
				var found = false;
				for (var i = 0; i < sel.options.length; i++) {
					if (sel.options[i].text === %q) { sel.options[i].selected = true; found = true; break; }
				}
				if (!found) throw new Error('no option with label ' + %q);
				sel.dispatchEvent(new Event('change', {bubbles: true}));
			})()`, inp.Selector, inp.Label, inp.Label)
		} else if inp.Index != nil {
			js = fmt.Sprintf(`(function() {
				var sel = document.querySelector(%q);
				if (!sel) throw new Error('element not found');
				if (%d < 0 || %d >= sel.options.length) throw new Error('index out of range');
				sel.options[%d].selected = true;
				sel.dispatchEvent(new Event('change', {bubbles: true}));
			})()`, inp.Selector, *inp.Index, *inp.Index, *inp.Index)
		} else {
			return nil, struct{}{}, fmt.Errorf("exactly one of value, label, or index must be provided")
		}

		var res interface{}
		if err := chromedp.Run(t.Context(), chromedp.Evaluate(js, &res)); err != nil {
			return nil, struct{}{}, err
		}
		return nil, struct{}{}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "submit_form",
		Description: "Submit a form by CSS selector. Fires the submit event so JS handlers can intercept it.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, inp SubmitFormInput) (*mcp.CallToolResult, struct{}, error) {
		t, err := mgr.ResolveTab("", inp.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}
		// Use requestSubmit() which fires the submit event (unlike the
		// native .submit() method used by chromedp.Submit). This allows
		// JS submit handlers and e.preventDefault() to work correctly.
		js := fmt.Sprintf(`(function() {
			var el = document.querySelector(%q);
			if (!el) throw new Error('element not found');
			var form = el.nodeName === 'FORM' ? el : el.form || el.closest('form');
			if (!form) throw new Error('no form found for selector');
			form.requestSubmit();
		})()`, inp.Selector)
		var res interface{}
		if err := chromedp.Run(t.Context(), chromedp.Evaluate(js, &res)); err != nil {
			return nil, struct{}{}, err
		}
		return nil, struct{}{}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "scroll",
		Description: "Scroll a page or scroll an element into view.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, inp ScrollInput) (*mcp.CallToolResult, struct{}, error) {
		t, err := mgr.ResolveTab("", inp.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}

		if inp.Selector != "" {
			sctx, cancel := selectorContext(t.Context(), inp.Timeout)
			defer cancel()
			err = chromedp.Run(sctx, chromedp.ScrollIntoView(inp.Selector, chromedp.ByQuery))
		} else {
			js := fmt.Sprintf("window.scrollBy(%d, %d)", inp.X, inp.Y)
			var res interface{}
			err = chromedp.Run(t.Context(), chromedp.Evaluate(js, &res))
		}
		if err != nil {
			return nil, struct{}{}, err
		}
		return nil, struct{}{}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "hover",
		Description: "Hover over an element by CSS selector.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, inp HoverInput) (*mcp.CallToolResult, struct{}, error) {
		t, err := mgr.ResolveTab("", inp.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}

		tctx := t.Context()
		sctx, cancel := selectorContext(tctx, inp.Timeout)
		defer cancel()

		// Use chromedp.MouseClickXY coordinates with CDP Input.dispatchMouseEvent
		// (mouseMoved) so the browser activates its native :hover CSS state.
		// JS event dispatch alone does NOT trigger :hover pseudo-class.
		var nodes []*cdp.Node
		if err := chromedp.Run(sctx, chromedp.Nodes(inp.Selector, &nodes, chromedp.ByQuery)); err != nil {
			return nil, struct{}{}, err
		}
		if len(nodes) == 0 {
			return nil, struct{}{}, fmt.Errorf("selector %q matched no elements", inp.Selector)
		}

		// Get element center coordinates via JS (getBoundingClientRect).
		var coords [2]float64
		js := fmt.Sprintf(`(function() {
			var el = document.querySelector(%q);
			var rect = el.getBoundingClientRect();
			return [rect.x + rect.width/2, rect.y + rect.height/2];
		})()`, inp.Selector)
		if err := chromedp.Run(tctx, chromedp.Evaluate(js, &coords)); err != nil {
			return nil, struct{}{}, err
		}

		// Dispatch a CDP mouseMoved event to the element center.
		// This triggers the browser's native :hover state.
		if err := chromedp.Run(tctx, chromedp.ActionFunc(func(ctx context.Context) error {
			return input.DispatchMouseEvent(input.MouseMoved, coords[0], coords[1]).Do(ctx)
		})); err != nil {
			return nil, struct{}{}, err
		}
		return nil, struct{}{}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "focus",
		Description: "Focus an element by CSS selector.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, inp FocusInput) (*mcp.CallToolResult, struct{}, error) {
		t, err := mgr.ResolveTab("", inp.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}
		sctx, cancel := selectorContext(t.Context(), inp.Timeout)
		defer cancel()
		if err := chromedp.Run(sctx, chromedp.Focus(inp.Selector, chromedp.ByQuery)); err != nil {
			return nil, struct{}{}, err
		}
		return nil, struct{}{}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "press_key",
		Description: "Press a keyboard key, optionally with modifiers.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, inp PressKeyInput) (*mcp.CallToolResult, struct{}, error) {
		t, err := mgr.ResolveTab("", inp.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}

		var modifiers input.Modifier
		for _, m := range inp.Modifiers {
			switch m {
			case "ctrl":
				modifiers |= input.ModifierCtrl
			case "shift":
				modifiers |= input.ModifierShift
			case "alt":
				modifiers |= input.ModifierAlt
			case "meta":
				modifiers |= input.ModifierMeta
			}
		}

		if err := chromedp.Run(t.Context(), chromedp.ActionFunc(func(ctx context.Context) error {
			down := input.DispatchKeyEvent(input.KeyDown).WithKey(inp.Key).WithModifiers(modifiers)
			// For single printable characters, set the text field so the
			// character is inserted into focused input/textarea elements.
			if len(inp.Key) == 1 && modifiers == 0 {
				down = down.WithText(inp.Key)
			}
			if err := down.Do(ctx); err != nil {
				return err
			}
			return input.DispatchKeyEvent(input.KeyUp).WithKey(inp.Key).WithModifiers(modifiers).Do(ctx)
		})); err != nil {
			return nil, struct{}{}, err
		}
		return nil, struct{}{}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "upload_files",
		Description: "Set files on a file input element.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, inp UploadFilesInput) (*mcp.CallToolResult, struct{}, error) {
		t, err := mgr.ResolveTab("", inp.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}
		sctx, cancel := selectorContext(t.Context(), inp.Timeout)
		defer cancel()
		if err := chromedp.Run(sctx, chromedp.SetUploadFiles(inp.Selector, inp.Paths, chromedp.ByQuery)); err != nil {
			return nil, struct{}{}, err
		}
		return nil, struct{}{}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "handle_dialog",
		Description: "Handle a JavaScript dialog (alert, confirm, prompt, beforeunload).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, inp HandleDialogInput) (*mcp.CallToolResult, struct{}, error) {
		t, err := mgr.ResolveTab("", inp.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}

		err = chromedp.Run(t.Context(), chromedp.ActionFunc(func(ctx context.Context) error {
			params := page.HandleJavaScriptDialog(inp.Accept)
			if inp.Text != "" {
				params = params.WithPromptText(inp.Text)
			}
			return params.Do(ctx)
		}))
		if err != nil {
			return nil, struct{}{}, err
		}
		return nil, struct{}{}, nil
	})
}
