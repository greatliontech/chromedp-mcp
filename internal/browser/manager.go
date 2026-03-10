package browser

import (
	"context"
	"fmt"
	"sync"

	"github.com/greatliontech/chromedp-mcp/internal/id"
	"github.com/greatliontech/chromedp-mcp/internal/tab"
)

// Manager manages multiple browser instances and tracks the active one.
type Manager struct {
	mu        sync.RWMutex
	browsers  map[string]*Browser
	activeID  string
	order     []string // MRU order (most recent last)
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
func (m *Manager) Launch(opts LaunchOptions) (*Browser, error) {
	newID := id.Generate()
	b, err := Launch(m.parentCtx, newID, opts)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.browsers[newID] = b
	m.activeID = newID
	m.order = append(m.order, newID)
	m.mu.Unlock()
	return b, nil
}

// Connect connects to an existing Chrome browser and registers it as the
// active browser.
func (m *Manager) Connect(url string, opts ConnectOptions) (*Browser, error) {
	newID := id.Generate()
	b, err := Connect(m.parentCtx, newID, url, opts)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.browsers[newID] = b
	m.activeID = newID
	m.order = append(m.order, newID)
	m.mu.Unlock()
	return b, nil
}

// Get returns a browser by ID. If the browser's Chrome process has
// been killed or disconnected, it is removed and an error is returned.
func (m *Manager) Get(id string) (*Browser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.browsers[id]
	if !ok {
		return nil, fmt.Errorf("browser %q not found", id)
	}
	if !b.Alive() {
		m.removeLocked(id)
		return nil, fmt.Errorf("browser %q is no longer running (Chrome process was killed or disconnected)", id)
	}
	return b, nil
}

// Active returns the active browser. Dead browsers are pruned automatically.
// Returns an error if no browser is running.
func (m *Manager) Active() (*Browser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneDeadLocked()
	if m.activeID == "" {
		return nil, fmt.Errorf("no browser running — use browser_launch or browser_connect first")
	}
	return m.browsers[m.activeID], nil
}

// Close closes a browser by ID. If the closed browser was active, the
// most recently used remaining browser becomes active.
func (m *Manager) Close(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.browsers[id]
	if !ok {
		return fmt.Errorf("browser %q not found", id)
	}
	b.Close()
	m.removeLocked(id)
	return nil
}

// List returns info about all managed browsers. Dead browsers are pruned.
func (m *Manager) List() []BrowserInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneDeadLocked()
	infos := make([]BrowserInfo, 0, len(m.browsers))
	for _, b := range m.browsers {
		mode := "launch"
		if b.Mode == ModeConnect {
			mode = "connect"
		}
		infos = append(infos, BrowserInfo{
			ID:     b.ID,
			Mode:   mode,
			Active: b.ID == m.activeID,
			Tabs:   b.Tabs.Len(),
		})
	}
	return infos
}

// CloseAll closes all browsers.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, b := range m.browsers {
		b.Close()
	}
	m.browsers = make(map[string]*Browser)
	m.activeID = ""
	m.order = nil
}

// ResolveTab resolves a tab from the given browser and tab IDs.
// If browserID is empty, uses the active browser.
// If tabID is empty, uses the active tab.
// Returns an error if no browser is running or no tabs are open.
func (m *Manager) ResolveTab(browserID, tabID string) (*tab.Tab, error) {
	var b *Browser
	var err error
	if browserID != "" {
		b, err = m.Get(browserID)
	} else {
		b, err = m.Active()
	}
	if err != nil {
		return nil, err
	}
	return b.Tabs.Resolve(tabID)
}

// CloseTab finds a tab by ID across all browsers and closes it.
func (m *Manager) CloseTab(tabID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneDeadLocked()
	for _, b := range m.browsers {
		if _, err := b.Tabs.Get(tabID); err == nil {
			return b.Tabs.Close(tabID)
		}
	}
	return fmt.Errorf("tab %q not found in any browser", tabID)
}

// ActivateTab finds a tab by ID across all browsers and activates it.
func (m *Manager) ActivateTab(tabID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneDeadLocked()
	for _, b := range m.browsers {
		if _, err := b.Tabs.Get(tabID); err == nil {
			return b.Tabs.Activate(tabID)
		}
	}
	return fmt.Errorf("tab %q not found in any browser", tabID)
}

// BrowserInfo contains summary information about a browser.
type BrowserInfo struct {
	ID     string `json:"id"`
	Mode   string `json:"mode"`
	Active bool   `json:"active"`
	Tabs   int    `json:"tabs"`
}

// pruneDeadLocked removes all browsers whose Chrome process has been
// killed or disconnected. Must be called with m.mu held.
func (m *Manager) pruneDeadLocked() {
	for id, b := range m.browsers {
		if !b.Alive() {
			m.removeLocked(id)
		}
	}
}

// removeLocked removes a browser from the map and MRU order, and updates
// the active browser if needed. Must be called with m.mu held.
func (m *Manager) removeLocked(id string) {
	delete(m.browsers, id)
	for i, oid := range m.order {
		if oid == id {
			m.order = append(m.order[:i], m.order[i+1:]...)
			break
		}
	}
	if m.activeID == id {
		if len(m.order) > 0 {
			m.activeID = m.order[len(m.order)-1]
		} else {
			m.activeID = ""
		}
	}
}
