package tab

import (
	"context"
	"fmt"
	"slices"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/css"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

func (t *Tab) resetForcedPseudoStates() {
	t.cssStateMu.Lock()
	t.cssDocGen++
	hadForced := len(t.cssForced) > 0
	clear(t.cssForced)
	t.cssStateMu.Unlock()
	if hadForced {
		// Disable CSS asynchronously off WithListener's event-reader goroutine
		// so cross-document navigations never leave CSS enabled with zero forced nodes.
		go t.disableCSSIfIdle()
	}
}

func (t *Tab) disableCSSIfIdle() {
	t.cssOpMu.Lock()
	defer t.cssOpMu.Unlock()
	if t.cssEnabled && t.forcedPseudoCount() == 0 {
		t.disableCSSLocked()
	}
}

func (t *Tab) snapshotForcedPseudoStates() (map[cdp.BackendNodeID][]string, uint64) {
	t.cssStateMu.Lock()
	defer t.cssStateMu.Unlock()
	if len(t.cssForced) == 0 {
		return nil, t.cssDocGen
	}
	cp := make(map[cdp.BackendNodeID][]string, len(t.cssForced))
	for id, st := range t.cssForced {
		cp[id] = slices.Clone(st)
	}
	return cp, t.cssDocGen
}

func (t *Tab) forcedPseudoCount() int {
	t.cssStateMu.Lock()
	defer t.cssStateMu.Unlock()
	return len(t.cssForced)
}

// ForcePseudoState applies (or clears when states is empty) forced CSS
// pseudo-classes on the element identified by backendNodeID. Keeps the CSS
// domain enabled while any element has forced states and disables it
// automatically once all forced states on the tab are cleared.
func (t *Tab) ForcePseudoState(ctx context.Context, backendNodeID cdp.BackendNodeID, states []string) ([]string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if backendNodeID == 0 {
		return nil, fmt.Errorf("invalid backend node ID")
	}

	t.cssOpMu.Lock()
	defer t.cssOpMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	t.cssStateMu.Lock()
	gen := t.cssDocGen
	t.cssStateMu.Unlock()

	forced := make([]string, len(states))
	copy(forced, states)

	callCtx, cancel := t.LinkContext(ctx, 0)
	defer cancel()

	wasEnabled := t.cssEnabled
	err := chromedp.Do(callCtx, chromedp.Func(func(ctx context.Context, tgt *chromedp.Target) error {
		if len(forced) > 0 && !t.cssEnabled {
			if _, err := cdp.Call(ctx, tgt, dom.Enable, dom.EnableParams{}); err != nil {
				return fmt.Errorf("enable DOM domain: %w", err)
			}
			if _, err := cdp.Call(ctx, tgt, css.Enable, cdp.Empty{}); err != nil {
				return fmt.Errorf("enable CSS domain: %w", err)
			}
			t.cssEnabled = true
		}

		if t.cssEnabled {
			pushRes, err := cdp.Call(ctx, tgt, dom.PushNodesByBackendIDsToFrontend, dom.PushNodesByBackendIDsToFrontendParams{
				BackendNodeIDs: []cdp.BackendNodeID{backendNodeID},
			})
			if err != nil {
				return fmt.Errorf("resolve DOM node: %w", err)
			}
			if len(pushRes.NodeIDs) == 0 || pushRes.NodeIDs[0] == 0 {
				return fmt.Errorf("element is no longer attached to the document")
			}
			if _, err := cdp.Call(ctx, tgt, css.ForcePseudoState, css.ForcePseudoStateParams{
				NodeID:              pushRes.NodeIDs[0],
				ForcedPseudoClasses: forced,
			}); err != nil {
				return fmt.Errorf("forcePseudoState: %w", err)
			}
		}
		return nil
	}))
	if err != nil {
		if (!wasEnabled && (t.cssEnabled || (t.ctx.Err() == nil && ctx.Err() != nil))) && t.forcedPseudoCount() == 0 {
			t.disableCSSLocked()
		}
		return nil, err
	}

	t.cssStateMu.Lock()
	if t.cssDocGen == gen {
		if len(forced) == 0 {
			delete(t.cssForced, backendNodeID)
		} else {
			if t.cssForced == nil {
				t.cssForced = make(map[cdp.BackendNodeID][]string)
			}
			t.cssForced[backendNodeID] = slices.Clone(forced)
		}
	}
	remaining := len(t.cssForced)
	t.cssStateMu.Unlock()

	if remaining == 0 && t.cssEnabled {
		t.disableCSSLocked()
	}
	return forced, nil
}

// WithCSSCoverage runs a CSS coverage collection callback while coordinating
// CSS.enable/CSS.disable with any active forced pseudo-states.
func (t *Tab) WithCSSCoverage(ctx context.Context, fn func(ctx context.Context, tgt *chromedp.Target) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	t.cssOpMu.Lock()
	defer t.cssOpMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}

	if t.cssEnabled {
		t.disableCSSLocked()
	}

	callCtx, cancel := t.LinkContext(ctx, 0)
	defer cancel()
	err := chromedp.Do(callCtx, chromedp.Func(fn))
	if err != nil && t.ctx.Err() == nil && ctx.Err() != nil {
		// Caller cancelled mid-call (potentially mid-CSS.enable before the
		// callback's defer CSS.disable registered); disable best-effort so CSS
		// cannot stay stuck enabled and break subsequent styleSheetAdded events.
		t.disableCSSLocked()
	} else {
		t.cssEnabled = false
	}

	t.restoreForcedPseudoStatesLocked()
	return err
}

// disableCSSLocked disables the CSS domain under a short bounded context
// derived from t.ctx. Caller must hold t.cssOpMu.
func (t *Tab) disableCSSLocked() {
	if t.ctx.Err() != nil {
		t.cssEnabled = false
		return
	}
	disableCtx, cancel := context.WithTimeout(t.ctx, defaultTabInfoTimeout)
	defer cancel()
	if _, err := chromedp.Call(disableCtx, css.Disable, cdp.Empty{}); err == nil || t.ctx.Err() != nil {
		t.cssEnabled = false
	}
}

func (t *Tab) restoreForcedPseudoStatesLocked() {
	if t.ctx.Err() != nil {
		return
	}
	forced, snapGen := t.snapshotForcedPseudoStates()
	if len(forced) == 0 {
		return
	}

	restoreCtx, cancel := context.WithTimeout(t.ctx, defaultTabInfoTimeout)
	defer cancel()

	ids := make([]cdp.BackendNodeID, 0, len(forced))
	for id := range forced {
		ids = append(ids, id)
	}

	applied := 0
	var stale []cdp.BackendNodeID
	err := chromedp.Do(restoreCtx, chromedp.Func(func(ctx context.Context, tgt *chromedp.Target) error {
		if _, err := cdp.Call(ctx, tgt, dom.Enable, dom.EnableParams{}); err != nil {
			return err
		}
		if _, err := cdp.Call(ctx, tgt, css.Enable, cdp.Empty{}); err != nil {
			return err
		}
		t.cssEnabled = true
		pushRes, err := cdp.Call(ctx, tgt, dom.PushNodesByBackendIDsToFrontend, dom.PushNodesByBackendIDsToFrontendParams{
			BackendNodeIDs: ids,
		})
		if err != nil {
			return err
		}
		dialogOpen := t.Dialog != nil && t.Dialog.Open()
		for i, bid := range ids {
			if i >= len(pushRes.NodeIDs) || pushRes.NodeIDs[i] == 0 {
				stale = append(stale, bid)
				continue
			}
			if !dialogOpen {
				connected, connErr := isBackendNodeConnected(ctx, tgt, bid)
				if connErr != nil {
					// Transient timeout/cancel while probing connectedness:
					// do not falsely prune live forced pseudo-states.
					return connErr
				}
				if !connected {
					stale = append(stale, bid)
					continue
				}
			}
			if _, err := cdp.Call(ctx, tgt, css.ForcePseudoState, css.ForcePseudoStateParams{
				NodeID:              pushRes.NodeIDs[i],
				ForcedPseudoClasses: forced[bid],
			}); err == nil {
				applied++
			}
		}
		return nil
	}))
	if len(stale) > 0 {
		t.cssStateMu.Lock()
		if t.cssDocGen == snapGen {
			for _, bid := range stale {
				delete(t.cssForced, bid)
			}
		}
		t.cssStateMu.Unlock()
	}
	if err != nil || applied == 0 {
		if t.cssEnabled {
			t.disableCSSLocked()
		}
	}
}

func isBackendNodeConnected(ctx context.Context, tgt *chromedp.Target, bid cdp.BackendNodeID) (bool, error) {
	res, err := cdp.Call(ctx, tgt, dom.ResolveNode, dom.ResolveNodeParams{
		BackendNodeID: bid,
	})
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, nil
	}
	if res.Object == nil || res.Object.ObjectID == "" {
		return false, nil
	}
	oid := res.Object.ObjectID
	defer func() {
		cleanCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), defaultTabInfoTimeout)
		defer cancel()
		_, _ = cdp.Call(cleanCtx, tgt, runtime.ReleaseObject, runtime.ReleaseObjectParams{
			ObjectID: oid,
		})
	}()
	callRes, err := cdp.Call(ctx, tgt, runtime.CallFunctionOn, runtime.CallFunctionOnParams{
		ObjectID:            oid,
		FunctionDeclaration: `function() { return Boolean(this && this.isConnected); }`,
		ReturnByValue:       new(true),
		Silent:              new(true),
	})
	if err != nil {
		return false, err
	}
	if callRes.ExceptionDetails != nil || callRes.Result == nil {
		return false, nil
	}
	return string(callRes.Result.Value) == "true", nil
}
