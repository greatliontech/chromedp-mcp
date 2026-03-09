package tools

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/image/draw"

	"github.com/thegrumpylion/chromedp-mcp/internal/browser"
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
		tctx := t.Context()

		if input.Selector != "" {
			// Screenshot a specific element.
			err = chromedp.Run(tctx, chromedp.Screenshot(input.Selector, &buf, chromedp.NodeVisible))
		} else if input.FullPage {
			quality := 100 // PNG
			if input.Format == "jpeg" {
				quality = input.Quality
				if quality <= 0 {
					quality = 80
				}
			}
			err = chromedp.Run(tctx, chromedp.FullScreenshot(&buf, quality))
		} else {
			// Viewport screenshot. Use CDP directly so we can specify
			// the image format (chromedp.CaptureScreenshot is always PNG).
			err = chromedp.Run(tctx, chromedp.ActionFunc(func(ctx context.Context) error {
				params := page.CaptureScreenshot()
				if input.Format == "jpeg" {
					params = params.WithFormat(page.CaptureScreenshotFormatJpeg)
					quality := input.Quality
					if quality <= 0 {
						quality = 80
					}
					params = params.WithQuality(int64(quality))
				}
				var captureErr error
				buf, captureErr = params.Do(ctx)
				return captureErr
			}))
		}
		if err != nil {
			return nil, struct{}{}, err
		}

		// Downscale if the image exceeds max_dimension.
		if input.MaxDimension > 0 {
			buf, err = constrainImageSize(buf, input.MaxDimension, input.Format, input.Quality)
			if err != nil {
				return nil, struct{}{}, fmt.Errorf("constraining image size: %w", err)
			}
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

		var pdfData []byte
		err = chromedp.Run(t.Context(), chromedp.ActionFunc(func(ctx context.Context) error {
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

		scale := input.DeviceScaleFactor
		if scale <= 0 {
			scale = 1.0
		}

		err = chromedp.Run(t.Context(), chromedp.ActionFunc(func(ctx context.Context) error {
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

// constrainImageSize decodes the encoded image (PNG or JPEG), checks whether
// either dimension exceeds maxDim, and if so downscales proportionally using
// high-quality CatmullRom interpolation. The result is re-encoded in the
// original format. If the image already fits, the original bytes are returned
// unchanged.
func constrainImageSize(data []byte, maxDim int, format string, quality int) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decoding image: %w", err)
	}

	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()

	// Already within limits.
	if w <= maxDim && h <= maxDim {
		return data, nil
	}

	// Compute new dimensions preserving aspect ratio.
	scale := float64(maxDim) / float64(max(w, h))
	newW := int(float64(w) * scale)
	newH := int(float64(h) * scale)
	if newW < 1 {
		newW = 1
	}
	if newH < 1 {
		newH = 1
	}

	dst := image.NewRGBA(image.Rect(0, 0, newW, newH))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, bounds, draw.Over, nil)

	var buf bytes.Buffer
	switch format {
	case "jpeg":
		q := quality
		if q <= 0 {
			q = 80
		}
		err = jpeg.Encode(&buf, dst, &jpeg.Options{Quality: q})
	default:
		err = png.Encode(&buf, dst)
	}
	if err != nil {
		return nil, fmt.Errorf("encoding resized image: %w", err)
	}
	return buf.Bytes(), nil
}
