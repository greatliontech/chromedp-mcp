package tab

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/performance"
	"github.com/chromedp/chromedp"
)

// GetPerformanceMetrics enables the CDP Performance domain transiently, reads
// runtime metrics, and disables the domain before returning. Calls on the same
// tab are serialized so concurrent readers never disable the domain out from
// under each other.
func (t *Tab) GetPerformanceMetrics(ctx context.Context) ([]*performance.Metric, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	const dialogOpenMsg = "cannot read performance metrics while a JavaScript dialog is open — use dialog tool first"
	if t.Dialog != nil && t.Dialog.Open() {
		return nil, errors.New(dialogOpenMsg)
	}

	t.perfOpMu.Lock()
	defer t.perfOpMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var openedCh <-chan struct{}
	if t.Dialog != nil {
		openedCh = t.Dialog.OpeningNotify()
		select {
		case <-openedCh:
			return nil, errors.New(dialogOpenMsg)
		default:
		}
	}

	callCtx, cancel := t.LinkContext(ctx, 0)
	defer cancel()

	var dialogInterrupted atomic.Bool
	if openedCh != nil {
		done := make(chan struct{})
		defer close(done)
		go func() {
			select {
			case <-openedCh:
				dialogInterrupted.Store(true)
				cancel()
			case <-done:
			case <-callCtx.Done():
			}
		}()
	}

	var metrics []*performance.Metric
	err := chromedp.Do(callCtx, chromedp.Func(func(runCtx context.Context, tgt *chromedp.Target) error {
		enabled := false
		defer func() {
			// Skip disable when the tab is closing or when Enable failed with a
			// non-cancel CDP error (nothing was enabled). When Enable returned
			// due to runCtx cancellation (caller cancel or dialog interrupt),
			// still issue Disable unless a dialog is currently blocking the
			// renderer — avoiding a 2s stall against a paused main thread
			// (the next GetPerformanceMetrics call disables cleanly on exit).
			if t.ctx.Err() != nil || (!enabled && runCtx.Err() == nil) {
				return
			}
			if t.Dialog != nil && t.Dialog.Open() {
				return
			}
			disableCtx, disableCancel := context.WithTimeout(t.ctx, defaultTabInfoTimeout)
			defer disableCancel()
			_, _ = cdp.Call(disableCtx, tgt, performance.Disable, cdp.Empty{})
		}()
		if _, err := cdp.Call(runCtx, tgt, performance.Enable, performance.EnableParams{}); err != nil {
			return err
		}
		enabled = true
		res, err := cdp.Call(runCtx, tgt, performance.GetMetrics, cdp.Empty{})
		if err != nil {
			return err
		}
		metrics = res.Metrics
		return nil
	}))
	if err != nil {
		if dialogInterrupted.Load() && ctx.Err() == nil && t.ctx.Err() == nil {
			return nil, errors.New(dialogOpenMsg)
		}
		return nil, err
	}
	return metrics, nil
}
