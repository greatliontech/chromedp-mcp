package browser

import (
	"context"
	"os"
	"sync"
	"testing"
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

	b, err := mgr.Launch(LaunchOptions{Headless: true})
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
}

func TestManagerActive(t *testing.T) {
	mgr := NewManager(testCtx)
	defer mgr.CloseAll()

	// No browsers — Active should fail.
	_, err := mgr.Active()
	if err == nil {
		t.Error("Active() with no browsers should return error")
	}

	b1, err := mgr.Launch(LaunchOptions{Headless: true})
	if err != nil {
		t.Fatalf("Launch b1: %v", err)
	}
	b2, err := mgr.Launch(LaunchOptions{Headless: true})
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

	b1, err := mgr.Launch(LaunchOptions{Headless: true})
	if err != nil {
		t.Fatalf("Launch b1: %v", err)
	}
	b2, err := mgr.Launch(LaunchOptions{Headless: true})
	if err != nil {
		t.Fatalf("Launch b2: %v", err)
	}

	infos := mgr.List()
	if len(infos) != 2 {
		t.Fatalf("List returned %d browsers, want 2", len(infos))
	}

	// Verify active flag.
	idToInfo := map[string]BrowserInfo{}
	for _, info := range infos {
		idToInfo[info.ID] = info
	}
	if !idToInfo[b2.ID].Active {
		t.Error("b2 should be active")
	}
	if idToInfo[b1.ID].Active {
		t.Error("b1 should not be active")
	}
}

func TestManagerPruneDeadBrowsers(t *testing.T) {
	mgr := NewManager(testCtx)
	defer mgr.CloseAll()

	b, err := mgr.Launch(LaunchOptions{Headless: true})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}

	// Kill the browser from outside the manager.
	b.Close()

	// Active should prune the dead browser and return error.
	_, err = mgr.Active()
	if err == nil {
		t.Error("Active() should fail after browser was killed")
	}

	infos := mgr.List()
	if len(infos) != 0 {
		t.Errorf("List should be empty after prune, got %d", len(infos))
	}
}

func TestManagerMRUOrder(t *testing.T) {
	mgr := NewManager(testCtx)
	defer mgr.CloseAll()

	b1, err := mgr.Launch(LaunchOptions{Headless: true})
	if err != nil {
		t.Fatalf("Launch b1: %v", err)
	}
	b2, err := mgr.Launch(LaunchOptions{Headless: true})
	if err != nil {
		t.Fatalf("Launch b2: %v", err)
	}
	b3, err := mgr.Launch(LaunchOptions{Headless: true})
	if err != nil {
		t.Fatalf("Launch b3: %v", err)
	}

	// Active is b3 (most recent). Close it — should fall back to b2.
	mgr.Close(b3.ID)
	active, _ := mgr.Active()
	if active.ID != b2.ID {
		t.Errorf("after closing b3, active = %q, want %q", active.ID, b2.ID)
	}

	// Close b2 — should fall back to b1.
	mgr.Close(b2.ID)
	active, _ = mgr.Active()
	if active.ID != b1.ID {
		t.Errorf("after closing b2, active = %q, want %q", active.ID, b1.ID)
	}
}

func TestManagerConcurrentListAndClose(t *testing.T) {
	mgr := NewManager(testCtx)
	defer mgr.CloseAll()

	const numBrowsers = 5
	ids := make([]string, numBrowsers)
	for i := 0; i < numBrowsers; i++ {
		b, err := mgr.Launch(LaunchOptions{Headless: true})
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

func TestManagerCloseTabAcrossBrowsers(t *testing.T) {
	mgr := NewManager(testCtx)
	defer mgr.CloseAll()

	b, err := mgr.Launch(LaunchOptions{Headless: true})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}

	tab, err := b.Tabs.NewTab()
	if err != nil {
		t.Fatalf("NewTab: %v", err)
	}

	// CloseTab should find the tab across browsers.
	if err := mgr.CloseTab(tab.ID); err != nil {
		t.Fatalf("CloseTab: %v", err)
	}

	// Tab should be gone.
	err = mgr.CloseTab(tab.ID)
	if err == nil {
		t.Error("second CloseTab should fail (tab already closed)")
	}
}

func TestManagerActivateTabAcrossBrowsers(t *testing.T) {
	mgr := NewManager(testCtx)
	defer mgr.CloseAll()

	b, err := mgr.Launch(LaunchOptions{Headless: true})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}

	t1, err := b.Tabs.NewTab()
	if err != nil {
		t.Fatalf("NewTab t1: %v", err)
	}
	t2, err := b.Tabs.NewTab()
	if err != nil {
		t.Fatalf("NewTab t2: %v", err)
	}

	// Active should be t2.
	active := b.Tabs.Active()
	if active.ID != t2.ID {
		t.Errorf("active = %q, want %q", active.ID, t2.ID)
	}

	// Activate t1 via manager.
	if err := mgr.ActivateTab(t1.ID); err != nil {
		t.Fatalf("ActivateTab: %v", err)
	}

	active = b.Tabs.Active()
	if active.ID != t1.ID {
		t.Errorf("after activate, active = %q, want %q", active.ID, t1.ID)
	}
}
