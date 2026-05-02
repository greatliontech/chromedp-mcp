package collector

import (
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
)

// URLChangeKind distinguishes how a navigation happened.
type URLChangeKind string

const (
	// URLChangeCrossDocument is a normal navigation that loaded a new
	// document (full page load, server response, fresh DOM).
	URLChangeCrossDocument URLChangeKind = "cross_document"
	// URLChangeSameDocument is an SPA-style URL change via the History
	// API (pushState/replaceState/popstate). The DOM is not reloaded.
	URLChangeSameDocument URLChangeKind = "same_document"
)

// URLChangeEntry records a top-frame URL transition.
type URLChangeEntry struct {
	From      string        `json:"from"`
	To        string        `json:"to"`
	Kind      URLChangeKind `json:"kind"`
	Timestamp time.Time     `json:"timestamp"`
}

// URLChanges collects top-frame URL transitions for a tab. Subframe
// navigations are intentionally ignored — they don't represent the
// user-visible URL of the page.
type URLChanges struct {
	mu      sync.Mutex
	buf     *RingBuffer[URLChangeEntry]
	mainID  cdp.FrameID
	lastURL string
}

// NewURLChanges creates a URL change collector with the given buffer size.
func NewURLChanges(maxSize int) *URLChanges {
	return &URLChanges{
		buf: NewRingBuffer[URLChangeEntry](maxSize),
	}
}

// HandleFrameNavigated records cross-document navigations of the top frame.
// Subframe navigations are ignored. The first navigation establishes the
// "last URL" baseline without producing an entry — only transitions emit.
func (u *URLChanges) HandleFrameNavigated(ev *page.EventFrameNavigated) {
	if ev.Frame == nil || ev.Frame.ParentID != "" {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.mainID == "" {
		u.mainID = ev.Frame.ID
	} else if ev.Frame.ID != u.mainID {
		// Different top-level frame ID can happen when the target swaps
		// (e.g., back-forward cache restore from a different tab). We
		// continue tracking transitions regardless of the ID change.
		u.mainID = ev.Frame.ID
	}
	from := u.lastURL
	to := ev.Frame.URL
	u.lastURL = to
	if from == "" || from == to {
		return
	}
	u.buf.Add(URLChangeEntry{
		From:      from,
		To:        to,
		Kind:      URLChangeCrossDocument,
		Timestamp: time.Now(),
	})
}

// HandleNavigatedWithinDocument records same-document URL changes
// (History API: pushState, replaceState, popstate). Drops events that
// arrive before the main frame's first frameNavigated has established
// the baseline — without this guard a subframe pushState fired before
// the main frame was registered would be incorrectly recorded as a
// top-frame URL change and pollute lastURL.
func (u *URLChanges) HandleNavigatedWithinDocument(ev *page.EventNavigatedWithinDocument) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.mainID == "" {
		// No main frame baseline yet; we can't be sure this is the
		// top frame. Drop the event rather than risk poisoning
		// lastURL with a subframe URL.
		return
	}
	if ev.FrameID != u.mainID {
		return
	}
	from := u.lastURL
	to := ev.URL
	u.lastURL = to
	if from == "" || from == to {
		return
	}
	u.buf.Add(URLChangeEntry{
		From:      from,
		To:        to,
		Kind:      URLChangeSameDocument,
		Timestamp: time.Now(),
	})
}

// CountSince returns the number of URL changes recorded at or after t.
func (u *URLChanges) CountSince(t time.Time) int {
	entries := u.buf.Peek(nil)
	n := 0
	for _, e := range entries {
		if !e.Timestamp.Before(t) {
			n++
		}
	}
	return n
}

// Clear removes the buffered entries but preserves the last-URL and
// main-frame-ID baselines. The page's actual URL didn't change just
// because the caller drained the history; preserving the baseline
// means the next URL transition is recorded with the real previous URL,
// not from="".
func (u *URLChanges) Clear() {
	u.buf.Clear()
}
