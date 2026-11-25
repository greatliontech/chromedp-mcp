package collector

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/chromedp/cdproto/browser"
)

func TestDownloadDedupAndCollisionRename(t *testing.T) {
	dir := t.TempDir()
	d := NewDownload(10, dir)

	// Write three distinct GUID files simulating three downloads with the same filename.
	for i, content := range []string{"first", "second", "third"} {
		guid := fmt.Sprintf("guid-%d", i)
		if err := os.WriteFile(filepath.Join(dir, guid), []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", guid, err)
		}
		evBegin := &browser.EventDownloadWillBegin{
			GUID:              guid,
			URL:               "https://example.com/report.txt",
			SuggestedFilename: "report.txt",
		}
		d.HandleDownloadWillBegin(evBegin)
		// Duplicate WillBegin while pending must be ignored.
		d.HandleDownloadWillBegin(evBegin)

		d.HandleDownloadProgress(&browser.EventDownloadProgress{
			GUID:          guid,
			ReceivedBytes: float64(len(content)),
			TotalBytes:    float64(len(content)),
			State:         browser.DownloadProgressStateInProgress,
		})
		evDone := &browser.EventDownloadProgress{
			GUID:          guid,
			ReceivedBytes: float64(len(content)),
			TotalBytes:    float64(len(content)),
			State:         browser.DownloadProgressStateCompleted,
		}
		d.HandleDownloadProgress(evDone)
		// Duplicate completion event while finalizing/after completion must be ignored.
		d.HandleDownloadProgress(evDone)
		// Wait for rename to finish before firing duplicate WillBegin + Completed post-completion.
		_ = d.Peek(0)
		d.HandleDownloadWillBegin(evBegin)
		d.HandleDownloadProgress(evDone)
	}

	if inProg := d.InProgress(); len(inProg) != 0 {
		t.Fatalf("InProgress = %+v, want empty (duplicate post-completion WillBegin must be ignored)", inProg)
	}
	entries := d.Peek(0)
	if len(entries) != 3 {
		t.Fatalf("len = %d, want 3 (no duplicates)", len(entries))
	}

	wantNames := []string{"report.txt", "report-1.txt", "report-2.txt"}
	wantContents := []string{"first", "second", "third"}
	for i, e := range entries {
		if e.State != DownloadStateCompleted {
			t.Errorf("entry[%d].State = %q, want completed", i, e.State)
		}
		if filepath.Base(e.Path) != wantNames[i] {
			t.Errorf("entry[%d].Path base = %q, want %q", i, filepath.Base(e.Path), wantNames[i])
		}
		got, err := os.ReadFile(e.Path)
		if err != nil {
			t.Fatalf("read %s: %v", e.Path, err)
		}
		if string(got) != wantContents[i] {
			t.Errorf("file %s content = %q, want %q", e.Path, got, wantContents[i])
		}
		// GUID file must no longer exist.
		if _, err := os.Stat(filepath.Join(dir, e.GUID)); !os.IsNotExist(err) {
			t.Errorf("GUID file %s still exists after rename", e.GUID)
		}
	}
}

func TestDownloadUnsafeSuggestedFilenameAndPathTraversal(t *testing.T) {
	dir := t.TempDir()
	d := NewDownload(20, dir)

	// Unsafe names that collapse to empty safe name: keep GUID path on disk.
	for i, bad := range []string{"", ".", "..", "/", "\\", "a/b/..", "bad\x00name"} {
		guid := fmt.Sprintf("unsafe-%d", i)
		guidPath := filepath.Join(dir, guid)
		if err := os.WriteFile(guidPath, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		d.HandleDownloadWillBegin(&browser.EventDownloadWillBegin{
			GUID:              guid,
			SuggestedFilename: bad,
		})
		d.HandleDownloadProgress(&browser.EventDownloadProgress{
			GUID:  guid,
			State: browser.DownloadProgressStateCompleted,
		})
	}

	// Traversal prefixes are stripped to safe basename inside dir.
	for i, name := range []string{"../../escape.txt", `C:\Users\evil\escape.txt`} {
		guid := fmt.Sprintf("trav-%d", i)
		if err := os.WriteFile(filepath.Join(dir, guid), []byte("safe"), 0o600); err != nil {
			t.Fatal(err)
		}
		d.HandleDownloadWillBegin(&browser.EventDownloadWillBegin{
			GUID:              guid,
			SuggestedFilename: name,
		})
		d.HandleDownloadProgress(&browser.EventDownloadProgress{
			GUID:  guid,
			State: browser.DownloadProgressStateCompleted,
		})
	}

	// Dotfile collision produces .env and .env-1.
	for i := 0; i < 2; i++ {
		guid := fmt.Sprintf("dot-%d", i)
		if err := os.WriteFile(filepath.Join(dir, guid), []byte("dot"), 0o600); err != nil {
			t.Fatal(err)
		}
		d.HandleDownloadWillBegin(&browser.EventDownloadWillBegin{
			GUID:              guid,
			SuggestedFilename: ".env",
		})
		d.HandleDownloadProgress(&browser.EventDownloadProgress{
			GUID:  guid,
			State: browser.DownloadProgressStateCompleted,
		})
	}

	// Malformed GUID containing traversal must not touch outside dir.
	d.HandleDownloadWillBegin(&browser.EventDownloadWillBegin{
		GUID:              "../outside",
		SuggestedFilename: "evil.txt",
	})
	d.HandleDownloadProgress(&browser.EventDownloadProgress{
		GUID:  "../outside",
		State: browser.DownloadProgressStateCompleted,
	})

	entries := d.Drain(0)
	if len(entries) != 12 {
		t.Fatalf("len = %d, want 12", len(entries))
	}
	byGUID := make(map[string]DownloadEntry, len(entries))
	for _, e := range entries {
		byGUID[e.GUID] = e
	}
	for i := 0; i < 7; i++ {
		guid := fmt.Sprintf("unsafe-%d", i)
		wantPath := filepath.Join(dir, guid)
		if got := byGUID[guid].Path; got != wantPath {
			t.Errorf("%s.Path = %q, want %q", guid, got, wantPath)
		}
	}
	// Traversal entries must both land directly inside dir with distinct non-overwriting names.
	travPaths := map[string]bool{
		filepath.Base(byGUID["trav-0"].Path): true,
		filepath.Base(byGUID["trav-1"].Path): true,
	}
	if filepath.Dir(byGUID["trav-0"].Path) != dir || filepath.Dir(byGUID["trav-1"].Path) != dir {
		t.Errorf("traversal escaped dir %q: %q, %q", dir, byGUID["trav-0"].Path, byGUID["trav-1"].Path)
	}
	if !travPaths["escape.txt"] || !travPaths["escape-1.txt"] {
		t.Errorf("trav basenames = %v, want escape.txt and escape-1.txt", travPaths)
	}
	// Dotfile collision entries must produce .env and .env-1.
	dotPaths := map[string]bool{
		filepath.Base(byGUID["dot-0"].Path): true,
		filepath.Base(byGUID["dot-1"].Path): true,
	}
	if !dotPaths[".env"] || !dotPaths[".env-1"] {
		t.Errorf("dotfile basenames = %v, want .env and .env-1", dotPaths)
	}
	// Malformed GUID entry must have empty Path.
	if got := byGUID["../outside"].Path; got != "" {
		t.Errorf("traversal GUID Path = %q, want empty", got)
	}
}

func TestDownloadInProgressOrderCanceledAndEmptyDir(t *testing.T) {
	d := NewDownload(10, "")

	// Nil and empty-GUID guards.
	d.HandleDownloadWillBegin(nil)
	d.HandleDownloadWillBegin(&browser.EventDownloadWillBegin{GUID: ""})
	d.HandleDownloadProgress(nil)
	d.HandleDownloadProgress(&browser.EventDownloadProgress{GUID: ""})
	d.HandleDownloadProgress(&browser.EventDownloadProgress{GUID: "unknown", State: browser.DownloadProgressStateCompleted})

	for _, id := range []string{"g1", "g2", "g3"} {
		d.HandleDownloadWillBegin(&browser.EventDownloadWillBegin{GUID: id, SuggestedFilename: id + ".bin"})
	}
	// InProgress must preserve deterministic creation order.
	inProg := d.InProgress()
	if len(inProg) != 3 || inProg[0].GUID != "g1" || inProg[1].GUID != "g2" || inProg[2].GUID != "g3" {
		t.Fatalf("InProgress = %+v, want [g1, g2, g3]", inProg)
	}

	// Cancel g2, complete g1 (no downloadDir => synchronous with empty Path).
	d.HandleDownloadProgress(&browser.EventDownloadProgress{GUID: "g2", State: browser.DownloadProgressStateCanceled})
	d.HandleDownloadProgress(&browser.EventDownloadProgress{GUID: "g1", State: browser.DownloadProgressStateCompleted})

	inProg = d.InProgress()
	if len(inProg) != 1 || inProg[0].GUID != "g3" {
		t.Fatalf("remaining InProgress = %+v, want [g3]", inProg)
	}
	done := d.Peek(0)
	if len(done) != 2 || done[0].State != DownloadStateCanceled || done[1].State != DownloadStateCompleted || done[1].Path != "" {
		t.Fatalf("done = %+v", done)
	}
}

func TestDownloadDanglingSymlinkNotOverwritten(t *testing.T) {
	dir := t.TempDir()
	// Create a dangling symlink at report.txt; download must advance to report-1.txt
	// rather than overwriting the symlink.
	symlinkPath := filepath.Join(dir, "report.txt")
	if err := os.Symlink(filepath.Join(dir, "nonexistent-target"), symlinkPath); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	guid := "sym-guid"
	if err := os.WriteFile(filepath.Join(dir, guid), []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}

	d := NewDownload(10, dir)
	d.HandleDownloadWillBegin(&browser.EventDownloadWillBegin{
		GUID:              guid,
		SuggestedFilename: "report.txt",
	})
	d.HandleDownloadProgress(&browser.EventDownloadProgress{
		GUID:  guid,
		State: browser.DownloadProgressStateCompleted,
	})

	entries := d.Peek(0)
	if len(entries) != 1 {
		t.Fatalf("len = %d, want 1", len(entries))
	}
	if filepath.Base(entries[0].Path) != "report-1.txt" {
		t.Errorf("Path = %q, want report-1.txt (dangling symlink preserved)", entries[0].Path)
	}
	if fi, err := os.Lstat(symlinkPath); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("dangling symlink at %s was modified or removed: fi=%v err=%v", symlinkPath, fi, err)
	}
}

func TestDownloadPendingCapDoesNotDropCompletingDownload(t *testing.T) {
	dir := t.TempDir()
	d := NewDownload(10, dir)
	d.maxPending = 3

	for i := 0; i < 5; i++ {
		d.HandleDownloadWillBegin(&browser.EventDownloadWillBegin{
			GUID: fmt.Sprintf("p-%d", i),
		})
	}
	inProg := d.InProgress()
	if len(inProg) != 3 {
		t.Fatalf("InProgress len = %d, want 3 (capped)", len(inProg))
	}
	if inProg[0].GUID != "p-2" || inProg[2].GUID != "p-4" {
		t.Errorf("InProgress = %v, want [p-2, p-3, p-4]", inProg)
	}

	// Even with maxPending=1, completing a download frees its pending slot
	// immediately so an incoming WillBegin cannot evict its async completion.
	d2 := NewDownload(10, dir)
	d2.maxPending = 1
	if err := os.WriteFile(filepath.Join(dir, "g-first"), []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	d2.HandleDownloadWillBegin(&browser.EventDownloadWillBegin{GUID: "g-first", SuggestedFilename: "one.txt"})
	d2.HandleDownloadProgress(&browser.EventDownloadProgress{GUID: "g-first", State: browser.DownloadProgressStateCompleted})
	d2.HandleDownloadWillBegin(&browser.EventDownloadWillBegin{GUID: "g-second", SuggestedFilename: "two.txt"})

	done := d2.Peek(0)
	if len(done) != 1 || done[0].GUID != "g-first" || filepath.Base(done[0].Path) != "one.txt" {
		t.Errorf("completing download lost under maxPending=1: %+v", done)
	}
	if inProg := d2.InProgress(); len(inProg) != 1 || inProg[0].GUID != "g-second" {
		t.Errorf("InProgress = %+v, want [g-second]", inProg)
	}
}

func TestDownloadConcurrentRenameAndClear(t *testing.T) {
	dir := t.TempDir()
	const count = 20
	d := NewDownload(count*2, dir)

	// Deterministic gen-mismatch check: start Clear() while a rename is in
	// flight so Clear increments d.gen and blocks in waitRenamesLocked; then
	// complete the stale rename and confirm it does not leak into the buffer.
	d.mu.Lock()
	staleGen := d.gen
	d.renaming++
	d.mu.Unlock()
	cleared := make(chan struct{})
	go func() {
		d.Clear()
		close(cleared)
	}()
	for {
		d.mu.Lock()
		advanced := d.gen != staleGen
		d.mu.Unlock()
		if advanced {
			break
		}
	}
	d.finishRename(staleGen, DownloadEntry{GUID: "stale", State: DownloadStateCompleted})
	<-cleared
	if got := d.Peek(0); len(got) != 0 {
		t.Fatalf("stale finishRename leaked after Clear: %+v", got)
	}

	// All 20 concurrent completions collide on "race.bin" with sufficient
	// maxPending so none are evicted before completing.
	for i := 0; i < count; i++ {
		guid := fmt.Sprintf("race-%d", i)
		if err := os.WriteFile(filepath.Join(dir, guid), []byte(fmt.Sprintf("data-%d", i)), 0o600); err != nil {
			t.Fatal(err)
		}
		d.HandleDownloadWillBegin(&browser.EventDownloadWillBegin{
			GUID:              guid,
			SuggestedFilename: "race.bin",
		})
	}
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		guid := fmt.Sprintf("race-%d", i)
		wg.Add(1)
		go func(g string) {
			defer wg.Done()
			d.HandleDownloadProgress(&browser.EventDownloadProgress{
				GUID:  g,
				State: browser.DownloadProgressStateCompleted,
			})
		}(guid)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = d.Peek(5)
		_ = d.InProgress()
	}()
	wg.Wait()

	entries := d.Peek(0)
	if len(entries) != count {
		t.Fatalf("concurrent collisions completed = %d, want %d", len(entries), count)
	}
	paths := make(map[string]struct{}, count)
	for _, e := range entries {
		if e.Path == "" {
			t.Errorf("empty Path on %s", e.GUID)
		}
		if _, dup := paths[e.Path]; dup {
			t.Errorf("duplicate Path %q across concurrent downloads", e.Path)
		}
		paths[e.Path] = struct{}{}
	}

	d.Clear()
	if len(d.Peek(0)) != 0 || len(d.InProgress()) != 0 {
		t.Errorf("after Clear, expected empty buffer and pending")
	}
}
