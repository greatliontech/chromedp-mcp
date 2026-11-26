package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync/atomic"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"

	"github.com/greatliontech/chromedp-mcp/internal/collector"
	"github.com/greatliontech/chromedp-mcp/internal/tab"
)

// shadowPiercingSeparator splits a selector into chained CSS segments that
// cross open Shadow DOM boundaries (e.g. "my-card >>> button.save").
const shadowPiercingSeparator = ">>>"

// parseShadowSelector splits selector on unquoted ">>>" combinators (respecting
// single/double-quoted CSS attribute values and backslash escapes). Plain
// selectors return ([]string{selector}, false, nil). Empty segments around
// ">>>" fail with a descriptive validation error.
func parseShadowSelector(selector string) ([]string, bool, error) {
	var (
		parts    []string
		quote    byte
		escaped  bool
		segStart int
		foundSep bool
	)
	for i := 0; i < len(selector); i++ {
		c := selector[i]
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
			continue
		}
		if strings.HasPrefix(selector[i:], shadowPiercingSeparator) {
			foundSep = true
			seg := strings.TrimSpace(selector[segStart:i])
			if seg == "" {
				return nil, true, fmt.Errorf("invalid shadow selector %q: empty segment around %q", selector, shadowPiercingSeparator)
			}
			parts = append(parts, seg)
			i += len(shadowPiercingSeparator) - 1
			segStart = i + 1
		}
	}
	if !foundSep {
		return []string{selector}, false, nil
	}
	last := strings.TrimSpace(selector[segStart:])
	if last == "" {
		return nil, true, fmt.Errorf("invalid shadow selector %q: empty segment around %q", selector, shadowPiercingSeparator)
	}
	parts = append(parts, last)
	return parts, true, nil
}

// queryAllElementsJS returns a JS expression evaluating to an Array of all
// matching Elements in the current document, crossing open shadow roots when
// selector uses the ">>>" combinator.
func queryAllElementsJS(selector string) (string, error) {
	parts, isShadow, err := parseShadowSelector(selector)
	if err != nil {
		return "", err
	}
	if !isShadow {
		return fmt.Sprintf(`Array.from(document.querySelectorAll(%s))`, jsQuote(selector)), nil
	}
	return queryAllElementsFromPartsJS(parts)
}

func queryAllElementsFromPartsJS(parts []string) (string, error) {
	segsJSON, err := json.Marshal(parts)
	if err != nil {
		return "", err
	}
	// Validate every segment's CSS syntax up front so invalid CSS after an
	// unmatched or closed shadow host fails fast instead of spinning until timeout.
	return fmt.Sprintf(`(function(segs) {
		const check = document.createDocumentFragment();
		for (let i = 0; i < segs.length; i++) {
			check.querySelector(segs[i]);
		}
		let roots = [document];
		for (let i = 0; i < segs.length; i++) {
			const matches = [];
			for (let r = 0; r < roots.length; r++) {
				const list = roots[r].querySelectorAll(segs[i]);
				for (let j = 0; j < list.length; j++) matches.push(list[j]);
			}
			if (i === segs.length - 1) return matches;
			const next = [];
			for (let j = 0; j < matches.length; j++) {
				if (matches[j].shadowRoot) next.push(matches[j].shadowRoot);
			}
			if (next.length === 0) return [];
			roots = next;
		}
		return [];
	})(%s)`, string(segsJSON)), nil
}

// queryOneElementJS returns a JS expression evaluating to the first matching
// Element (or null) in the current document, supporting ">>>" shadow piercing.
func queryOneElementJS(selector string) (string, error) {
	parts, isShadow, err := parseShadowSelector(selector)
	if err != nil {
		return "", err
	}
	if !isShadow {
		return fmt.Sprintf(`document.querySelector(%s)`, jsQuote(selector)), nil
	}
	allExpr, err := queryAllElementsFromPartsJS(parts)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`((%s)[0] || null)`, allExpr), nil
}

// waitForSelector waits for selector to match an element in the main frame
// (frameID == "") or inside frameID's execution context, supporting ">>>"
// Shadow DOM piercing. When frameID is non-empty, returns the live
// UniqueContextID (re-resolving across same-origin subframe navigations).
func waitForSelector(sctx, tctx context.Context, t *tab.Tab, uniqueCtxID, frameID, selector string) (string, error) {
	_, isShadow, err := parseShadowSelector(selector)
	if err != nil {
		return "", err
	}
	if frameID == "" && !isShadow {
		if err := chromedp.Do(sctx, chromedp.WaitReady(chromedp.CSS(selector))); err != nil {
			return "", selectorError(tctx, selector, err)
		}
		return "", nil
	}

	findExpr, err := queryOneElementJS(selector)
	if err != nil {
		return "", err
	}
	checkJS := fmt.Sprintf(`(%s) !== null`, findExpr)
	return pollFrameEval(sctx, tctx, t, uniqueCtxID, frameID, checkJS, false, 50*time.Millisecond,
		func(found bool) bool { return found },
		func(_ bool, _ bool) error {
			if frameID != "" {
				return fmt.Errorf("selector %q not found in frame %q: %w", selector, frameID, sctx.Err())
			}
			return fmt.Errorf("element %q not found in the DOM (timed out waiting for it to appear)", selector)
		},
		fmt.Sprintf("selector %q", selector),
	)
}

// pollFrameEval polls jsExpr in frameID (or the main frame when frameID == "")
// until done(val) returns true or sctx expires. It handles stale execution
// contexts across navigations/frame reloads and fails fast on JavaScript
// exceptions (e.g. invalid CSS syntax or rejected Promises). Returns the last
// live UniqueContextID when frameID != "".
func pollFrameEval[T any](
	sctx, tctx context.Context,
	t *tab.Tab,
	uniqueCtxID, frameID, jsExpr string,
	awaitPromise bool,
	interval time.Duration,
	done func(T) bool,
	timeoutErr func(lastVal T, hasVal bool) error,
	label string,
) (string, error) {
	curCtxID := uniqueCtxID
	deadCtxIDs := make(map[string]bool)
	var (
		lastVal T
		hasVal  bool
	)
	refreshFrameCtx := func() (retryNow bool, err error) {
		ec, reachable, known, swapped := t.ExecContexts.ContextForFrame(frameID)
		if !known {
			return false, fmt.Errorf("frame %q detached while waiting for %s", frameID, label)
		}
		if swapped {
			return false, fmt.Errorf("frame %q became out-of-process (cross-origin) while waiting for %s", frameID, label)
		}
		if reachable && !deadCtxIDs[ec.UniqueID] {
			curCtxID = ec.UniqueID
			return true, nil
		}
		curCtxID = ""
		return false, nil
	}
	onTimeout := func() error {
		if frameID != "" && curCtxID == "" {
			if _, reachable, known, _ := t.ExecContexts.ContextForFrame(frameID); known && !reachable {
				return fmt.Errorf("frame %q has no reachable execution context while waiting for %s: %w", frameID, label, sctx.Err())
			}
		}
		return timeoutErr(lastVal, hasVal)
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		if frameID == "" || curCtxID != "" {
			var (
				val     T
				excText string
			)
			err := chromedp.Do(sctx, chromedp.Func(func(ctx context.Context, tgt *chromedp.Target) error {
				params := runtime.EvaluateParams{
					Expression:    jsExpr,
					ReturnByValue: new(true),
					AwaitPromise:  new(awaitPromise),
				}
				if curCtxID != "" {
					params.UniqueContextID = curCtxID
				}
				res, err := cdp.Call(ctx, tgt, runtime.Evaluate, params)
				if err != nil {
					return err
				}
				if res.ExceptionDetails != nil {
					releaseEvalException(tctx, tgt, res.Result, res.ExceptionDetails)
					excText = formatExceptionDetails(res.ExceptionDetails)
					return nil
				}
				if res.Result != nil && len(res.Result.Value) > 0 {
					if err := json.Unmarshal(res.Result.Value, &val); err != nil {
						return err
					}
				}
				return nil
			}))
			if excText != "" {
				if frameID != "" {
					return "", fmt.Errorf("%s in frame %q: %s", label, frameID, excText)
				}
				return "", fmt.Errorf("%s: %s", label, excText)
			}
			if err == nil {
				lastVal = val
				hasVal = true
				if done(val) {
					return curCtxID, nil
				}
			} else {
				if tctx.Err() != nil {
					return "", tctx.Err()
				}
				if errors.Is(sctx.Err(), context.DeadlineExceeded) {
					return "", onTimeout()
				}
				if isStaleContextError(err) {
					if frameID != "" {
						deadCtxIDs[curCtxID] = true
						retryNow, rerr := refreshFrameCtx()
						if rerr != nil {
							return "", rerr
						}
						if retryNow {
							continue
						}
					}
					// For main-frame evaluations, retry until the incoming
					// document finishes navigating or sctx expires.
				} else {
					return "", err
				}
			}
		} else {
			retryNow, rerr := refreshFrameCtx()
			if rerr != nil {
				return "", rerr
			}
			if retryNow {
				continue
			}
		}

		select {
		case <-tctx.Done():
			return "", tctx.Err()
		case <-sctx.Done():
			return "", onTimeout()
		case <-ticker.C:
		}
	}
}

// resolvedElement holds a live CDP RemoteObjectID (and optional BackendNodeID)
// for an element resolved from either a snapshot ref or a CSS/shadow selector.
// Callers must always call Release once done.
type resolvedElement struct {
	ObjectID      runtime.RemoteObjectID
	BackendNodeID cdp.BackendNodeID
	FrameID       cdp.FrameID
	Label         string
	dialog        *collector.Dialog
}

func (e *resolvedElement) Release(ctx context.Context, tgt *chromedp.Target) {
	if e == nil || e.ObjectID == "" {
		return
	}
	oid := e.ObjectID
	e.ObjectID = ""
	// Skip Runtime.releaseObject when a JS modal dialog is currently blocking
	// the renderer main thread; otherwise cleanupContext would stall for 2s.
	// V8 automatically reclaims the handle on document navigation or tab close.
	if e.dialog != nil && e.dialog.Open() {
		return
	}
	releaseRemoteObject(ctx, tgt, oid)
}

func releaseRemoteObject(ctx context.Context, tgt *chromedp.Target, oid runtime.RemoteObjectID) {
	if oid == "" {
		return
	}
	cleanCtx, cancel := cleanupContext(ctx)
	defer cancel()
	_, _ = cdp.Call(cleanCtx, tgt, runtime.ReleaseObject, runtime.ReleaseObjectParams{
		ObjectID: oid,
	})
}

// releaseExceptionObject releases the V8 RemoteObjectID attached to a CDP
// Runtime.ExceptionDetails payload (if any and distinct from skipOID).
func releaseExceptionObject(ctx context.Context, tgt *chromedp.Target, ex *runtime.ExceptionDetails, skipOID runtime.RemoteObjectID) {
	if ex == nil || ex.Exception == nil || ex.Exception.ObjectID == "" || ex.Exception.ObjectID == skipOID {
		return
	}
	releaseRemoteObject(ctx, tgt, ex.Exception.ObjectID)
}

// releaseEvalException releases any V8 RemoteObjectIDs carried on a failed
// Runtime.evaluate result and its ExceptionDetails.
func releaseEvalException(ctx context.Context, tgt *chromedp.Target, res *runtime.RemoteObject, ex *runtime.ExceptionDetails) {
	var resOID runtime.RemoteObjectID
	if res != nil && res.ObjectID != "" {
		resOID = res.ObjectID
		releaseRemoteObject(ctx, tgt, resOID)
	}
	releaseExceptionObject(ctx, tgt, ex, resOID)
}

// validateElementTarget checks mutual exclusion between ref, selector, and frame.
func validateElementTarget(ref, selector, frameID string, requireOne bool) error {
	if ref != "" && selector != "" {
		return fmt.Errorf("ref and selector are mutually exclusive")
	}
	if ref != "" && frameID != "" {
		return fmt.Errorf("ref and frame are mutually exclusive (refs already bind to their frame)")
	}
	if requireOne && ref == "" && selector == "" {
		return fmt.Errorf("either ref or selector is required")
	}
	if selector != "" {
		if _, _, err := parseShadowSelector(selector); err != nil {
			return err
		}
	}
	return nil
}

// resolveElementObject resolves either a snapshot ref (via BackendNodeID) or a
// CSS/shadow selector (in the main frame or a same-origin iframe) into a live
// CDP RemoteObjectID (plus BackendNodeID when needBackendID is true). Always
// call el.Release when finished.
func resolveElementObject(
	tctx context.Context,
	tgt *chromedp.Target,
	t *tab.Tab,
	ref, selector, frameID string,
	timeoutMs int,
	needBackendID bool,
) (*resolvedElement, error) {
	if err := validateElementTarget(ref, selector, frameID, true); err != nil {
		return nil, err
	}

	if ref != "" {
		snapRef, err := t.ResolveRef(ref)
		if err != nil {
			return nil, err
		}
		res, err := cdp.Call(tctx, tgt, dom.ResolveNode, dom.ResolveNodeParams{
			BackendNodeID: snapRef.BackendNodeID,
		})
		if err != nil {
			if tctx.Err() != nil {
				return nil, tctx.Err()
			}
			return nil, fmt.Errorf("ref %q is no longer attached to the DOM — call snapshot again", ref)
		}
		if res.Object == nil || res.Object.ObjectID == "" {
			return nil, fmt.Errorf("ref %q could not be resolved to a DOM node — call snapshot again", ref)
		}
		oid := res.Object.ObjectID
		connected, err := callOnElement[bool](tctx, tgt, oid, `function() { return Boolean(this && this.isConnected); }`, false)
		if err != nil || !connected {
			releaseRemoteObject(tctx, tgt, oid)
			if err != nil && tctx.Err() != nil {
				return nil, tctx.Err()
			}
			return nil, fmt.Errorf("ref %q is no longer attached to the DOM — call snapshot again", ref)
		}
		return &resolvedElement{
			ObjectID:      oid,
			BackendNodeID: snapRef.BackendNodeID,
			FrameID:       snapRef.FrameID,
			Label:         fmt.Sprintf("ref %q", ref),
			dialog:        t.Dialog,
		}, nil
	}

	uniqueCtxID, err := resolveFrameContext(t, frameID, true)
	if err != nil {
		return nil, err
	}
	sctx, scancel := selectorContext(tctx, timeoutMs)
	uniqueCtxID, err = waitForSelector(sctx, tctx, t, uniqueCtxID, frameID, selector)
	scancel()
	if err != nil {
		return nil, err
	}

	findExpr, err := queryOneElementJS(selector)
	if err != nil {
		return nil, err
	}
	evalParams := runtime.EvaluateParams{
		Expression:    findExpr,
		ReturnByValue: new(false),
	}
	if uniqueCtxID != "" {
		evalParams.UniqueContextID = uniqueCtxID
	}
	evalRes, err := cdp.Call(tctx, tgt, runtime.Evaluate, evalParams)
	if err != nil {
		return nil, err
	}
	if evalRes.ExceptionDetails != nil {
		releaseEvalException(tctx, tgt, evalRes.Result, evalRes.ExceptionDetails)
		return nil, fmt.Errorf("selector %q: %s", selector, formatExceptionDetails(evalRes.ExceptionDetails))
	}
	if evalRes.Result == nil || evalRes.Result.ObjectID == "" {
		return nil, fmt.Errorf("selector %q matched no elements", selector)
	}
	oid := evalRes.Result.ObjectID

	var bid cdp.BackendNodeID
	if needBackendID {
		desc, err := cdp.Call(tctx, tgt, dom.DescribeNode, dom.DescribeNodeParams{
			ObjectID: oid,
		})
		if err != nil {
			if tctx.Err() != nil {
				releaseRemoteObject(tctx, tgt, oid)
				return nil, tctx.Err()
			}
		} else if desc.Node != nil {
			bid = desc.Node.BackendNodeID
		}
	}

	return &resolvedElement{
		ObjectID:      oid,
		BackendNodeID: bid,
		FrameID:       cdp.FrameID(frameID),
		Label:         fmt.Sprintf("selector %q", selector),
		dialog:        t.Dialog,
	}, nil
}

// formatExceptionDetails extracts the most descriptive error message from a
// CDP Runtime.ExceptionDetails payload, covering Error objects (Description),
// thrown primitives (Value), and fallback Text.
func formatExceptionDetails(ex *runtime.ExceptionDetails) string {
	if ex == nil {
		return "unknown JavaScript exception"
	}
	if ex.Exception != nil {
		if ex.Exception.Description != "" {
			return ex.Exception.Description
		}
		if len(ex.Exception.Value) > 0 {
			if ex.Exception.Value[0] == '"' {
				var s string
				if err := json.Unmarshal(ex.Exception.Value, &s); err == nil && s != "" {
					return s
				}
			} else {
				return string(ex.Exception.Value)
			}
		}
	}
	if ex.Text != "" {
		return ex.Text
	}
	return "unknown JavaScript exception"
}

// callOnElement runs fnDecl against oid via Runtime.callFunctionOn with
// ReturnByValue=true and unmarshals the JSON result into T.
func callOnElement[T any](
	ctx context.Context,
	tgt *chromedp.Target,
	oid runtime.RemoteObjectID,
	fnDecl string,
	awaitPromise bool,
	args ...*runtime.CallArgument,
) (T, error) {
	var zero T
	params := runtime.CallFunctionOnParams{
		ObjectID:            oid,
		FunctionDeclaration: fnDecl,
		Arguments:           args,
		ReturnByValue:       new(true),
		AwaitPromise:        new(awaitPromise),
	}
	res, err := cdp.Call(ctx, tgt, runtime.CallFunctionOn, params)
	if err != nil {
		return zero, err
	}
	if res.ExceptionDetails != nil {
		releaseExceptionObject(ctx, tgt, res.ExceptionDetails, "")
		return zero, fmt.Errorf("%s", formatExceptionDetails(res.ExceptionDetails))
	}
	if res.Result == nil || len(res.Result.Value) == 0 {
		return zero, nil
	}
	var out T
	if err := json.Unmarshal(res.Result.Value, &out); err != nil {
		return zero, err
	}
	return out, nil
}

// validatePointerTarget validates target fields for pointer tools (click,
// hover, drag endpoints) accepting either viewport coordinates (x, y) OR an
// element target (ref / selector + optional frame).
func validatePointerTarget(ref, selector, frameID string, x, y *float64, requireOne bool) error {
	hasX := x != nil
	hasY := y != nil
	if hasX != hasY {
		return fmt.Errorf("x and y coordinates must both be provided together")
	}
	if hasX {
		if math.IsNaN(*x) || math.IsInf(*x, 0) || math.IsNaN(*y) || math.IsInf(*y, 0) {
			return fmt.Errorf("x and y coordinates must be finite numbers")
		}
		if ref != "" || selector != "" || frameID != "" {
			return fmt.Errorf("coordinates (x, y) are mutually exclusive with ref, selector, and frame")
		}
		return nil
	}
	if requireOne && ref == "" && selector == "" {
		return fmt.Errorf("one of ref, selector, or (x, y) coordinates is required")
	}
	return validateElementTarget(ref, selector, frameID, false)
}

// parseModifiers parses modifier key names ("ctrl", "shift", "alt", "meta")
// into a chromedp.Modifier bitmask.
func parseModifiers(mods []string) (chromedp.Modifier, error) {
	var mask chromedp.Modifier
	for _, m := range mods {
		switch m {
		case "ctrl":
			mask |= chromedp.ModifierCtrl
		case "shift":
			mask |= chromedp.ModifierShift
		case "alt":
			mask |= chromedp.ModifierAlt
		case "meta":
			mask |= chromedp.ModifierMeta
		default:
			return 0, fmt.Errorf("invalid modifiers: unknown modifier %q (must be ctrl, shift, alt, or meta)", m)
		}
	}
	return mask, nil
}

// contentQuadCenter returns the root-viewport center coordinates of a DOM node
// via DOM.getContentQuads without scrolling it. Works uniformly across main
// frame, Shadow DOM, and same-origin iframes.
func contentQuadCenter(ctx context.Context, tgt *chromedp.Target, bid cdp.BackendNodeID) (x, y float64, err error) {
	if bid == 0 {
		return 0, 0, fmt.Errorf("element has no backend DOM node ID")
	}
	res, err := cdp.Call(ctx, tgt, dom.GetContentQuads, dom.GetContentQuadsParams{
		BackendNodeID: bid,
	})
	if err != nil {
		return 0, 0, fmt.Errorf("getContentQuads: %w", err)
	}
	if len(res.Quads) == 0 {
		return 0, 0, fmt.Errorf("element has no content quads (not laid out)")
	}
	q := res.Quads[0]
	if len(q) < 8 {
		return 0, 0, fmt.Errorf("unexpected quad length: %d", len(q))
	}
	return (q[0] + q[2] + q[4] + q[6]) / 4, (q[1] + q[3] + q[5] + q[7]) / 4, nil
}

// scrollAndCenterBackendNode scrolls the DOM node into view (when needed) and
// returns its root-viewport center coordinates via DOM.getContentQuads. Works
// uniformly across main-frame elements, Shadow DOM nodes, and same-origin iframes.
func scrollAndCenterBackendNode(ctx context.Context, tgt *chromedp.Target, bid cdp.BackendNodeID) (x, y float64, err error) {
	if bid == 0 {
		return 0, 0, fmt.Errorf("element has no backend DOM node ID")
	}
	_, _ = cdp.Call(ctx, tgt, dom.ScrollIntoViewIfNeeded, dom.ScrollIntoViewIfNeededParams{
		BackendNodeID: bid,
	})
	return contentQuadCenter(ctx, tgt, bid)
}

// layoutBoxStateJS checks whether a DOM node is connected and has a layout box
// (matching chromedp's NodeVisible: true for visibility:hidden and zero-size
// inputs, false for display:none).
const layoutBoxStateJS = `function() {
	if (!this.isConnected) return "detached";
	if (this.offsetWidth || this.offsetHeight ||
		(typeof this.getClientRects === "function" && this.getClientRects().length > 0)) {
		return "visible";
	}
	return "hidden";
}`

// waitForElementLayoutBox polls until the resolved element has a layout box
// (visible or visibility:hidden, not display:none) within timeoutMs. Fails fast
// if the element detaches or its execution context is destroyed.
func waitForElementLayoutBox(
	tctx context.Context,
	tgt *chromedp.Target,
	el *resolvedElement,
	timeoutMs int,
) error {
	sctx, cancel := selectorContext(tctx, timeoutMs)
	defer cancel()

	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		state, err := callOnElement[string](sctx, tgt, el.ObjectID, layoutBoxStateJS, false)
		if err != nil {
			if tctx.Err() != nil {
				return tctx.Err()
			}
			if sctx.Err() != nil {
				return fmt.Errorf("element %s exists but is not visible (timed out waiting for layout box)", el.Label)
			}
			return fmt.Errorf("waiting for %s to become visible: %w", el.Label, err)
		}
		switch state {
		case "visible":
			return nil
		case "detached":
			return fmt.Errorf("element %s was detached from the DOM while waiting to become visible", el.Label)
		}
		if tctx.Err() != nil {
			return tctx.Err()
		}
		if sctx.Err() != nil {
			return fmt.Errorf("element %s exists but is not visible (timed out waiting for layout box)", el.Label)
		}
		select {
		case <-tctx.Done():
			return tctx.Err()
		case <-sctx.Done():
			return fmt.Errorf("element %s exists but is not visible (timed out waiting for layout box)", el.Label)
		case <-ticker.C:
		}
	}
}

// focusResolvedElement focuses an element via DOM.focus(BackendNodeID),
// propagating non-focusable / disabled errors instead of silently sending
// subsequent keystrokes to a previously focused element. Falls back to JS
// focus only when BackendNodeID is unavailable, verifying focus actually landed.
func focusResolvedElement(ctx context.Context, tgt *chromedp.Target, el *resolvedElement) error {
	if el.BackendNodeID > 0 {
		if _, err := cdp.Call(ctx, tgt, dom.Focus, dom.FocusParams{
			BackendNodeID: el.BackendNodeID,
		}); err != nil {
			return fmt.Errorf("focus %s: %w", el.Label, err)
		}
		return nil
	}
	_, err := callOnElement[any](ctx, tgt, el.ObjectID, `function() {
		if (!this.isConnected) throw new Error('element is no longer attached to the DOM');
		if (typeof this.matches === 'function' && this.matches(':disabled')) {
			throw new Error('element is disabled and cannot be focused');
		}
		if (typeof this.focus !== 'function') throw new Error('element is not focusable');
		this.focus();
		const root = this.getRootNode();
		if (!root || root.activeElement !== this) {
			throw new Error('element is not focusable');
		}
	}`, false)
	if err != nil {
		return fmt.Errorf("focus %s: %w", el.Label, err)
	}
	return nil
}

// runDialogInterruptible executes a CDP dispatch (such as MouseReleased or
// KeyEvent) that Chromium blocks when a synchronous page handler opens a JS
// modal dialog (alert/confirm/prompt). If a new dialog opens while fn waits
// for the renderer ACK, fn's child context is cancelled so the tool returns
// promptly with the dialog open instead of hanging until request timeout.
func runDialogInterruptible(ctx context.Context, dlg *collector.Dialog, fn func(context.Context) error) error {
	if dlg == nil {
		return fn(ctx)
	}
	openedCh := dlg.OpeningNotify()
	select {
	case <-openedCh:
		// A dialog was already open before dispatch; run without masking errors.
		return fn(ctx)
	default:
	}
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var dialogInterrupted atomic.Bool
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-openedCh:
			dialogInterrupted.Store(true)
			cancel()
		case <-done:
		case <-ctx.Done():
		}
	}()

	err := fn(callCtx)
	if err != nil && errors.Is(err, context.Canceled) && dialogInterrupted.Load() && ctx.Err() == nil {
		return nil
	}
	return err
}
