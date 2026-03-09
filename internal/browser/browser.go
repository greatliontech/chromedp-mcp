// Package browser manages Chrome browser instances — launching new ones
// or connecting to existing ones via remote debugging.
package browser

import (
	"context"
	"fmt"

	"github.com/chromedp/chromedp"

	"github.com/thegrumpylion/chromedp-mcp/internal/collector"
	"github.com/thegrumpylion/chromedp-mcp/internal/tab"
)

// DefaultDownloadBuffer is the default buffer size for tracking downloads.
const DefaultDownloadBuffer = 200

// Mode indicates how the browser was created.
type Mode int

const (
	// ModeLaunch means the server launched the Chrome process.
	ModeLaunch Mode = iota
	// ModeConnect means the server connected to an existing Chrome instance.
	ModeConnect
)

// Browser represents a single Chrome browser instance with its tabs.
type Browser struct {
	ID            string
	Mode          Mode
	allocCancel   context.CancelFunc
	browserCtx    context.Context
	browserCancel context.CancelFunc
	Tabs          *tab.Manager
	Downloads     *collector.Download
}

// LaunchOptions configures a launched browser.
type LaunchOptions struct {
	Headless    bool
	Width       int
	Height      int
	DownloadDir string
}

// DefaultLaunchOptions returns the default launch configuration.
func DefaultLaunchOptions() LaunchOptions {
	return LaunchOptions{
		Headless: true,
		Width:    1920,
		Height:   1080,
	}
}

// Launch starts a new Chrome browser process.
func Launch(parentCtx context.Context, id string, opts LaunchOptions) (*Browser, error) {
	allocOpts := chromedp.DefaultExecAllocatorOptions[:]
	if opts.Headless {
		allocOpts = append(allocOpts, chromedp.Headless)
	} else {
		// Remove the default headless flag.
		filtered := make([]chromedp.ExecAllocatorOption, 0, len(allocOpts))
		for _, o := range allocOpts {
			filtered = append(filtered, o)
		}
		allocOpts = append(filtered, chromedp.Flag("headless", false))
	}
	if opts.Width > 0 && opts.Height > 0 {
		allocOpts = append(allocOpts, chromedp.WindowSize(opts.Width, opts.Height))
	}

	allocCtx, allocCancel := chromedp.NewExecAllocator(parentCtx, allocOpts...)
	browserCtx, browserCancel := chromedp.NewContext(allocCtx)

	// Force-start the browser by running a no-op. chromedp creates the
	// browser process lazily on the first Run call.
	if err := chromedp.Run(browserCtx); err != nil {
		browserCancel()
		allocCancel()
		return nil, fmt.Errorf("launch browser: %w", err)
	}

	dl := collector.NewDownload(DefaultDownloadBuffer, opts.DownloadDir)

	return &Browser{
		ID:            id,
		Mode:          ModeLaunch,
		allocCancel:   allocCancel,
		browserCtx:    browserCtx,
		browserCancel: browserCancel,
		Tabs: tab.NewManager(browserCtx, &tab.TabOptions{
			Downloads:   dl,
			DownloadDir: opts.DownloadDir,
		}),
		Downloads: dl,
	}, nil
}

// ConnectOptions configures a connected browser.
type ConnectOptions struct {
	DownloadDir string
}

// Connect connects to an existing Chrome browser via its remote debugging URL.
func Connect(parentCtx context.Context, id string, url string, opts ConnectOptions) (*Browser, error) {
	allocCtx, allocCancel := chromedp.NewRemoteAllocator(parentCtx, url)
	browserCtx, browserCancel := chromedp.NewContext(allocCtx)

	// Force the connection.
	if err := chromedp.Run(browserCtx); err != nil {
		browserCancel()
		allocCancel()
		return nil, fmt.Errorf("connect to browser at %s: %w", url, err)
	}

	dl := collector.NewDownload(DefaultDownloadBuffer, opts.DownloadDir)

	return &Browser{
		ID:            id,
		Mode:          ModeConnect,
		allocCancel:   allocCancel,
		browserCtx:    browserCtx,
		browserCancel: browserCancel,
		Tabs: tab.NewManager(browserCtx, &tab.TabOptions{
			Downloads:   dl,
			DownloadDir: opts.DownloadDir,
		}),
		Downloads: dl,
	}, nil
}

// Context returns the browser's root chromedp context.
func (b *Browser) Context() context.Context {
	return b.browserCtx
}

// Alive returns true if the browser's context is still active (Chrome
// process hasn't been killed or disconnected).
func (b *Browser) Alive() bool {
	return b.browserCtx.Err() == nil
}

// Close shuts down the browser. In launch mode, this kills the Chrome
// process. In connect mode, it disconnects.
func (b *Browser) Close() {
	b.Tabs.CloseAll()
	b.browserCancel()
	b.allocCancel()
}
