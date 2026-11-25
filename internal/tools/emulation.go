package tools

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/chromedp/cdproto/cdp"
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

var visionDeficiencyValues = []string{
	string(emulation.SetEmulatedVisionDeficiencyTypeNone),
	string(emulation.SetEmulatedVisionDeficiencyTypeBlurredVision),
	string(emulation.SetEmulatedVisionDeficiencyTypeReducedContrast),
	string(emulation.SetEmulatedVisionDeficiencyTypeAchromatopsia),
	string(emulation.SetEmulatedVisionDeficiencyTypeDeuteranopia),
	string(emulation.SetEmulatedVisionDeficiencyTypeProtanopia),
	string(emulation.SetEmulatedVisionDeficiencyTypeTritanopia),
}

func registerEmulationTools(s *mcp.Server, mgr *browser.Manager) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "set_geolocation",
		Description: "Override device geolocation. Omit all coordinate fields to reset to default. Requires geolocation permission to be granted via set_permission.",
		Annotations: idempotentWriteAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input SetGeolocationInput) (*mcp.CallToolResult, struct{}, error) {
		if (input.Latitude == nil) != (input.Longitude == nil) {
			return nil, struct{}{}, fmt.Errorf("latitude and longitude must both be provided (or both omitted to reset)")
		}
		if input.Latitude != nil {
			lat := *input.Latitude
			if math.IsNaN(lat) || math.IsInf(lat, 0) || lat < -90 || lat > 90 {
				return nil, struct{}{}, fmt.Errorf("latitude must be between -90 and 90, got %v", lat)
			}
			lon := *input.Longitude
			if math.IsNaN(lon) || math.IsInf(lon, 0) || lon < -180 || lon > 180 {
				return nil, struct{}{}, fmt.Errorf("longitude must be between -180 and 180, got %v", lon)
			}
		} else if input.Accuracy != nil {
			return nil, struct{}{}, fmt.Errorf("accuracy requires latitude and longitude")
		}
		if input.Accuracy != nil {
			if err := validateNonNegativeFinite("accuracy", *input.Accuracy); err != nil {
				return nil, struct{}{}, err
			}
		}

		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}

		params := emulation.SetGeolocationOverrideParams{
			Latitude:  input.Latitude,
			Longitude: input.Longitude,
			Accuracy:  input.Accuracy,
		}
		if params.Accuracy == nil && input.Latitude != nil {
			// Default accuracy when setting coordinates.
			params.Accuracy = new(1.0)
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		_, err = chromedp.Call(tctx, emulation.SetGeolocationOverride, params)
		return nil, struct{}{}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "set_timezone",
		Description: "Override timezone. Empty string resets to default. Uses IANA timezone IDs (e.g. America/New_York, Europe/London, Asia/Tokyo).",
		Annotations: idempotentWriteAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input SetTimezoneInput) (*mcp.CallToolResult, struct{}, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		_, err = chromedp.Call(tctx, emulation.SetTimezoneOverride, emulation.SetTimezoneOverrideParams{
			TimezoneID: input.TimezoneID,
		})
		return nil, struct{}{}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "set_locale",
		Description: "Override browser locale. Empty string resets to default. Uses ICU locale IDs (e.g. en_US, fr_FR, ja_JP).",
		Annotations: idempotentWriteAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input SetLocaleInput) (*mcp.CallToolResult, struct{}, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		_, err = chromedp.Call(tctx, emulation.SetLocaleOverride, emulation.SetLocaleOverrideParams{
			Locale: input.Locale,
		})
		return nil, struct{}{}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "set_user_agent",
		Description: "Override the browser user agent string. Also allows overriding accept-language and platform.",
		Annotations: idempotentWriteAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input SetUserAgentInput) (*mcp.CallToolResult, struct{}, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		_, err = chromedp.Call(tctx, emulation.SetUserAgentOverride, emulation.SetUserAgentOverrideParams{
			UserAgent:      input.UserAgent,
			AcceptLanguage: input.AcceptLanguage,
			Platform:       input.Platform,
		})
		return nil, struct{}{}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "set_cpu_throttling",
		Description: "Throttle CPU to simulate slow devices. Rate 1 = no throttle, 4 = 4x slowdown. Set to 1 to disable.",
		Annotations: idempotentWriteAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input SetCPUThrottlingInput) (*mcp.CallToolResult, struct{}, error) {
		if err := validateNonNegativeFinite("rate", input.Rate); err != nil {
			return nil, struct{}{}, err
		}
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
		_, err = chromedp.Call(tctx, emulation.SetCPUThrottlingRate, emulation.SetCPUThrottlingRateParams{
			Rate: rate,
		})
		return nil, struct{}{}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "set_vision_deficiency",
		Description: "Simulate vision deficiencies for accessibility testing: none, blurredVision, reducedContrast, achromatopsia, deuteranopia, protanopia, tritanopia.",
		InputSchema: schemaWithEnums[SetVisionDeficiencyInput](map[string][]string{
			"type": visionDeficiencyValues,
		}),
		Annotations: idempotentWriteAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input SetVisionDeficiencyInput) (*mcp.CallToolResult, struct{}, error) {
		if err := validateEnum("vision deficiency type", input.Type, false, visionDeficiencyValues); err != nil {
			return nil, struct{}{}, err
		}
		defType := emulation.SetEmulatedVisionDeficiencyType(input.Type)

		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		_, err = chromedp.Call(tctx, emulation.SetEmulatedVisionDeficiency, emulation.SetEmulatedVisionDeficiencyParams{
			Type: defType,
		})
		return nil, struct{}{}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "emulate_network",
		Description: "Emulate network conditions (offline, latency, throttled bandwidth). Call with all zeros/defaults to reset to normal.",
		Annotations: idempotentWriteAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input EmulateNetworkInput) (*mcp.CallToolResult, struct{}, error) {
		if err := validateNonNegativeFinite("latency", input.Latency); err != nil {
			return nil, struct{}{}, err
		}
		if math.IsNaN(input.DownloadThroughput) || math.IsInf(input.DownloadThroughput, 0) || (input.DownloadThroughput < 0 && input.DownloadThroughput != -1) {
			return nil, struct{}{}, fmt.Errorf("download_throughput must be -1 or a finite number >= 0, got %v", input.DownloadThroughput)
		}
		if math.IsNaN(input.UploadThroughput) || math.IsInf(input.UploadThroughput, 0) || (input.UploadThroughput < 0 && input.UploadThroughput != -1) {
			return nil, struct{}{}, fmt.Errorf("upload_throughput must be -1 or a finite number >= 0, got %v", input.UploadThroughput)
		}

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

		conditions := []*cdpnetwork.Conditions{}
		if input.Offline || input.Latency > 0 || download > 0 || upload > 0 {
			conditions = []*cdpnetwork.Conditions{{
				Offline:            input.Offline,
				Latency:            input.Latency,
				DownloadThroughput: download,
				UploadThroughput:   upload,
			}}
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		err = chromedp.Do(tctx, chromedp.Func(func(ctx context.Context, tgt *chromedp.Target) error {
			if _, err := cdp.Call(ctx, tgt, cdpnetwork.EmulateNetworkConditionsByRule, cdpnetwork.EmulateNetworkConditionsByRuleParams{
				EmulateOfflineServiceWorker: new(input.Offline),
				MatchedNetworkConditions:    conditions,
			}); err != nil {
				return err
			}
			_, err := cdp.Call(ctx, tgt, cdpnetwork.OverrideNetworkState, cdpnetwork.OverrideNetworkStateParams{
				Offline:            input.Offline,
				Latency:            input.Latency,
				DownloadThroughput: download,
				UploadThroughput:   upload,
			})
			return err
		}))
		return nil, struct{}{}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "block_urls",
		Description: "Block URLs matching patterns (supports * wildcards and WHATWG URLPattern syntax). Pass empty array to clear all blocks.",
		Annotations: idempotentWriteAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input BlockURLsInput) (*mcp.CallToolResult, struct{}, error) {
		urlPatterns := make([]*cdpnetwork.BlockPattern, 0, len(input.Patterns))
		for _, p := range input.Patterns {
			if p == "" {
				return nil, struct{}{}, fmt.Errorf("block_urls: pattern must not be empty")
			}
			urlPatterns = append(urlPatterns, &cdpnetwork.BlockPattern{
				URLPattern: normalizeBlockPattern(p),
				Block:      true,
			})
		}

		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		_, err = chromedp.Call(tctx, cdpnetwork.SetBlockedURLs, cdpnetwork.SetBlockedURLsParams{
			URLPatterns: urlPatterns,
		})
		return nil, struct{}{}, err
	})
}

// normalizeBlockPattern converts shorthand wildcard patterns (e.g. "*.png",
// "*/api/*", "*.example.com", "*.example.com/*", "ads.example.com",
// "127.0.0.1/api/*") into absolute WHATWG URLPattern strings required by CDP's
// Network.setBlockedURLs urlPatterns field. Already-absolute patterns
// containing "://" are returned unchanged.
//
// Host patterns without an explicit port receive a ":*" port wildcard because
// WHATWG URLPattern otherwise matches only protocol default ports (80/443).
func normalizeBlockPattern(p string) string {
	if p == "" || strings.Contains(p, "://") {
		return p
	}
	if p == "*" {
		return "*://*:*/*"
	}
	if strings.HasPrefix(p, "*/") {
		return "*://*:*" + p[1:]
	}
	if strings.HasPrefix(p, "/") {
		return "*://*:*" + p
	}
	if strings.HasPrefix(p, "*.") {
		if host, rest, hasSlash := strings.Cut(p, "/"); hasSlash {
			return "*://" + withWildcardPort(host) + "/" + rest
		}
		// Distinguish subdomain wildcards (*.example.com) from file-extension
		// wildcards (*.png): a domain has at least one dot after "*.".
		if strings.Contains(p[2:], ".") {
			return "*://" + withWildcardPort(p) + "/*"
		}
		return "*://*:*/" + p
	}
	if strings.HasPrefix(p, "*") {
		return "*://*:*/" + p
	}
	if host, rest, hasSlash := strings.Cut(p, "/"); hasSlash {
		return "*://" + withWildcardPort(host) + "/" + rest
	}
	return "*://" + withWildcardPort(p) + "/*"
}

// withWildcardPort appends ":*" to a hostname or host wildcard when it does
// not already specify an explicit port (respecting bracketed IPv6 literals).
func withWildcardPort(host string) string {
	if strings.LastIndex(host, ":") > strings.LastIndex(host, "]") {
		return host
	}
	return host + ":*"
}
