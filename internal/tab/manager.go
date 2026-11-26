package tab

import (
	"context"
	"errors"
	"fmt"
	"slices"
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
	attaching map[target.ID]chan struct{} // in-flight Attach dedup by Chrome target ID
	order     []string                    // MRU order (most recent last; tail is active)
	closed    bool
	parentCtx context.Context
}

// NewManager creates a tab manager for tabs within the given browser context.
func NewManager(parentCtx context.Context) *Manager {
	return &Manager{
		tabs:      make(map[string]*Tab),
		attaching: make(map[target.ID]chan struct{}),
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

	t, err := New(m.parentCtx, ctx)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		t.Close()
		return nil, fmt.Errorf("tab manager is closed")
	}
	if !m.registerTabLocked(t, true) {
		m.mu.Unlock()
		t.Close()
		return nil, chromedp.ErrTargetDetached
	}
	m.mu.Unlock()
	return t, nil
}

// HasTarget reports whether a Chrome target ID is already tracked or currently
// being attached by this manager.
func (m *Manager) HasTarget(targetID target.ID) bool {
	if targetID == "" {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.findByTargetLocked(targetID) != nil {
		return true
	}
	_, busy := m.attaching[targetID]
	return busy
}

// AttachTarget attaches to an existing Chrome target and registers it as the
// active tab. If the target is already tracked, returns the existing Tab.
func (m *Manager) AttachTarget(ctx context.Context, targetID target.ID) (*Tab, error) {
	return m.attachTarget(ctx, targetID, true)
}

// AttachBackgroundTarget attaches to a newly opened popup / target="_blank"
// target without stealing active focus from an existing active tab (it becomes
// active only if no other tab is currently open).
func (m *Manager) AttachBackgroundTarget(ctx context.Context, targetID target.ID) (*Tab, error) {
	return m.attachTarget(ctx, targetID, false)
}

func (m *Manager) attachTarget(ctx context.Context, targetID target.ID, makeActive bool) (*Tab, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if targetID == "" {
		return nil, fmt.Errorf("tab attach: target ID is required")
	}

	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return nil, fmt.Errorf("tab manager is closed")
		}
		if existing := m.findByTargetLocked(targetID); existing != nil {
			m.mu.Unlock()
			return existing, nil
		}
		if waitCh, busy := m.attaching[targetID]; busy {
			m.mu.Unlock()
			select {
			case <-waitCh:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		done := make(chan struct{})
		m.attaching[targetID] = done
		m.mu.Unlock()

		t, err := m.runAttachAndRegister(ctx, targetID, makeActive, done)
		return t, err
	}
}

func (m *Manager) runAttachAndRegister(ctx context.Context, targetID target.ID, makeActive bool, done chan struct{}) (*Tab, error) {
	cleaned := false
	cleanupAttachingLocked := func() {
		if !cleaned {
			delete(m.attaching, targetID)
			close(done)
			cleaned = true
		}
	}
	defer func() {
		if !cleaned {
			m.mu.Lock()
			cleanupAttachingLocked()
			m.mu.Unlock()
		}
	}()

	t, err := Attach(m.parentCtx, ctx, targetID)

	m.mu.Lock()
	cleanupAttachingLocked()
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	if m.closed {
		m.mu.Unlock()
		t.Close()
		return nil, fmt.Errorf("tab manager is closed")
	}
	if existing := m.findByTargetLocked(targetID); existing != nil {
		m.mu.Unlock()
		t.Close()
		return existing, nil
	}
	if !m.registerTabLocked(t, makeActive) {
		m.mu.Unlock()
		t.Close()
		return nil, chromedp.ErrTargetDetached
	}
	m.mu.Unlock()
	return t, nil
}

func (m *Manager) findByTargetLocked(targetID target.ID) *Tab {
	for _, existing := range m.tabs {
		if existing.targetID == targetID {
			return existing
		}
	}
	return nil
}

// registerTabLocked assigns a unique tab ID, inserts t into m.order (at tail if
// makeActive or empty, else at head so background popups do not steal active
// focus), and watches for external target detachment so dead tabs are pruned.
// Returns false without registering if t is already detached.
// Must be called with m.mu held for writing.
func (m *Manager) registerTabLocked(t *Tab, makeActive bool) bool {
	detachedCh, detErr := chromedp.Detached(t.ctx)
	if detErr == nil {
		select {
		case <-detachedCh:
			return false
		default:
		}
	}

	var newID string
	for {
		newID = id.Generate()
		if _, exists := m.tabs[newID]; !exists {
			break
		}
	}
	t.ID = newID
	m.tabs[newID] = t
	if makeActive || len(m.order) == 0 {
		m.order = append(m.order, newID)
	} else {
		m.order = slices.Insert(m.order, 0, newID)
	}

	if detErr == nil {
		go func() {
			select {
			case <-detachedCh:
				m.pruneDetached(newID, t)
			case <-t.ctx.Done():
			}
		}()
	}
	return true
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
	m.removeFromOrderLocked(id)
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

// Activate focuses the tab in Chrome via Page.bringToFront and promotes it to
// the active tab in MRU order.
func (m *Manager) Activate(ctx context.Context, id string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.RLock()
	t, ok := m.tabs[id]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("tab %q: %w", id, ErrNotFound)
	}

	if err := t.BringToFront(ctx); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, chromedp.ErrTargetDetached) || t.ctx.Err() != nil {
			m.pruneDetached(id, t)
			return fmt.Errorf("tab %q: %w", id, ErrNotFound)
		}
		return fmt.Errorf("bring tab %q to front: %w", id, err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.tabs[id] != t {
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
	m.removeFromOrderLocked(id)
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
// NewTab or Attach calls cannot leak live targets after shutdown.
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

func (m *Manager) activeIDLocked() string {
	if len(m.order) == 0 {
		return ""
	}
	return m.order[len(m.order)-1]
}

func (m *Manager) touchLocked(id string) {
	m.removeFromOrderLocked(id)
	m.order = append(m.order, id)
}

func (m *Manager) removeFromOrderLocked(id string) {
	if i := slices.Index(m.order, id); i >= 0 {
		m.order = slices.Delete(m.order, i, i+1)
	}
}
