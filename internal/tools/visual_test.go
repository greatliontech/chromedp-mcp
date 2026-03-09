package tools

import (
	"bytes"
	"image"
	_ "image/png" // register PNG decoder for image.Decode
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
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

// TestScreenshotMaxDimension verifies that the screenshot tool respects
// max_dimension by downscaling captures via Chrome's clip.Scale parameter.
func TestScreenshotMaxDimension(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// Set a large viewport so the screenshot will be big.
	callToolRaw(t, "set_viewport", map[string]any{
		"tab":    tabID,
		"width":  1920,
		"height": 1080,
	})

	t.Run("full page", func(t *testing.T) {
		result := callToolRaw(t, "screenshot", map[string]any{
			"tab":           tabID,
			"full_page":     true,
			"max_dimension": 500,
		})
		if result.IsError {
			t.Fatalf("screenshot error: %s", contentText(result))
		}
		assertImageWithinLimit(t, result, 500)
	})

	t.Run("viewport", func(t *testing.T) {
		result := callToolRaw(t, "screenshot", map[string]any{
			"tab":           tabID,
			"max_dimension": 500,
		})
		if result.IsError {
			t.Fatalf("screenshot error: %s", contentText(result))
		}
		assertImageWithinLimit(t, result, 500)
	})

	t.Run("element", func(t *testing.T) {
		result := callToolRaw(t, "screenshot", map[string]any{
			"tab":           tabID,
			"selector":      "#title",
			"max_dimension": 100,
		})
		if result.IsError {
			t.Fatalf("screenshot error: %s", contentText(result))
		}
		assertImageWithinLimit(t, result, 100)
	})

	t.Run("no-op when within limits", func(t *testing.T) {
		// max_dimension larger than the viewport — should not scale.
		result := callToolRaw(t, "screenshot", map[string]any{
			"tab":           tabID,
			"max_dimension": 5000,
		})
		if result.IsError {
			t.Fatalf("screenshot error: %s", contentText(result))
		}
		for _, c := range result.Content {
			if img, ok := c.(*mcp.ImageContent); ok {
				decoded, _, err := image.Decode(bytes.NewReader(img.Data))
				if err != nil {
					t.Fatalf("decoding image: %v", err)
				}
				bounds := decoded.Bounds()
				// Viewport is 1920x1080 — image should not be downscaled.
				if bounds.Dx() < 1000 {
					t.Errorf("width = %d, expected >= 1000 (no downscale)", bounds.Dx())
				}
				return
			}
		}
		t.Fatal("screenshot did not return ImageContent")
	})
}

// assertImageWithinLimit checks that all image content in the result
// has both dimensions within maxDim.
func assertImageWithinLimit(t *testing.T, result *mcp.CallToolResult, maxDim int) {
	t.Helper()
	for _, c := range result.Content {
		if img, ok := c.(*mcp.ImageContent); ok {
			decoded, _, err := image.Decode(bytes.NewReader(img.Data))
			if err != nil {
				t.Fatalf("decoding result image: %v", err)
			}
			bounds := decoded.Bounds()
			if bounds.Dx() > maxDim || bounds.Dy() > maxDim {
				t.Errorf("dimensions %dx%d exceed max_dimension %d", bounds.Dx(), bounds.Dy(), maxDim)
			}
			return
		}
	}
	t.Fatal("screenshot did not return ImageContent")
}

// TestScreenshotSaveToDisk verifies that requesting a filename on screenshot
// saves the file to the download directory and still returns the image inline.
func TestScreenshotSaveToDisk(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	result := callToolRaw(t, "screenshot", map[string]any{
		"tab":      tabID,
		"filename": "test-screenshot.png",
	})

	// Should have both image content and text content with path.
	var hasImage, hasText bool
	for _, c := range result.Content {
		switch c.(type) {
		case *mcp.ImageContent:
			hasImage = true
		case *mcp.TextContent:
			hasText = true
		}
	}
	if !hasImage {
		t.Fatal("expected image content in result")
	}
	if !hasText {
		t.Fatal("expected text content with file path in result")
	}

	// Verify file exists on disk.
	path := filepath.Join(harness.downloadDir, "test-screenshot.png")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("screenshot file not found: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("screenshot file is empty")
	}
	os.Remove(path)
}

// TestPDFSaveToDisk verifies that requesting a filename on pdf
// saves the file to the download directory.
func TestPDFSaveToDisk(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	result := callToolRaw(t, "pdf", map[string]any{
		"tab":      tabID,
		"filename": "test-output.pdf",
	})

	// Should return text content with path (not the PDF blob).
	text := contentText(result)
	if !strings.Contains(text, "Saved to") {
		t.Fatalf("expected 'Saved to' in result, got: %s", text)
	}

	// Verify file exists on disk.
	path := filepath.Join(harness.downloadDir, "test-output.pdf")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("PDF file not found: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("PDF file is empty")
	}
	os.Remove(path)
}
