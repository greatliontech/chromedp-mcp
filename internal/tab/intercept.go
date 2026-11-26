package tab

import (
	"context"
	"fmt"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"

	"github.com/greatliontech/chromedp-mcp/internal/collector"
)

// AddInterceptRule enables CDP Fetch interception lazily on the first rule and
// registers rule on this tab. Returns the assigned rule ID.
func (t *Tab) AddInterceptRule(ctx context.Context, rule collector.InterceptRule) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	t.interceptOpMu.Lock()
	defer t.interceptOpMu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}

	if !t.fetchEnabled {
		callCtx, cancel := t.LinkContext(ctx, 0)
		_, err := chromedp.Call(callCtx, fetch.Enable, fetch.EnableParams{
			Patterns: []*fetch.RequestPattern{{URLPattern: "*"}},
		})
		cancel()
		if err != nil {
			// If caller ctx cancelled while Fetch.enable was in flight, Chrome
			// may have already enabled Fetch before our reply arrived; disable
			// best-effort so Fetch cannot stay stuck enabled with zero rules.
			if t.ctx.Err() == nil && ctx.Err() != nil {
				t.disableFetchLocked()
			}
			return "", fmt.Errorf("enable fetch interception: %w", err)
		}
		t.fetchEnabled = true
	}

	return t.Intercept.Add(rule), nil
}

// RemoveInterceptRule deletes a rule by ID and disables CDP Fetch interception
// when the last rule is removed.
func (t *Tab) RemoveInterceptRule(ctx context.Context, ruleID string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	t.interceptOpMu.Lock()
	defer t.interceptOpMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}

	remaining, err := t.Intercept.Remove(ruleID)
	if err != nil {
		return err
	}
	if remaining == 0 && t.fetchEnabled {
		t.disableFetchLocked()
	}
	return nil
}

// ClearInterceptRules removes all rules on this tab and disables CDP Fetch
// interception if enabled. Returns the number of rules removed.
func (t *Tab) ClearInterceptRules(ctx context.Context) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	t.interceptOpMu.Lock()
	defer t.interceptOpMu.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	removed := t.Intercept.Clear()
	if t.fetchEnabled {
		t.disableFetchLocked()
	}
	return removed, nil
}

// ListInterceptRules returns summaries of active interception rules in order.
func (t *Tab) ListInterceptRules() []collector.InterceptRuleInfo {
	return t.Intercept.List()
}

// disableFetchLocked calls Fetch.disable under a short bounded context derived
// from t.ctx so caller cancellation cannot leave Fetch stuck enabled with zero
// active rules. Caller must hold t.interceptOpMu.
func (t *Tab) disableFetchLocked() {
	if t.ctx.Err() != nil {
		t.fetchEnabled = false
		return
	}
	disableCtx, cancel := context.WithTimeout(t.ctx, defaultTabInfoTimeout)
	defer cancel()
	if _, err := chromedp.Call(disableCtx, fetch.Disable, cdp.Empty{}); err == nil || t.ctx.Err() != nil {
		t.fetchEnabled = false
	}
}

func (t *Tab) handleRequestPaused(ev *fetch.EventRequestPaused, rule collector.InterceptRule, matched bool) {
	callCtx, cancel := context.WithTimeout(t.ctx, defaultInterceptDispatchTimeout)
	defer cancel()

	if !matched {
		_, _ = chromedp.Call(callCtx, fetch.ContinueRequest, fetch.ContinueRequestParams{
			RequestID: ev.RequestID,
		})
		return
	}

	var err error
	switch rule.Action {
	case collector.InterceptActionFulfill:
		status := rule.Status
		if status == 0 {
			status = 200
		}
		_, err = chromedp.Call(callCtx, fetch.FulfillRequest, fetch.FulfillRequestParams{
			RequestID:       ev.RequestID,
			ResponseCode:    int64(status),
			ResponseHeaders: collector.HeaderEntriesFromMap(rule.Headers),
			Body:            rule.Body,
		})
	case collector.InterceptActionModify:
		params := fetch.ContinueRequestParams{
			RequestID: ev.RequestID,
			URL:       rule.URL,
			Method:    rule.OverrideMethod,
			PostData:  rule.PostData,
		}
		if len(rule.Headers) > 0 {
			if ev.Request != nil {
				params.Headers = collector.MergeRequestHeaders(ev.Request.Headers, rule.Headers)
			} else {
				params.Headers = collector.HeaderEntriesFromMap(rule.Headers)
			}
		}
		_, err = chromedp.Call(callCtx, fetch.ContinueRequest, params)
	case collector.InterceptActionFail:
		reason := rule.ErrorReason
		if reason == "" {
			reason = network.ErrorReasonFailed
		}
		_, err = chromedp.Call(callCtx, fetch.FailRequest, fetch.FailRequestParams{
			RequestID:   ev.RequestID,
			ErrorReason: reason,
		})
	default:
		_, _ = chromedp.Call(callCtx, fetch.ContinueRequest, fetch.ContinueRequestParams{
			RequestID: ev.RequestID,
		})
		return
	}

	// Best-effort fallback continue if the matched action failed while the tab
	// is still alive so a paused request never stalls the browser indefinitely.
	if err != nil && callCtx.Err() == nil {
		_, _ = chromedp.Call(callCtx, fetch.ContinueRequest, fetch.ContinueRequestParams{
			RequestID: ev.RequestID,
		})
	}
}
