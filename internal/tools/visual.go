package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/browser"
)

// ScreenshotInput is the input for screenshot.
type ScreenshotInput struct {
	SelectorInput
	Selector     string `json:"selector,omitempty" jsonschema:"CSS selector to screenshot a specific element. If omitted captures the viewport."`
	FullPage     bool   `json:"full_page,omitempty" jsonschema:"Capture the full scrollable page (default false). Ignored if selector is set."`
	Format       string `json:"format,omitempty" jsonschema:"Image format: png (default) or jpeg"`
	Quality      int    `json:"quality,omitempty" jsonschema:"JPEG quality 1-100 (default 80). Ignored for PNG."`
	Filename     string `json:"filename,omitempty" jsonschema:"Save to disk with this filename (requires --download-dir). Omit to return inline only. The image is still returned inline."`
	MaxDimension int    `json:"max_dimension,omitempty" jsonschema:"Maximum allowed width or height in physical pixels. If the screenshot exceeds this in either dimension it is downscaled proportionally. Useful to stay within API image-size limits (e.g. 8000 for Anthropic)."`
}

// PDFInput is the input for pdf.
type PDFInput struct {
	TabInput
	Landscape       bool    `json:"landscape,omitempty" jsonschema:"Landscape orientation (default false)"`
	PrintBackground *bool   `json:"print_background,omitempty" jsonschema:"Include background graphics (default true)"`
	Scale           float64 `json:"scale,omitempty" jsonschema:"Page rendering scale (default 1.0)"`
	PaperWidth      float64 `json:"paper_width,omitempty" jsonschema:"Paper width in inches (default 8.5)"`
	PaperHeight     float64 `json:"paper_height,omitempty" jsonschema:"Paper height in inches (default 11)"`
	PageRanges      string  `json:"page_ranges,omitempty" jsonschema:"Page ranges e.g. 1-5 8. Defaults to all pages."`
	Filename        string  `json:"filename,omitempty" jsonschema:"Save to disk with this filename (requires --download-dir). Omit to return inline only."`
}

// SetViewportInput is the input for set_viewport.
type SetViewportInput struct {
	TabInput
	Width             int     `json:"width" jsonschema:"Viewport width in pixels"`
	Height            int     `json:"height" jsonschema:"Viewport height in pixels"`
	DeviceScaleFactor float64 `json:"device_scale_factor,omitempty" jsonschema:"Device scale factor (default 1.0)"`
	Mobile            bool    `json:"mobile,omitempty" jsonschema:"Emulate mobile device (default false)"`
}

var screenshotFormatValues = []string{"png", "jpeg"}

func registerVisualTools(s *mcp.Server, mgr *browser.Manager, opts *Options) {
	// screenshot and pdf are read-only when --download-dir is not configured
	// (filename always errors), and write/overwrite files on disk when it is.
	captureAnnotations := func() *mcp.ToolAnnotations {
		if opts.DownloadDir != "" {
			return idempotentWriteAnnotations()
		}
		return readOnlyAnnotations()
	}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "screenshot",
		Description: "Capture a screenshot of the viewport, full page, or a specific element.",
		InputSchema: schemaWithEnums[ScreenshotInput](map[string][]string{
			"format": screenshotFormatValues,
		}),
		Annotations: captureAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ScreenshotInput) (*mcp.CallToolResult, struct{}, error) {
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
			saveName, err = validateSaveFilename(opts.DownloadDir, input.Filename, ext)
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

		if input.Selector != "" {
			// Bound only the visibility wait with selectorContext so slow
			// image encoding cannot produce a false "not visible" error.
			sctx, scancel := selectorContext(tctx, input.Timeout)
			nodes, waitErr := chromedp.Run(sctx, chromedp.Nodes(chromedp.CSS(input.Selector), chromedp.NodeVisible))
			scancel()
			if waitErr != nil {
				return nil, struct{}{}, selectorError(tctx, input.Selector, waitErr)
			}
			if len(nodes) == 0 {
				return nil, struct{}{}, fmt.Errorf("selector %q did not return any nodes", input.Selector)
			}
			buf, err = chromedp.Run(tctx, captureElementNode(input.Selector, nodes[0].BackendNodeID, format, jpegQuality, input.MaxDimension))
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
			path, err := writeDownloadFile(opts.DownloadDir, saveName, buf)
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
	}, func(ctx context.Context, req *mcp.CallToolRequest, input PDFInput) (*mcp.CallToolResult, struct{}, error) {
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
			saveName, err = validateSaveFilename(opts.DownloadDir, input.Filename, ".pdf")
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
			path, err := writeDownloadFile(opts.DownloadDir, saveName, pdfData)
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

	mcp.AddTool(s, &mcp.Tool{
		Name:        "set_viewport",
		Description: "Set the browser viewport dimensions and device emulation settings.",
		Annotations: idempotentWriteAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input SetViewportInput) (*mcp.CallToolResult, struct{}, error) {
		if input.Width <= 0 || input.Height <= 0 {
			return nil, struct{}{}, fmt.Errorf("width and height must be > 0, got %dx%d", input.Width, input.Height)
		}
		if err := validateNonNegativeFinite("device_scale_factor", input.DeviceScaleFactor); err != nil {
			return nil, struct{}{}, err
		}

		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		scale := input.DeviceScaleFactor
		if scale == 0 {
			scale = 1.0
		}

		if _, err := chromedp.Call(tctx, emulation.SetDeviceMetricsOverride, emulation.SetDeviceMetricsOverrideParams{
			Width:             int64(input.Width),
			Height:            int64(input.Height),
			DeviceScaleFactor: scale,
			Mobile:            input.Mobile,
		}); err != nil {
			return nil, struct{}{}, err
		}
		return nil, struct{}{}, nil
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

// writeDownloadFile writes data to a validated filename inside dir.
func writeDownloadFile(dir, filename string, data []byte) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating download dir: %w", err)
	}
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("writing file: %w", err)
	}
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

// captureElementNode captures a screenshot of a resolved DOM backend node with
// the given format, quality, and optional maxDim physical-pixel cap. It uses
// viewport bounding coordinates plus window.scrollX/scrollY (matching Chrome's
// document-canvas clip origin even when <html> has margin, border, or transform)
// and factors window.devicePixelRatio into scale.
func captureElementNode(selector string, backendNodeID cdp.BackendNodeID, format page.CaptureScreenshotFormat, quality int64, maxDim int) chromedp.Action[[]byte] {
	return func(ctx context.Context, tgt *chromedp.Target) ([]byte, error) {
		objRes, err := cdp.Call(ctx, tgt, dom.ResolveNode, dom.ResolveNodeParams{
			BackendNodeID: backendNodeID,
		})
		if err != nil {
			return nil, fmt.Errorf("resolving element %q: %w", selector, err)
		}
		if objRes.Object == nil || objRes.Object.ObjectID == "" {
			return nil, fmt.Errorf("resolving element %q: empty remote object", selector)
		}
		defer func() {
			cleanCtx, cancel := cleanupContext(ctx)
			defer cancel()
			_, _ = cdp.Call(cleanCtx, tgt, runtime.ReleaseObject, runtime.ReleaseObjectParams{
				ObjectID: objRes.Object.ObjectID,
			})
		}()

		const rectFn = `function() {
			const e = this.getBoundingClientRect();
			return {
				x: e.left + window.scrollX,
				y: e.top + window.scrollY,
				width: e.width,
				height: e.height,
				dpr: window.devicePixelRatio || 1
			};
		}`
		callRes, err := cdp.Call(ctx, tgt, runtime.CallFunctionOn, runtime.CallFunctionOnParams{
			FunctionDeclaration: rectFn,
			ObjectID:            objRes.Object.ObjectID,
			ReturnByValue:       new(true),
		})
		if err != nil {
			return nil, fmt.Errorf("measuring element %q: %w", selector, err)
		}
		if callRes.ExceptionDetails != nil {
			return nil, fmt.Errorf("measuring element %q: %s", selector, callRes.ExceptionDetails.Text)
		}
		if callRes.Result == nil || len(callRes.Result.Value) == 0 {
			return nil, fmt.Errorf("measuring element %q: empty bounding rect", selector)
		}

		var rect struct {
			X      float64 `json:"x"`
			Y      float64 `json:"y"`
			Width  float64 `json:"width"`
			Height float64 `json:"height"`
			DPR    float64 `json:"dpr"`
		}
		if err := json.Unmarshal(callRes.Result.Value, &rect); err != nil {
			return nil, fmt.Errorf("decoding bounding rect for %q: %w", selector, err)
		}
		if rect.Width <= 0 || rect.Height <= 0 {
			return nil, fmt.Errorf("element %q has zero dimensions (%.0fx%.0f)", selector, rect.Width, rect.Height)
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
			return nil, fmt.Errorf("element %q rounded to zero dimensions", selector)
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
}
