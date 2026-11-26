package browser

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/greatliontech/chromedp-mcp/internal/tab"
)

var testCtx context.Context
var testCancel context.CancelFunc

func TestMain(m *testing.M) {
	testCtx, testCancel = context.WithCancel(context.Background())
	code := m.Run()
	testCancel()
	os.Exit(code)
}

func TestManagerLaunchAndGet(t *testing.T) {
	mgr := NewManager(testCtx)
	defer mgr.CloseAll()

	b, err := mgr.Launch(context.Background(), LaunchOptions{Headless: true})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if b.ID == "" {
		t.Error("launched browser has empty ID")
	}

	// Get the same browser by ID.
	got, err := mgr.Get(b.ID)
	if err != nil {
		t.Fatalf("Get(%q): %v", b.ID, err)
	}
	if got.ID != b.ID {
		t.Errorf("Get returned browser %q, want %q", got.ID, b.ID)
	}

	// Launch must adopt Chrome's initial default page target into b.Tabs
	// without leaving an extra untracked blank tab behind.
	activeTab := b.Tabs.Active()
	if activeTab == nil {
		t.Fatal("launched browser has no active default tab")
	}
	countPageTargets := func() int {
		t.Helper()
		infos, err := chromedp.Targets(b.browserCtx)
		if err != nil {
			t.Fatalf("chromedp.Targets: %v", err)
		}
		n := 0
		for _, info := range infos {
			if isAttachablePageTarget(info) {
				n++
			}
		}
		return n
	}
	if n := countPageTargets(); n != 1 {
		t.Errorf("Chrome page targets after Launch = %d, want 1", n)
	}

	// Closing the managed default tab must close the Chrome target (not detach),
	// leaving 0 page targets while keeping the browser alive for NewTab.
	if err := b.Tabs.Close(activeTab.ID); err != nil {
		t.Fatalf("Close default tab: %v", err)
	}
	if b.Tabs.Active() != nil {
		t.Error("expected Active() == nil after closing default tab")
	}
	if n := countPageTargets(); n != 0 {
		t.Errorf("Chrome page targets after closing managed default tab = %d, want 0", n)
	}
	if _, err := b.Tabs.NewTab(context.Background()); err != nil {
		t.Fatalf("NewTab after closing default tab failed: %v", err)
	}
}

func TestManagerActive(t *testing.T) {
	mgr := NewManager(testCtx)
	defer mgr.CloseAll()

	// No browsers — Active should fail.
	_, err := mgr.Active()
	if err == nil {
		t.Error("Active() with no browsers should return error")
	}

	b1, err := mgr.Launch(context.Background(), LaunchOptions{Headless: true})
	if err != nil {
		t.Fatalf("Launch b1: %v", err)
	}
	b2, err := mgr.Launch(context.Background(), LaunchOptions{Headless: true})
	if err != nil {
		t.Fatalf("Launch b2: %v", err)
	}

	// Active should be the most recently launched.
	active, err := mgr.Active()
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if active.ID != b2.ID {
		t.Errorf("Active = %q, want %q (most recent)", active.ID, b2.ID)
	}

	// Get(b1) must NOT silently change the active browser.
	if _, err := mgr.Get(b1.ID); err != nil {
		t.Fatalf("Get(b1): %v", err)
	}
	active, _ = mgr.Active()
	if active.ID != b2.ID {
		t.Errorf("Get(b1) changed Active to %q, want %q", active.ID, b2.ID)
	}

	// Close b2 — active should fall back to b1.
	if err := mgr.Close(b2.ID); err != nil {
		t.Fatalf("Close b2: %v", err)
	}
	active, err = mgr.Active()
	if err != nil {
		t.Fatalf("Active after close: %v", err)
	}
	if active.ID != b1.ID {
		t.Errorf("Active after close = %q, want %q (fallback)", active.ID, b1.ID)
	}
}

func TestManagerGetNotFound(t *testing.T) {
	mgr := NewManager(testCtx)
	defer mgr.CloseAll()

	_, err := mgr.Get("nonexistent")
	if err == nil {
		t.Error("Get(nonexistent) should return error")
	}
}

func TestManagerCloseNotFound(t *testing.T) {
	mgr := NewManager(testCtx)
	defer mgr.CloseAll()

	err := mgr.Close("nonexistent")
	if err == nil {
		t.Error("Close(nonexistent) should return error")
	}
}

func TestManagerList(t *testing.T) {
	mgr := NewManager(testCtx)
	defer mgr.CloseAll()

	b1, err := mgr.Launch(context.Background(), LaunchOptions{Headless: true})
	if err != nil {
		t.Fatalf("Launch b1: %v", err)
	}
	b2, err := mgr.Launch(context.Background(), LaunchOptions{Headless: true})
	if err != nil {
		t.Fatalf("Launch b2: %v", err)
	}

	infos := mgr.List()
	if len(infos) != 2 {
		t.Fatalf("List returned %d browsers, want 2", len(infos))
	}
	// Deterministic MRU order: b1 oldest, b2 active at tail.
	if infos[0].ID != b1.ID || infos[1].ID != b2.ID {
		t.Errorf("List order = [%q, %q], want [%q, %q]", infos[0].ID, infos[1].ID, b1.ID, b2.ID)
	}
	if infos[0].Active {
		t.Error("b1 should not be active")
	}
	if !infos[1].Active {
		t.Error("b2 should be active")
	}
}

func TestManagerPruneDeadBrowsersClosesResources(t *testing.T) {
	mgr := NewManager(testCtx)
	defer mgr.CloseAll()

	b, err := mgr.Launch(context.Background(), LaunchOptions{Headless: true})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}

	// Simulate Chrome disconnecting (browserCtx cancelled) while allocCancel
	// has not yet been called; verify pruning invokes b.Close() -> allocCancel.
	var allocClosed atomic.Bool
	origAllocCancel := b.allocCancel
	b.allocCancel = func() {
		allocClosed.Store(true)
		origAllocCancel()
	}
	b.browserCancel()

	// Active should prune the dead browser, invoke allocCancel, and return error.
	_, err = mgr.Active()
	if err == nil {
		t.Error("Active() should fail after browser died")
	}
	if !allocClosed.Load() {
		t.Error("pruning dead browser did not close allocCancel")
	}

	infos := mgr.List()
	if len(infos) != 0 {
		t.Errorf("List should be empty after prune, got %d", len(infos))
	}
}

func TestManagerGetDeadBrowserClosesResources(t *testing.T) {
	mgr := NewManager(testCtx)
	defer mgr.CloseAll()

	b, err := mgr.Launch(context.Background(), LaunchOptions{Headless: true})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	var allocClosed atomic.Bool
	origAllocCancel := b.allocCancel
	b.allocCancel = func() {
		allocClosed.Store(true)
		origAllocCancel()
	}
	b.browserCancel()

	if _, err := mgr.Get(b.ID); err == nil {
		t.Fatal("Get() on dead browser should return error")
	}
	if !allocClosed.Load() {
		t.Error("Get() on dead browser did not close allocCancel")
	}
}

func TestManagerMRUOrderAndActivate(t *testing.T) {
	mgr := NewManager(testCtx)
	defer mgr.CloseAll()

	b1, err := mgr.Launch(context.Background(), LaunchOptions{Headless: true})
	if err != nil {
		t.Fatalf("Launch b1: %v", err)
	}
	b2, err := mgr.Launch(context.Background(), LaunchOptions{Headless: true})
	if err != nil {
		t.Fatalf("Launch b2: %v", err)
	}
	b3, err := mgr.Launch(context.Background(), LaunchOptions{Headless: true})
	if err != nil {
		t.Fatalf("Launch b3: %v", err)
	}

	// Activate b1; MRU order becomes [b2, b3, b1].
	if err := mgr.Activate(b1.ID); err != nil {
		t.Fatalf("Activate(b1): %v", err)
	}
	active, _ := mgr.Active()
	if active.ID != b1.ID {
		t.Fatalf("after Activate(b1), active = %q, want %q", active.ID, b1.ID)
	}

	// Close b1 — should fall back to b3 (most recently used remaining).
	if err := mgr.Close(b1.ID); err != nil {
		t.Fatalf("Close b1: %v", err)
	}
	active, _ = mgr.Active()
	if active.ID != b3.ID {
		t.Errorf("after closing b1, active = %q, want %q", active.ID, b3.ID)
	}

	// Close b3 — should fall back to b2.
	if err := mgr.Close(b3.ID); err != nil {
		t.Fatalf("Close b3: %v", err)
	}
	active, _ = mgr.Active()
	if active.ID != b2.ID {
		t.Errorf("after closing b3, active = %q, want %q", active.ID, b2.ID)
	}
}

func TestManagerConcurrentListAndClose(t *testing.T) {
	mgr := NewManager(testCtx)
	defer mgr.CloseAll()

	const numBrowsers = 5
	ids := make([]string, numBrowsers)
	for i := 0; i < numBrowsers; i++ {
		b, err := mgr.Launch(context.Background(), LaunchOptions{Headless: true})
		if err != nil {
			t.Fatalf("Launch %d: %v", i, err)
		}
		ids[i] = b.ID
	}

	// Run concurrent List and Close operations. The race detector
	// will catch any lock ordering or TOCTOU issues.
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = mgr.List()
		}()
	}

	// Close half the browsers concurrently.
	for i := 0; i < numBrowsers/2; i++ {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_ = mgr.Close(id)
		}(ids[i])
	}

	wg.Wait()

	// Manager should still be functional.
	infos := mgr.List()
	// At least the browsers we didn't close should remain.
	remaining := numBrowsers - numBrowsers/2
	if len(infos) != remaining {
		t.Errorf("List returned %d, want %d", len(infos), remaining)
	}
}

func TestManagerMultiBrowserResolveActivateAndCloseTab(t *testing.T) {
	mgr := NewManager(testCtx)
	defer mgr.CloseAll()

	b1, err := mgr.Launch(context.Background(), LaunchOptions{Headless: true})
	if err != nil {
		t.Fatalf("Launch b1: %v", err)
	}
	t1, err := b1.Tabs.NewTab(context.Background())
	if err != nil {
		t.Fatalf("NewTab t1: %v", err)
	}

	b2, err := mgr.Launch(context.Background(), LaunchOptions{Headless: true})
	if err != nil {
		t.Fatalf("Launch b2: %v", err)
	}
	t2, err := b2.Tabs.NewTab(context.Background())
	if err != nil {
		t.Fatalf("NewTab t2: %v", err)
	}

	// b2 is active, but ResolveTab("", t1.ID) must still find t1 in b1.
	got, err := mgr.ResolveTab("", t1.ID)
	if err != nil {
		t.Fatalf("ResolveTab('', t1) across browsers: %v", err)
	}
	if got.ID != t1.ID {
		t.Errorf("ResolveTab('', t1) = %q, want %q", got.ID, t1.ID)
	}
	// ResolveTab with explicit tab ID must not switch the active browser.
	activeBrowser, _ := mgr.Active()
	if activeBrowser.ID != b2.ID {
		t.Errorf("ResolveTab changed active browser to %q, want %q", activeBrowser.ID, b2.ID)
	}

	// ActivateTab(t1) must activate t1 AND switch the active browser to b1
	// so default-tab calls (ResolveTab("", "")) now resolve to t1.
	if err := mgr.ActivateTab(t1.ID); err != nil {
		t.Fatalf("ActivateTab(t1): %v", err)
	}
	activeBrowser, _ = mgr.Active()
	if activeBrowser.ID != b1.ID {
		t.Errorf("after ActivateTab(t1), active browser = %q, want %q", activeBrowser.ID, b1.ID)
	}
	defaultTab, err := mgr.ResolveTab("", "")
	if err != nil {
		t.Fatalf("ResolveTab('', ''): %v", err)
	}
	if defaultTab.ID != t1.ID {
		t.Errorf("after ActivateTab(t1), default tab = %q, want %q", defaultTab.ID, t1.ID)
	}

	// CloseTab must find and close t2 in the non-active browser b2.
	if err := mgr.CloseTab(t2.ID); err != nil {
		t.Fatalf("CloseTab(t2): %v", err)
	}
	if err := mgr.CloseTab(t2.ID); err == nil {
		t.Error("second CloseTab(t2) should fail")
	}
}

func TestManagerCloseAllBlocksSubsequentLaunch(t *testing.T) {
	mgr := NewManager(testCtx)
	mgr.CloseAll()

	if _, err := mgr.Launch(context.Background(), LaunchOptions{Headless: true}); err == nil {
		t.Fatal("Launch after CloseAll should fail")
	}
	if _, err := mgr.Connect(context.Background(), "http://127.0.0.1:1", ConnectOptions{}); err == nil {
		t.Fatal("Connect after CloseAll should fail")
	}
}

func TestManagerConcurrentCloseAllAndLaunch(t *testing.T) {
	mgr := NewManager(testCtx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = mgr.Launch(context.Background(), LaunchOptions{Headless: true})
	}()
	time.Sleep(15 * time.Millisecond)
	mgr.CloseAll()
	wg.Wait()

	if infos := mgr.List(); len(infos) != 0 {
		t.Errorf("List() after concurrent CloseAll = %d, want 0", len(infos))
	}
}

func TestManagerNoBrowserTabErrorsAndSentinel(t *testing.T) {
	mgr := NewManager(testCtx)
	defer mgr.CloseAll()

	if err := mgr.CloseTab("missing"); err == nil || !strings.Contains(err.Error(), "no browser running") {
		t.Errorf("CloseTab with no browsers = %v, want no browser running", err)
	}
	if err := mgr.ActivateTab("missing"); err == nil || !strings.Contains(err.Error(), "no browser running") {
		t.Errorf("ActivateTab with no browsers = %v, want no browser running", err)
	}

	b, err := mgr.Launch(context.Background(), LaunchOptions{Headless: true})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if err := mgr.CloseTab("missing"); !errors.Is(err, tab.ErrNotFound) {
		t.Errorf("CloseTab(missing) = %v, want tab.ErrNotFound", err)
	}
	if err := mgr.ActivateTab("missing"); !errors.Is(err, tab.ErrNotFound) {
		t.Errorf("ActivateTab(missing) = %v, want tab.ErrNotFound", err)
	}
	// Closing a dead browser directly by ID via Close() succeeds cleanly.
	b.browserCancel()
	if err := mgr.Close(b.ID); err != nil {
		t.Errorf("Close(dead browser) = %v, want nil", err)
	}
}

func TestLaunchValidationAndCancellation(t *testing.T) {
	mgr := NewManager(testCtx)
	defer mgr.CloseAll()

	// Half-specified profile options must error rather than silently launching ephemeral.
	if _, err := mgr.Launch(context.Background(), LaunchOptions{Headless: true, UserDataDir: "/tmp/x"}); err == nil {
		t.Error("Launch with UserDataDir only should fail")
	}
	if _, err := mgr.Launch(context.Background(), LaunchOptions{Headless: true, ProfileDir: "Default"}); err == nil {
		t.Error("Launch with ProfileDir only should fail")
	}

	// Pre-cancelled startup context must abort before launching Chrome.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := mgr.Launch(ctx, LaunchOptions{Headless: true}); !errors.Is(err, context.Canceled) {
		t.Errorf("Launch with cancelled ctx = %v, want context.Canceled", err)
	}

	// Mid-flight cancellation (exercises context.AfterFunc while Chrome is starting)
	// must return promptly without deadlocking on chromedp's browserCancel.
	for _, delay := range []time.Duration{1 * time.Millisecond, 10 * time.Millisecond} {
		midCtx, midCancel := context.WithTimeout(context.Background(), delay)
		done := make(chan error, 1)
		go func() {
			b, err := mgr.Launch(midCtx, LaunchOptions{Headless: true})
			if b != nil {
				_ = mgr.Close(b.ID)
			}
			done <- err
		}()
		select {
		case <-done:
			// Returned cleanly (either cancelled or launched quickly); no deadlock.
		case <-time.After(10 * time.Second):
			midCancel()
			t.Fatalf("Launch with mid-flight %v timeout deadlocked", delay)
		}
		midCancel()
	}

	// Launching with a non-existent DownloadDir must create it automatically.
	dlDir := filepath.Join(t.TempDir(), "nested", "downloads")
	b, err := mgr.Launch(context.Background(), LaunchOptions{Headless: true, DownloadDir: dlDir})
	if err != nil {
		t.Fatalf("Launch with missing DownloadDir: %v", err)
	}
	defer mgr.Close(b.ID)
	if fi, err := os.Stat(dlDir); err != nil || !fi.IsDir() {
		t.Errorf("DownloadDir %q was not created: fi=%v err=%v", dlDir, fi, err)
	}
}

func TestResolveDebuggerURL(t *testing.T) {
	// Direct /devtools/browser/ ws URL with IP literal passes through.
	direct := "ws://127.0.0.1:9222/devtools/browser/abc-123"
	got, err := resolveDebuggerURL(context.Background(), direct)
	if err != nil || got != direct {
		t.Fatalf("resolveDebuggerURL(direct) = (%q, %v), want (%q, nil)", got, err, direct)
	}

	// Direct wss:// URL preserves hostname (must NOT rewrite to IP, which breaks TLS SNI).
	directWSS := "wss://chrome.invalid:9222/devtools/browser/abc-123?token=xyz"
	got, err = resolveDebuggerURL(context.Background(), directWSS)
	if err != nil || got != directWSS {
		t.Fatalf("resolveDebuggerURL(wss) = (%q, %v), want (%q, nil)", got, err, directWSS)
	}

	// Direct ws:// with empty host is rejected.
	if _, err := resolveDebuggerURL(context.Background(), "ws:///devtools/browser/abc"); err == nil {
		t.Error("expected error for ws URL with empty host")
	}

	// Unsupported scheme is rejected.
	if _, err := resolveDebuggerURL(context.Background(), "ftp://127.0.0.1:9222"); err == nil {
		t.Error("expected error for unsupported scheme")
	}

	// resolvePlaintextHost handles missing port and IPv6 literals.
	if h, err := resolvePlaintextHost(context.Background(), "127.0.0.1", ""); err != nil || h != "127.0.0.1" {
		t.Errorf("resolvePlaintextHost(127.0.0.1, '') = (%q, %v)", h, err)
	}
	if h, err := resolvePlaintextHost(context.Background(), "::1", ""); err != nil || h != "[::1]" {
		t.Errorf("resolvePlaintextHost(::1, '') = (%q, %v)", h, err)
	}
	if h, err := resolvePlaintextHost(context.Background(), "::1", "9222"); err != nil || h != "[::1]:9222" {
		t.Errorf("resolvePlaintextHost(::1, 9222) = (%q, %v)", h, err)
	}

	// HTTP endpoint returning JSON without webSocketDebuggerUrl must return
	// an error rather than panicking inside chromedp.
	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Browser":"Chrome"}`))
	}))
	defer badSrv.Close()

	_, err = resolveDebuggerURL(context.Background(), badSrv.URL)
	if err == nil || !strings.Contains(err.Error(), "webSocketDebuggerUrl") {
		t.Fatalf("resolveDebuggerURL(missing field) err = %v, want webSocketDebuggerUrl error", err)
	}

	// Valid /json/version endpoint resolves cleanly and preserves query params.
	var gotQuery string
	goodSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/json/version" {
			http.NotFound(w, r)
			return
		}
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"webSocketDebuggerUrl":"ws://127.0.0.1:9222/devtools/browser/xyz"}`))
	}))
	defer goodSrv.Close()

	got, err = resolveDebuggerURL(context.Background(), goodSrv.URL+"/?token=secret")
	if err != nil {
		t.Fatalf("resolveDebuggerURL(good) err = %v", err)
	}
	if got != "ws://127.0.0.1:9222/devtools/browser/xyz" {
		t.Errorf("resolveDebuggerURL(good) = %q, want ws://127.0.0.1:9222/devtools/browser/xyz", got)
	}
	if gotQuery != "token=secret" {
		t.Errorf("/json/version RawQuery = %q, want token=secret", gotQuery)
	}
}

func captureLaunchArgs(t *testing.T, opts LaunchOptions) []string {
	t.Helper()
	var gotArgs []string
	parentCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	allocOpts := append(
		launchAllocatorOptions(opts),
		chromedp.ExecPath(filepath.Join(t.TempDir(), "nonexistent-chrome")),
		chromedp.ModifyCmdFunc(func(cmd *exec.Cmd) {
			gotArgs = slices.Clone(cmd.Args)
			cancel()
		}),
	)
	allocCtx, allocCancel := chromedp.NewExecAllocator(parentCtx, allocOpts...)
	defer allocCancel()
	browserCtx, browserCancel := chromedp.NewContext(allocCtx)
	defer browserCancel()

	_, err := chromedp.Targets(browserCtx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("chromedp.Targets err = %v, want context.Canceled", err)
	}
	if len(gotArgs) == 0 {
		t.Fatal("ModifyCmdFunc did not capture Chrome command args")
	}
	return gotArgs
}

func TestLaunchAllocatorOptions(t *testing.T) {
	hasFlag := func(args []string, want string) bool {
		return slices.Contains(args, want)
	}
	hasFlagPrefix := func(args []string, prefix string) bool {
		return slices.ContainsFunc(args, func(a string) bool {
			return strings.HasPrefix(a, prefix)
		})
	}

	t.Run("headless enables DisableGPU and SwiftShader", func(t *testing.T) {
		args := captureLaunchArgs(t, LaunchOptions{Headless: true})
		for _, want := range []string{"--headless", "--disable-gpu", "--enable-unsafe-swiftshader"} {
			if !hasFlag(args, want) {
				t.Errorf("headless args missing %q: %v", want, args)
			}
		}
		if hasFlagPrefix(args, "--window-size=") {
			t.Errorf("zero dimensions should omit --window-size: %v", args)
		}
		if hasFlagPrefix(args, "--profile-directory=") {
			t.Errorf("empty profile should omit --profile-directory: %v", args)
		}
	})

	t.Run("headful omits headless and GPU-disabling flags", func(t *testing.T) {
		args := captureLaunchArgs(t, LaunchOptions{Headless: false})
		for _, forbidden := range []string{"--headless", "--disable-gpu", "--enable-unsafe-swiftshader"} {
			if hasFlag(args, forbidden) || hasFlagPrefix(args, forbidden+"=") {
				t.Errorf("headful args unexpectedly contain %q: %v", forbidden, args)
			}
		}
	})

	t.Run("window size and profile flags", func(t *testing.T) {
		userDataDir := filepath.Join(t.TempDir(), "chrome-profile")
		args := captureLaunchArgs(t, LaunchOptions{
			Headless:    true,
			Width:       1280,
			Height:      720,
			UserDataDir: userDataDir,
			ProfileDir:  "Profile 2",
		})
		for _, want := range []string{
			"--window-size=1280,720",
			"--user-data-dir=" + userDataDir,
			"--profile-directory=Profile 2",
		} {
			if !hasFlag(args, want) {
				t.Errorf("args missing %q: %v", want, args)
			}
		}
	})

	t.Run("non-positive or partial dimensions omit window-size", func(t *testing.T) {
		cases := []LaunchOptions{
			{Headless: true, Width: 1280, Height: 0},
			{Headless: true, Width: 0, Height: 720},
			{Headless: true, Width: -1, Height: 720},
			{Headless: true, Width: 1280, Height: -1},
		}
		for _, opts := range cases {
			args := captureLaunchArgs(t, opts)
			if hasFlagPrefix(args, "--window-size=") {
				t.Errorf("opts %+v should omit --window-size: %v", opts, args)
			}
		}
	})

	t.Run("half-specified profile omits user-data-dir and profile-directory", func(t *testing.T) {
		userDataDir := filepath.Join(t.TempDir(), "custom-chrome-profile")
		for _, opts := range []LaunchOptions{
			{Headless: true, UserDataDir: userDataDir},
			{Headless: true, ProfileDir: "Profile 2"},
		} {
			args := captureLaunchArgs(t, opts)
			if hasFlag(args, "--user-data-dir="+userDataDir) {
				t.Errorf("opts %+v should omit custom --user-data-dir: %v", opts, args)
			}
			if hasFlagPrefix(args, "--profile-directory=") {
				t.Errorf("opts %+v should omit --profile-directory: %v", opts, args)
			}
		}
	})
}
