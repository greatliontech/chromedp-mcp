package browser

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/greatliontech/chromedp-mcp/internal/id"
	"github.com/greatliontech/chromedp-mcp/internal/tab"
)

// Manager manages multiple browser instances and tracks the active one.
//
// The active browser is always the tail of order (order[len(order)-1]), giving
// a single source of truth for MRU ordering and active selection.
type Manager struct {
	mu        sync.Mutex
	browsers  map[string]*Browser
	order     []string // MRU order (most recent last; tail is active)
	closed    bool
	parentCtx context.Context
}

// NewManager creates a browser manager.
func NewManager(parentCtx context.Context) *Manager {
	return &Manager{
		browsers:  make(map[string]*Browser),
		parentCtx: parentCtx,
	}
}

// Launch starts a new Chrome browser and registers it as the active browser.
// ctx bounds browser startup without tying the browser's lifetime to ctx.
func (m *Manager) Launch(ctx context.Context, opts LaunchOptions) (*Browser, error) {
	return m.startAndRegister(func() (*Browser, error) {
		return Launch(m.parentCtx, ctx, "", opts)
	})
}

// Connect connects to an existing Chrome browser and registers it as the
// active browser. ctx bounds connection setup without tying the browser's
// lifetime to ctx.
func (m *Manager) Connect(ctx context.Context, url string, opts ConnectOptions) (*Browser, error) {
	return m.startAndRegister(func() (*Browser, error) {
		return Connect(m.parentCtx, ctx, "", url, opts)
	})
}

func (m *Manager) startAndRegister(start func() (*Browser, error)) (*Browser, error) {
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return nil, fmt.Errorf("browser manager is closed")
	}

	b, err := start()
	if err != nil {
		return nil, err
	}
	if err := m.register(b); err != nil {
		b.Close()
		return nil, err
	}
	return b, nil
}

func (m *Manager) register(b *Browser) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return fmt.Errorf("browser manager is closed")
	}
	var newID string
	for {
		newID = id.Generate()
		if _, exists := m.browsers[newID]; !exists {
			break
		}
	}
	b.ID = newID
	m.browsers[newID] = b
	m.order = append(m.order, newID)
	return nil
}

// Get returns a browser by ID without changing which browser is active.
// If the browser's Chrome process has been killed or disconnected, it is
// removed, its resources are closed, and an error is returned.
func (m *Manager) Get(id string) (*Browser, error) {
	m.mu.Lock()
	b, ok := m.browsers[id]
	if !ok {
		m.mu.Unlock()
		return nil, fmt.Errorf("browser %q not found", id)
	}
	if !b.Alive() {
		m.removeLocked(id)
		m.mu.Unlock()
		b.Close()
		return nil, fmt.Errorf("browser %q is no longer running (Chrome process was killed or disconnected)", id)
	}
	m.mu.Unlock()
	return b, nil
}

// Active returns the active browser. Dead browsers are pruned and closed
// automatically. Returns an error if no browser is running.
func (m *Manager) Active() (*Browser, error) {
	m.mu.Lock()
	dead := m.pruneDeadLocked()
	activeID := m.activeIDLocked()
	var active *Browser
	if activeID != "" {
		active = m.browsers[activeID]
	}
	m.mu.Unlock()

	closeBrowsers(dead)
	if active == nil {
		return nil, fmt.Errorf("no browser running — use browser_launch or browser_connect first")
	}
	return active, nil
}

// Resolve returns the browser for the given ID, or the active browser if id is
// empty. Returns an error if no browser is running or the ID is not found.
func (m *Manager) Resolve(id string) (*Browser, error) {
	if id != "" {
		return m.Get(id)
	}
	return m.Active()
}

// Activate makes the given live browser the active browser.
func (m *Manager) Activate(id string) error {
	m.mu.Lock()
	b, ok := m.browsers[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("browser %q not found", id)
	}
	if !b.Alive() {
		m.removeLocked(id)
		m.mu.Unlock()
		b.Close()
		return fmt.Errorf("browser %q is no longer running (Chrome process was killed or disconnected)", id)
	}
	m.touchLocked(id)
	m.mu.Unlock()
	return nil
}

// Close closes a browser by ID. If the closed browser was active, the
// most recently used remaining browser becomes active.
func (m *Manager) Close(id string) error {
	m.mu.Lock()
	b, ok := m.browsers[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("browser %q not found", id)
	}
	m.removeLocked(id)
	m.mu.Unlock()

	b.Close()
	return nil
}

// List returns info about all managed browsers in deterministic MRU order
// (oldest to most recently used). Dead browsers are pruned and closed.
func (m *Manager) List() []BrowserInfo {
	m.mu.Lock()
	dead := m.pruneDeadLocked()
	activeID := m.activeIDLocked()
	infos := make([]BrowserInfo, 0, len(m.order))
	for _, oid := range m.order {
		b, ok := m.browsers[oid]
		if !ok {
			continue
		}
		mode := "launch"
		if b.Mode == ModeConnect {
			mode = "connect"
		}
		infos = append(infos, BrowserInfo{
			ID:     b.ID,
			Mode:   mode,
			Active: b.ID == activeID,
			Tabs:   b.Tabs.Len(),
		})
	}
	m.mu.Unlock()

	closeBrowsers(dead)
	return infos
}

// CloseAll closes all browsers and marks the manager closed so concurrent
// in-flight Launch/Connect calls cannot leak live browsers after shutdown.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	m.closed = true
	browsers := make([]*Browser, 0, len(m.browsers))
	for _, b := range m.browsers {
		browsers = append(browsers, b)
	}
	m.browsers = make(map[string]*Browser)
	m.order = nil
	m.mu.Unlock()

	closeBrowsers(browsers)
}

// ResolveTab resolves a tab from the given browser and tab IDs.
//   - If browserID is set, looks up the tab inside that browser.
//   - If both browserID and tabID are empty, returns the active tab of the
//     active browser.
//   - If browserID is empty and tabID is set, searches the active browser first
//     and then all other live browsers so explicit tab IDs work across multiple
//     managed browsers.
func (m *Manager) ResolveTab(browserID, tabID string) (*tab.Tab, error) {
	_, t, err := m.ResolveTabWithBrowser(browserID, tabID)
	return t, err
}

// ResolveTabWithBrowser resolves both the owning browser and the tab for the
// given browser and tab IDs using the same lookup rules as ResolveTab.
func (m *Manager) ResolveTabWithBrowser(browserID, tabID string) (*Browser, *tab.Tab, error) {
	if browserID != "" {
		b, err := m.Get(browserID)
		if err != nil {
			return nil, nil, err
		}
		t, err := b.Tabs.Resolve(tabID)
		if err != nil {
			return nil, nil, err
		}
		return b, t, nil
	}
	if tabID == "" {
		b, err := m.Active()
		if err != nil {
			return nil, nil, err
		}
		t, err := b.Tabs.Resolve("")
		if err != nil {
			return nil, nil, err
		}
		return b, t, nil
	}

	live := m.liveBrowsersMRU()
	if len(live) == 0 {
		return nil, nil, fmt.Errorf("no browser running — use browser_launch or browser_connect first")
	}
	for _, b := range live {
		if t, err := b.Tabs.Get(tabID); err == nil {
			return b, t, nil
		}
	}
	return nil, nil, fmt.Errorf("tab %q: %w", tabID, tab.ErrNotFound)
}

// CloseTab finds a tab by ID across all live browsers and closes it.
func (m *Manager) CloseTab(tabID string) error {
	live := m.liveBrowsersMRU()
	if len(live) == 0 {
		return fmt.Errorf("no browser running — use browser_launch or browser_connect first")
	}
	for _, b := range live {
		err := b.Tabs.Close(tabID)
		if err == nil {
			return nil
		}
		if !errors.Is(err, tab.ErrNotFound) {
			return err
		}
	}
	return fmt.Errorf("tab %q: %w", tabID, tab.ErrNotFound)
}

// ActivateTab finds a tab by ID across all live browsers, activates it inside
// its owning browser, and makes that browser the active browser so subsequent
// default-tab calls target the activated tab.
func (m *Manager) ActivateTab(tabID string) error {
	live := m.liveBrowsersMRU()
	if len(live) == 0 {
		return fmt.Errorf("no browser running — use browser_launch or browser_connect first")
	}
	for _, b := range live {
		err := b.Tabs.Activate(tabID)
		if err == nil {
			m.mu.Lock()
			if m.browsers[b.ID] != b {
				m.mu.Unlock()
				return fmt.Errorf("browser %q was closed while activating tab %q", b.ID, tabID)
			}
			if !b.Alive() {
				m.removeLocked(b.ID)
				m.mu.Unlock()
				b.Close()
				return fmt.Errorf("browser %q is no longer running (Chrome process was killed or disconnected)", b.ID)
			}
			m.touchLocked(b.ID)
			m.mu.Unlock()
			return nil
		}
		if !errors.Is(err, tab.ErrNotFound) {
			return err
		}
	}
	return fmt.Errorf("tab %q: %w", tabID, tab.ErrNotFound)
}

// BrowserInfo contains summary information about a browser.
type BrowserInfo struct {
	ID     string `json:"id"`
	Mode   string `json:"mode"`
	Active bool   `json:"active"`
	Tabs   int    `json:"tabs"`
}

// liveBrowsersMRU prunes dead browsers (closing them outside the lock) and
// returns live browsers ordered most-recently-used first.
func (m *Manager) liveBrowsersMRU() []*Browser {
	m.mu.Lock()
	dead := m.pruneDeadLocked()
	live := make([]*Browser, 0, len(m.order))
	for i := len(m.order) - 1; i >= 0; i-- {
		if b, ok := m.browsers[m.order[i]]; ok {
			live = append(live, b)
		}
	}
	m.mu.Unlock()

	closeBrowsers(dead)
	return live
}

func closeBrowsers(browsers []*Browser) {
	for _, b := range browsers {
		b.Close()
	}
}

// activeIDLocked returns the active browser ID (the MRU tail), or "" if empty.
// Must be called with m.mu held.
func (m *Manager) activeIDLocked() string {
	if len(m.order) == 0 {
		return ""
	}
	return m.order[len(m.order)-1]
}

// pruneDeadLocked removes all browsers whose Chrome process has been killed or
// disconnected and returns them so the caller can close their resources after
// releasing m.mu. Must be called with m.mu held.
func (m *Manager) pruneDeadLocked() []*Browser {
	var dead []*Browser
	for id, b := range m.browsers {
		if !b.Alive() {
			dead = append(dead, b)
			m.removeLocked(id)
		}
	}
	return dead
}

// touchLocked moves a browser ID to the tail of the MRU order (making it
// active). Must be called with m.mu held.
func (m *Manager) touchLocked(id string) {
	m.removeFromOrderLocked(id)
	m.order = append(m.order, id)
}

// removeLocked removes a browser from the map and MRU order.
// Must be called with m.mu held.
func (m *Manager) removeLocked(id string) {
	delete(m.browsers, id)
	m.removeFromOrderLocked(id)
}

// removeFromOrderLocked removes an ID from the MRU order slice.
// Must be called with m.mu held.
func (m *Manager) removeFromOrderLocked(id string) {
	for i, oid := range m.order {
		if oid == id {
			m.order = append(m.order[:i], m.order[i+1:]...)
			return
		}
	}
}
