package tab

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// browserCtx is a chromedp browser context shared by all tests.
// TestMain launches Chrome once and tears it down after all tests complete.
var browserCtx context.Context
var browserCancel context.CancelFunc
var allocCancel context.CancelFunc

func TestMain(m *testing.M) {
	allocCtx, ac := chromedp.NewExecAllocator(
		context.Background(),
		append(slices.Clone(chromedp.DefaultExecAllocatorOptions[:]), chromedp.Headless, chromedp.DisableGPU)...,
	)
	allocCancel = ac

	bCtx, bc := chromedp.NewContext(allocCtx)
	browserCancel = bc

	// Force Chrome to start.
	if err := chromedp.Do(bCtx); err != nil {
		bc()
		ac()
		os.Exit(1)
	}
	browserCtx = bCtx

	code := m.Run()

	browserCancel()
	allocCancel()
	os.Exit(code)
}

func TestNewTabAndGet(t *testing.T) {
	mgr := NewManager(browserCtx)
	defer mgr.CloseAll()

	tab, err := mgr.NewTab(context.Background())
	if err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	if tab.ID == "" {
		t.Error("new tab has empty ID")
	}

	got, err := mgr.Get(tab.ID)
	if err != nil {
		t.Fatalf("Get(%q): %v", tab.ID, err)
	}
	if got.ID != tab.ID {
		t.Errorf("Get returned tab %q, want %q", got.ID, tab.ID)
	}
}

func TestActive(t *testing.T) {
	mgr := NewManager(browserCtx)
	defer mgr.CloseAll()

	// No tabs — Active should return nil.
	if active := mgr.Active(); active != nil {
		t.Errorf("Active() with no tabs should return nil, got %q", active.ID)
	}

	t1, err := mgr.NewTab(context.Background())
	if err != nil {
		t.Fatalf("NewTab t1: %v", err)
	}
	t2, err := mgr.NewTab(context.Background())
	if err != nil {
		t.Fatalf("NewTab t2: %v", err)
	}

	// Active should be the most recently created tab.
	active := mgr.Active()
	if active == nil || active.ID != t2.ID {
		t.Errorf("Active = %v, want %q (most recent)", active, t2.ID)
	}

	// Close t2 — active should fall back to t1.
	if err := mgr.Close(t2.ID); err != nil {
		t.Fatalf("Close t2: %v", err)
	}
	active = mgr.Active()
	if active == nil || active.ID != t1.ID {
		t.Errorf("Active after close = %v, want %q (fallback)", active, t1.ID)
	}
}

func TestGetNotFound(t *testing.T) {
	mgr := NewManager(browserCtx)
	defer mgr.CloseAll()

	_, err := mgr.Get("nonexistent")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(nonexistent) err = %v, want ErrNotFound", err)
	}
}

func TestActivateAndMRU(t *testing.T) {
	mgr := NewManager(browserCtx)
	defer mgr.CloseAll()

	t1, err := mgr.NewTab(context.Background())
	if err != nil {
		t.Fatalf("NewTab t1: %v", err)
	}
	t2, err := mgr.NewTab(context.Background())
	if err != nil {
		t.Fatalf("NewTab t2: %v", err)
	}
	t3, err := mgr.NewTab(context.Background())
	if err != nil {
		t.Fatalf("NewTab t3: %v", err)
	}

	// Active is t3 (most recent). Activate t1 — it should become active.
	if err := mgr.Activate(context.Background(), t1.ID); err != nil {
		t.Fatalf("Activate t1: %v", err)
	}
	active := mgr.Active()
	if active == nil || active.ID != t1.ID {
		t.Errorf("after Activate(t1), Active = %v, want %q", active, t1.ID)
	}

	// Close t1 — active should fall back to t3 (next most recent in MRU).
	if err := mgr.Close(t1.ID); err != nil {
		t.Fatalf("Close t1: %v", err)
	}
	active = mgr.Active()
	if active == nil || active.ID != t3.ID {
		t.Errorf("after closing t1, Active = %v, want %q", active, t3.ID)
	}

	_ = t2 // keep reference
}

func TestActivateNotFound(t *testing.T) {
	mgr := NewManager(browserCtx)
	defer mgr.CloseAll()

	err := mgr.Activate(context.Background(), "nonexistent")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Activate(nonexistent) err = %v, want ErrNotFound", err)
	}
}

func TestCloseNotFound(t *testing.T) {
	mgr := NewManager(browserCtx)
	defer mgr.CloseAll()

	err := mgr.Close("nonexistent")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Close(nonexistent) err = %v, want ErrNotFound", err)
	}
}

func TestList(t *testing.T) {
	mgr := NewManager(browserCtx)
	defer mgr.CloseAll()

	t1, err := mgr.NewTab(context.Background())
	if err != nil {
		t.Fatalf("NewTab t1: %v", err)
	}
	t2, err := mgr.NewTab(context.Background())
	if err != nil {
		t.Fatalf("NewTab t2: %v", err)
	}

	infos, err := mgr.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("List returned %d tabs, want 2", len(infos))
	}
	// Deterministic MRU order: t1 oldest, t2 active at tail.
	if infos[0].ID != t1.ID || infos[1].ID != t2.ID {
		t.Errorf("List order = [%q, %q], want [%q, %q]", infos[0].ID, infos[1].ID, t1.ID, t2.ID)
	}
	if infos[0].Active {
		t.Error("t1 should not be active")
	}
	if !infos[1].Active {
		t.Error("t2 should be active")
	}
}

func TestLen(t *testing.T) {
	mgr := NewManager(browserCtx)
	defer mgr.CloseAll()

	if mgr.Len() != 0 {
		t.Errorf("Len() = %d, want 0", mgr.Len())
	}

	_, err := mgr.NewTab(context.Background())
	if err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	if mgr.Len() != 1 {
		t.Errorf("Len() = %d, want 1", mgr.Len())
	}

	_, err = mgr.NewTab(context.Background())
	if err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	if mgr.Len() != 2 {
		t.Errorf("Len() = %d, want 2", mgr.Len())
	}
}

func TestResolve(t *testing.T) {
	mgr := NewManager(browserCtx)
	defer mgr.CloseAll()

	// Resolve with no tabs should fail.
	_, err := mgr.Resolve("")
	if err == nil {
		t.Error("Resolve('') with no tabs should return error")
	}

	t1, err := mgr.NewTab(context.Background())
	if err != nil {
		t.Fatalf("NewTab t1: %v", err)
	}
	t2, err := mgr.NewTab(context.Background())
	if err != nil {
		t.Fatalf("NewTab t2: %v", err)
	}

	// Resolve("") returns the active tab (t2).
	resolved, err := mgr.Resolve("")
	if err != nil {
		t.Fatalf("Resolve(''): %v", err)
	}
	if resolved.ID != t2.ID {
		t.Errorf("Resolve('') = %q, want %q (active)", resolved.ID, t2.ID)
	}

	// Resolve(t1.ID) returns t1 specifically.
	resolved, err = mgr.Resolve(t1.ID)
	if err != nil {
		t.Fatalf("Resolve(%q): %v", t1.ID, err)
	}
	if resolved.ID != t1.ID {
		t.Errorf("Resolve(%q) = %q, want %q", t1.ID, resolved.ID, t1.ID)
	}

	// Resolve nonexistent ID.
	_, err = mgr.Resolve("nonexistent")
	if err == nil {
		t.Error("Resolve(nonexistent) should return error")
	}
}

func TestCloseAll(t *testing.T) {
	mgr := NewManager(browserCtx)

	for i := 0; i < 3; i++ {
		if _, err := mgr.NewTab(context.Background()); err != nil {
			t.Fatalf("NewTab %d: %v", i, err)
		}
	}

	if mgr.Len() != 3 {
		t.Fatalf("Len() = %d, want 3", mgr.Len())
	}

	mgr.CloseAll()

	if mgr.Len() != 0 {
		t.Errorf("Len() after CloseAll = %d, want 0", mgr.Len())
	}
	if active := mgr.Active(); active != nil {
		t.Errorf("Active() after CloseAll should be nil, got %q", active.ID)
	}
	// NewTab after CloseAll must fail rather than leak a live target.
	if _, err := mgr.NewTab(context.Background()); err == nil {
		t.Error("NewTab after CloseAll should return error")
	}
}

func TestMRUOrder(t *testing.T) {
	mgr := NewManager(browserCtx)
	defer mgr.CloseAll()

	t1, err := mgr.NewTab(context.Background())
	if err != nil {
		t.Fatalf("NewTab t1: %v", err)
	}
	t2, err := mgr.NewTab(context.Background())
	if err != nil {
		t.Fatalf("NewTab t2: %v", err)
	}
	t3, err := mgr.NewTab(context.Background())
	if err != nil {
		t.Fatalf("NewTab t3: %v", err)
	}

	// Active is t3. Close it — should fall back to t2.
	mgr.Close(t3.ID)
	active := mgr.Active()
	if active == nil || active.ID != t2.ID {
		t.Errorf("after closing t3, Active = %v, want %q", active, t2.ID)
	}

	// Close t2 — should fall back to t1.
	mgr.Close(t2.ID)
	active = mgr.Active()
	if active == nil || active.ID != t1.ID {
		t.Errorf("after closing t2, Active = %v, want %q", active, t1.ID)
	}

	// Close t1 — no tabs left.
	mgr.Close(t1.ID)
	active = mgr.Active()
	if active != nil {
		t.Errorf("after closing all, Active should be nil, got %q", active.ID)
	}
}

func TestConcurrentOps(t *testing.T) {
	mgr := NewManager(browserCtx)
	defer mgr.CloseAll()

	const numTabs = 5
	tabs := make([]*Tab, numTabs)
	for i := 0; i < numTabs; i++ {
		tb, err := mgr.NewTab(context.Background())
		if err != nil {
			t.Fatalf("NewTab %d: %v", i, err)
		}
		tabs[i] = tb
	}

	// Run concurrent List, Get, and Close operations.
	// The race detector will catch any data race issues.
	var wg sync.WaitGroup

	// Concurrent List calls.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = mgr.List(context.Background())
		}()
	}

	// Concurrent Get calls.
	for i := 0; i < numTabs; i++ {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_, _ = mgr.Get(id)
		}(tabs[i].ID)
	}

	// Close half the tabs concurrently.
	for i := 0; i < numTabs/2; i++ {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_ = mgr.Close(id)
		}(tabs[i].ID)
	}

	wg.Wait()

	remaining := numTabs - numTabs/2
	if mgr.Len() != remaining {
		t.Errorf("Len() = %d, want %d", mgr.Len(), remaining)
	}
}

func TestTabURL(t *testing.T) {
	mgr := NewManager(browserCtx)
	defer mgr.CloseAll()

	tab, err := mgr.NewTab(context.Background())
	if err != nil {
		t.Fatalf("NewTab: %v", err)
	}

	// New tab should have about:blank URL.
	url, err := tab.URL(context.Background())
	if err != nil {
		t.Fatalf("URL: %v", err)
	}
	if url != "about:blank" {
		t.Errorf("URL = %q, want %q", url, "about:blank")
	}
}

func TestTabInfo(t *testing.T) {
	mgr := NewManager(browserCtx)
	defer mgr.CloseAll()

	tb, err := mgr.NewTab(context.Background())
	if err != nil {
		t.Fatalf("NewTab: %v", err)
	}

	url, title, err := tb.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if url != "about:blank" || title != "" {
		t.Errorf("Info = (%q, %q), want (\"about:blank\", \"\")", url, title)
	}
}

func TestTabNewCancelledStartupContext(t *testing.T) {
	mgr := NewManager(browserCtx)
	defer mgr.CloseAll()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := mgr.NewTab(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("NewTab with cancelled ctx = %v, want context.Canceled", err)
	}
	if mgr.Len() != 0 {
		t.Errorf("Len() = %d after cancelled NewTab, want 0", mgr.Len())
	}
}

func TestTabInfoDoesNotHangOnJSDialog(t *testing.T) {
	mgr := NewManager(browserCtx)
	defer mgr.CloseAll()

	tb, err := mgr.NewTab(context.Background())
	if err != nil {
		t.Fatalf("NewTab: %v", err)
	}

	dialogOpened := make(chan struct{}, 1)
	lctx, lcancel := context.WithCancel(tb.Context())
	defer lcancel()
	if err := chromedp.Listen(lctx, func(ev any) {
		if _, ok := ev.(*page.EventJavascriptDialogOpening); ok {
			select {
			case dialogOpened <- struct{}{}:
			default:
			}
		}
	}); err != nil {
		t.Fatalf("Listen: %v", err)
	}

	// Trigger an async alert() that blocks the renderer's JS thread.
	if err := chromedp.Do(tb.Context(), chromedp.Evaluate[chromedp.Void](`setTimeout(() => alert('blocked'), 10); document.title = 'Dialog Page';`)); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	select {
	case <-dialogOpened:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for alert dialog to open")
	}

	// List/Info must return promptly without blocking on Runtime.evaluate.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	infos, err := mgr.List(ctx)
	if err != nil {
		t.Fatalf("List while dialog open: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("List returned %d tabs, want 1", len(infos))
	}
	if infos[0].Title != "Dialog Page" {
		t.Errorf("Title while dialog open = %q, want %q", infos[0].Title, "Dialog Page")
	}

	// Dismiss the dialog cleanly.
	_, _ = chromedp.Call(tb.Context(), page.HandleJavaScriptDialog, page.HandleJavaScriptDialogParams{Accept: true})
}

func TestListPropagatesCancelledContext(t *testing.T) {
	mgr := NewManager(browserCtx)
	defer mgr.CloseAll()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Must propagate cancellation even with zero open tabs.
	if _, err := mgr.List(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("List(cancelled, empty) err = %v, want context.Canceled", err)
	}

	if _, err := mgr.NewTab(context.Background()); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	if _, err := mgr.List(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("List(cancelled, 1 tab) err = %v, want context.Canceled", err)
	}
}

func TestNewTabConcurrentCloseAll(t *testing.T) {
	mgr := NewManager(browserCtx)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = mgr.NewTab(context.Background())
		}()
	}
	mgr.CloseAll()
	wg.Wait()

	if mgr.Len() != 0 {
		t.Errorf("Len() after concurrent CloseAll = %d, want 0", mgr.Len())
	}
}

func TestAttachTargetValidationDedupAndClosed(t *testing.T) {
	mgr := NewManager(browserCtx)
	defer mgr.CloseAll()

	// Empty target ID must fail cleanly.
	if _, err := mgr.AttachTarget(context.Background(), ""); err == nil {
		t.Fatal("AttachTarget('') should return an error")
	}

	// Create a raw Chrome target outside mgr and adopt it via AttachTarget.
	res, err := chromedp.CallBrowser(browserCtx, target.CreateTarget, target.CreateTargetParams{
		URL: "about:blank",
	})
	if err != nil {
		t.Fatalf("CreateTarget: %v", err)
	}
	targetID := res.TargetID
	defer func() {
		_, _ = chromedp.CallBrowser(browserCtx, target.CloseTarget, target.CloseTargetParams{TargetID: targetID})
	}()

	t1, err := mgr.AttachTarget(context.Background(), targetID)
	if err != nil {
		t.Fatalf("AttachTarget: %v", err)
	}
	if t1.ID == "" || mgr.Len() != 1 {
		t.Fatalf("after first AttachTarget: id=%q len=%d", t1.ID, mgr.Len())
	}

	// Second AttachTarget for the same targetID must deduplicate.
	t2, err := mgr.AttachTarget(context.Background(), targetID)
	if err != nil {
		t.Fatalf("second AttachTarget: %v", err)
	}
	if t2 != t1 || mgr.Len() != 1 {
		t.Fatalf("AttachTarget did not deduplicate: t1=%p t2=%p len=%d", t1, t2, mgr.Len())
	}

	// After CloseAll, AttachTarget must fail rather than register on a closed manager.
	mgr.CloseAll()
	if _, err := mgr.AttachTarget(context.Background(), targetID); err == nil {
		t.Fatal("AttachTarget after CloseAll should return an error")
	}
	if mgr.Len() != 0 {
		t.Errorf("Len() after CloseAll + AttachTarget = %d, want 0", mgr.Len())
	}
}

func TestExternallyClosedTabIsPrunedAutomatically(t *testing.T) {
	mgr := NewManager(browserCtx)
	defer mgr.CloseAll()

	t1, err := mgr.NewTab(context.Background())
	if err != nil {
		t.Fatalf("NewTab t1: %v", err)
	}
	t2, err := mgr.NewTab(context.Background())
	if err != nil {
		t.Fatalf("NewTab t2: %v", err)
	}
	if active := mgr.Active(); active == nil || active.ID != t2.ID {
		t.Fatalf("expected t2 active before external close, got %v", active)
	}

	// Simulate user closing t2 directly in Chrome (bypassing mgr.Close).
	if _, err := chromedp.CallBrowser(browserCtx, target.CloseTarget, target.CloseTargetParams{
		TargetID: t2.targetID,
	}); err != nil {
		t.Fatalf("external CloseTarget: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if mgr.Len() == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if mgr.Len() != 1 {
		t.Fatalf("externally closed tab was not pruned: Len()=%d, want 1", mgr.Len())
	}
	active := mgr.Active()
	if active == nil || active.ID != t1.ID {
		t.Fatalf("Active() after external close = %v, want %q", active, t1.ID)
	}
	infos, err := mgr.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(infos) != 1 || infos[0].ID != t1.ID || !infos[0].Active {
		t.Errorf("List() after external close = %+v, want only active %q", infos, t1.ID)
	}
}

func TestListSkipsCancelledTabContext(t *testing.T) {
	mgr := NewManager(browserCtx)
	defer mgr.CloseAll()

	t1, err := mgr.NewTab(context.Background())
	if err != nil {
		t.Fatalf("NewTab t1: %v", err)
	}
	t2, err := mgr.NewTab(context.Background())
	if err != nil {
		t.Fatalf("NewTab t2: %v", err)
	}

	// Cancel t2's context directly (simulating a race where t2 is closed/pruned
	// after List snapshots tabs but before t2.Info runs).
	t2.Close()

	infos, err := mgr.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(infos) != 1 || infos[0].ID != t1.ID || !infos[0].Active {
		t.Errorf("List() with cancelled active tab = %+v, want only active %q", infos, t1.ID)
	}
	if mgr.Len() != 1 {
		t.Errorf("Len() after List pruned cancelled tab = %d, want 1", mgr.Len())
	}
}

func TestActivateCancelledAndBringToFront(t *testing.T) {
	mgr := NewManager(browserCtx)
	defer mgr.CloseAll()

	t1, err := mgr.NewTab(context.Background())
	if err != nil {
		t.Fatalf("NewTab t1: %v", err)
	}
	t2, err := mgr.NewTab(context.Background())
	if err != nil {
		t.Fatalf("NewTab t2: %v", err)
	}

	// Cancelled context must fail without mutating active tab order.
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := mgr.Activate(cctx, t1.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("Activate(cancelled) err = %v, want context.Canceled", err)
	}
	if active := mgr.Active(); active == nil || active.ID != t2.ID {
		t.Fatalf("Active after cancelled Activate = %v, want %q", active, t2.ID)
	}

	// Activating t1 brings it to front and updates MRU even when a dialog is open.
	dialogOpened := make(chan struct{}, 1)
	lctx, lcancel := context.WithCancel(t1.Context())
	defer lcancel()
	if err := chromedp.Listen(lctx, func(ev any) {
		if _, ok := ev.(*page.EventJavascriptDialogOpening); ok {
			select {
			case dialogOpened <- struct{}{}:
			default:
			}
		}
	}); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	if err := chromedp.Do(t1.Context(), chromedp.Evaluate[chromedp.Void](`setTimeout(() => alert('bg'), 10);`)); err != nil {
		t.Fatalf("Evaluate alert: %v", err)
	}
	select {
	case <-dialogOpened:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for alert dialog")
	}

	actCtx, actCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer actCancel()
	if err := mgr.Activate(actCtx, t1.ID); err != nil {
		t.Fatalf("Activate(t1) while dialog open: %v", err)
	}
	if active := mgr.Active(); active == nil || active.ID != t1.ID {
		t.Errorf("Active = %v, want %q", active, t1.ID)
	}
	_, _ = chromedp.Call(t1.Context(), page.HandleJavaScriptDialog, page.HandleJavaScriptDialogParams{Accept: true})

	// Activating a tab whose target context died must prune it and return ErrNotFound.
	t2.Close()
	if err := mgr.Activate(context.Background(), t2.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Activate(closed tab) err = %v, want ErrNotFound", err)
	}
	if mgr.Len() != 1 {
		t.Errorf("Len() after Activate pruned closed tab = %d, want 1", mgr.Len())
	}
}

func TestAttachBackgroundTargetAndConcurrentDedup(t *testing.T) {
	// Empty manager: AttachBackgroundTarget must still become Active.
	emptyMgr := NewManager(browserCtx)
	defer emptyMgr.CloseAll()
	res0, err := chromedp.CallBrowser(browserCtx, target.CreateTarget, target.CreateTargetParams{
		URL: "about:blank",
	})
	if err != nil {
		t.Fatalf("CreateTarget (empty): %v", err)
	}
	tid0 := res0.TargetID
	defer func() {
		_, _ = chromedp.CallBrowser(browserCtx, target.CloseTarget, target.CloseTargetParams{TargetID: tid0})
	}()
	bg0, err := emptyMgr.AttachBackgroundTarget(context.Background(), tid0)
	if err != nil {
		t.Fatalf("AttachBackgroundTarget on empty manager: %v", err)
	}
	if active := emptyMgr.Active(); active == nil || active.ID != bg0.ID {
		t.Errorf("Active on empty manager = %v, want %q", active, bg0.ID)
	}

	// Non-empty manager: concurrent AttachBackgroundTarget calls must deduplicate
	// without stealing active focus from primary.
	mgr := NewManager(browserCtx)
	defer mgr.CloseAll()

	primary, err := mgr.NewTab(context.Background())
	if err != nil {
		t.Fatalf("NewTab primary: %v", err)
	}

	res, err := chromedp.CallBrowser(browserCtx, target.CreateTarget, target.CreateTargetParams{
		URL: "about:blank",
	})
	if err != nil {
		t.Fatalf("CreateTarget: %v", err)
	}
	tid := res.TargetID
	defer func() {
		_, _ = chromedp.CallBrowser(browserCtx, target.CloseTarget, target.CloseTargetParams{TargetID: tid})
	}()

	const callers = 4
	results := make([]*Tab, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx], errs[idx] = mgr.AttachBackgroundTarget(context.Background(), tid)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d AttachBackgroundTarget: %v", i, err)
		}
		if results[i] == nil || results[i] != results[0] {
			t.Fatalf("caller %d returned %v, want deduped %v", i, results[i], results[0])
		}
	}
	if mgr.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", mgr.Len())
	}
	if !mgr.HasTarget(tid) {
		t.Error("HasTarget(tid) = false, want true")
	}
	if active := mgr.Active(); active == nil || active.ID != primary.ID {
		t.Errorf("AttachBackgroundTarget stole Active: got %v, want %q", active, primary.ID)
	}

	// Waiter recovery: if an in-flight attacher fails/cancels, a blocked waiter
	// must wake up and succeed on its own attach attempt.
	res2, err := chromedp.CallBrowser(browserCtx, target.CreateTarget, target.CreateTargetParams{
		URL: "about:blank",
	})
	if err != nil {
		t.Fatalf("CreateTarget (waiter): %v", err)
	}
	tid2 := res2.TargetID
	defer func() {
		_, _ = chromedp.CallBrowser(browserCtx, target.CloseTarget, target.CloseTargetParams{TargetID: tid2})
	}()
	blocker := make(chan struct{})
	mgr.mu.Lock()
	mgr.attaching[tid2] = blocker
	mgr.mu.Unlock()
	waiterDone := make(chan struct {
		tb  *Tab
		err error
	}, 1)
	go func() {
		tb, err := mgr.AttachBackgroundTarget(context.Background(), tid2)
		waiterDone <- struct {
			tb  *Tab
			err error
		}{tb, err}
	}()
	time.Sleep(15 * time.Millisecond)
	mgr.mu.Lock()
	delete(mgr.attaching, tid2)
	close(blocker)
	mgr.mu.Unlock()
	got := <-waiterDone
	if got.err != nil || got.tb == nil {
		t.Fatalf("waiter recovery failed: tb=%v err=%v", got.tb, got.err)
	}
}

func TestSnapshotRefsAndNavigationInvalidation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>Nav</title></head><body>ok</body></html>`))
	}))
	defer srv.Close()

	mgr := NewManager(browserCtx)
	defer mgr.CloseAll()

	tb, err := mgr.NewTab(context.Background())
	if err != nil {
		t.Fatalf("NewTab: %v", err)
	}

	// Navigating BEFORE any snapshot must still report "no active snapshot" (not stale).
	if err := chromedp.Do(tb.Context(), chromedp.Navigate(srv.URL)); err != nil {
		t.Fatalf("initial Navigate: %v", err)
	}
	if _, err := tb.ResolveRef("e1"); err == nil || !strings.Contains(err.Error(), "no active snapshot") {
		t.Fatalf("ResolveRef before first snapshot = %v, want no active snapshot error", err)
	}

	gen := tb.SnapshotGeneration()
	if !tb.SetSnapshotRefs(gen, map[string]SnapshotRef{
		"e1": {BackendNodeID: cdp.BackendNodeID(42), FrameID: cdp.FrameID("main"), Role: "button", Name: "Go"},
		"e2": {BackendNodeID: cdp.BackendNodeID(43), FrameID: cdp.FrameID("child-1"), Role: "link", Name: "Sub"},
	}) {
		t.Fatal("SetSnapshotRefs with current gen returned false")
	}
	got, err := tb.ResolveRef("e1")
	if err != nil || got.BackendNodeID != 42 || got.Role != "button" || got.Name != "Go" {
		t.Fatalf("ResolveRef(e1) = (%+v, %v)", got, err)
	}
	if _, err := tb.ResolveRef("e99"); err == nil || !strings.Contains(err.Error(), "not found in current snapshot") {
		t.Fatalf("ResolveRef(e99) = %v, want not found in current snapshot", err)
	}

	// Subframe invalidation removes only that frame's refs without wiping main-frame refs.
	tb.InvalidateFrameSnapshotRefs(cdp.FrameID("child-1"))
	if _, err := tb.ResolveRef("e2"); err == nil {
		t.Fatal("expected child frame ref e2 to be pruned")
	}
	if _, err := tb.ResolveRef("e1"); err != nil {
		t.Fatalf("main-frame ref e1 unexpectedly invalidated by subframe prune: %v", err)
	}

	// Main-frame navigation must invalidate snapshot refs with a stale reason AND
	// reject commits carrying the pre-navigation generation.
	if err := chromedp.Do(tb.Context(), chromedp.Navigate(srv.URL+"/?v=2")); err != nil {
		t.Fatalf("Navigate v2: %v", err)
	}
	if _, err := tb.ResolveRef("e1"); err == nil || !strings.Contains(err.Error(), "stale (page navigated)") {
		t.Fatalf("ResolveRef after navigation = %v, want stale (page navigated)", err)
	}
	if tb.SetSnapshotRefs(gen, map[string]SnapshotRef{"e3": {BackendNodeID: 99}}) {
		t.Fatal("SetSnapshotRefs with stale generation unexpectedly succeeded")
	}
	if _, err := tb.ResolveRef("e3"); err == nil || !strings.Contains(err.Error(), "stale (page navigated)") {
		t.Fatalf("stale commit leaked ref e3: err = %v", err)
	}
}
