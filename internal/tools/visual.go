package tools

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/browser"
)

// screenshotInput is the input for screenshot.
type screenshotInput struct {
	selectorInput
	Ref          string `json:"ref,omitempty" jsonschema:"Element ref from snapshot to screenshot (mutually exclusive with selector/frame)."`
	Frame        string `json:"frame,omitempty" jsonschema:"Optional same-origin frame ID for selector."`
	Selector     string `json:"selector,omitempty" jsonschema:"CSS/shadow ('>>>') selector to screenshot a specific element. If both ref and selector are omitted, captures the viewport."`
	FullPage     bool   `json:"full_page,omitempty" jsonschema:"Capture the full scrollable page (default false). Ignored if ref or selector is set."`
	Format       string `json:"format,omitempty" jsonschema:"Image format: png (default) or jpeg."`
	Quality      int    `json:"quality,omitempty" jsonschema:"JPEG quality 1-100 (default 80). Ignored for PNG."`
	Filename     string `json:"filename,omitempty" jsonschema:"Save to disk with this filename (requires --download-dir). Omit to return inline only. The image is still returned inline."`
	MaxDimension int    `json:"max_dimension,omitempty" jsonschema:"Maximum allowed width or height in physical pixels (downscaled proportionally if exceeded)."`
}

// pdfInput is the input for pdf.
type pdfInput struct {
	tabInput
	Landscape       bool    `json:"landscape,omitempty" jsonschema:"Landscape orientation (default false)"`
	PrintBackground *bool   `json:"print_background,omitempty" jsonschema:"Include background graphics (default true)"`
	Scale           float64 `json:"scale,omitempty" jsonschema:"Page rendering scale (default 1.0)"`
	PaperWidth      float64 `json:"paper_width,omitempty" jsonschema:"Paper width in inches (default 8.5)"`
	PaperHeight     float64 `json:"paper_height,omitempty" jsonschema:"Paper height in inches (default 11)"`
	PageRanges      string  `json:"page_ranges,omitempty" jsonschema:"Page ranges e.g. '1-5, 8'. Defaults to all pages."`
	Filename        string  `json:"filename,omitempty" jsonschema:"Save to disk with this filename (requires --download-dir). Omit to return inline only."`
}

var screenshotFormatValues = []string{"png", "jpeg"}

func registerVisualTools(s *mcp.Server, mgr *browser.Manager, opts resolvedOptions) {
	// screenshot and pdf are read-only when --download-dir is not configured
	// (filename always errors), and write/overwrite files on disk when it is.
	captureAnnotations := func() *mcp.ToolAnnotations {
		if opts.downloadDir != "" {
			return idempotentWriteAnnotations()
		}
		return readOnlyAnnotations()
	}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "screenshot",
		Description: "Capture a screenshot of the viewport, full page, or a specific element (by snapshot ref or CSS/shadow selector).",
		InputSchema: schemaWithEnums[screenshotInput](map[string][]string{
			"format": screenshotFormatValues,
		}),
		Annotations: captureAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input screenshotInput) (*mcp.CallToolResult, struct{}, error) {
		if err := validateElementTarget(input.Ref, input.Selector, input.Frame, false); err != nil {
			return nil, struct{}{}, err
		}
		if input.Frame != "" && input.Selector == "" {
			return nil, struct{}{}, fmt.Errorf("frame requires selector")
		}
		if err := validateSelectorTimeout(input.Timeout); err != nil {
			return nil, struct{}{}, err
		}
		if err := validateEnum("format", input.Format, true, screenshotFormatValues); err != nil {
			return nil, struct{}{}, err
		}
		format := page.CaptureScreenshotFormatPng
		mimeType := "image/png"
		ext := ".png"
		jpegQuality := int64(0)
		if input.Quality < 0 || input.Quality > 100 {
			return nil, struct{}{}, fmt.Errorf("quality must be between 1 and 100, got %d", input.Quality)
		}
		if input.Format == "jpeg" {
			format = page.CaptureScreenshotFormatJpeg
			mimeType = "image/jpeg"
			ext = ".jpg"
			jpegQuality = int64(input.Quality)
			if jpegQuality == 0 {
				jpegQuality = 80
			}
		}
		if input.MaxDimension < 0 {
			return nil, struct{}{}, fmt.Errorf("max_dimension must be >= 0, got %d", input.MaxDimension)
		}

		var saveName string
		if input.Filename != "" {
			var err error
			saveName, err = validateSaveFilename(opts.downloadDir, input.Filename, ext)
			if err != nil {
				return nil, struct{}{}, err
			}
		}

		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}

		var buf []byte
		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()

		if input.Ref != "" || input.Selector != "" {
			err = chromedp.Do(tctx, chromedp.Func(func(ctx context.Context, tgt *chromedp.Target) error {
				el, err := resolveElementObject(ctx, tgt, t, input.Ref, input.Selector, input.Frame, input.Timeout, false)
				if err != nil {
					return err
				}
				defer el.Release(ctx, tgt)

				if err := waitForElementLayoutBox(ctx, tgt, el, input.Timeout); err != nil {
					return err
				}
				buf, err = captureElementObject(ctx, tgt, el, format, jpegQuality, input.MaxDimension)
				return err
			}))
			if err != nil {
				return nil, struct{}{}, err
			}
		} else {
			// Viewport or full-page screenshot. Build the CDP command
			// directly so we can set format, quality, and clip.Scale.
			err = chromedp.Do(tctx, chromedp.Func(func(ctx context.Context, tgt *chromedp.Target) error {
				params := page.CaptureScreenshotParams{
					Format:      format,
					FromSurface: new(true),
				}
				if jpegQuality > 0 {
					params.Quality = new(jpegQuality)
				}
				if input.FullPage {
					params.CaptureBeyondViewport = new(true)
				}

				// When max_dimension is set, use a clip with
				// Scale < 1 so Chrome downscales during capture.
				if input.MaxDimension > 0 {
					clip, captureScale, err := captureClip(ctx, tgt, input.FullPage, input.MaxDimension)
					if err != nil {
						return err
					}
					if captureScale < 1.0 {
						clip.Scale = captureScale
						params.Clip = clip
						params.CaptureBeyondViewport = new(true)
					}
				}

				res, err := cdp.Call(ctx, tgt, page.CaptureScreenshot, params)
				if err != nil {
					return err
				}
				buf = res.Data
				return nil
			}))
			if err != nil {
				return nil, struct{}{}, err
			}
		}

		content := []mcp.Content{
			&mcp.ImageContent{
				Data:     buf,
				MIMEType: mimeType,
			},
		}

		// Save to disk if requested.
		if saveName != "" {
			path, err := writeDownloadFile(opts.downloadDir, saveName, buf)
			if err != nil {
				return nil, struct{}{}, err
			}
			content = append(content, &mcp.TextContent{
				Text: fmt.Sprintf("Saved to %s", path),
			})
		}

		return &mcp.CallToolResult{Content: content}, struct{}{}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "pdf",
		Description: "Generate a PDF of the current page.",
		Annotations: captureAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input pdfInput) (*mcp.CallToolResult, struct{}, error) {
		if err := validateNonNegativeFinite("scale", input.Scale); err != nil {
			return nil, struct{}{}, err
		}
		if err := validateNonNegativeFinite("paper_width", input.PaperWidth); err != nil {
			return nil, struct{}{}, err
		}
		if err := validateNonNegativeFinite("paper_height", input.PaperHeight); err != nil {
			return nil, struct{}{}, err
		}
		var saveName string
		if input.Filename != "" {
			var err error
			saveName, err = validateSaveFilename(opts.downloadDir, input.Filename, ".pdf")
			if err != nil {
				return nil, struct{}{}, err
			}
		}

		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		printBg := input.PrintBackground == nil || *input.PrintBackground
		pdfParams := page.PrintToPDFParams{
			PrintBackground: new(printBg),
			PageRanges:      input.PageRanges,
		}
		if input.Landscape {
			pdfParams.Landscape = new(true)
		}
		if input.Scale > 0 {
			pdfParams.Scale = input.Scale
		}
		if input.PaperWidth > 0 {
			pdfParams.PaperWidth = input.PaperWidth
		}
		if input.PaperHeight > 0 {
			pdfParams.PaperHeight = input.PaperHeight
		}
		pdfRes, err := chromedp.Call(tctx, page.PrintToPDF, pdfParams)
		if err != nil {
			return nil, struct{}{}, err
		}
		pdfData := pdfRes.Data

		// When saving to disk, return the file path instead of the
		// (potentially large) inline blob.
		if saveName != "" {
			path, err := writeDownloadFile(opts.downloadDir, saveName, pdfData)
			if err != nil {
				return nil, struct{}{}, err
			}
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{
						Text: fmt.Sprintf("Saved to %s", path),
					},
				},
			}, struct{}{}, nil
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.EmbeddedResource{
					Resource: &mcp.ResourceContents{
						URI:      "pdf://current-page",
						MIMEType: "application/pdf",
						Blob:     pdfData,
					},
				},
			},
		}, struct{}{}, nil
	})
}

// validateSaveFilename verifies that dir is configured and filename is a safe
// single path segment (no traversal, separators, NUL bytes, "." or ".."),
// appending defaultExt when filename has no extension.
func validateSaveFilename(dir, filename, defaultExt string) (string, error) {
	if dir == "" {
		return "", fmt.Errorf("--download-dir not configured; cannot save to disk")
	}
	if filename == "" || filename == "." || filename == ".." ||
		strings.ContainsAny(filename, `/\`) || strings.ContainsRune(filename, '\x00') ||
		filepath.Base(filename) != filename {
		return "", fmt.Errorf("invalid filename %q: must be a plain filename without path separators or traversal", filename)
	}
	if filepath.Ext(filename) == "" {
		filename += defaultExt
	}
	return filename, nil
}

// writeDownloadFile atomically writes data to a validated filename inside dir
// via a same-directory temp file and rename so readers never observe partial files.
func writeDownloadFile(dir, filename string, data []byte) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating download dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".chromedp-mcp-*.tmp")
	if err != nil {
		return "", fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("writing file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("closing temp file: %w", err)
	}
	path := filepath.Join(dir, filename)
	if err := os.Rename(tmpPath, path); err != nil {
		return "", fmt.Errorf("renaming file: %w", err)
	}
	cleanup = false
	return path, nil
}

// pageDevicePixelRatio returns window.devicePixelRatio for the target,
// defaulting to 1.0 if unavailable or non-positive.
func pageDevicePixelRatio(ctx context.Context, tgt *chromedp.Target) float64 {
	dpr, err := chromedp.Evaluate[float64]("window.devicePixelRatio || 1")(ctx, tgt)
	if err != nil || dpr <= 0 || math.IsNaN(dpr) || math.IsInf(dpr, 0) {
		return 1.0
	}
	return dpr
}

// downscaleFactor returns a CDP clip.Scale <= 1.0 so that Chrome's float32
// scale conversion and pixel ceiling cannot exceed maxDim physical pixels.
func downscaleFactor(w, h, dpr float64, maxDim int) float64 {
	if maxDim <= 0 {
		return 1.0
	}
	largestPx := math.Max(w, h) * dpr
	if largestPx <= float64(maxDim) {
		return 1.0
	}
	s32 := float32(float64(maxDim) / largestPx)
	if math.Ceil(float64(s32)*largestPx) > float64(maxDim) {
		s32 = math.Nextafter32(s32, 0)
	}
	return float64(s32)
}

// captureClip returns a page.Viewport clip and a scale factor for
// Page.captureScreenshot. When the physical capture area (CSS dimensions * DPR)
// exceeds maxDim in either dimension, the returned scale is < 1 so Chrome
// downscales during capture. If fullPage is true the clip covers the entire
// document; otherwise it covers the currently scrolled CSS viewport.
func captureClip(ctx context.Context, tgt *chromedp.Target, fullPage bool, maxDim int) (*page.Viewport, float64, error) {
	metrics, err := cdp.Call(ctx, tgt, page.GetLayoutMetrics, cdp.Empty{})
	if err != nil {
		return nil, 0, fmt.Errorf("getting layout metrics: %w", err)
	}

	var x, y, w, h float64
	if fullPage {
		if metrics.CSSContentSize == nil {
			return nil, 0, fmt.Errorf("getting layout metrics: missing CSS content size")
		}
		w = math.Ceil(metrics.CSSContentSize.Width)
		h = math.Ceil(metrics.CSSContentSize.Height)
	} else {
		if metrics.CSSLayoutViewport == nil {
			return nil, 0, fmt.Errorf("getting layout metrics: missing CSS layout viewport")
		}
		if metrics.CSSVisualViewport != nil {
			x = metrics.CSSVisualViewport.PageX
			y = metrics.CSSVisualViewport.PageY
		} else {
			x = float64(metrics.CSSLayoutViewport.PageX)
			y = float64(metrics.CSSLayoutViewport.PageY)
		}
		w = float64(metrics.CSSLayoutViewport.ClientWidth)
		h = float64(metrics.CSSLayoutViewport.ClientHeight)
	}

	dpr := pageDevicePixelRatio(ctx, tgt)
	scale := downscaleFactor(w, h, dpr, maxDim)

	return &page.Viewport{
		X:      x,
		Y:      y,
		Width:  w,
		Height: h,
		Scale:  scale,
	}, scale, nil
}

// captureElementObject captures a screenshot of a resolved DOM element
// (main frame, open Shadow DOM, or same-origin iframe) with the given format,
// quality, and optional maxDim physical-pixel cap. For elements inside
// same-origin iframes, scrolls the target into its iframe's viewport, clips
// against each ancestor iframe's visible content area (accounting for border,
// padding, and CSS scale/zoom), and fails clearly if completely clipped rather
// than silently capturing unrelated parent-page pixels.
func captureElementObject(ctx context.Context, tgt *chromedp.Target, el *resolvedElement, format page.CaptureScreenshotFormat, quality int64, maxDim int) ([]byte, error) {
	const rectFn = `function() {
		let w = (this.ownerDocument && this.ownerDocument.defaultView) || window;
		if (w && w.frameElement && typeof this.scrollIntoView === 'function') {
			this.scrollIntoView({block: 'nearest', inline: 'nearest'});
		}
		const r = this.getBoundingClientRect();
		if (r.width <= 0 || r.height <= 0) {
			return {width: r.width, height: r.height};
		}
		let x = r.left;
		let y = r.top;
		let width = r.width;
		let height = r.height;
		while (w && w.frameElement) {
			const de = w.document && w.document.compatMode === 'CSS1Compat' ? w.document.documentElement : null;
			const vw = (de && de.clientWidth) || w.innerWidth;
			const vh = (de && de.clientHeight) || w.innerHeight;
			const ix0 = Math.max(0, x);
			const iy0 = Math.max(0, y);
			const ix1 = Math.min(vw, x + width);
			const iy1 = Math.min(vh, y + height);
			if (ix1 <= ix0 || iy1 <= iy0) {
				return {clipped: true};
			}
			x = ix0;
			y = iy0;
			width = ix1 - ix0;
			height = iy1 - iy0;

			const fe = w.frameElement;
			const fr = fe.getBoundingClientRect();
			const pw = (fe.ownerDocument && fe.ownerDocument.defaultView) || w.parent;
			const cs = pw.getComputedStyle(fe);
			const bl = parseFloat(cs.borderLeftWidth) || 0;
			const bt = parseFloat(cs.borderTopWidth) || 0;
			const pl = parseFloat(cs.paddingLeft) || 0;
			const pt = parseFloat(cs.paddingTop) || 0;
			const sx = fe.offsetWidth > 0 ? fr.width / fe.offsetWidth : 1;
			const sy = fe.offsetHeight > 0 ? fr.height / fe.offsetHeight : 1;
			x = fr.left + (bl + pl + x) * sx;
			y = fr.top + (bt + pt + y) * sy;
			width *= sx;
			height *= sy;
			w = pw;
		}
		return {
			x: x + (w ? w.scrollX : 0),
			y: y + (w ? w.scrollY : 0),
			width: width,
			height: height,
			dpr: (w && w.devicePixelRatio) || 1
		};
	}`

	type elemRect struct {
		X       float64 `json:"x"`
		Y       float64 `json:"y"`
		Width   float64 `json:"width"`
		Height  float64 `json:"height"`
		DPR     float64 `json:"dpr"`
		Clipped bool    `json:"clipped"`
	}
	rect, err := callOnElement[elemRect](ctx, tgt, el.ObjectID, rectFn, false)
	if err != nil {
		return nil, fmt.Errorf("measuring %s: %w", el.Label, err)
	}
	if rect.Clipped {
		return nil, fmt.Errorf("element %s is outside its iframe viewport and cannot be captured", el.Label)
	}
	if rect.Width <= 0 || rect.Height <= 0 {
		return nil, fmt.Errorf("element %s has zero dimensions (%.0fx%.0f)", el.Label, rect.Width, rect.Height)
	}
	if rect.DPR <= 0 || math.IsNaN(rect.DPR) || math.IsInf(rect.DPR, 0) {
		rect.DPR = 1.0
	}

	// Round coordinates the same way Puppeteer and chromedp do to
	// handle fractional dimensions properly.
	x, y := math.Round(rect.X), math.Round(rect.Y)
	w := math.Round(rect.Width + rect.X - x)
	h := math.Round(rect.Height + rect.Y - y)
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("element %s rounded to zero dimensions", el.Label)
	}

	scale := downscaleFactor(w, h, rect.DPR, maxDim)
	clip := &page.Viewport{
		X:      x,
		Y:      y,
		Width:  w,
		Height: h,
		Scale:  scale,
	}

	params := page.CaptureScreenshotParams{
		Format:                format,
		CaptureBeyondViewport: new(true),
		FromSurface:           new(true),
		Clip:                  clip,
	}
	if quality > 0 {
		params.Quality = new(quality)
	}

	res, err := cdp.Call(ctx, tgt, page.CaptureScreenshot, params)
	if err != nil {
		return nil, err
	}
	return res.Data, nil
}
