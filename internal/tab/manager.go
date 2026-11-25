package tab

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"

	"github.com/greatliontech/chromedp-mcp/internal/id"
)

// Manager manages the set of open tabs within a single browser and tracks
// which tab is active.
//
// The active tab is always the tail of order (order[len(order)-1]), so there
// is a single source of truth for MRU ordering and active selection.
type Manager struct {
	mu        sync.RWMutex
	tabs      map[string]*Tab
	order     []string // MRU order (most recent last; tail is active)
	closed    bool
	parentCtx context.Context
}

// NewManager creates a tab manager for tabs within the given browser context.
func NewManager(parentCtx context.Context) *Manager {
	return &Manager{
		tabs:      make(map[string]*Tab),
		parentCtx: parentCtx,
	}
}

// NewTab creates a new tab and makes it the active tab. ctx bounds tab
// initialization (e.g. MCP request cancellation) without tying the tab's
// lifetime to ctx.
func (m *Manager) NewTab(ctx context.Context) (*Tab, error) {
	m.mu.RLock()
	closed := m.closed
	m.mu.RUnlock()
	if closed {
		return nil, fmt.Errorf("tab manager is closed")
	}

	t, err := New(m.parentCtx, ctx, "")
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		t.Close()
		return nil, fmt.Errorf("tab manager is closed")
	}
	m.registerTabLocked(t)
	m.mu.Unlock()
	return t, nil
}

// AttachTarget attaches to an existing Chrome target (using WithDetachOnCancel
// so failed attaches, concurrent dedup, and browser disconnect preserve the
// target, while Close closes it explicitly via CloseTarget) and registers it
// as the active tab. If the target is already tracked, returns the existing Tab.
func (m *Manager) AttachTarget(ctx context.Context, targetID target.ID) (*Tab, error) {
	m.mu.RLock()
	if m.closed {
		m.mu.RUnlock()
		return nil, fmt.Errorf("tab manager is closed")
	}
	for _, existing := range m.tabs {
		if existing.targetID == targetID {
			m.mu.RUnlock()
			return existing, nil
		}
	}
	m.mu.RUnlock()

	t, err := Attach(m.parentCtx, ctx, "", targetID)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		t.Close()
		return nil, fmt.Errorf("tab manager is closed")
	}
	for _, existing := range m.tabs {
		if existing.targetID == targetID {
			m.mu.Unlock()
			t.Close()
			return existing, nil
		}
	}
	m.registerTabLocked(t)
	m.mu.Unlock()
	return t, nil
}

// registerTabLocked assigns a unique tab ID, inserts t at the MRU tail, and
// watches for external target detachment (e.g. user closing an adopted tab in
// Chrome or page calling window.close()) so dead tabs are pruned automatically.
// Must be called with m.mu held for writing.
func (m *Manager) registerTabLocked(t *Tab) {
	var newID string
	for {
		newID = id.Generate()
		if _, exists := m.tabs[newID]; !exists {
			break
		}
	}
	t.ID = newID
	m.tabs[newID] = t
	m.order = append(m.order, newID)

	if detachedCh, err := chromedp.Detached(t.ctx); err == nil {
		go func() {
			select {
			case <-detachedCh:
				m.pruneDetached(newID, t)
			case <-t.ctx.Done():
			}
		}()
	}
}

// pruneDetached removes an externally detached tab from the manager and
// cancels its context so dead targets never linger as ghost active tabs.
func (m *Manager) pruneDetached(id string, t *Tab) {
	m.mu.Lock()
	if m.tabs[id] != t {
		m.mu.Unlock()
		return
	}
	delete(m.tabs, id)
	m.removeFromOrder(id)
	m.mu.Unlock()
	t.Close()
}

// Get returns a tab by ID. Returns an error wrapping ErrNotFound if not found.
func (m *Manager) Get(id string) (*Tab, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.tabs[id]
	if !ok {
		return nil, fmt.Errorf("tab %q: %w", id, ErrNotFound)
	}
	return t, nil
}

// Active returns the active tab. Returns nil if no tabs exist.
func (m *Manager) Active() *Tab {
	m.mu.RLock()
	defer m.mu.RUnlock()
	activeID := m.activeIDLocked()
	if activeID == "" {
		return nil
	}
	return m.tabs[activeID]
}

// Activate sets a tab as the active tab by ID.
func (m *Manager) Activate(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.tabs[id]; !ok {
		return fmt.Errorf("tab %q: %w", id, ErrNotFound)
	}
	m.touchLocked(id)
	return nil
}

// Close closes a tab by ID and removes it from the manager. If the closed
// tab was the active tab, the most recently used remaining tab becomes active.
// For adopted tabs this also explicitly closes the Chrome target (unlike
// CloseAll, which detaches adopted tabs on browser disconnect).
func (m *Manager) Close(id string) error {
	m.mu.Lock()
	t, ok := m.tabs[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("tab %q: %w", id, ErrNotFound)
	}
	delete(m.tabs, id)
	m.removeFromOrder(id)
	m.mu.Unlock()

	t.CloseTarget(m.parentCtx)
	return nil
}

// List returns info about all open tabs in deterministic MRU order
// (oldest to most recently used). Returns an error if ctx is cancelled.
func (m *Manager) List(ctx context.Context) ([]TabInfo, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Snapshot tab pointers in deterministic order under the lock, then
	// release before doing CDP I/O.
	m.mu.RLock()
	tabs := make([]*Tab, 0, len(m.order))
	for _, oid := range m.order {
		if t, ok := m.tabs[oid]; ok {
			tabs = append(tabs, t)
		}
	}
	activeID := m.activeIDLocked()
	m.mu.RUnlock()

	infos := make([]TabInfo, 0, len(tabs))
	anyActive := false
	for _, t := range tabs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		url, title, err := t.Info(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if errors.Is(err, chromedp.ErrTargetDetached) || t.ctx.Err() != nil {
				m.pruneDetached(t.ID, t)
				continue
			}
		}
		isActive := t.ID == activeID
		if isActive {
			anyActive = true
		}
		infos = append(infos, TabInfo{
			ID:     t.ID,
			URL:    url,
			Title:  title,
			Active: isActive,
		})
	}
	if !anyActive && len(infos) > 0 {
		infos[len(infos)-1].Active = true
	}
	return infos, nil
}

// CloseAll closes all tabs and marks the manager closed so concurrent in-flight
// NewTab calls cannot leak live targets after shutdown.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	m.closed = true
	tabs := m.tabs
	m.tabs = make(map[string]*Tab)
	m.order = nil
	m.mu.Unlock()

	for _, t := range tabs {
		t.Close()
	}
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
// Returns an error if no tabs exist.
func (m *Manager) Resolve(id string) (*Tab, error) {
	if id != "" {
		return m.Get(id)
	}
	t := m.Active()
	if t != nil {
		return t, nil
	}
	return nil, fmt.Errorf("no open tabs — use tab_new to create one")
}

// activeIDLocked returns the active tab ID (the MRU tail), or "" when empty.
// Must be called with m.mu held (read or write).
func (m *Manager) activeIDLocked() string {
	if len(m.order) == 0 {
		return ""
	}
	return m.order[len(m.order)-1]
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
