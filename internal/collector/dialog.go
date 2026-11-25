package collector

import (
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
)

// DialogState represents a currently open JavaScript modal dialog
// (alert, confirm, prompt, or beforeunload) on a tab.
type DialogState struct {
	Type              string    `json:"type"`
	Message           string    `json:"message"`
	DefaultPrompt     string    `json:"default_prompt,omitempty"`
	URL               string    `json:"url,omitempty"`
	HasBrowserHandler bool      `json:"has_browser_handler"`
	OpenedAt          time.Time `json:"opened_at"`
}

// Dialog tracks the active JavaScript modal dialog on a tab from CDP
// Page.javascriptDialogOpening and Page.javascriptDialogClosed events.
// Chrome permits at most one active JS modal dialog per page target.
type Dialog struct {
	mu      sync.Mutex
	current *DialogState
	frameID cdp.FrameID
	seq     uint64
}

// NewDialog creates an empty Dialog state tracker.
func NewDialog() *Dialog {
	return &Dialog{}
}

// HandleOpening records a newly opened JavaScript dialog.
func (d *Dialog) HandleOpening(ev *page.EventJavascriptDialogOpening) {
	if ev == nil {
		return
	}
	d.mu.Lock()
	d.seq++
	d.current = &DialogState{
		Type:              string(ev.Type),
		Message:           ev.Message,
		DefaultPrompt:     ev.DefaultPrompt,
		URL:               ev.URL,
		HasBrowserHandler: ev.HasBrowserHandler,
		OpenedAt:          time.Now(),
	}
	d.frameID = ev.FrameID
	d.mu.Unlock()
}

// HandleClosed clears the active dialog when the matching dialog closes.
func (d *Dialog) HandleClosed(ev *page.EventJavascriptDialogClosed) {
	if ev == nil {
		return
	}
	d.mu.Lock()
	if d.current != nil && (ev.FrameID == "" || d.frameID == "" || ev.FrameID == d.frameID) {
		d.current = nil
		d.frameID = ""
	}
	d.mu.Unlock()
}

// HandleFrameNavigated clears the active dialog if a top-level navigation or a
// navigation of the dialog's owning frame discards the dialog without emitting
// Page.javascriptDialogClosed.
func (d *Dialog) HandleFrameNavigated(ev *page.EventFrameNavigated) {
	if ev == nil || ev.Frame == nil {
		return
	}
	d.mu.Lock()
	if d.current != nil && (ev.Frame.ParentID == "" || (d.frameID != "" && ev.Frame.ID == d.frameID)) {
		d.current = nil
		d.frameID = ""
	}
	d.mu.Unlock()
}

// HandleFrameDetached clears the active dialog if its owning frame detaches
// without emitting Page.javascriptDialogClosed.
func (d *Dialog) HandleFrameDetached(ev *page.EventFrameDetached) {
	if ev == nil || ev.FrameID == "" {
		return
	}
	d.mu.Lock()
	if d.current != nil && d.frameID != "" && ev.FrameID == d.frameID {
		d.current = nil
		d.frameID = ""
	}
	d.mu.Unlock()
}

// Snapshot returns a defensive copy of the currently open dialog state along
// with its monotonic opening sequence number, or (nil, 0) if no dialog is open.
func (d *Dialog) Snapshot() (*DialogState, uint64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.current == nil {
		return nil, 0
	}
	cp := *d.current
	return &cp, d.seq
}

// Current returns a copy of the currently open dialog state, or nil if no
// dialog is open.
func (d *Dialog) Current() *DialogState {
	cur, _ := d.Snapshot()
	return cur
}

// ClearIfSeq clears the active dialog only if it is still the same opening
// generation identified by seq. Used after Page.handleJavaScriptDialog succeeds
// so callers do not observe a stale open dialog before the async
// Page.javascriptDialogClosed event arrives, without clobbering a chained
// subsequent dialog that already opened.
func (d *Dialog) ClearIfSeq(seq uint64) {
	if seq == 0 {
		return
	}
	d.mu.Lock()
	if d.current != nil && d.seq == seq {
		d.current = nil
		d.frameID = ""
	}
	d.mu.Unlock()
}

// Open reports whether a JavaScript dialog is currently open on the tab.
func (d *Dialog) Open() bool {
	d.mu.Lock()
	open := d.current != nil
	d.mu.Unlock()
	return open
}

// Clear resets the tracked dialog state.
func (d *Dialog) Clear() {
	d.mu.Lock()
	d.current = nil
	d.frameID = ""
	d.mu.Unlock()
}
