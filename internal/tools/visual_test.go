package tools

import (
	"bytes"
	"image"
	_ "image/png" // register PNG decoder for image.Decode
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestValidateAndWriteDownloadFile(t *testing.T) {
	data := []byte("test data")
	save := func(dir, filename, ext string) (string, error) {
		name, err := validateSaveFilename(dir, filename, ext)
		if err != nil {
			return "", err
		}
		return writeDownloadFile(dir, name, data)
	}

	t.Run("no dir configured", func(t *testing.T) {
		_, err := save("", "file.png", ".png")
		if err == nil {
			t.Fatal("expected error when dir is empty")
		}
		if !strings.Contains(err.Error(), "--download-dir not configured") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("writes file with explicit name", func(t *testing.T) {
		dir := t.TempDir()
		path, err := save(dir, "shot.png", ".png")
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
		path, err := save(dir, "shot", ".png")
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
		path, err := save(dir, "report.pdf", ".png")
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Base(path) != "report.pdf" {
			t.Fatalf("expected report.pdf, got %s", filepath.Base(path))
		}
	})

	t.Run("rejects empty filename", func(t *testing.T) {
		dir := t.TempDir()
		if _, err := save(dir, "", ".jpg"); err == nil {
			t.Fatal("expected error for empty filename")
		}
	})

	t.Run("blocks path traversal and special segments", func(t *testing.T) {
		dir := t.TempDir()
		cases := []string{
			"../etc/passwd",
			"subdir/file.png",
			"./file.png",
			".",
			"..",
			`sub\file.png`,
			"bad\x00name.png",
		}
		for _, name := range cases {
			_, err := save(dir, name, ".png")
			if err == nil {
				t.Fatalf("expected error for filename %q", name)
			}
		}
	})

	t.Run("creates directory if missing", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "nested", "dir")
		path, err := save(dir, "file.png", ".png")
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

func decodeScreenshotImage(t *testing.T, result *mcp.CallToolResult) image.Image {
	t.Helper()
	if result.IsError {
		t.Fatalf("screenshot returned error: %s", contentText(result))
	}
	for _, c := range result.Content {
		if imgContent, ok := c.(*mcp.ImageContent); ok {
			img, _, err := image.Decode(bytes.NewReader(imgContent.Data))
			if err != nil {
				t.Fatalf("decode screenshot image: %v", err)
			}
			return img
		}
	}
	t.Fatal("no ImageContent in screenshot result")
	return nil
}

// TestScreenshotScrolledElementAndViewportCapturesActualPixels verifies that
// scrolled element screenshots and scrolled max_dimension viewport screenshots
// capture the scrolled coordinates rather than (0, 0) at the top of the page.
func TestScreenshotScrolledElementAndViewportCapturesActualPixels(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	// Build a 2600px tall page: solid red box at y=0..200, solid blue box at y=2000..2200.
	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": `(() => {
			document.body.style.margin = '0';
			document.body.style.background = '#ffffff';
			document.body.innerHTML =
				'<div id="top-red" style="position:absolute;top:0;left:0;width:300px;height:200px;background:#ff0000"></div>' +
				'<div id="bottom-blue" style="position:absolute;top:2000px;left:20px;width:240px;height:180px;background:#0000ff"></div>' +
				'<div style="height:2600px"></div>';
			window.scrollTo(0, 1950);
			return window.scrollY;
		})()`,
	})

	// 1. Element screenshot of #bottom-blue while scrolled down: center pixel MUST be blue.
	elRes := callToolRaw(t, "screenshot", map[string]any{
		"tab":      tabID,
		"selector": "#bottom-blue",
	})
	elImg := decodeScreenshotImage(t, elRes)
	b := elImg.Bounds()
	r, g, bl, _ := elImg.At(b.Dx()/2, b.Dy()/2).RGBA()
	if bl>>8 < 200 || r>>8 > 50 || g>>8 > 50 {
		t.Fatalf("scrolled element center pixel RGB=(%d,%d,%d), want solid blue (captured wrong scroll offset)", r>>8, g>>8, bl>>8)
	}

	// 2. Scrolled viewport screenshot with max_dimension: must stay <= 300px and contain blue pixels, not red.
	vpRes := callToolRaw(t, "screenshot", map[string]any{
		"tab":           tabID,
		"max_dimension": 300,
	})
	vpImg := decodeScreenshotImage(t, vpRes)
	vb := vpImg.Bounds()
	if vb.Dx() > 300 || vb.Dy() > 300 {
		t.Fatalf("scrolled viewport dims %dx%d exceed max_dimension 300", vb.Dx(), vb.Dy())
	}
	foundBlue, foundRed := false, false
	for y := vb.Min.Y; y < vb.Max.Y; y += 2 {
		for x := vb.Min.X; x < vb.Max.X; x += 2 {
			pr, pg, pb, _ := vpImg.At(x, y).RGBA()
			if pb>>8 > 200 && pr>>8 < 50 && pg>>8 < 50 {
				foundBlue = true
			}
			if pr>>8 > 200 && pb>>8 < 50 && pg>>8 < 50 {
				foundRed = true
			}
		}
	}
	if !foundBlue || foundRed {
		t.Fatalf("scrolled viewport with max_dimension: foundBlue=%v foundRed=%v, want blue=true red=false", foundBlue, foundRed)
	}
}

// TestScreenshotMaxDimensionRespectsDPR verifies that max_dimension caps
// physical pixel dimensions when device_scale_factor > 1.
func TestScreenshotMaxDimensionRespectsDPR(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "set_viewport", map[string]any{
		"tab":                 tabID,
		"width":               800,
		"height":              600,
		"device_scale_factor": 2.0,
	})

	// At DPR=2, 800x600 CSS viewport would render 1600x1200 physical px without DPR scaling.
	res := callToolRaw(t, "screenshot", map[string]any{
		"tab":           tabID,
		"max_dimension": 500,
	})
	img := decodeScreenshotImage(t, res)
	if img.Bounds().Dx() > 500 || img.Bounds().Dy() > 500 {
		t.Fatalf("DPR=2 screenshot dims = %dx%d, want both <= 500", img.Bounds().Dx(), img.Bounds().Dy())
	}

	// Non-dyadic ratio (503 * 2 = 1006, max_dimension = 500) must not overshoot to 501 due to float32 ceil.
	s := downscaleFactor(503, 400, 2.0, 500)
	if ceilPx := math.Ceil(float64(float32(s)) * 503 * 2.0); ceilPx > 500 {
		t.Fatalf("downscaleFactor(503, 400, 2, 500) = %v: float32 ceil = %v, want <= 500", s, ceilPx)
	}
	callTool[struct{}](t, "set_viewport", map[string]any{
		"tab":                 tabID,
		"width":               503,
		"height":              400,
		"device_scale_factor": 2.0,
	})
	resOdd := callToolRaw(t, "screenshot", map[string]any{
		"tab":           tabID,
		"max_dimension": 500,
	})
	imgOdd := decodeScreenshotImage(t, resOdd)
	if imgOdd.Bounds().Dx() > 500 || imgOdd.Bounds().Dy() > 500 {
		t.Fatalf("non-dyadic DPR=2 screenshot dims = %dx%d, want both <= 500", imgOdd.Bounds().Dx(), imgOdd.Bounds().Dy())
	}
}

// TestScreenshotElementWithHTMLMargin verifies element screenshots align
// accurately when <html> has non-zero margin.
func TestScreenshotElementWithHTMLMargin(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": `(() => {
			document.documentElement.style.margin = '60px 0 0 40px';
			document.documentElement.style.background = '#ff0000';
			document.body.style.margin = '0';
			document.body.innerHTML = '<div id="target" style="width:120px;height:120px;background:#00ff00"></div>';
			return true;
		})()`,
	})

	res := callToolRaw(t, "screenshot", map[string]any{
		"tab":      tabID,
		"selector": "#target",
	})
	img := decodeScreenshotImage(t, res)
	b := img.Bounds()
	// Check top-left inset (10,10) as well as center: if offset by html margin, top band would be red/white.
	for _, pt := range [][2]int{{10, 10}, {b.Dx() / 2, b.Dy() / 2}} {
		r, g, bl, _ := img.At(pt[0], pt[1]).RGBA()
		if g>>8 < 200 || r>>8 > 50 || bl>>8 > 50 {
			t.Fatalf("pixel (%d,%d) RGB=(%d,%d,%d), want solid green (html margin misaligned clip)", pt[0], pt[1], r>>8, g>>8, bl>>8)
		}
	}
}

// TestScreenshotValidationAndSelectorTimeout verifies upfront input validation
// and bounded selector timeouts on screenshot, pdf, and set_viewport.
func TestScreenshotValidationAndSelectorTimeout(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	if err := callToolExpectErr(t, "screenshot", map[string]any{"tab": tabID, "format": "webp"}); !strings.Contains(err, "format") {
		t.Errorf("invalid format err = %q", err)
	}
	if err := callToolExpectErr(t, "screenshot", map[string]any{"tab": tabID, "format": "jpeg", "quality": 101}); !strings.Contains(err, "quality") {
		t.Errorf("quality=101 err = %q", err)
	}
	if err := callToolExpectErr(t, "screenshot", map[string]any{"tab": tabID, "max_dimension": -10}); !strings.Contains(err, "max_dimension") {
		t.Errorf("max_dimension=-10 err = %q", err)
	}
	if err := callToolExpectErr(t, "screenshot", map[string]any{"tab": tabID, "filename": ".."}); !strings.Contains(err, "invalid filename") {
		t.Errorf("filename=.. err = %q", err)
	}
	if err := callToolExpectErr(t, "pdf", map[string]any{"tab": tabID, "scale": -1}); !strings.Contains(err, "scale") {
		t.Errorf("pdf scale=-1 err = %q", err)
	}
	if err := callToolExpectErr(t, "set_viewport", map[string]any{"tab": tabID, "width": 0, "height": 600}); !strings.Contains(err, "width and height") {
		t.Errorf("set_viewport width=0 err = %q", err)
	}

	// Selector timeout must be bounded by input.Timeout and wrapped by selectorError.
	start := time.Now()
	errText := callToolExpectErr(t, "screenshot", map[string]any{
		"tab":      tabID,
		"selector": "#does-not-exist-anywhere",
		"timeout":  250,
	})
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("screenshot selector timeout took %v, want bounded near 250ms", elapsed)
	}
	if !strings.Contains(errText, "not found in the DOM") {
		t.Errorf("screenshot missing selector err = %q, want 'not found in the DOM'", errText)
	}
}
