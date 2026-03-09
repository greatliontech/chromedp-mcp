package tab

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
)

// Manager manages the set of open tabs within a single browser and tracks
// which tab is active.
type Manager struct {
	mu        sync.RWMutex
	tabs      map[string]*Tab
	activeID  string
	order     []string // MRU order (most recent last)
	parentCtx context.Context
}

// NewManager creates a tab manager for tabs within the given browser context.
func NewManager(parentCtx context.Context) *Manager {
	return &Manager{
		tabs:      make(map[string]*Tab),
		parentCtx: parentCtx,
	}
}

// NewTab creates a new tab, optionally navigating to a URL. The new tab
// becomes the active tab.
func (m *Manager) NewTab() (*Tab, error) {
	id := generateID()
	t, err := New(m.parentCtx, id)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.tabs[id] = t
	m.activeID = id
	m.order = append(m.order, id)
	m.mu.Unlock()
	return t, nil
}

// Get returns a tab by ID. Returns an error if not found.
func (m *Manager) Get(id string) (*Tab, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.tabs[id]
	if !ok {
		return nil, fmt.Errorf("tab %q not found", id)
	}
	return t, nil
}

// Active returns the active tab. Returns nil if no tabs exist.
func (m *Manager) Active() *Tab {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.activeID == "" {
		return nil
	}
	return m.tabs[m.activeID]
}

// Activate sets a tab as the active tab by ID.
func (m *Manager) Activate(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.tabs[id]; !ok {
		return fmt.Errorf("tab %q not found", id)
	}
	m.activeID = id
	m.touchLocked(id)
	return nil
}

// Close closes a tab by ID and removes it from the manager. If the closed
// tab was the active tab, the most recently used remaining tab becomes active.
func (m *Manager) Close(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tabs[id]
	if !ok {
		return fmt.Errorf("tab %q not found", id)
	}
	t.Close()
	delete(m.tabs, id)
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

// List returns info about all open tabs.
func (m *Manager) List() []TabInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	infos := make([]TabInfo, 0, len(m.tabs))
	for _, t := range m.tabs {
		url, _ := t.URL()
		title, _ := t.Title()
		infos = append(infos, TabInfo{
			ID:     t.ID,
			URL:    url,
			Title:  title,
			Active: t.ID == m.activeID,
		})
	}
	return infos
}

// CloseAll closes all tabs.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.tabs {
		t.Close()
	}
	m.tabs = make(map[string]*Tab)
	m.activeID = ""
	m.order = nil
}

// Len returns the number of open tabs.
func (m *Manager) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.tabs)
}

// TabInfo contains summary information about a tab.
type TabInfo struct {
	ID     string `json:"id"`
	URL    string `json:"url"`
	Title  string `json:"title"`
	Active bool   `json:"active"`
}

// Resolve returns the tab for the given ID, or the active tab if id is empty.
// If no tabs exist, it creates one automatically.
func (m *Manager) Resolve(id string) (*Tab, error) {
	if id != "" {
		return m.Get(id)
	}
	t := m.Active()
	if t != nil {
		return t, nil
	}
	// Auto-create a tab if none exist.
	return m.NewTab()
}

// touchLocked moves an ID to the end of the MRU order. Must be called with mu held.
func (m *Manager) touchLocked(id string) {
	m.removeFromOrder(id)
	m.order = append(m.order, id)
}

// removeFromOrder removes an ID from the order slice. Must be called with mu held.
func (m *Manager) removeFromOrder(id string) {
	for i, oid := range m.order {
		if oid == id {
			m.order = append(m.order[:i], m.order[i+1:]...)
			return
		}
	}
}

// generateID produces a short random hex ID for a tab.
func generateID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
