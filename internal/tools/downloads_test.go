package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/greatliontech/chromedp-mcp/internal/collector"
)

// TestDownloadLinkClick verifies that clicking a download link saves the
// file to the download directory and the download appears in get_downloads.
func TestDownloadLinkClick(t *testing.T) {
	tabID := navigateToFixture(t, "download.html")
	defer closeTab(t, tabID)

	// Click the download link.
	callTool[struct{}](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#download-link",
	})

	// Wait for the download to complete. Downloads are async — poll
	// get_downloads until we see a completed entry.
	var downloads getDownloadsOutput
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		// Peek so we don't drain the buffer on each check.
		downloads = callTool[getDownloadsOutput](t, "get_downloads", map[string]any{
			"mode": "peek",
		})
		if len(downloads.Downloads) > 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	if len(downloads.Downloads) == 0 {
		t.Fatal("expected at least one completed download")
	}

	dl := downloads.Downloads[0]
	if dl.State != collector.DownloadStateCompleted {
		t.Fatalf("expected completed state, got %s", dl.State)
	}
	if dl.SuggestedFilename != "test-file.txt" {
		t.Fatalf("expected suggested filename test-file.txt, got %s", dl.SuggestedFilename)
	}
	if dl.Path == "" {
		t.Fatal("expected non-empty file path")
	}

	// Verify the file exists and has the expected content.
	content, err := os.ReadFile(dl.Path)
	if err != nil {
		t.Fatalf("reading downloaded file: %v", err)
	}
	if string(content) != "hello from download test" {
		t.Fatalf("unexpected file content: %q", content)
	}

	// Verify the file was renamed from GUID to suggested filename.
	if filepath.Base(dl.Path) != "test-file.txt" {
		t.Fatalf("expected file renamed to test-file.txt, got %s", filepath.Base(dl.Path))
	}

	// Now drain the buffer and verify it's empty after.
	drained := callTool[getDownloadsOutput](t, "get_downloads", map[string]any{"mode": "drain"})
	if len(drained.Downloads) == 0 {
		t.Fatal("expected downloads in drain result")
	}

	after := callTool[getDownloadsOutput](t, "get_downloads", map[string]any{
		"mode": "peek",
	})
	if len(after.Downloads) != 0 {
		t.Fatalf("expected empty buffer after drain, got %d entries", len(after.Downloads))
	}
}

// TestDownloadCSV verifies downloading a CSV file.
func TestDownloadCSV(t *testing.T) {
	tabID := navigateToFixture(t, "download.html")
	defer closeTab(t, tabID)

	// Drain any previous downloads.
	callTool[getDownloadsOutput](t, "get_downloads", map[string]any{"mode": "drain"})

	// Click the CSV download link.
	callTool[struct{}](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#download-csv",
	})

	// Wait for completion.
	var downloads getDownloadsOutput
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		downloads = callTool[getDownloadsOutput](t, "get_downloads", map[string]any{
			"mode": "peek",
		})
		if len(downloads.Downloads) > 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	if len(downloads.Downloads) == 0 {
		t.Fatal("expected CSV download to complete")
	}

	dl := downloads.Downloads[0]
	if dl.SuggestedFilename != "data.csv" {
		t.Fatalf("expected suggested filename data.csv, got %s", dl.SuggestedFilename)
	}

	content, err := os.ReadFile(dl.Path)
	if err != nil {
		t.Fatalf("reading downloaded CSV: %v", err)
	}
	if !strings.Contains(string(content), "alpha,1") {
		t.Fatalf("unexpected CSV content: %q", content)
	}
}

// TestDownloadBlobJS verifies downloading a blob created in JavaScript.
func TestDownloadBlobJS(t *testing.T) {
	tabID := navigateToFixture(t, "download.html")
	defer closeTab(t, tabID)

	// Drain any previous downloads.
	callTool[getDownloadsOutput](t, "get_downloads", map[string]any{"mode": "drain"})

	// Click the blob download button.
	callTool[struct{}](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#download-blob",
	})

	// Wait for completion.
	var downloads getDownloadsOutput
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		downloads = callTool[getDownloadsOutput](t, "get_downloads", map[string]any{
			"mode": "peek",
		})
		if len(downloads.Downloads) > 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	if len(downloads.Downloads) == 0 {
		t.Fatal("expected blob download to complete")
	}

	dl := downloads.Downloads[0]
	if dl.State != collector.DownloadStateCompleted {
		t.Fatalf("expected completed state, got %s", dl.State)
	}
	if dl.SuggestedFilename != "blob-file.txt" {
		t.Fatalf("expected suggested filename blob-file.txt, got %s", dl.SuggestedFilename)
	}

	content, err := os.ReadFile(dl.Path)
	if err != nil {
		t.Fatalf("reading downloaded blob: %v", err)
	}
	if string(content) != "blob content from javascript" {
		t.Fatalf("unexpected blob content: %q", content)
	}
}

// TestGetDownloadsNoDownloads verifies get_downloads returns empty
// results when no downloads have occurred.
func TestGetDownloadsNoDownloads(t *testing.T) {
	// Drain to clear any prior state.
	downloads := callTool[getDownloadsOutput](t, "get_downloads", map[string]any{"mode": "drain"})
	// After drain, peek should be empty.
	downloads = callTool[getDownloadsOutput](t, "get_downloads", map[string]any{
		"mode": "peek",
	})
	if len(downloads.Downloads) != 0 {
		t.Fatalf("expected no downloads, got %d", len(downloads.Downloads))
	}
	if len(downloads.InProgress) != 0 {
		t.Fatalf("expected no in-progress downloads, got %d", len(downloads.InProgress))
	}
}

// TestDownloadFileRenamed verifies that the downloaded file is renamed
// from its GUID name to the suggested filename.
func TestDownloadFileRenamed(t *testing.T) {
	tabID := navigateToFixture(t, "download.html")
	defer closeTab(t, tabID)

	// Drain any previous downloads.
	callTool[getDownloadsOutput](t, "get_downloads", map[string]any{"mode": "drain"})

	callTool[struct{}](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#download-link",
	})

	// Wait for completion.
	var downloads getDownloadsOutput
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		downloads = callTool[getDownloadsOutput](t, "get_downloads", map[string]any{
			"mode": "peek",
		})
		if len(downloads.Downloads) > 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	if len(downloads.Downloads) == 0 {
		t.Fatal("expected download to complete")
	}

	dl := downloads.Downloads[0]

	// The GUID-named file should NOT exist (it was renamed).
	guidPath := filepath.Join(ensureHarness(t).downloadDir, dl.GUID)
	if _, err := os.Stat(guidPath); err == nil {
		t.Fatalf("GUID file %s should have been renamed", guidPath)
	}

	// The suggested-name file should exist.
	if _, err := os.Stat(dl.Path); err != nil {
		t.Fatalf("renamed file %s not found: %v", dl.Path, err)
	}
}

// TestDownloadMultiTabNoDuplicatesAndNoOverwrite verifies that with multiple
// open tabs in the same browser, each download produces exactly one entry
// (no per-tab event duplication) and repeated downloads of the same filename
// are saved to distinct non-overwriting paths.
func TestDownloadMultiTabNoDuplicatesAndNoOverwrite(t *testing.T) {
	tab1 := navigateToFixture(t, "download.html")
	defer closeTab(t, tab1)
	tab2 := navigateToFixture(t, "page2.html")
	defer closeTab(t, tab2)

	// Drain any previous downloads.
	callTool[getDownloadsOutput](t, "get_downloads", map[string]any{"mode": "drain"})

	// Trigger two downloads of the same filename with 2 open tabs.
	var paths []string
	for i := 0; i < 2; i++ {
		callTool[struct{}](t, "click", map[string]any{
			"tab":      tab1,
			"selector": "#download-link",
		})
		var got getDownloadsOutput
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			got = callTool[getDownloadsOutput](t, "get_downloads", map[string]any{"mode": "peek"})
			if len(got.Downloads) >= i+1 {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if len(got.Downloads) != i+1 {
			t.Fatalf("after download %d, got %d entries, want %d", i+1, len(got.Downloads), i+1)
		}
		paths = append(paths, got.Downloads[i].Path)
	}

	// Drain and verify no delayed duplicate events arrived.
	final := callTool[getDownloadsOutput](t, "get_downloads", map[string]any{"mode": "drain"})
	if len(final.Downloads) != 2 {
		t.Fatalf("total downloads = %d, want 2 (no multi-tab duplication)", len(final.Downloads))
	}
	if paths[0] == "" || paths[1] == "" || paths[0] == paths[1] {
		t.Fatalf("expected two distinct non-empty paths, got %q and %q", paths[0], paths[1])
	}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("reading %s: %v", p, err)
		}
		if string(data) != "hello from download test" {
			t.Errorf("file %s content = %q", p, data)
		}
	}
}

// TestGetDownloadsGatedOnDownloadDir verifies get_downloads is omitted from
// tools/list when DownloadDir is empty and registered when DownloadDir is set.
func TestGetDownloadsGatedOnDownloadDir(t *testing.T) {
	hasGetDownloads := func(opts *Options) bool {
		t.Helper()
		for _, tool := range listAllRegisteredTools(t, opts) {
			if tool.Name == "get_downloads" {
				return true
			}
		}
		return false
	}

	if hasGetDownloads(&Options{}) {
		t.Error("get_downloads must not be registered when DownloadDir is empty")
	}
	if !hasGetDownloads(&Options{DownloadDir: t.TempDir()}) {
		t.Error("get_downloads must be registered when DownloadDir is non-empty")
	}
}
