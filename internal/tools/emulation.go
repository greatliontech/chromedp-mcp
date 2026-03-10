package tools

import (
	"context"
	"fmt"

	"github.com/chromedp/cdproto/emulation"
	cdpnetwork "github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/browser"
)

// SetGeolocationInput is the input for set_geolocation.
type SetGeolocationInput struct {
	TabInput
	Latitude  *float64 `json:"latitude,omitempty" jsonschema:"Mock latitude (-90 to 90). Omit all fields to reset."`
	Longitude *float64 `json:"longitude,omitempty" jsonschema:"Mock longitude (-180 to 180). Omit all fields to reset."`
	Accuracy  *float64 `json:"accuracy,omitempty" jsonschema:"Mock accuracy in meters (default 1)."`
}

// SetTimezoneInput is the input for set_timezone.
type SetTimezoneInput struct {
	TabInput
	TimezoneID string `json:"timezone_id" jsonschema:"IANA timezone ID (e.g. America/New_York, Europe/London). Empty string resets to default."`
}

// SetLocaleInput is the input for set_locale.
type SetLocaleInput struct {
	TabInput
	Locale string `json:"locale" jsonschema:"ICU locale (e.g. en_US, fr_FR, ja_JP). Empty string resets to default."`
}

// SetUserAgentInput is the input for set_user_agent.
type SetUserAgentInput struct {
	TabInput
	UserAgent      string `json:"user_agent" jsonschema:"User agent string to use."`
	AcceptLanguage string `json:"accept_language,omitempty" jsonschema:"Browser language to emulate (e.g. en-US, fr-FR)."`
	Platform       string `json:"platform,omitempty" jsonschema:"Platform navigator.platform should return (e.g. Win32, Linux x86_64, MacIntel)."`
}

// SetCPUThrottlingInput is the input for set_cpu_throttling.
type SetCPUThrottlingInput struct {
	TabInput
	Rate float64 `json:"rate" jsonschema:"Throttling rate (1 = no throttle, 2 = 2x slowdown, 4 = 4x slowdown). Set to 1 to disable."`
}

// SetVisionDeficiencyInput is the input for set_vision_deficiency.
type SetVisionDeficiencyInput struct {
	TabInput
	Type string `json:"type" jsonschema:"Vision deficiency to emulate: none, blurredVision, reducedContrast, achromatopsia, deuteranopia, protanopia, tritanopia"`
}

// EmulateNetworkInput is the input for emulate_network.
type EmulateNetworkInput struct {
	TabInput
	Offline            bool    `json:"offline,omitempty" jsonschema:"Simulate offline mode (default false)"`
	Latency            float64 `json:"latency,omitempty" jsonschema:"Minimum latency in milliseconds (0 to disable)"`
	DownloadThroughput float64 `json:"download_throughput,omitempty" jsonschema:"Maximum download throughput in bytes/sec (-1 = disabled, 0 = disabled)"`
	UploadThroughput   float64 `json:"upload_throughput,omitempty" jsonschema:"Maximum upload throughput in bytes/sec (-1 = disabled, 0 = disabled)"`
}

// BlockURLsInput is the input for block_urls.
type BlockURLsInput struct {
	TabInput
	Patterns []string `json:"patterns" jsonschema:"URL patterns to block (supports * wildcards). Pass empty array to clear."`
}

func registerEmulationTools(s *mcp.Server, mgr *browser.Manager) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "set_geolocation",
		Description: "Override device geolocation. Omit all coordinate fields to reset to default. Requires geolocation permission to be granted via set_permission.",
		Annotations: &mcp.ToolAnnotations{
			IdempotentHint: true,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input SetGeolocationInput) (*mcp.CallToolResult, struct{}, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}

		params := emulation.SetGeolocationOverride()
		if input.Latitude != nil {
			params = params.WithLatitude(*input.Latitude)
		}
		if input.Longitude != nil {
			params = params.WithLongitude(*input.Longitude)
		}
		if input.Accuracy != nil {
			params = params.WithAccuracy(*input.Accuracy)
		} else if input.Latitude != nil || input.Longitude != nil {
			// Default accuracy when setting coordinates.
			params = params.WithAccuracy(1)
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		err = chromedp.Run(tctx, params)
		return nil, struct{}{}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "set_timezone",
		Description: "Override timezone. Empty string resets to default. Uses IANA timezone IDs (e.g. America/New_York, Europe/London, Asia/Tokyo).",
		Annotations: &mcp.ToolAnnotations{
			IdempotentHint: true,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input SetTimezoneInput) (*mcp.CallToolResult, struct{}, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		err = chromedp.Run(tctx, emulation.SetTimezoneOverride(input.TimezoneID))
		return nil, struct{}{}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "set_locale",
		Description: "Override browser locale. Empty string resets to default. Uses ICU locale IDs (e.g. en_US, fr_FR, ja_JP).",
		Annotations: &mcp.ToolAnnotations{
			IdempotentHint: true,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input SetLocaleInput) (*mcp.CallToolResult, struct{}, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		err = chromedp.Run(tctx, emulation.SetLocaleOverride().WithLocale(input.Locale))
		return nil, struct{}{}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "set_user_agent",
		Description: "Override the browser user agent string. Also allows overriding accept-language and platform.",
		Annotations: &mcp.ToolAnnotations{
			IdempotentHint: true,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input SetUserAgentInput) (*mcp.CallToolResult, struct{}, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}

		params := emulation.SetUserAgentOverride(input.UserAgent)
		if input.AcceptLanguage != "" {
			params = params.WithAcceptLanguage(input.AcceptLanguage)
		}
		if input.Platform != "" {
			params = params.WithPlatform(input.Platform)
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		err = chromedp.Run(tctx, params)
		return nil, struct{}{}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "set_cpu_throttling",
		Description: "Throttle CPU to simulate slow devices. Rate 1 = no throttle, 4 = 4x slowdown. Set to 1 to disable.",
		Annotations: &mcp.ToolAnnotations{
			IdempotentHint: true,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input SetCPUThrottlingInput) (*mcp.CallToolResult, struct{}, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}

		rate := input.Rate
		if rate < 1 {
			rate = 1
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		err = chromedp.Run(tctx, emulation.SetCPUThrottlingRate(rate))
		return nil, struct{}{}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "set_vision_deficiency",
		Description: "Simulate vision deficiencies for accessibility testing: none, blurredVision, reducedContrast, achromatopsia, deuteranopia, protanopia, tritanopia.",
		Annotations: &mcp.ToolAnnotations{
			IdempotentHint: true,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input SetVisionDeficiencyInput) (*mcp.CallToolResult, struct{}, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}

		defType := emulation.SetEmulatedVisionDeficiencyType(input.Type)
		switch defType {
		case emulation.SetEmulatedVisionDeficiencyTypeNone,
			emulation.SetEmulatedVisionDeficiencyTypeBlurredVision,
			emulation.SetEmulatedVisionDeficiencyTypeReducedContrast,
			emulation.SetEmulatedVisionDeficiencyTypeAchromatopsia,
			emulation.SetEmulatedVisionDeficiencyTypeDeuteranopia,
			emulation.SetEmulatedVisionDeficiencyTypeProtanopia,
			emulation.SetEmulatedVisionDeficiencyTypeTritanopia:
			// valid
		default:
			return nil, struct{}{}, fmt.Errorf("invalid vision deficiency type %q: must be none, blurredVision, reducedContrast, achromatopsia, deuteranopia, protanopia, or tritanopia", input.Type)
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		err = chromedp.Run(tctx, emulation.SetEmulatedVisionDeficiency(defType))
		return nil, struct{}{}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "emulate_network",
		Description: "Emulate network conditions (offline, latency, throttled bandwidth). Call with all zeros/defaults to reset to normal.",
		Annotations: &mcp.ToolAnnotations{
			IdempotentHint: true,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input EmulateNetworkInput) (*mcp.CallToolResult, struct{}, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}

		download := input.DownloadThroughput
		if download == 0 {
			download = -1 // -1 = disabled (no throttling)
		}
		upload := input.UploadThroughput
		if upload == 0 {
			upload = -1
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		err = chromedp.Run(tctx, cdpnetwork.EmulateNetworkConditions(input.Offline, input.Latency, download, upload))
		return nil, struct{}{}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "block_urls",
		Description: "Block URLs matching patterns (supports * wildcards). Pass empty array to clear all blocks.",
		Annotations: &mcp.ToolAnnotations{
			IdempotentHint: true,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input BlockURLsInput) (*mcp.CallToolResult, struct{}, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}

		patterns := input.Patterns
		if patterns == nil {
			patterns = []string{}
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		err = chromedp.Run(tctx, cdpnetwork.SetBlockedURLs(patterns))
		return nil, struct{}{}, err
	})
}
