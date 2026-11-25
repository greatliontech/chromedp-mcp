package tab

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/page"
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
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Headless)...,
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
	mgr := NewManager(browserCtx, nil)
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
	mgr := NewManager(browserCtx, nil)
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
	mgr := NewManager(browserCtx, nil)
	defer mgr.CloseAll()

	_, err := mgr.Get("nonexistent")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(nonexistent) err = %v, want ErrNotFound", err)
	}
}

func TestActivateAndMRU(t *testing.T) {
	mgr := NewManager(browserCtx, nil)
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
	if err := mgr.Activate(t1.ID); err != nil {
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
	mgr := NewManager(browserCtx, nil)
	defer mgr.CloseAll()

	err := mgr.Activate("nonexistent")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Activate(nonexistent) err = %v, want ErrNotFound", err)
	}
}

func TestCloseNotFound(t *testing.T) {
	mgr := NewManager(browserCtx, nil)
	defer mgr.CloseAll()

	err := mgr.Close("nonexistent")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Close(nonexistent) err = %v, want ErrNotFound", err)
	}
}

func TestList(t *testing.T) {
	mgr := NewManager(browserCtx, nil)
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
	mgr := NewManager(browserCtx, nil)
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
	mgr := NewManager(browserCtx, nil)
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
	mgr := NewManager(browserCtx, nil)

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
	mgr := NewManager(browserCtx, nil)
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
	mgr := NewManager(browserCtx, nil)
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
	mgr := NewManager(browserCtx, nil)
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

func TestTabTitle(t *testing.T) {
	mgr := NewManager(browserCtx, nil)
	defer mgr.CloseAll()

	tab, err := mgr.NewTab(context.Background())
	if err != nil {
		t.Fatalf("NewTab: %v", err)
	}

	// New tab title should be empty or blank.
	title, err := tab.Title(context.Background())
	if err != nil {
		t.Fatalf("Title: %v", err)
	}
	// about:blank has empty title
	if title != "" {
		t.Errorf("Title = %q, want empty string", title)
	}
}

func TestTabNewCancelledStartupContext(t *testing.T) {
	mgr := NewManager(browserCtx, nil)
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
	mgr := NewManager(browserCtx, nil)
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
	mgr := NewManager(browserCtx, nil)
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
	mgr := NewManager(browserCtx, nil)
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
