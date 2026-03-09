package tools

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
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

// makePNG creates a solid-colored PNG image of the given dimensions and returns
// the encoded bytes.
func makePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: 100, G: 150, B: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encoding test PNG: %v", err)
	}
	return buf.Bytes()
}

func TestConstrainImageSize(t *testing.T) {
	t.Run("no-op when within limits", func(t *testing.T) {
		data := makePNG(t, 800, 600)
		out, err := constrainImageSize(data, 8000, "png", 0)
		if err != nil {
			t.Fatal(err)
		}
		// Should return the original bytes unchanged.
		if !bytes.Equal(out, data) {
			t.Error("expected original bytes to be returned unchanged")
		}
	})

	t.Run("downscales when width exceeds", func(t *testing.T) {
		data := makePNG(t, 10000, 5000)
		out, err := constrainImageSize(data, 8000, "png", 0)
		if err != nil {
			t.Fatal(err)
		}
		img, _, err := image.Decode(bytes.NewReader(out))
		if err != nil {
			t.Fatal(err)
		}
		bounds := img.Bounds()
		if bounds.Dx() != 8000 {
			t.Errorf("width = %d, want 8000", bounds.Dx())
		}
		if bounds.Dy() != 4000 {
			t.Errorf("height = %d, want 4000", bounds.Dy())
		}
	})

	t.Run("downscales when height exceeds", func(t *testing.T) {
		data := makePNG(t, 750, 16000)
		out, err := constrainImageSize(data, 8000, "png", 0)
		if err != nil {
			t.Fatal(err)
		}
		img, _, err := image.Decode(bytes.NewReader(out))
		if err != nil {
			t.Fatal(err)
		}
		bounds := img.Bounds()
		if bounds.Dy() != 8000 {
			t.Errorf("height = %d, want 8000", bounds.Dy())
		}
		// 750 * (8000/16000) = 375
		if bounds.Dx() != 375 {
			t.Errorf("width = %d, want 375", bounds.Dx())
		}
	})

	t.Run("downscales jpeg format", func(t *testing.T) {
		data := makePNG(t, 10000, 10000)
		out, err := constrainImageSize(data, 5000, "jpeg", 80)
		if err != nil {
			t.Fatal(err)
		}
		img, _, err := image.Decode(bytes.NewReader(out))
		if err != nil {
			t.Fatal(err)
		}
		bounds := img.Bounds()
		if bounds.Dx() != 5000 || bounds.Dy() != 5000 {
			t.Errorf("dimensions = %dx%d, want 5000x5000", bounds.Dx(), bounds.Dy())
		}
	})

	t.Run("exact limit is not downscaled", func(t *testing.T) {
		data := makePNG(t, 8000, 8000)
		out, err := constrainImageSize(data, 8000, "png", 0)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(out, data) {
			t.Error("image at exact limit should not be re-encoded")
		}
	})
}

// TestScreenshotMaxDimension verifies that the screenshot tool respects
// max_dimension by downscaling oversized captures.
func TestScreenshotMaxDimension(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// Set a large viewport so the screenshot will be big.
	callToolRaw(t, "set_viewport", map[string]any{
		"tab":    tabID,
		"width":  1920,
		"height": 1080,
	})

	// Take a full-page screenshot with a restrictive max_dimension.
	result := callToolRaw(t, "screenshot", map[string]any{
		"tab":           tabID,
		"full_page":     true,
		"max_dimension": 500,
	})
	if result.IsError {
		t.Fatalf("screenshot error: %s", contentText(result))
	}

	for _, c := range result.Content {
		if img, ok := c.(*mcp.ImageContent); ok {
			decoded, _, err := image.Decode(bytes.NewReader(img.Data))
			if err != nil {
				t.Fatalf("decoding result image: %v", err)
			}
			bounds := decoded.Bounds()
			if bounds.Dx() > 500 || bounds.Dy() > 500 {
				t.Errorf("dimensions %dx%d exceed max_dimension 500", bounds.Dx(), bounds.Dy())
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
