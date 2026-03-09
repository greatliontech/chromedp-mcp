package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveToDownloadDir(t *testing.T) {
	data := []byte("test data")

	t.Run("no dir configured", func(t *testing.T) {
		_, err := saveToDownloadDir("", "file.png", ".png", data)
		if err == nil {
			t.Fatal("expected error when dir is empty")
		}
		if !strings.Contains(err.Error(), "--download-dir not configured") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("writes file with explicit name", func(t *testing.T) {
		dir := t.TempDir()
		path, err := saveToDownloadDir(dir, "shot.png", ".png", data)
		if err != nil {
			t.Fatal(err)
		}
		if path != filepath.Join(dir, "shot.png") {
			t.Fatalf("unexpected path: %s", path)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(data) {
			t.Fatalf("file content mismatch: got %q", got)
		}
	})

	t.Run("appends default extension when missing", func(t *testing.T) {
		dir := t.TempDir()
		path, err := saveToDownloadDir(dir, "shot", ".png", data)
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Ext(path) != ".png" {
			t.Fatalf("expected .png extension, got %s", filepath.Ext(path))
		}
		if filepath.Base(path) != "shot.png" {
			t.Fatalf("expected shot.png, got %s", filepath.Base(path))
		}
	})

	t.Run("preserves explicit extension", func(t *testing.T) {
		dir := t.TempDir()
		path, err := saveToDownloadDir(dir, "report.pdf", ".png", data)
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Base(path) != "report.pdf" {
			t.Fatalf("expected report.pdf, got %s", filepath.Base(path))
		}
	})

	t.Run("generates timestamp name when empty", func(t *testing.T) {
		dir := t.TempDir()
		path, err := saveToDownloadDir(dir, "", ".jpg", data)
		if err != nil {
			t.Fatal(err)
		}
		base := filepath.Base(path)
		if filepath.Ext(base) != ".jpg" {
			t.Fatalf("expected .jpg extension, got %s", filepath.Ext(base))
		}
		// Timestamp format: 20060102-150405.jpg — 15 chars + ext
		name := strings.TrimSuffix(base, ".jpg")
		if len(name) != 15 {
			t.Fatalf("expected 15-char timestamp, got %q (%d chars)", name, len(name))
		}
	})

	t.Run("blocks path traversal", func(t *testing.T) {
		dir := t.TempDir()
		cases := []string{
			"../etc/passwd",
			"subdir/file.png",
			"./file.png",
		}
		for _, name := range cases {
			_, err := saveToDownloadDir(dir, name, ".png", data)
			if err == nil {
				t.Fatalf("expected error for filename %q", name)
			}
			if !strings.Contains(err.Error(), "path separators") {
				t.Fatalf("expected path separator error for %q, got: %v", name, err)
			}
		}
	})

	t.Run("creates directory if missing", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "nested", "dir")
		path, err := saveToDownloadDir(dir, "file.png", ".png", data)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("file not created: %v", err)
		}
	})
}

// TestScreenshotFilenameNoDownloadDir verifies that requesting a filename
// on screenshot when --download-dir is not configured returns an error.
func TestScreenshotFilenameNoDownloadDir(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "screenshot", map[string]any{
		"tab":      tabID,
		"filename": "test.png",
	})
	if !strings.Contains(errText, "download-dir not configured") {
		t.Fatalf("expected download-dir error, got: %s", errText)
	}
}

// TestPDFFilenameNoDownloadDir verifies that requesting a filename
// on pdf when --download-dir is not configured returns an error.
func TestPDFFilenameNoDownloadDir(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "pdf", map[string]any{
		"tab":      tabID,
		"filename": "test.pdf",
	})
	if !strings.Contains(errText, "download-dir not configured") {
		t.Fatalf("expected download-dir error, got: %s", errText)
	}
}
