package collector

import (
	"os"
	"path/filepath"
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
type Download struct {
	mu          sync.Mutex
	pending     map[string]*DownloadEntry
	buf         *RingBuffer[DownloadEntry]
	downloadDir string
}

// NewDownload creates a download collector with the given buffer size
// and download directory. The download directory is used to rename
// GUID-named files to their suggested filenames on completion.
func NewDownload(maxSize int, downloadDir string) *Download {
	return &Download{
		pending:     make(map[string]*DownloadEntry),
		buf:         NewRingBuffer[DownloadEntry](maxSize),
		downloadDir: downloadDir,
	}
}

// HandleDownloadWillBegin records a new download.
func (d *Download) HandleDownloadWillBegin(ev *browser.EventDownloadWillBegin) {
	entry := &DownloadEntry{
		GUID:              ev.GUID,
		URL:               ev.URL,
		SuggestedFilename: ev.SuggestedFilename,
		State:             DownloadStateInProgress,
		StartTime:         time.Now(),
	}
	d.mu.Lock()
	d.pending[ev.GUID] = entry
	d.mu.Unlock()
}

// HandleDownloadProgress updates download progress or completes a download.
func (d *Download) HandleDownloadProgress(ev *browser.EventDownloadProgress) {
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
		delete(d.pending, ev.GUID)
		d.mu.Unlock()

		entry.State = DownloadStateCompleted
		entry.EndTime = time.Now()

		// Rename the GUID-named file to the suggested filename.
		if d.downloadDir != "" && entry.SuggestedFilename != "" {
			guidPath := filepath.Join(d.downloadDir, ev.GUID)
			destPath := filepath.Join(d.downloadDir, filepath.Base(entry.SuggestedFilename))
			if err := os.Rename(guidPath, destPath); err == nil {
				entry.Path = destPath
			} else {
				// Rename failed — use the GUID path if the file exists.
				if _, statErr := os.Stat(guidPath); statErr == nil {
					entry.Path = guidPath
				}
			}
		}
	case browser.DownloadProgressStateCanceled:
		delete(d.pending, ev.GUID)
		d.mu.Unlock()

		entry.State = DownloadStateCanceled
		entry.EndTime = time.Now()
	}

	d.buf.Add(*entry)
}

// Drain returns all completed/canceled entries and clears the buffer.
func (d *Download) Drain(limit int) []DownloadEntry {
	entries := d.buf.Drain(nil)
	return applyLimit(entries, limit)
}

// Peek returns entries without clearing the buffer.
func (d *Download) Peek(limit int) []DownloadEntry {
	entries := d.buf.Peek(nil)
	return applyLimit(entries, limit)
}

// InProgress returns all currently in-progress downloads.
func (d *Download) InProgress() []DownloadEntry {
	d.mu.Lock()
	defer d.mu.Unlock()
	entries := make([]DownloadEntry, 0, len(d.pending))
	for _, e := range d.pending {
		entries = append(entries, *e)
	}
	return entries
}

// Clear removes all entries from both the pending map and completed buffer.
func (d *Download) Clear() {
	d.mu.Lock()
	d.pending = make(map[string]*DownloadEntry)
	d.mu.Unlock()
	d.buf.Clear()
}
