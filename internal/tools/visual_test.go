package tools

import (
	"bytes"
	"image"
	_ "image/png" // register PNG decoder for image.Decode
	"math"
	"net/http"
	"net/http/httptest"
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

func TestScreenshot(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	result := callToolRaw(t, "screenshot", map[string]any{"tab": tabID})
	if result.IsError {
		t.Fatalf("screenshot returned error")
	}
	// Should have image content.
	found := false
	for _, c := range result.Content {
		if img, ok := c.(*mcp.ImageContent); ok {
			found = true
			if len(img.Data) == 0 {
				t.Error("screenshot returned empty image data")
			}
			if img.MIMEType != "image/png" {
				t.Errorf("screenshot MIME = %q, want image/png", img.MIMEType)
			}
		}
	}
	if !found {
		t.Error("screenshot result has no ImageContent")
	}
}

func TestElementScreenshot(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	result := callToolRaw(t, "screenshot", map[string]any{
		"tab":      tabID,
		"selector": "#title",
	})
	if result.IsError {
		t.Fatalf("element screenshot returned error")
	}
	for _, c := range result.Content {
		if img, ok := c.(*mcp.ImageContent); ok {
			if len(img.Data) == 0 {
				t.Error("element screenshot returned empty data")
			}
			return
		}
	}
	t.Error("element screenshot has no ImageContent")
}

func TestSetViewport(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "set_viewport", map[string]any{
		"tab":    tabID,
		"width":  375,
		"height": 667,
	})

	// Verify via JS.
	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "window.innerWidth",
	})
	if string(out.Result) != "375" {
		t.Errorf("viewport width = %s, want 375", out.Result)
	}
}

func TestFullPageScreenshot(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	result := callToolRaw(t, "screenshot", map[string]any{
		"tab":       tabID,
		"full_page": true,
	})
	if result.IsError {
		t.Fatalf("full_page screenshot error: %s", contentText(result))
	}
	var found bool
	for _, c := range result.Content {
		if img, ok := c.(*mcp.ImageContent); ok {
			found = true
			if len(img.Data) == 0 {
				t.Error("full_page screenshot returned empty data")
			}
		}
	}
	if !found {
		t.Error("full_page screenshot did not return ImageContent")
	}
}

func TestPDF(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	result := callToolRaw(t, "pdf", map[string]any{"tab": tabID})
	if result.IsError {
		t.Fatalf("pdf error: %s", contentText(result))
	}
	var found bool
	for _, c := range result.Content {
		if res, ok := c.(*mcp.EmbeddedResource); ok {
			found = true
			if res.Resource.MIMEType != "application/pdf" {
				t.Errorf("pdf MIME = %q, want application/pdf", res.Resource.MIMEType)
			}
			if len(res.Resource.Blob) == 0 {
				t.Error("pdf returned empty blob")
			}
		}
	}
	if !found {
		t.Error("pdf did not return EmbeddedResource")
	}
}

func TestScreenshotJPEG(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	result := callToolRaw(t, "screenshot", map[string]any{
		"tab":       tabID,
		"format":    "jpeg",
		"quality":   50,
		"full_page": true,
	})
	if result.IsError {
		t.Fatalf("JPEG screenshot error: %s", contentText(result))
	}
	var found bool
	for _, c := range result.Content {
		if img, ok := c.(*mcp.ImageContent); ok {
			found = true
			if img.MIMEType != "image/jpeg" {
				t.Errorf("JPEG screenshot MIME = %q, want image/jpeg", img.MIMEType)
			}
			if len(img.Data) == 0 {
				t.Error("JPEG screenshot returned empty data")
			}
		}
	}
	if !found {
		t.Error("JPEG screenshot did not return ImageContent")
	}
}

func TestScreenshotPNGDefault(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// Default format should be PNG.
	result := callToolRaw(t, "screenshot", map[string]any{
		"tab": tabID,
	})
	if result.IsError {
		t.Fatalf("screenshot error: %s", contentText(result))
	}
	for _, c := range result.Content {
		if img, ok := c.(*mcp.ImageContent); ok {
			if img.MIMEType != "image/png" {
				t.Errorf("default format MIME = %q, want image/png", img.MIMEType)
			}
			// PNG magic bytes: 0x89 0x50 0x4E 0x47
			if len(img.Data) > 4 && img.Data[0] != 0x89 {
				t.Error("PNG data doesn't start with PNG magic bytes")
			}
			return
		}
	}
	t.Error("no ImageContent in result")
}

func TestPDFLandscape(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	result := callToolRaw(t, "pdf", map[string]any{
		"tab":       tabID,
		"landscape": true,
	})
	if result.IsError {
		t.Fatalf("landscape PDF error: %s", contentText(result))
	}
	for _, c := range result.Content {
		if res, ok := c.(*mcp.EmbeddedResource); ok {
			if len(res.Resource.Blob) == 0 {
				t.Error("landscape PDF returned empty blob")
			}
			return
		}
	}
	t.Error("no EmbeddedResource in result")
}

func TestPDFPrintBackgroundFalse(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	result := callToolRaw(t, "pdf", map[string]any{
		"tab":              tabID,
		"print_background": false,
	})
	if result.IsError {
		t.Fatalf("PDF print_background=false error: %s", contentText(result))
	}
	for _, c := range result.Content {
		if res, ok := c.(*mcp.EmbeddedResource); ok {
			if len(res.Resource.Blob) == 0 {
				t.Error("PDF returned empty blob")
			}
			return
		}
	}
	t.Error("no EmbeddedResource in result")
}

func TestPDFCustomScale(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	result := callToolRaw(t, "pdf", map[string]any{
		"tab":   tabID,
		"scale": 0.5,
	})
	if result.IsError {
		t.Fatalf("PDF scale=0.5 error: %s", contentText(result))
	}
	for _, c := range result.Content {
		if res, ok := c.(*mcp.EmbeddedResource); ok {
			if len(res.Resource.Blob) == 0 {
				t.Error("PDF returned empty blob")
			}
			return
		}
	}
	t.Error("no EmbeddedResource in result")
}

func TestSetViewportDeviceScaleFactor(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "set_viewport", map[string]any{
		"tab":                 tabID,
		"width":               800,
		"height":              600,
		"device_scale_factor": 2.0,
	})

	// Verify width.
	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "window.innerWidth",
	})
	if string(out.Result) != "800" {
		t.Errorf("viewport width = %s, want 800", out.Result)
	}

	// Verify height.
	out = callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "window.innerHeight",
	})
	if string(out.Result) != "600" {
		t.Errorf("viewport height = %s, want 600", out.Result)
	}

	// Verify device pixel ratio matches scale factor.
	out = callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "window.devicePixelRatio",
	})
	if string(out.Result) != "2" {
		t.Errorf("devicePixelRatio = %s, want 2", out.Result)
	}
}

func TestSetViewportMobile(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "set_viewport", map[string]any{
		"tab":    tabID,
		"width":  390,
		"height": 844,
		"mobile": true,
	})

	// In mobile mode, screen.width/height reflects the device metrics
	// regardless of layout viewport.
	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "screen.width",
	})
	if string(out.Result) != "390" {
		t.Errorf("screen.width = %s, want 390", out.Result)
	}

	out = callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "screen.height",
	})
	if string(out.Result) != "844" {
		t.Errorf("screen.height = %s, want 844", out.Result)
	}
}

func TestScreenshotSelectorOverridesFullPage(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// When both selector and full_page are given, selector should take priority.
	result := callToolRaw(t, "screenshot", map[string]any{
		"tab":       tabID,
		"selector":  "#title",
		"full_page": true,
	})
	if result.IsError {
		t.Fatalf("screenshot error: %s", contentText(result))
	}
	// Get element screenshot size for comparison.
	var selectorSize int
	for _, c := range result.Content {
		if img, ok := c.(*mcp.ImageContent); ok {
			selectorSize = len(img.Data)
		}
	}

	// Now get a full-page screenshot (no selector).
	result2 := callToolRaw(t, "screenshot", map[string]any{
		"tab":       tabID,
		"full_page": true,
	})
	var fullSize int
	for _, c := range result2.Content {
		if img, ok := c.(*mcp.ImageContent); ok {
			fullSize = len(img.Data)
		}
	}

	// Element screenshot should be smaller than full page.
	if selectorSize >= fullSize {
		t.Errorf("element screenshot (%d bytes) should be smaller than full page (%d bytes)", selectorSize, fullSize)
	}
}

func TestPDFCustomPaperSize(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	result := callToolRaw(t, "pdf", map[string]any{
		"tab":          tabID,
		"paper_width":  11,
		"paper_height": 17,
	})
	if result.IsError {
		t.Fatalf("PDF custom paper error: %s", contentText(result))
	}
	for _, c := range result.Content {
		if res, ok := c.(*mcp.EmbeddedResource); ok {
			if len(res.Resource.Blob) == 0 {
				t.Error("PDF returned empty blob")
			}
			return
		}
	}
	t.Error("no EmbeddedResource in result")
}

func TestPDFCombinedParams(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	result := callToolRaw(t, "pdf", map[string]any{
		"tab":              tabID,
		"landscape":        true,
		"scale":            0.75,
		"print_background": true,
	})
	if result.IsError {
		t.Fatalf("PDF combined params error: %s", contentText(result))
	}
	for _, c := range result.Content {
		if res, ok := c.(*mcp.EmbeddedResource); ok {
			if len(res.Resource.Blob) == 0 {
				t.Error("PDF returned empty blob")
			}
			return
		}
	}
	t.Error("no EmbeddedResource in result")
}

func TestSetViewportDefaultScaleFactor(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "set_viewport", map[string]any{
		"tab":    tabID,
		"width":  1024,
		"height": 768,
		// device_scale_factor omitted — should default to 1.0.
	})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "window.devicePixelRatio",
	})
	if string(out.Result) != "1" {
		t.Errorf("default devicePixelRatio = %s, want 1", out.Result)
	}
}

func TestScreenshotJPEGViewport(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	result := callToolRaw(t, "screenshot", map[string]any{
		"tab":    tabID,
		"format": "jpeg",
	})
	if result.IsError {
		t.Fatalf("screenshot error: %s", contentText(result))
	}
	for _, c := range result.Content {
		if img, ok := c.(*mcp.ImageContent); ok {
			if img.MIMEType != "image/jpeg" {
				t.Errorf("MIME = %q, want image/jpeg", img.MIMEType)
			}
			if len(img.Data) == 0 {
				t.Error("screenshot returned empty data")
			}
			// JPEG magic bytes: FF D8.
			if len(img.Data) >= 2 && (img.Data[0] != 0xFF || img.Data[1] != 0xD8) {
				t.Error("data does not start with JPEG magic bytes")
			}
			return
		}
	}
	t.Error("no ImageContent in result")
}

func TestPDFPageRanges(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	result := callToolRaw(t, "pdf", map[string]any{
		"tab":         tabID,
		"page_ranges": "1",
	})
	if result.IsError {
		t.Fatalf("PDF page_ranges error: %s", contentText(result))
	}
	for _, c := range result.Content {
		if res, ok := c.(*mcp.EmbeddedResource); ok {
			if len(res.Resource.Blob) == 0 {
				t.Error("PDF returned empty blob")
			}
			// Verify PDF magic bytes.
			if len(res.Resource.Blob) >= 4 && string(res.Resource.Blob[:4]) != "%PDF" {
				t.Error("PDF data doesn't start with %PDF magic")
			}
			return
		}
	}
	t.Error("no EmbeddedResource in result")
}

func TestSetViewportScaleFactorZero(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "set_viewport", map[string]any{
		"tab":                 tabID,
		"width":               800,
		"height":              600,
		"device_scale_factor": 0,
	})

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "window.devicePixelRatio",
	})
	if string(out.Result) != "1" {
		t.Errorf("scale=0 devicePixelRatio = %s, want 1 (default)", out.Result)
	}
}

// TestElementScaleWithinMax verifies element screenshots succeed without
// downscaling when the element is smaller than max_dimension.
func TestElementScaleWithinMax(t *testing.T) {
	tabID := navigateToFixture(t, "cdp-coords.html")
	defer closeTab(t, tabID)

	// #top-box is 100x80 per the fixture; max_dimension=1000 requires no downscale.
	result := callToolRaw(t, "screenshot", map[string]any{
		"tab":           tabID,
		"selector":      "#top-box",
		"max_dimension": 1000,
	})
	if result.IsError {
		t.Fatalf("screenshot with max_dimension (no downscale) failed: %s", contentText(result))
	}
}

// TestElementScaleDownscale verifies element screenshots downscale when
// the element exceeds max_dimension.
func TestElementScaleDownscale(t *testing.T) {
	tabID := navigateToFixture(t, "cdp-coords.html")
	defer closeTab(t, tabID)

	// #top-box is 100x80. With max_dimension=50, scale should be ~0.5.
	// The resulting screenshot should succeed and be smaller.
	result := callToolRaw(t, "screenshot", map[string]any{
		"tab":           tabID,
		"selector":      "#top-box",
		"max_dimension": 50,
	})
	if result.IsError {
		t.Fatalf("screenshot with max_dimension failed: %s", contentText(result))
	}
}

// TestScreenshotNonExistentSelector verifies that a screenshot of a
// non-existent element returns a clear error.
func TestScreenshotNonExistentSelector(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "screenshot", map[string]any{
		"tab":      tabID,
		"selector": "#does-not-exist-at-all",
	})
	if errText == "" {
		t.Fatal("screenshot with non-existent selector should error")
	}
	// The error could be "not found" (selectorError) or "context deadline exceeded"
	// (chromedp timeout). Either way, a clear error is returned.
	lowerErr := strings.ToLower(errText)
	if !strings.Contains(lowerErr, "not found") && !strings.Contains(lowerErr, "deadline") && !strings.Contains(lowerErr, "timeout") {
		t.Errorf("error = %q, want to contain 'not found', 'deadline', or 'timeout'", errText)
	}
}

func TestScreenshotMaxDimensionInvalidSelector(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// Use max_dimension with a valid selector that exists.
	result := callToolRaw(t, "screenshot", map[string]any{
		"tab":           tabID,
		"selector":      "#title",
		"max_dimension": 500,
	})
	if result.IsError {
		t.Errorf("screenshot with valid selector and max_dimension should succeed, got: %s", contentText(result))
	}
}

func TestScreenshotMaxDimensionViewport(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// Viewport screenshot with max_dimension.
	result := callToolRaw(t, "screenshot", map[string]any{
		"tab":           tabID,
		"max_dimension": 500,
	})
	if result.IsError {
		t.Errorf("viewport screenshot with max_dimension should succeed, got: %s", contentText(result))
	}
}

func TestScreenshotMaxDimensionFullPage(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// Full-page screenshot with max_dimension.
	result := callToolRaw(t, "screenshot", map[string]any{
		"tab":           tabID,
		"full_page":     true,
		"max_dimension": 500,
	})
	if result.IsError {
		t.Errorf("full-page screenshot with max_dimension should succeed, got: %s", contentText(result))
	}
}

func TestScreenshotElementJPEG(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// Take an element screenshot in JPEG format.
	result := callToolRaw(t, "screenshot", map[string]any{
		"tab":      tabID,
		"selector": "#title",
		"format":   "jpeg",
		"quality":  80,
	})
	if result.IsError {
		t.Fatalf("JPEG element screenshot error: %s", contentText(result))
	}

	var found bool
	for _, c := range result.Content {
		if img, ok := c.(*mcp.ImageContent); ok {
			found = true
			if img.MIMEType != "image/jpeg" {
				t.Errorf("MIME = %q, want image/jpeg", img.MIMEType)
			}
			// JPEG files start with 0xFF 0xD8.
			if len(img.Data) >= 2 && (img.Data[0] != 0xFF || img.Data[1] != 0xD8) {
				t.Errorf("data starts with %x %x, want JPEG magic bytes FF D8", img.Data[0], img.Data[1])
			}
		}
	}
	if !found {
		t.Error("JPEG element screenshot did not return ImageContent")
	}
}

func TestScreenshotElementPNG(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// Element screenshot with default (PNG) format should still work.
	result := callToolRaw(t, "screenshot", map[string]any{
		"tab":      tabID,
		"selector": "#title",
	})
	if result.IsError {
		t.Fatalf("PNG element screenshot error: %s", contentText(result))
	}

	for _, c := range result.Content {
		if img, ok := c.(*mcp.ImageContent); ok {
			if img.MIMEType != "image/png" {
				t.Errorf("MIME = %q, want image/png", img.MIMEType)
			}
			// PNG files start with 0x89 0x50 0x4E 0x47.
			if len(img.Data) >= 4 && img.Data[0] != 0x89 {
				t.Error("data doesn't start with PNG magic bytes")
			}
			return
		}
	}
	t.Error("PNG element screenshot did not return ImageContent")
}

func TestScreenshotElementJPEGWithMaxDim(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// Combine JPEG format with max_dimension for element screenshot.
	result := callToolRaw(t, "screenshot", map[string]any{
		"tab":           tabID,
		"selector":      "#title",
		"format":        "jpeg",
		"quality":       70,
		"max_dimension": 200,
	})
	if result.IsError {
		t.Fatalf("JPEG element screenshot with max_dim error: %s", contentText(result))
	}

	for _, c := range result.Content {
		if img, ok := c.(*mcp.ImageContent); ok {
			if img.MIMEType != "image/jpeg" {
				t.Errorf("MIME = %q, want image/jpeg", img.MIMEType)
			}
			if len(img.Data) >= 2 && (img.Data[0] != 0xFF || img.Data[1] != 0xD8) {
				t.Errorf("data starts with %x %x, want JPEG magic bytes FF D8", img.Data[0], img.Data[1])
			}
			return
		}
	}
	t.Error("no ImageContent in result")
}

func TestScreenshotRefShadowFrameAndAtomicWrite(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/frame.html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><html><body style="margin:0;background:#fff;">
			<button id="if-red" style="position:absolute;left:20px;top:15px;width:80px;height:40px;border:0;background:rgb(255,0,0);color:rgb(255,0,0);">Frame Red</button>
			<div style="height:500px;"></div>
			<button id="if-far-green" style="width:80px;height:40px;border:0;background:rgb(0,200,0);color:rgb(0,200,0);">Far Green</button>
		</body></html>`))
	})
	mux.HandleFunc("/scaled.html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><html><body style="margin:0;background:#fff;">
			<div id="sc-purple" style="margin:20px;width:120px;height:80px;background:rgb(180,0,220);"></div>
		</body></html>`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><html><body style="margin:0;background:#fff;">
			<div id="s-host" style="margin:10px;"></div>
			<iframe id="f" src="/frame.html" style="margin-left:50px;margin-top:30px;border:4px solid #000;padding:6px;width:200px;height:120px;"></iframe>
			<iframe id="f-scaled" src="/scaled.html" style="position:absolute;left:320px;top:40px;width:200px;height:140px;border:0;transform:scale(0.5);transform-origin:0 0;"></iframe>
			<script>
				const sr = document.getElementById('s-host').attachShadow({mode: 'open'});
				sr.innerHTML = '<button id="s-blue" style="width:90px;height:36px;border:0;background:rgb(0,0,255);color:rgb(0,0,255);">Shadow Blue</button>';
			</script>
		</body></html>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)
	callTool[NavigateOutput](t, "navigate", map[string]any{"tab": tabID, "url": srv.URL + "/"})
	callTool[struct{}](t, "wait_for", map[string]any{
		"tab":        tabID,
		"expression": `document.getElementById('f').contentDocument?.getElementById('if-red') && document.getElementById('f-scaled').contentDocument?.getElementById('sc-purple')`,
	})

	decodePNG := func(res *mcp.CallToolResult) image.Image {
		t.Helper()
		if res.IsError {
			t.Fatalf("screenshot failed: %s", contentText(res))
		}
		for _, c := range res.Content {
			if img, ok := c.(*mcp.ImageContent); ok {
				decoded, _, err := image.Decode(bytes.NewReader(img.Data))
				if err != nil {
					t.Fatalf("decode PNG: %v", err)
				}
				return decoded
			}
		}
		t.Fatal("screenshot returned no ImageContent")
		return nil
	}

	// 1. Screenshot inside open Shadow DOM via '>>>'.
	imgShadow := decodePNG(callToolRaw(t, "screenshot", map[string]any{
		"tab":      tabID,
		"selector": "#s-host >>> #s-blue",
	}))
	r, g, b, _ := imgShadow.At(imgShadow.Bounds().Dx()/2, imgShadow.Bounds().Dy()/2).RGBA()
	if b < 0xC000 || r > 0x4000 || g > 0x4000 {
		t.Errorf("shadow screenshot center pixel = (%#x,%#x,%#x), want blue", r, g, b)
	}

	// 2. Screenshot via snapshot ref of the Shadow DOM button.
	snap := callTool[SnapshotOutput](t, "snapshot", map[string]any{"tab": tabID})
	blueRef := findSnapshotRef(t, snap.Snapshot, `"Shadow Blue"`)
	imgRef := decodePNG(callToolRaw(t, "screenshot", map[string]any{
		"tab": tabID,
		"ref": blueRef,
	}))
	r, g, b, _ = imgRef.At(imgRef.Bounds().Dx()/2, imgRef.Bounds().Dy()/2).RGBA()
	if b < 0xC000 || r > 0x4000 || g > 0x4000 {
		t.Errorf("ref screenshot center pixel = (%#x,%#x,%#x), want blue", r, g, b)
	}

	// 3. Screenshot of elements inside same-origin iframes (border/padding,
	//    offscreen-inside-iframe scroll-into-view, and CSS scale(0.5)).
	frames := callTool[ListFramesOutput](t, "list_frames", map[string]any{"tab": tabID})
	var childID, scaledID string
	for _, fr := range frames.Frames {
		if strings.HasSuffix(fr.URL, "/frame.html") {
			childID = fr.ID
		} else if strings.HasSuffix(fr.URL, "/scaled.html") {
			scaledID = fr.ID
		}
	}
	if childID == "" || scaledID == "" {
		t.Fatalf("expected child and scaled frames in %+v", frames.Frames)
	}
	imgFrame := decodePNG(callToolRaw(t, "screenshot", map[string]any{
		"tab":      tabID,
		"frame":    childID,
		"selector": "#if-red",
	}))
	r, g, b, _ = imgFrame.At(imgFrame.Bounds().Dx()/2, imgFrame.Bounds().Dy()/2).RGBA()
	if r < 0xC000 || g > 0x4000 || b > 0x4000 {
		t.Errorf("iframe screenshot center pixel = (%#x,%#x,%#x), want red (bounds=%v)", r, g, b, imgFrame.Bounds())
	}

	fSnap := callTool[SnapshotOutput](t, "snapshot", map[string]any{"tab": tabID, "frame": childID})
	redRef := findSnapshotRef(t, fSnap.Snapshot, `"Frame Red"`)
	imgFrameRef := decodePNG(callToolRaw(t, "screenshot", map[string]any{
		"tab": tabID,
		"ref": redRef,
	}))
	r, g, b, _ = imgFrameRef.At(imgFrameRef.Bounds().Dx()/2, imgFrameRef.Bounds().Dy()/2).RGBA()
	if r < 0xC000 || g > 0x4000 || b > 0x4000 {
		t.Errorf("iframe ref screenshot center pixel = (%#x,%#x,%#x), want red", r, g, b)
	}

	// Offscreen element inside iframe scrolls into iframe viewport and captures green.
	imgFarGreen := decodePNG(callToolRaw(t, "screenshot", map[string]any{
		"tab":      tabID,
		"frame":    childID,
		"selector": "#if-far-green",
	}))
	r, g, b, _ = imgFarGreen.At(imgFarGreen.Bounds().Dx()/2, imgFarGreen.Bounds().Dy()/2).RGBA()
	if g < 0xA000 || r > 0x4000 || b > 0x4000 {
		t.Errorf("offscreen iframe screenshot center pixel = (%#x,%#x,%#x), want green (bounds=%v)", r, g, b, imgFarGreen.Bounds())
	}

	// Scaled iframe (transform: scale(0.5)) projects clip coordinates and size accurately.
	imgScaled := decodePNG(callToolRaw(t, "screenshot", map[string]any{
		"tab":      tabID,
		"frame":    scaledID,
		"selector": "#sc-purple",
	}))
	r, g, b, _ = imgScaled.At(imgScaled.Bounds().Dx()/2, imgScaled.Bounds().Dy()/2).RGBA()
	if r < 0x8000 || b < 0xA000 || g > 0x4000 {
		t.Errorf("scaled iframe screenshot center pixel = (%#x,%#x,%#x), want purple (bounds=%v)", r, g, b, imgScaled.Bounds())
	}
	if imgScaled.Bounds().Dx() > 70 || imgScaled.Bounds().Dy() > 50 {
		t.Errorf("scaled iframe screenshot bounds = %v, want ~60x40 (scaled 0.5)", imgScaled.Bounds())
	}

	// 4. Atomic writeDownloadFile overwrites cleanly and leaves zero temp files behind.
	dir := t.TempDir()
	p1, err := writeDownloadFile(dir, "shot.png", []byte("first"))
	if err != nil {
		t.Fatalf("writeDownloadFile #1: %v", err)
	}
	p2, err := writeDownloadFile(dir, "shot.png", []byte("second-payload"))
	if err != nil {
		t.Fatalf("writeDownloadFile #2: %v", err)
	}
	if p1 != p2 {
		t.Errorf("paths differ: %q vs %q", p1, p2)
	}
	got, err := os.ReadFile(p2)
	if err != nil || string(got) != "second-payload" {
		t.Errorf("read overwritten file = %q err=%v, want 'second-payload'", got, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "shot.png" {
		t.Errorf("unexpected leftover files in download dir: %+v", entries)
	}
}
