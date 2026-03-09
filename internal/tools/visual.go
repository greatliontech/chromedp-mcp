package tools

import (
	"context"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/thegrumpylion/chromedp-mcp/internal/browser"
)

// ScreenshotInput is the input for screenshot.
type ScreenshotInput struct {
	TabInput
	Selector string `json:"selector,omitempty" jsonschema:"CSS selector to screenshot a specific element. If omitted captures the viewport."`
	FullPage bool   `json:"full_page,omitempty" jsonschema:"Capture the full scrollable page (default false). Ignored if selector is set."`
	Format   string `json:"format,omitempty" jsonschema:"Image format: png (default) or jpeg"`
	Quality  int    `json:"quality,omitempty" jsonschema:"JPEG quality 1-100 (default 80). Ignored for PNG."`
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
}

// SetViewportInput is the input for set_viewport.
type SetViewportInput struct {
	TabInput
	Width             int     `json:"width" jsonschema:"Viewport width in pixels"`
	Height            int     `json:"height" jsonschema:"Viewport height in pixels"`
	DeviceScaleFactor float64 `json:"device_scale_factor,omitempty" jsonschema:"Device scale factor (default 1.0)"`
	Mobile            bool    `json:"mobile,omitempty" jsonschema:"Emulate mobile device (default false)"`
}

func registerVisualTools(s *mcp.Server, mgr *browser.Manager) {
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
			err = chromedp.Run(tctx, chromedp.CaptureScreenshot(&buf))
		}
		if err != nil {
			return nil, struct{}{}, err
		}

		mimeType := "image/png"
		if input.Format == "jpeg" {
			mimeType = "image/jpeg"
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.ImageContent{
					Data:     buf,
					MIMEType: mimeType,
				},
			},
		}, struct{}{}, nil
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
