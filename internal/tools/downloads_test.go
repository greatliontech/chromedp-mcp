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
	var downloads GetDownloadsOutput
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		// Peek so we don't drain the buffer on each check.
		downloads = callTool[GetDownloadsOutput](t, "get_downloads", map[string]any{
			"peek": true,
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
	drained := callTool[GetDownloadsOutput](t, "get_downloads", map[string]any{})
	if len(drained.Downloads) == 0 {
		t.Fatal("expected downloads in drain result")
	}

	after := callTool[GetDownloadsOutput](t, "get_downloads", map[string]any{
		"peek": true,
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
	callTool[GetDownloadsOutput](t, "get_downloads", map[string]any{})

	// Click the CSV download link.
	callTool[struct{}](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#download-csv",
	})

	// Wait for completion.
	var downloads GetDownloadsOutput
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		downloads = callTool[GetDownloadsOutput](t, "get_downloads", map[string]any{
			"peek": true,
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
	callTool[GetDownloadsOutput](t, "get_downloads", map[string]any{})

	// Click the blob download button.
	callTool[struct{}](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#download-blob",
	})

	// Wait for completion.
	var downloads GetDownloadsOutput
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		downloads = callTool[GetDownloadsOutput](t, "get_downloads", map[string]any{
			"peek": true,
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
	downloads := callTool[GetDownloadsOutput](t, "get_downloads", map[string]any{})
	// After drain, peek should be empty.
	downloads = callTool[GetDownloadsOutput](t, "get_downloads", map[string]any{
		"peek": true,
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
	callTool[GetDownloadsOutput](t, "get_downloads", map[string]any{})

	callTool[struct{}](t, "click", map[string]any{
		"tab":      tabID,
		"selector": "#download-link",
	})

	// Wait for completion.
	var downloads GetDownloadsOutput
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		downloads = callTool[GetDownloadsOutput](t, "get_downloads", map[string]any{
			"peek": true,
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
	guidPath := filepath.Join(harness.downloadDir, dl.GUID)
	if _, err := os.Stat(guidPath); err == nil {
		t.Fatalf("GUID file %s should have been renamed", guidPath)
	}

	// The suggested-name file should exist.
	if _, err := os.Stat(dl.Path); err != nil {
		t.Fatalf("renamed file %s not found: %v", dl.Path, err)
	}
}
