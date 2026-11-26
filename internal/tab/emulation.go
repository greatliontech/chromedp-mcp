package tab

import (
	"context"
	"fmt"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/css"
	"github.com/chromedp/cdproto/dom"
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
				t.cssForced = make(map[cdp.BackendNodeID]struct{})
			}
			t.cssForced[backendNodeID] = struct{}{}
		}
	}
	remaining := len(t.cssForced)
	t.cssStateMu.Unlock()

	if remaining == 0 && t.cssEnabled {
		t.disableCSSLocked()
	}
	return forced, nil
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
