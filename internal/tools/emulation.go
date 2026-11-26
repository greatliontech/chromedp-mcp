package tools

import (
	"context"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	cdpnetwork "github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/device"
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

// ForcePseudoStateInput is the input for force_pseudo_state.
type ForcePseudoStateInput struct {
	SelectorInput
	Selector string   `json:"selector" jsonschema:"CSS selector of the element to force pseudo-states on"`
	States   []string `json:"states,omitempty" jsonschema:"CSS pseudo-classes to force (active, focus, hover, visited, focus-within, focus-visible, target). Omit or pass [] to clear forced pseudo-states on this element."`
}

// ForcePseudoStateOutput is the output for force_pseudo_state.
type ForcePseudoStateOutput struct {
	States []string `json:"states"`
}

// EmulateDeviceInput is the input for emulate_device.
type EmulateDeviceInput struct {
	TabInput
	Device    string `json:"device" jsonschema:"Device preset name to emulate, or 'reset' to clear device emulation and restore desktop defaults."`
	Landscape bool   `json:"landscape,omitempty" jsonschema:"Emulate landscape orientation (default false, portrait). Cannot be true when device='reset'."`
}

// EmulateDeviceOutput reports the effective device emulation settings applied.
type EmulateDeviceOutput struct {
	Device            string  `json:"device"`
	Width             int64   `json:"width,omitempty"`
	Height            int64   `json:"height,omitempty"`
	DeviceScaleFactor float64 `json:"device_scale_factor,omitempty"`
	Mobile            bool    `json:"mobile,omitempty"`
	Touch             bool    `json:"touch,omitempty"`
	Landscape         bool    `json:"landscape,omitempty"`
	UserAgent         string  `json:"user_agent,omitempty"`
}

type devicePreset struct {
	name      string
	portrait  chromedp.Device
	landscape chromedp.Device
}

var devicePresetList = []devicePreset{
	{name: "iPhone SE", portrait: device.IPhoneSE3rdgen, landscape: device.IPhoneSE3rdgenlandscape},
	{name: "iPhone 13", portrait: device.IPhone13, landscape: device.IPhone13landscape},
	{name: "iPhone 14", portrait: device.IPhone14, landscape: device.IPhone14landscape},
	{name: "iPhone 14 Pro", portrait: device.IPhone14Pro, landscape: device.IPhone14Prolandscape},
	{name: "iPhone 15", portrait: device.IPhone15, landscape: device.IPhone15landscape},
	{name: "iPhone 15 Pro", portrait: device.IPhone15Pro, landscape: device.IPhone15Prolandscape},
	{name: "iPhone 15 Pro Max", portrait: device.IPhone15ProMax, landscape: device.IPhone15ProMaxlandscape},
	{name: "iPhone 16", portrait: device.IPhone16, landscape: device.IPhone16landscape},
	{name: "iPhone 16 Pro", portrait: device.IPhone16Pro, landscape: device.IPhone16Prolandscape},
	{name: "iPhone 16 Pro Max", portrait: device.IPhone16ProMax, landscape: device.IPhone16ProMaxlandscape},
	{name: "iPad", portrait: device.IPadgen7, landscape: device.IPadgen7landscape},
	{name: "iPad Mini", portrait: device.IPadMini, landscape: device.IPadMinilandscape},
	{name: "iPad Pro 11", portrait: device.IPadPro11, landscape: device.IPadPro11landscape},
	{name: "Pixel 5", portrait: device.Pixel5, landscape: device.Pixel5landscape},
	{name: "Galaxy S9+", portrait: device.GalaxyS9, landscape: device.GalaxyS9landscape},
	{name: "Galaxy Tab S4", portrait: device.GalaxyTabS4, landscape: device.GalaxyTabS4landscape},
}

var (
	emulateDeviceValues []string
	devicePresetsByName map[string]devicePreset

	forcePseudoStateValues = []string{
		"active",
		"focus",
		"hover",
		"visited",
		"focus-within",
		"focus-visible",
		"target",
	}
)

func init() {
	emulateDeviceValues = make([]string, 0, len(devicePresetList)+1)
	emulateDeviceValues = append(emulateDeviceValues, "reset")
	devicePresetsByName = make(map[string]devicePreset, len(devicePresetList))
	for _, p := range devicePresetList {
		emulateDeviceValues = append(emulateDeviceValues, p.name)
		devicePresetsByName[p.name] = p
	}
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
		Name:        "force_pseudo_state",
		Description: "Force CSS pseudo-classes (:hover, :focus, :active, :visited, :focus-within, :focus-visible, :target) on an element for visual/style inspection. Omit states or pass [] to clear forced pseudo-states on that element.",
		InputSchema: schemaWithEnums[ForcePseudoStateInput](map[string][]string{
			"states": forcePseudoStateValues,
		}),
		Annotations: idempotentWriteAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ForcePseudoStateInput) (*mcp.CallToolResult, ForcePseudoStateOutput, error) {
		if input.Selector == "" {
			return nil, ForcePseudoStateOutput{}, fmt.Errorf("selector is required")
		}
		if err := validateSelectorTimeout(input.Timeout); err != nil {
			return nil, ForcePseudoStateOutput{}, err
		}
		states := make([]string, 0, len(input.States))
		for _, st := range input.States {
			if err := validateEnum("pseudo state", st, false, forcePseudoStateValues); err != nil {
				return nil, ForcePseudoStateOutput{}, err
			}
			if !slices.Contains(states, st) {
				states = append(states, st)
			}
		}

		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, ForcePseudoStateOutput{}, err
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		sctx, scancel := selectorContext(tctx, input.Timeout)
		defer scancel()
		nodes, err := chromedp.Run(sctx, chromedp.Nodes(chromedp.CSS(input.Selector), chromedp.NodeReady))
		if err != nil {
			return nil, ForcePseudoStateOutput{}, selectorError(tctx, input.Selector, err)
		}
		if len(nodes) == 0 {
			return nil, ForcePseudoStateOutput{}, fmt.Errorf("selector %q matched no elements", input.Selector)
		}

		applied, err := t.ForcePseudoState(tctx, nodes[0].BackendNodeID, states)
		if err != nil {
			return nil, ForcePseudoStateOutput{}, err
		}
		if applied == nil {
			applied = []string{}
		}
		return nil, ForcePseudoStateOutput{States: applied}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "emulate_device",
		Description: "Emulate a mobile or tablet device preset (viewport, device scale factor, mobile flag, orientation, touch emulation, and user agent), or pass device='reset' to clear overrides and restore desktop defaults.",
		InputSchema: schemaWithEnums[EmulateDeviceInput](map[string][]string{
			"device": emulateDeviceValues,
		}),
		Annotations: idempotentWriteAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input EmulateDeviceInput) (*mcp.CallToolResult, EmulateDeviceOutput, error) {
		if err := validateEnum("device", input.Device, false, emulateDeviceValues); err != nil {
			return nil, EmulateDeviceOutput{}, err
		}
		if input.Device == "reset" && input.Landscape {
			return nil, EmulateDeviceOutput{}, fmt.Errorf("landscape cannot be true when device is 'reset'")
		}

		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, EmulateDeviceOutput{}, err
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()

		if input.Device == "reset" {
			err = chromedp.Do(tctx, chromedp.Func(func(ctx context.Context, tgt *chromedp.Target) error {
				if _, err := cdp.Call(ctx, tgt, emulation.ClearDeviceMetricsOverride, cdp.Empty{}); err != nil {
					return err
				}
				if _, err := cdp.Call(ctx, tgt, emulation.SetUserAgentOverride, emulation.SetUserAgentOverrideParams{
					UserAgent: "",
				}); err != nil {
					return err
				}
				if _, err := cdp.Call(ctx, tgt, emulation.SetTouchEmulationEnabled, emulation.SetTouchEmulationEnabledParams{
					Enabled: false,
				}); err != nil {
					return err
				}
				// Wait one animation frame (bounded and skipped when a JS dialog
				// blocks the renderer) so Blink finishes applying the cleared
				// visual properties before returning to the caller.
				if t.Dialog == nil || !t.Dialog.Open() {
					settleCtx, settleCancel := context.WithTimeout(ctx, 250*time.Millisecond)
					_, _ = chromedp.Evaluate[chromedp.Void](
						`new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)))`,
						chromedp.EvalAwaitPromise,
					)(settleCtx, tgt)
					settleCancel()
				}
				return nil
			}))
			if err != nil {
				return nil, EmulateDeviceOutput{}, err
			}
			return nil, EmulateDeviceOutput{Device: "reset"}, nil
		}

		preset := devicePresetsByName[input.Device]
		dev := preset.portrait
		if input.Landscape {
			dev = preset.landscape
		}
		if err := chromedp.Do(tctx, chromedp.Emulate(dev)); err != nil {
			return nil, EmulateDeviceOutput{}, err
		}
		info := dev.Device()
		return nil, EmulateDeviceOutput{
			Device:            preset.name,
			Width:             info.Width,
			Height:            info.Height,
			DeviceScaleFactor: info.Scale,
			Mobile:            info.Mobile,
			Touch:             info.Touch,
			Landscape:         info.Landscape,
			UserAgent:         info.UserAgent,
		}, nil
	})
}
