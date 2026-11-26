// Package browser manages Chrome browser instances — launching new ones
// or connecting to existing ones via remote debugging.
package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	cdpbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/remote"

	"github.com/greatliontech/chromedp-mcp/internal/collector"
	"github.com/greatliontech/chromedp-mcp/internal/tab"
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
	closeOnce     sync.Once
	Tabs          *tab.Manager
	Downloads     *collector.Download
}

// LaunchOptions configures a launched browser.
type LaunchOptions struct {
	Headless    bool
	Width       int
	Height      int
	DownloadDir string
	// UserDataDir is the Chrome user data directory (e.g.
	// ~/.config/chromium). When set together with ProfileDir, the
	// browser is launched with an existing user profile instead of a
	// temporary ephemeral one.
	UserDataDir string
	// ProfileDir is the profile directory name within UserDataDir
	// (e.g. "Default", "Profile 2"). Must be set together with
	// UserDataDir.
	ProfileDir string
}

// DefaultLaunchOptions returns the default launch configuration.
func DefaultLaunchOptions() LaunchOptions {
	return LaunchOptions{
		Headless: true,
		Width:    1920,
		Height:   1080,
	}
}

func launchAllocatorOptions(opts LaunchOptions) []chromedp.ExecAllocatorOption {
	allocOpts := slices.Clone(chromedp.DefaultExecAllocatorOptions[:])
	if opts.Headless {
		// Disable hardware GPU compositing in headless mode (with SwiftShader
		// fallback for Chromium 139+) so Page.captureScreenshot stays reliable
		// on GPU-less Linux VMs and containers.
		allocOpts = append(allocOpts, chromedp.Headless, chromedp.DisableGPU)
	} else {
		// DefaultExecAllocatorOptions includes Headless which sets
		// the "headless" flag to true. Override it to false. Since
		// flags are stored in a map, the last write wins.
		allocOpts = append(allocOpts, chromedp.Flag("headless", false))
	}
	if opts.Width > 0 && opts.Height > 0 {
		allocOpts = append(allocOpts, chromedp.WindowSize(opts.Width, opts.Height))
	}
	if opts.UserDataDir != "" && opts.ProfileDir != "" {
		allocOpts = append(allocOpts,
			chromedp.UserDataDir(opts.UserDataDir),
			chromedp.Flag("profile-directory", opts.ProfileDir),
		)
	}
	return allocOpts
}

// Launch starts a new Chrome browser process. startupCtx bounds browser
// startup (e.g. MCP request cancellation) without tying the browser's
// post-startup lifetime to startupCtx; nil defaults to context.Background().
func Launch(parentCtx, startupCtx context.Context, opts LaunchOptions) (*Browser, error) {
	if startupCtx == nil {
		startupCtx = context.Background()
	}
	if err := startupCtx.Err(); err != nil {
		return nil, fmt.Errorf("launch browser: %w", err)
	}
	if (opts.UserDataDir != "") != (opts.ProfileDir != "") {
		return nil, fmt.Errorf("launch browser: UserDataDir and ProfileDir must either both be set or both be empty")
	}

	allocCtx, allocCancel := chromedp.NewExecAllocator(parentCtx, launchAllocatorOptions(opts)...)
	browserCtx, browserCancel := chromedp.NewContext(allocCtx)

	// Start Chrome at the browser CDP level via chromedp.Targets (NOT
	// chromedp.Do) so browserCtx does not bind Chrome's initial default tab as
	// an untracked target; instead we discover it and register it in b.Tabs.
	targets, err := runBrowserStartup(startupCtx, allocCancel, browserCancel, func() ([]*target.Info, error) {
		return waitForInitialPageTargets(browserCtx, initialTargetWaitTimeout)
	})
	if err != nil {
		return nil, fmt.Errorf("launch browser: %w", err)
	}

	b, err := newBrowser(startupCtx, ModeLaunch, allocCancel, browserCtx, browserCancel, opts.DownloadDir)
	if err != nil {
		browserCancel()
		allocCancel()
		return nil, fmt.Errorf("launch browser: %w", err)
	}

	if err := adoptPageTargets(startupCtx, b, targets); err != nil {
		b.Close()
		return nil, fmt.Errorf("launch browser: %w", err)
	}
	// Guarantee a launched browser starts with at least one active tab even if
	// Chrome started with zero attachable page targets.
	if b.Tabs.Active() == nil {
		if _, err := b.Tabs.NewTab(startupCtx); err != nil {
			b.Close()
			return nil, fmt.Errorf("launch browser: open initial tab: %w", err)
		}
	}
	if err := b.enableTargetDiscovery(startupCtx); err != nil {
		b.Close()
		return nil, fmt.Errorf("launch browser: %w", err)
	}
	return b, nil
}

// ConnectOptions configures a connected browser.
type ConnectOptions struct {
	DownloadDir string
}

// Connect connects to an existing Chrome browser via its remote debugging URL.
// startupCtx bounds connection setup without tying the browser's lifetime to it.
func Connect(parentCtx, startupCtx context.Context, rawURL string, opts ConnectOptions) (*Browser, error) {
	if startupCtx == nil {
		startupCtx = context.Background()
	}
	if err := startupCtx.Err(); err != nil {
		return nil, fmt.Errorf("connect to browser at %s: %w", rawURL, err)
	}

	wsURL, err := resolveDebuggerURL(startupCtx, rawURL)
	if err != nil {
		return nil, fmt.Errorf("connect to browser at %s: %w", rawURL, err)
	}

	allocCtx, rawAllocCancel := remote.NewAllocator(parentCtx, wsURL, remote.NoModifyURL)
	// remote.NewAllocator's cancel does not call Allocator.Wait (unlike
	// NewExecAllocator). Only call Wait AFTER runBrowserStartup returns (and
	// in Close) — never inside runBrowserStartup's AfterFunc, which would race
	// Allocator.Wait() against remote.Allocator.Allocate()'s wg.Go on early cancel.
	waitAlloc := func() {
		if c := chromedp.FromContext(allocCtx); c != nil && c.Allocator != nil {
			c.Allocator.Wait()
		}
	}
	browserCtx, browserCancel := chromedp.NewContext(allocCtx)

	// Connect at the browser CDP level via chromedp.Targets (NOT chromedp.Do)
	// so connecting never spawns an untracked blank tab in the user's Chrome,
	// while discovering all pre-existing page targets in the same round trip.
	targets, err := runBrowserStartup(startupCtx, rawAllocCancel, browserCancel, func() ([]*target.Info, error) {
		return chromedp.Targets(browserCtx)
	})
	if err != nil {
		waitAlloc()
		return nil, fmt.Errorf("connect to browser at %s: %w", rawURL, err)
	}

	allocCancel := func() {
		rawAllocCancel()
		waitAlloc()
	}
	b, err := newBrowser(startupCtx, ModeConnect, allocCancel, browserCtx, browserCancel, opts.DownloadDir)
	if err != nil {
		browserCancel()
		allocCancel()
		return nil, fmt.Errorf("connect to browser at %s: %w", rawURL, err)
	}

	// Adopt pre-existing page targets (excluding prerender targets) with
	// WithDetachOnCancel so they are immediately inspectable via tab_list and
	// preserved on browser_close.
	if err := adoptPageTargets(startupCtx, b, targets); err != nil {
		b.Close()
		return nil, fmt.Errorf("connect to browser at %s: %w", rawURL, err)
	}
	if err := b.enableTargetDiscovery(startupCtx); err != nil {
		b.Close()
		return nil, fmt.Errorf("connect to browser at %s: %w", rawURL, err)
	}
	return b, nil
}

// initialTargetWaitTimeout bounds how long Launch polls Target.getTargets
// after Chrome finishes starting while waiting for its initial default page
// target to register (chromedp.Targets notes the initial target may lag
// briefly behind browser startup).
const initialTargetWaitTimeout = 1 * time.Second

// adoptTargetTimeout bounds per-target attachment during Launch/Connect so a
// single wedged or dialog-paused tab does not hang startup.
const adoptTargetTimeout = 3 * time.Second

func waitForInitialPageTargets(browserCtx context.Context, timeout time.Duration) ([]*target.Info, error) {
	// First call triggers ExecAllocator.Allocate (spawning Chrome + CDP
	// handshake); start the target-lag poll deadline only AFTER Chrome is up
	// so slow cold starts never exhaust the retry window on iteration 1.
	targets, err := chromedp.Targets(browserCtx)
	if err != nil {
		return nil, err
	}
	for _, info := range targets {
		if isAttachablePageTarget(info) {
			return targets, nil
		}
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-timer.C:
		case <-browserCtx.Done():
			timer.Stop()
			return nil, browserCtx.Err()
		}
		targets, err = chromedp.Targets(browserCtx)
		if err != nil {
			return nil, err
		}
		for _, info := range targets {
			if isAttachablePageTarget(info) {
				return targets, nil
			}
		}
	}
	return targets, nil
}

func isAttachablePageTarget(info *target.Info) bool {
	return info != nil && info.Type == "page" && info.Subtype == "" && info.TargetID != ""
}

// adoptPageTargets attaches each pre-existing page target (excluding prerender
// targets) into b.Tabs via AttachTarget (WithDetachOnCancel) under a bounded
// per-target timeout so a wedged or dialog-paused tab is detached and skipped
// rather than destroyed or hanging startup. Explicit tab_close closes attached
// targets via Tab.CloseTarget.
func adoptPageTargets(startupCtx context.Context, b *Browser, targets []*target.Info) error {
	for _, info := range targets {
		if !isAttachablePageTarget(info) {
			continue
		}
		attachCtx, attachCancel := context.WithTimeout(startupCtx, adoptTargetTimeout)
		_, err := b.Tabs.AttachTarget(attachCtx, info.TargetID)
		attachCancel()
		if err != nil && startupCtx.Err() != nil {
			return context.Cause(startupCtx)
		}
	}
	return nil
}

// runBrowserStartup runs fn to allocate/connect browserCtx while bounding
// startup by startupCtx without tying post-startup lifetime to it.
//
// chromedp's browserCancel receives from a 1-buffered allocated channel that is
// only closed after Chrome starts; invoking browserCancel twice on an aborted
// startup deadlocks. Wrapping cleanup in sync.OnceFunc guarantees at-most-once
// cancellation and waits for any in-flight AfterFunc callback before returning.
func runBrowserStartup[T any](
	startupCtx context.Context,
	allocCancel context.CancelFunc,
	browserCancel context.CancelFunc,
	fn func() (T, error),
) (T, error) {
	cleanup := sync.OnceFunc(func() {
		browserCancel()
		allocCancel()
	})
	stopStartup := context.AfterFunc(startupCtx, cleanup)
	res, err := fn()
	stopped := stopStartup()
	if err != nil || !stopped {
		cleanup()
		if err == nil {
			err = context.Cause(startupCtx)
		}
		var zero T
		return zero, err
	}
	return res, nil
}

func newBrowser(
	startupCtx context.Context,
	mode Mode,
	allocCancel context.CancelFunc,
	browserCtx context.Context,
	browserCancel context.CancelFunc,
	downloadDir string,
) (*Browser, error) {
	dl := collector.NewDownload(DefaultDownloadBuffer, downloadDir)
	b := &Browser{
		Mode:          mode,
		allocCancel:   allocCancel,
		browserCtx:    browserCtx,
		browserCancel: browserCancel,
		Tabs:          tab.NewManager(browserCtx),
		Downloads:     dl,
	}

	// Register a single browser-level CDP listener for download events (when
	// enabled) and popup / target="_blank" page targets opened by existing pages.
	if err := chromedp.ListenBrowser(browserCtx, func(ev any) {
		switch ev := ev.(type) {
		case *cdpbrowser.EventDownloadWillBegin:
			if downloadDir != "" {
				dl.HandleDownloadWillBegin(ev)
			}
		case *cdpbrowser.EventDownloadProgress:
			if downloadDir != "" {
				dl.HandleDownloadProgress(ev)
			}
		case *target.EventTargetCreated:
			if isAttachablePageTarget(ev.TargetInfo) && ev.TargetInfo.OpenerID != "" && !b.Tabs.HasTarget(ev.TargetInfo.TargetID) {
				go b.adoptPopupTarget(ev.TargetInfo.TargetID)
			}
		}
	}); err != nil {
		return nil, fmt.Errorf("listen browser events: %w", err)
	}

	if downloadDir != "" {
		if err := os.MkdirAll(downloadDir, 0o755); err != nil {
			return nil, fmt.Errorf("create download dir %q: %w", downloadDir, err)
		}
		if err := callBrowserBounded(startupCtx, browserCtx, func(ctx context.Context) error {
			_, err := chromedp.CallBrowser(ctx, cdpbrowser.SetDownloadBehavior, cdpbrowser.SetDownloadBehaviorParams{
				Behavior:      cdpbrowser.SetDownloadBehaviorBehaviorAllowAndName,
				DownloadPath:  downloadDir,
				EventsEnabled: new(true),
			})
			return err
		}); err != nil {
			return nil, fmt.Errorf("configure download behavior: %w", err)
		}
	}
	return b, nil
}

func (b *Browser) enableTargetDiscovery(startupCtx context.Context) error {
	if err := callBrowserBounded(startupCtx, b.browserCtx, func(ctx context.Context) error {
		_, err := chromedp.CallBrowser(ctx, target.SetDiscoverTargets, target.SetDiscoverTargetsParams{
			Discover: true,
		})
		return err
	}); err != nil {
		return fmt.Errorf("enable target discovery: %w", err)
	}
	return nil
}

func (b *Browser) adoptPopupTarget(targetID target.ID) {
	attachCtx, cancel := context.WithTimeout(b.browserCtx, adoptTargetTimeout)
	defer cancel()
	_, _ = b.Tabs.AttachBackgroundTarget(attachCtx, targetID)
}

func callBrowserBounded(startupCtx, browserCtx context.Context, fn func(context.Context) error) error {
	callCtx, cancel := context.WithCancelCause(browserCtx)
	stop := context.AfterFunc(startupCtx, func() {
		cancel(context.Cause(startupCtx))
	})
	err := fn(callCtx)
	stopped := stop()
	cancel(nil)
	if err != nil || !stopped {
		if err == nil {
			err = context.Cause(startupCtx)
		}
		return err
	}
	return nil
}

// resolveDebuggerURL converts an HTTP(S) or base WebSocket Chrome remote
// debugging URL into a direct /devtools/browser/... WebSocket URL.
// Doing this explicitly avoids chromedp's unchecked type assertion on
// /json/version responses missing webSocketDebuggerUrl and respects ctx.
func resolveDebuggerURL(ctx context.Context, rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid URL: %w", err)
	}
	switch u.Scheme {
	case "ws", "wss", "http", "https":
	default:
		return "", fmt.Errorf("unsupported URL scheme %q (want http, https, ws, or wss)", u.Scheme)
	}
	hostname := u.Hostname()
	if hostname == "" {
		return "", fmt.Errorf("missing host in URL %q", rawURL)
	}
	port := u.Port()

	resolveCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	// Direct /devtools/browser/ WebSocket URL.
	if (u.Scheme == "ws" || u.Scheme == "wss") && strings.Contains(u.Path, "/devtools/browser/") {
		// For plaintext ws://, rewrite non-localhost/non-IP hostnames to IP
		// literals because Chrome rejects non-IP Host headers on debugging
		// sockets. Never rewrite wss:// (would break TLS SNI and cert checks).
		if u.Scheme == "ws" {
			resolvedHost, err := resolvePlaintextHost(resolveCtx, hostname, port)
			if err != nil {
				return "", err
			}
			u.Host = resolvedHost
		}
		return u.String(), nil
	}

	versionScheme := "http"
	versionHost := u.Host
	if u.Scheme == "https" || u.Scheme == "wss" {
		versionScheme = "https"
	} else {
		// Plaintext http:// (or ws:// base URL): rewrite non-IP hostnames
		// to IP literals so Chrome's /json/version accepts the Host header.
		resolvedHost, err := resolvePlaintextHost(resolveCtx, hostname, port)
		if err != nil {
			return "", err
		}
		versionHost = resolvedHost
	}

	versionURL := (&url.URL{
		Scheme:   versionScheme,
		User:     u.User,
		Host:     versionHost,
		Path:     "/json/version",
		RawQuery: u.RawQuery,
	}).String()

	req, err := http.NewRequestWithContext(resolveCtx, http.MethodGet, versionURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s returned status %d", versionURL, resp.StatusCode)
	}

	var payload struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode %s: %w", versionURL, err)
	}
	if payload.WebSocketDebuggerURL == "" {
		return "", fmt.Errorf("%s response missing webSocketDebuggerUrl", versionURL)
	}
	return payload.WebSocketDebuggerURL, nil
}

// resolvePlaintextHost resolves non-localhost, non-IP hostnames to an IP
// literal and formats host[:port] with IPv6 brackets when needed.
func resolvePlaintextHost(ctx context.Context, hostname, port string) (string, error) {
	if hostname != "localhost" {
		if _, err := netip.ParseAddr(hostname); err != nil {
			addrs, err := net.DefaultResolver.LookupIPAddr(ctx, hostname)
			if err != nil {
				return "", fmt.Errorf("resolve host %q: %w", hostname, err)
			}
			if len(addrs) == 0 {
				return "", fmt.Errorf("resolve host %q: no addresses found", hostname)
			}
			hostname = addrs[0].IP.String()
		}
	}
	if port != "" {
		return net.JoinHostPort(hostname, port), nil
	}
	if strings.Contains(hostname, ":") {
		return "[" + hostname + "]", nil
	}
	return hostname, nil
}

// Alive returns true if the browser's context is still active (Chrome
// process hasn't been killed or disconnected).
func (b *Browser) Alive() bool {
	return b.browserCtx.Err() == nil
}

// Close shuts down the browser. In launch mode, this kills the Chrome
// process. In connect mode, it disconnects. Safe to call multiple times.
func (b *Browser) Close() {
	b.closeOnce.Do(func() {
		b.Tabs.CloseAll()
		b.browserCancel()
		b.allocCancel()
	})
}
