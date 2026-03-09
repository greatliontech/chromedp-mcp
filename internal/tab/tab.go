// Package tab manages browser tabs and their associated event collectors.
package tab

import (
	"context"
	"fmt"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/performancetimeline"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"

	"github.com/thegrumpylion/chromedp-mcp/internal/collector"
)

// DefaultConsoleBuffer is the default console log buffer size.
const DefaultConsoleBuffer = 1000

// DefaultErrorBuffer is the default JS error buffer size.
const DefaultErrorBuffer = 500

// DefaultNetworkBuffer is the default network request buffer size.
const DefaultNetworkBuffer = 1000

// DefaultPerformanceBuffer is the default performance timeline buffer size.
const DefaultPerformanceBuffer = 200

// Tab represents a single browser tab with its event collectors.
type Tab struct {
	ID          string
	ctx         context.Context
	cancel      context.CancelFunc
	Console     *collector.Console
	JSErrors    *collector.JSErrors
	Network     *collector.Network
	Performance *collector.Performance
}

// New creates a new tab from a parent browser context. It creates a new
// Chrome target, starts event listeners, and initializes collectors.
func New(parentCtx context.Context, id string) (*Tab, error) {
	ctx, cancel := chromedp.NewContext(parentCtx)

	t := &Tab{
		ID:          id,
		ctx:         ctx,
		cancel:      cancel,
		Console:     collector.NewConsole(DefaultConsoleBuffer),
		JSErrors:    collector.NewJSErrors(DefaultErrorBuffer),
		Network:     collector.NewNetwork(DefaultNetworkBuffer),
		Performance: collector.NewPerformance(DefaultPerformanceBuffer, 50),
	}

	// Register CDP event listeners. These are called synchronously by
	// chromedp's event dispatcher, so they must not block.
	chromedp.ListenTarget(ctx, func(ev any) {
		switch ev := ev.(type) {
		case *runtime.EventConsoleAPICalled:
			t.Console.Handle(ev)
		case *runtime.EventExceptionThrown:
			t.JSErrors.Handle(ev)
		case *network.EventRequestWillBeSent:
			t.Network.HandleRequestWillBeSent(ev)
		case *network.EventResponseReceived:
			t.Network.HandleResponseReceived(ev)
		case *network.EventLoadingFinished:
			t.Network.HandleLoadingFinished(ev)
		case *network.EventLoadingFailed:
			t.Network.HandleLoadingFailed(ev)
		case *performancetimeline.EventTimelineEventAdded:
			t.Performance.HandleTimelineEvent(ev)
		}
	})

	// Enable performance timeline events. We need to run a chromedp action
	// to trigger the target allocation (chromedp creates the target lazily
	// on the first Run call).
	err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		return performancetimeline.Enable([]string{
			"largest-contentful-paint",
			"layout-shift",
		}).Do(ctx)
	}))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("tab init: %w", err)
	}

	return t, nil
}

// Context returns the tab's chromedp context. Tool handlers use this to
// run chromedp actions against this tab.
func (t *Tab) Context() context.Context {
	return t.ctx
}

// Close closes the tab by cancelling its context, which closes the
// underlying Chrome target.
func (t *Tab) Close() {
	t.cancel()
}

// URL returns the current URL of the tab.
func (t *Tab) URL() (string, error) {
	var url string
	err := chromedp.Run(t.ctx, chromedp.Location(&url))
	if err != nil {
		return "", err
	}
	return url, nil
}

// Title returns the current title of the tab.
func (t *Tab) Title() (string, error) {
	var title string
	err := chromedp.Run(t.ctx, chromedp.Title(&title))
	if err != nil {
		return "", err
	}
	return title, nil
}
