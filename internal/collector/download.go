package collector

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/browser"
)

// DownloadState represents the state of a download.
type DownloadState string

const (
	DownloadStateInProgress DownloadState = "inProgress"
	DownloadStateCompleted  DownloadState = "completed"
	DownloadStateCanceled   DownloadState = "canceled"
)

// DownloadEntry represents a tracked download.
type DownloadEntry struct {
	GUID              string        `json:"guid"`
	URL               string        `json:"url"`
	SuggestedFilename string        `json:"suggested_filename"`
	State             DownloadState `json:"state"`
	ReceivedBytes     float64       `json:"received_bytes"`
	TotalBytes        float64       `json:"total_bytes"`
	Path              string        `json:"path,omitempty"`
	StartTime         time.Time     `json:"start_time"`
	EndTime           time.Time     `json:"end_time,omitempty"`
}

// Download collects download events from the browser.
// Lock order: always d.mu before d.buf.mu (RingBuffer never acquires d.mu).
// renameMu is acquired only outside d.mu by async rename goroutines to
// serialize non-hardlink filesystem fallbacks without blocking CDP listeners.
type Download struct {
	mu           sync.Mutex
	renameCond   *sync.Cond
	renameMu     sync.Mutex
	pending      map[string]*DownloadEntry
	pendingOrder []string
	maxPending   int
	seen         map[string]struct{}
	seenOrder    []string
	maxSeen      int
	buf          *RingBuffer[DownloadEntry]
	downloadDir  string
	renaming     int
	gen          uint64
}

// NewDownload creates a download collector with the given buffer size
// and download directory. The download directory is used to rename
// GUID-named files to their suggested filenames on completion.
func NewDownload(maxSize int, downloadDir string) *Download {
	if maxSize < 1 {
		maxSize = 1
	}
	d := &Download{
		pending:     make(map[string]*DownloadEntry),
		maxPending:  max(maxSize*2, 200),
		seen:        make(map[string]struct{}),
		maxSeen:     max(maxSize*2, 256),
		buf:         NewRingBuffer[DownloadEntry](maxSize),
		downloadDir: downloadDir,
	}
	d.renameCond = sync.NewCond(&d.mu)
	return d
}

// HandleDownloadWillBegin records a new download. Duplicate GUIDs (e.g.
// re-delivered events) are ignored.
func (d *Download) HandleDownloadWillBegin(ev *browser.EventDownloadWillBegin) {
	if ev == nil || ev.GUID == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.pending[ev.GUID]; ok {
		return
	}
	if _, done := d.seen[ev.GUID]; done {
		return
	}
	if len(d.pendingOrder) >= d.maxPending {
		oldest := d.pendingOrder[0]
		copy(d.pendingOrder, d.pendingOrder[1:])
		d.pendingOrder[len(d.pendingOrder)-1] = ""
		d.pendingOrder = d.pendingOrder[:len(d.pendingOrder)-1]
		delete(d.pending, oldest)
	}
	d.pending[ev.GUID] = &DownloadEntry{
		GUID:              ev.GUID,
		URL:               ev.URL,
		SuggestedFilename: ev.SuggestedFilename,
		State:             DownloadStateInProgress,
		StartTime:         time.Now(),
	}
	d.pendingOrder = append(d.pendingOrder, ev.GUID)
}

// HandleDownloadProgress updates download progress or completes a download.
// Disk renames run off chromedp's event dispatcher goroutine so filesystem
// latency cannot stall CDP event delivery.
func (d *Download) HandleDownloadProgress(ev *browser.EventDownloadProgress) {
	if ev == nil || ev.GUID == "" {
		return
	}
	d.mu.Lock()
	entry, ok := d.pending[ev.GUID]
	if !ok {
		d.mu.Unlock()
		return
	}

	entry.ReceivedBytes = ev.ReceivedBytes
	entry.TotalBytes = ev.TotalBytes

	switch ev.State {
	case browser.DownloadProgressStateInProgress:
		d.mu.Unlock()
		return
	case browser.DownloadProgressStateCompleted:
		entry.State = DownloadStateCompleted
		entry.EndTime = time.Now()
		snap := *entry
		delete(d.pending, ev.GUID)
		d.removePendingOrderLocked(ev.GUID)
		d.markSeenLocked(ev.GUID)
		if d.downloadDir == "" {
			d.buf.Add(snap)
			d.mu.Unlock()
			return
		}
		// Perform filesystem rename asynchronously off the CDP event loop.
		// The entry is already removed from pending and recorded in seen so
		// duplicate events are ignored and pending eviction cannot drop it.
		// Readers wait on renameCond until finishRename adds snap to buf.
		d.renaming++
		dir := d.downloadDir
		gen := d.gen
		d.mu.Unlock()

		go func() {
			d.renameMu.Lock()
			path := finalizeDownloadFile(dir, snap.GUID, snap.SuggestedFilename)
			d.renameMu.Unlock()
			snap.Path = path
			d.finishRename(gen, snap)
		}()
		return
	case browser.DownloadProgressStateCanceled:
		entry.State = DownloadStateCanceled
		entry.EndTime = time.Now()
		snap := *entry
		delete(d.pending, ev.GUID)
		d.removePendingOrderLocked(ev.GUID)
		d.markSeenLocked(ev.GUID)
		d.buf.Add(snap)
		d.mu.Unlock()
		return
	default:
		// Unknown state (future Chrome versions may add new states).
		// Release the lock and ignore — don't add to the buffer.
		d.mu.Unlock()
		return
	}
}

func (d *Download) finishRename(gen uint64, entry DownloadEntry) {
	d.mu.Lock()
	defer func() {
		d.renaming--
		d.renameCond.Broadcast()
		d.mu.Unlock()
	}()
	if d.gen != gen {
		return
	}
	d.buf.Add(entry)
}

func (d *Download) waitRenamesLocked() {
	for d.renaming > 0 {
		d.renameCond.Wait()
	}
}

func (d *Download) removePendingOrderLocked(guid string) {
	d.pendingOrder = slices.DeleteFunc(d.pendingOrder, func(x string) bool {
		return x == guid
	})
}

func (d *Download) markSeenLocked(guid string) {
	if _, ok := d.seen[guid]; ok {
		return
	}
	if len(d.seenOrder) >= d.maxSeen {
		oldest := d.seenOrder[0]
		copy(d.seenOrder, d.seenOrder[1:])
		d.seenOrder[len(d.seenOrder)-1] = ""
		d.seenOrder = d.seenOrder[:len(d.seenOrder)-1]
		delete(d.seen, oldest)
	}
	d.seen[guid] = struct{}{}
	d.seenOrder = append(d.seenOrder, guid)
}

// finalizeDownloadFile renames Chrome's GUID-named file in dir to a safe,
// non-colliding filename derived from suggested. Returns the resulting file
// path, or "" if the GUID is invalid or the file does not exist on disk.
func finalizeDownloadFile(dir, guid, suggested string) string {
	if guid == "" || safeDownloadName(guid) != guid {
		return ""
	}
	guidPath := filepath.Join(dir, guid)
	if _, err := os.Stat(guidPath); err != nil {
		return ""
	}
	name := safeDownloadName(suggested)
	if name == "" {
		return guidPath
	}

	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	const maxAttempts = 1000
	for i := 0; i < maxAttempts; i++ {
		candidateName := name
		if i > 0 {
			if stem == "" {
				candidateName = fmt.Sprintf("%s-%d", name, i)
			} else {
				candidateName = fmt.Sprintf("%s-%d%s", stem, i, ext)
			}
		}
		destPath := filepath.Join(dir, candidateName)
		if destPath == guidPath {
			return guidPath
		}
		// Prefer os.Link + os.Remove so an existing file is never overwritten
		// atomically on POSIX filesystems.
		err := os.Link(guidPath, destPath)
		if err == nil {
			_ = os.Remove(guidPath)
			return destPath
		}
		if errors.Is(err, os.ErrExist) {
			continue
		}
		// Fallback for filesystems that do not support hard links. Use Lstat
		// so dangling symlinks are detected and never overwritten.
		if _, statErr := os.Lstat(destPath); statErr == nil {
			continue
		} else if !os.IsNotExist(statErr) {
			return guidPath
		}
		if err := os.Rename(guidPath, destPath); err == nil {
			return destPath
		}
		return guidPath
	}
	return guidPath
}

// safeDownloadName extracts a safe base filename from Chrome's suggested
// filename, rejecting empty names, traversal segments ("." / ".."), and
// separator-only inputs.
func safeDownloadName(suggested string) string {
	if suggested == "" {
		return ""
	}
	// Normalize Windows-style backslashes too so cross-platform suggested names
	// never leak directory separators on Linux/macOS.
	normalized := strings.ReplaceAll(suggested, "\\", "/")
	base := filepath.Base(normalized)
	if base == "" || base == "." || base == ".." || base == string(filepath.Separator) || strings.ContainsAny(base, `/\`) || strings.ContainsRune(base, '\x00') {
		return ""
	}
	return base
}

// Drain returns up to limit completed/canceled entries, removing only
// the returned ones from the buffer.
func (d *Download) Drain(limit int) []DownloadEntry {
	d.mu.Lock()
	d.waitRenamesLocked()
	entries := d.buf.Drain(nil, limit)
	d.mu.Unlock()
	return entries
}

// Peek returns up to limit entries without clearing the buffer.
func (d *Download) Peek(limit int) []DownloadEntry {
	d.mu.Lock()
	d.waitRenamesLocked()
	entries := d.buf.Peek(nil, limit)
	d.mu.Unlock()
	return entries
}

// InProgress returns all currently in-progress downloads in creation order.
func (d *Download) InProgress() []DownloadEntry {
	d.mu.Lock()
	d.waitRenamesLocked()
	entries := make([]DownloadEntry, 0, len(d.pendingOrder))
	for _, guid := range d.pendingOrder {
		if e, ok := d.pending[guid]; ok {
			entries = append(entries, *e)
		}
	}
	d.mu.Unlock()
	return entries
}

// Clear removes all completed and canceled entries from the buffer while
// preserving in-progress downloads (and seen-GUID deduplication) so active
// downloads remain visible in InProgress and still rename cleanly on disk when
// they complete.
func (d *Download) Clear() {
	d.mu.Lock()
	d.gen++
	d.buf.Clear()
	d.waitRenamesLocked()
	d.mu.Unlock()
}
