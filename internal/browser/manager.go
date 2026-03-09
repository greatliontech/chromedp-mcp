package browser

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"

	"github.com/thegrumpylion/chromedp-mcp/internal/tab"
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
	id := generateID()
	b, err := Launch(m.parentCtx, id, opts)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.browsers[id] = b
	m.activeID = id
	m.order = append(m.order, id)
	m.mu.Unlock()
	return b, nil
}

// Connect connects to an existing Chrome browser and registers it as the
// active browser.
func (m *Manager) Connect(url string) (*Browser, error) {
	id := generateID()
	b, err := Connect(m.parentCtx, id, url)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.browsers[id] = b
	m.activeID = id
	m.order = append(m.order, id)
	m.mu.Unlock()
	return b, nil
}

// Get returns a browser by ID.
func (m *Manager) Get(id string) (*Browser, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	b, ok := m.browsers[id]
	if !ok {
		return nil, fmt.Errorf("browser %q not found", id)
	}
	return b, nil
}

// Active returns the active browser. Returns nil if none exist.
func (m *Manager) Active() *Browser {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.activeID == "" {
		return nil
	}
	return m.browsers[m.activeID]
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
	delete(m.browsers, id)
	m.removeFromOrder(id)
	if m.activeID == id {
		if len(m.order) > 0 {
			m.activeID = m.order[len(m.order)-1]
		} else {
			m.activeID = ""
		}
	}
	return nil
}

// List returns info about all managed browsers.
func (m *Manager) List() []BrowserInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
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

// EnsureBrowser returns the active browser, launching one with defaults
// if none exist.
func (m *Manager) EnsureBrowser() (*Browser, error) {
	b := m.Active()
	if b != nil {
		return b, nil
	}
	return m.Launch(DefaultLaunchOptions())
}

// ResolveTab resolves a tab from the given browser and tab IDs. If
// browserID is empty, uses the active browser (auto-launching if needed).
// If tabID is empty, uses the active tab (auto-creating if needed).
func (m *Manager) ResolveTab(browserID, tabID string) (*tab.Tab, error) {
	var b *Browser
	var err error
	if browserID != "" {
		b, err = m.Get(browserID)
	} else {
		b, err = m.EnsureBrowser()
	}
	if err != nil {
		return nil, err
	}
	return b.Tabs.Resolve(tabID)
}

// BrowserInfo contains summary information about a browser.
type BrowserInfo struct {
	ID     string `json:"id"`
	Mode   string `json:"mode"`
	Active bool   `json:"active"`
	Tabs   int    `json:"tabs"`
}

func (m *Manager) removeFromOrder(id string) {
	for i, oid := range m.order {
		if oid == id {
			m.order = append(m.order[:i], m.order[i+1:]...)
			return
		}
	}
}

func generateID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
