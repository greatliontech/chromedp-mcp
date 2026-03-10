package tools

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/browser"
)

// ScreenshotInput is the input for screenshot.
type ScreenshotInput struct {
	TabInput
	Selector     string `json:"selector,omitempty" jsonschema:"CSS selector to screenshot a specific element. If omitted captures the viewport."`
	FullPage     bool   `json:"full_page,omitempty" jsonschema:"Capture the full scrollable page (default false). Ignored if selector is set."`
	Format       string `json:"format,omitempty" jsonschema:"Image format: png (default) or jpeg"`
	Quality      int    `json:"quality,omitempty" jsonschema:"JPEG quality 1-100 (default 80). Ignored for PNG."`
	Filename     string `json:"filename,omitempty" jsonschema:"Save to disk with this filename (requires --download-dir). Timestamp-based name used if empty. The image is still returned inline."`
	MaxDimension int    `json:"max_dimension,omitempty" jsonschema:"Maximum allowed width or height in pixels. If the screenshot exceeds this in either dimension it is downscaled proportionally. Useful to stay within API image-size limits (e.g. 8000 for Anthropic)."`
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
	Filename        string  `json:"filename,omitempty" jsonschema:"Save to disk with this filename (requires --download-dir). Timestamp-based name used if empty."`
}

// SetViewportInput is the input for set_viewport.
type SetViewportInput struct {
	TabInput
	Width             int     `json:"width" jsonschema:"Viewport width in pixels"`
	Height            int     `json:"height" jsonschema:"Viewport height in pixels"`
	DeviceScaleFactor float64 `json:"device_scale_factor,omitempty" jsonschema:"Device scale factor (default 1.0)"`
	Mobile            bool    `json:"mobile,omitempty" jsonschema:"Emulate mobile device (default false)"`
}

func registerVisualTools(s *mcp.Server, mgr *browser.Manager, opts *Options) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "screenshot",
		Description: "Capture a screenshot of the viewport, full page, or a specific element.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:   true,
			IdempotentHint: true,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ScreenshotInput) (*mcp.CallToolResult, struct{}, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}

		var buf []byte
		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()

		// Compute the CDP screenshot format/quality parameters once.
		format := page.CaptureScreenshotFormatPng
		jpegQuality := int64(0)
		if input.Format == "jpeg" {
			format = page.CaptureScreenshotFormatJpeg
			jpegQuality = int64(input.Quality)
			if jpegQuality <= 0 {
				jpegQuality = 80
			}
		}

		if input.Selector != "" {
			// Element screenshot. We build the CDP command manually so
			// that format and quality are respected (chromedp.ScreenshotScale
			// hardcodes PNG). The approach mirrors chromedp's ScreenshotNodes:
			// query the element's bounding rect, round it like Puppeteer does,
			// then call Page.captureScreenshot with the computed clip.
			scale := 1.0
			if input.MaxDimension > 0 {
				scale, err = elementScale(tctx, input.Selector, input.MaxDimension)
				if err != nil {
					return nil, struct{}{}, err
				}
			}
			err = chromedp.Run(tctx, elementScreenshot(input.Selector, format, jpegQuality, scale, &buf))
		} else {
			// Viewport or full-page screenshot. Build the CDP command
			// directly so we can set format, quality, and clip.Scale.
			err = chromedp.Run(tctx, chromedp.ActionFunc(func(ctx context.Context) error {
				params := page.CaptureScreenshot().
					WithFormat(format).
					WithFromSurface(true)
				if jpegQuality > 0 {
					params = params.WithQuality(jpegQuality)
				}
				if input.FullPage {
					params = params.WithCaptureBeyondViewport(true)
				}

				// When max_dimension is set, use a clip with
				// Scale < 1 so Chrome downscales during capture.
				if input.MaxDimension > 0 {
					clip, captureScale, err := captureClip(ctx, input.FullPage, input.MaxDimension)
					if err != nil {
						return err
					}
					if captureScale < 1.0 {
						clip.Scale = captureScale
						params = params.WithClip(clip).WithCaptureBeyondViewport(true)
					}
				}

				buf, err = params.Do(ctx)
				return err
			}))
		}
		if err != nil {
			return nil, struct{}{}, err
		}

		mimeType := "image/png"
		ext := ".png"
		if input.Format == "jpeg" {
			mimeType = "image/jpeg"
			ext = ".jpg"
		}

		content := []mcp.Content{
			&mcp.ImageContent{
				Data:     buf,
				MIMEType: mimeType,
			},
		}

		// Save to disk if requested and download dir is configured.
		if input.Filename != "" {
			path, err := saveToDownloadDir(opts.DownloadDir, input.Filename, ext, buf)
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
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:   true,
			IdempotentHint: true,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input PDFInput) (*mcp.CallToolResult, struct{}, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		var pdfData []byte
		err = chromedp.Run(tctx, chromedp.ActionFunc(func(ctx context.Context) error {
			printBg := input.PrintBackground == nil || *input.PrintBackground
			params := page.PrintToPDF().WithPrintBackground(printBg)
			if input.Landscape {
				params = params.WithLandscape(true)
			}
			if input.Scale > 0 {
				params = params.WithScale(input.Scale)
			}
			if input.PaperWidth > 0 {
				params = params.WithPaperWidth(input.PaperWidth)
			}
			if input.PaperHeight > 0 {
				params = params.WithPaperHeight(input.PaperHeight)
			}
			if input.PageRanges != "" {
				params = params.WithPageRanges(input.PageRanges)
			}
			var err error
			pdfData, _, err = params.Do(ctx)
			return err
		}))
		if err != nil {
			return nil, struct{}{}, err
		}

		// When saving to disk, return the file path instead of the
		// (potentially large) inline blob.
		if input.Filename != "" {
			path, err := saveToDownloadDir(opts.DownloadDir, input.Filename, ".pdf", pdfData)
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
		Annotations: &mcp.ToolAnnotations{
			IdempotentHint: true,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input SetViewportInput) (*mcp.CallToolResult, struct{}, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		scale := input.DeviceScaleFactor
		if scale <= 0 {
			scale = 1.0
		}

		err = chromedp.Run(tctx, chromedp.ActionFunc(func(ctx context.Context) error {
			return emulation.SetDeviceMetricsOverride(int64(input.Width), int64(input.Height), scale, input.Mobile).Do(ctx)
		}))
		if err != nil {
			return nil, struct{}{}, err
		}
		return nil, struct{}{}, nil
	})
}

// saveToDownloadDir writes data to a file in the configured download
// directory. If filename has no extension, defaultExt is appended. If
// filename is empty, a timestamp-based name is generated. Returns the
// absolute path of the written file.
func saveToDownloadDir(dir, filename, defaultExt string, data []byte) (string, error) {
	if dir == "" {
		return "", fmt.Errorf("--download-dir not configured; cannot save to disk")
	}

	// Ensure the directory exists.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating download dir: %w", err)
	}

	if filename == "" {
		filename = time.Now().Format("20060102-150405") + defaultExt
	} else if filepath.Ext(filename) == "" {
		filename += defaultExt
	}

	// Prevent path traversal.
	if filepath.Base(filename) != filename {
		return "", fmt.Errorf("filename must not contain path separators: %q", filename)
	}

	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("writing file: %w", err)
	}
	return path, nil
}

// captureClip returns a page.Viewport clip and a scale factor for
// Page.captureScreenshot. When the capture area exceeds maxDim in either
// dimension the returned scale is < 1, telling Chrome to downscale during
// capture (GPU-accelerated). If fullPage is true the clip covers the entire
// document; otherwise it covers the CSS viewport.
func captureClip(ctx context.Context, fullPage bool, maxDim int) (*page.Viewport, float64, error) {
	_, _, _, cssLayout, _, cssContent, err := page.GetLayoutMetrics().Do(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("getting layout metrics: %w", err)
	}

	var w, h float64
	if fullPage {
		w = math.Ceil(cssContent.Width)
		h = math.Ceil(cssContent.Height)
	} else {
		w = float64(cssLayout.ClientWidth)
		h = float64(cssLayout.ClientHeight)
	}

	scale := 1.0
	largest := math.Max(w, h)
	if largest > float64(maxDim) {
		scale = float64(maxDim) / largest
	}

	return &page.Viewport{
		X:      0,
		Y:      0,
		Width:  w,
		Height: h,
		Scale:  scale,
	}, scale, nil
}

// elementScreenshot captures a screenshot of the element matching selector with
// the given format, quality, and scale. It mirrors chromedp's ScreenshotNodes
// logic (bounding rect → rounded clip → CaptureScreenshot) but allows the
// caller to specify format and quality instead of hardcoding PNG.
func elementScreenshot(selector string, format page.CaptureScreenshotFormat, quality int64, scale float64, buf *[]byte) chromedp.QueryAction {
	return chromedp.QueryAfter(selector, func(ctx context.Context, _ runtime.ExecutionContextID, nodes ...*cdp.Node) error {
		if len(nodes) < 1 {
			return fmt.Errorf("selector %q did not return any nodes", selector)
		}

		// Get the element's bounding client rect via callFunctionOnNode.
		// chromedp doesn't export this helper, so we use JavaScript to
		// compute the same result that ScreenshotNodes uses internally.
		var rect struct {
			X      float64 `json:"x"`
			Y      float64 `json:"y"`
			Width  float64 `json:"width"`
			Height float64 `json:"height"`
		}
		js := fmt.Sprintf(`(() => {
			const el = document.querySelector(%q);
			if (!el) return null;
			const r = el.getBoundingClientRect();
			return {x: r.x, y: r.y, width: r.width, height: r.height};
		})()`, selector)
		if err := chromedp.Evaluate(js, &rect).Do(ctx); err != nil {
			return fmt.Errorf("getting bounding rect for %q: %w", selector, err)
		}

		// Round coordinates the same way Puppeteer and chromedp do to
		// handle fractional dimensions properly.
		x, y := math.Round(rect.X), math.Round(rect.Y)
		w := math.Round(rect.Width + rect.X - x)
		h := math.Round(rect.Height + rect.Y - y)

		clip := &page.Viewport{
			X:      x,
			Y:      y,
			Width:  w,
			Height: h,
			Scale:  scale,
		}

		params := page.CaptureScreenshot().
			WithFormat(format).
			WithCaptureBeyondViewport(true).
			WithFromSurface(true).
			WithClip(clip)
		if quality > 0 {
			params = params.WithQuality(quality)
		}

		var err error
		*buf, err = params.Do(ctx)
		return err
	}, chromedp.NodeVisible)
}

// elementScale returns the scale factor needed to fit an element within
// maxDim pixels. It evaluates the element's bounding rect in the browser.
func elementScale(ctx context.Context, selector string, maxDim int) (float64, error) {
	var w, h float64
	js := fmt.Sprintf(`(() => {
		const el = document.querySelector(%q);
		if (!el) return null;
		const r = el.getBoundingClientRect();
		return {w: r.width, h: r.height};
	})()`, selector)

	var result struct {
		W float64 `json:"w"`
		H float64 `json:"h"`
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(js, &result)); err != nil {
		return 0, fmt.Errorf("evaluating element dimensions for %q: %w", selector, err)
	}
	w, h = result.W, result.H
	if w == 0 && h == 0 {
		return 1.0, nil
	}

	largest := math.Max(w, h)
	if largest <= float64(maxDim) {
		return 1.0, nil
	}
	return float64(maxDim) / largest, nil
}
