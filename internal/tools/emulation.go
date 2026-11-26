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

const (
	emulateActionViewport         = "viewport"
	emulateActionDevice           = "device"
	emulateActionNetwork          = "network"
	emulateActionGeolocation      = "geolocation"
	emulateActionTimezone         = "timezone"
	emulateActionLocale           = "locale"
	emulateActionUserAgent        = "user_agent"
	emulateActionCPUThrottling    = "cpu_throttling"
	emulateActionVisionDeficiency = "vision_deficiency"
	emulateActionMedia            = "media"
	emulateActionPseudoState      = "pseudo_state"
)

var emulateActionValues = []string{
	emulateActionViewport,
	emulateActionDevice,
	emulateActionNetwork,
	emulateActionGeolocation,
	emulateActionTimezone,
	emulateActionLocale,
	emulateActionUserAgent,
	emulateActionCPUThrottling,
	emulateActionVisionDeficiency,
	emulateActionMedia,
	emulateActionPseudoState,
}

// mediaFeatureInput represents a single CSS media feature override.
type mediaFeatureInput struct {
	Name  string `json:"name" jsonschema:"Media feature name (e.g. prefers-color-scheme, prefers-reduced-motion)."`
	Value string `json:"value" jsonschema:"Media feature value (e.g. dark, light, reduce, no-preference)."`
}

// emulateInput is the unified input for emulate.
type emulateInput struct {
	tabInput
	Action string `json:"action" jsonschema:"Emulation action: 'viewport', 'device', 'network', 'geolocation', 'timezone', 'locale', 'user_agent', 'cpu_throttling', 'vision_deficiency', 'media', or 'pseudo_state'. Required."`

	// viewport
	Width             int     `json:"width,omitempty" jsonschema:"For action='viewport': viewport width in pixels (> 0)."`
	Height            int     `json:"height,omitempty" jsonschema:"For action='viewport': viewport height in pixels (> 0)."`
	DeviceScaleFactor float64 `json:"device_scale_factor,omitempty" jsonschema:"For action='viewport': device scale factor (default 1.0)."`
	Mobile            bool    `json:"mobile,omitempty" jsonschema:"For action='viewport': emulate mobile viewport (default false)."`

	// device
	Device    string `json:"device,omitempty" jsonschema:"For action='device': device preset name, or 'reset' to restore desktop defaults."`
	Landscape bool   `json:"landscape,omitempty" jsonschema:"For action='device': landscape orientation (default false; cannot be true when device='reset')."`

	// network
	Offline            bool    `json:"offline,omitempty" jsonschema:"For action='network': simulate offline mode (default false)."`
	Latency            float64 `json:"latency,omitempty" jsonschema:"For action='network': minimum latency in ms (0 to disable)."`
	DownloadThroughput float64 `json:"download_throughput,omitempty" jsonschema:"For action='network': max download bytes/sec (-1 or 0 to disable)."`
	UploadThroughput   float64 `json:"upload_throughput,omitempty" jsonschema:"For action='network': max upload bytes/sec (-1 or 0 to disable)."`

	// geolocation
	Latitude  *float64 `json:"latitude,omitempty" jsonschema:"For action='geolocation': mock latitude (-90 to 90). Omit all coordinate fields to reset."`
	Longitude *float64 `json:"longitude,omitempty" jsonschema:"For action='geolocation': mock longitude (-180 to 180). Omit all coordinate fields to reset."`
	Accuracy  *float64 `json:"accuracy,omitempty" jsonschema:"For action='geolocation': mock accuracy in meters (default 1)."`

	// timezone
	TimezoneID string `json:"timezone_id,omitempty" jsonschema:"For action='timezone': IANA timezone ID (e.g. America/New_York). Empty resets to default."`

	// locale
	Locale string `json:"locale,omitempty" jsonschema:"For action='locale': ICU locale ID (e.g. en_US, de_DE). Empty resets to default."`

	// user_agent
	UserAgent      *string `json:"user_agent,omitempty" jsonschema:"For action='user_agent': user agent string (empty string resets)."`
	AcceptLanguage string  `json:"accept_language,omitempty" jsonschema:"For action='user_agent': emulated Accept-Language (e.g. en-US, fr-FR)."`
	Platform       string  `json:"platform,omitempty" jsonschema:"For action='user_agent': navigator.platform override (e.g. Win32, Linux x86_64, MacIntel)."`

	// cpu_throttling
	Rate *float64 `json:"rate,omitempty" jsonschema:"For action='cpu_throttling': slowdown factor (1 = disabled, 4 = 4x slower; values < 1 clamp to 1)."`

	// vision_deficiency
	Type string `json:"type,omitempty" jsonschema:"For action='vision_deficiency': none, blurredVision, reducedContrast, achromatopsia, deuteranopia, protanopia, tritanopia."`

	// media
	Media    string              `json:"media,omitempty" jsonschema:"For action='media': CSS media type ('screen', 'print', or '' to reset)."`
	Features []mediaFeatureInput `json:"features,omitempty" jsonschema:"For action='media': CSS media features (e.g. prefers-color-scheme: dark, prefers-reduced-motion: reduce)."`

	// pseudo_state
	Ref      string   `json:"ref,omitempty" jsonschema:"For action='pseudo_state': element ref from snapshot (mutually exclusive with selector/frame)."`
	Selector string   `json:"selector,omitempty" jsonschema:"For action='pseudo_state': CSS/shadow ('>>>') selector of element."`
	Frame    string   `json:"frame,omitempty" jsonschema:"For action='pseudo_state': optional same-origin frame ID for selector."`
	States   []string `json:"states,omitempty" jsonschema:"For action='pseudo_state': CSS pseudo-classes to force (omit or [] to clear)."`
	Timeout  int      `json:"timeout,omitempty" jsonschema:"For action='pseudo_state': max time in milliseconds to wait for selector (default 5000)."`
}

// emulateOutput reports details for device and pseudo_state actions (empty {} for others).
type emulateOutput struct {
	// device
	Device            string  `json:"device,omitempty"`
	Width             int64   `json:"width,omitempty"`
	Height            int64   `json:"height,omitempty"`
	DeviceScaleFactor float64 `json:"device_scale_factor,omitempty"`
	Mobile            bool    `json:"mobile,omitempty"`
	Touch             bool    `json:"touch,omitempty"`
	Landscape         bool    `json:"landscape,omitempty"`
	UserAgent         string  `json:"user_agent,omitempty"`

	// pseudo_state
	States *[]string `json:"states,omitempty"`
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

	visionDeficiencyValues = []string{
		string(emulation.SetEmulatedVisionDeficiencyTypeNone),
		string(emulation.SetEmulatedVisionDeficiencyTypeBlurredVision),
		string(emulation.SetEmulatedVisionDeficiencyTypeReducedContrast),
		string(emulation.SetEmulatedVisionDeficiencyTypeAchromatopsia),
		string(emulation.SetEmulatedVisionDeficiencyTypeDeuteranopia),
		string(emulation.SetEmulatedVisionDeficiencyTypeProtanopia),
		string(emulation.SetEmulatedVisionDeficiencyTypeTritanopia),
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

func validateEmulateFields(input emulateInput) error {
	groups := []struct {
		action string
		set    bool
	}{
		{emulateActionViewport, input.Width != 0 || input.Height != 0 || input.DeviceScaleFactor != 0 || input.Mobile},
		{emulateActionDevice, input.Device != "" || input.Landscape},
		{emulateActionNetwork, input.Offline || input.Latency != 0 || input.DownloadThroughput != 0 || input.UploadThroughput != 0},
		{emulateActionGeolocation, input.Latitude != nil || input.Longitude != nil || input.Accuracy != nil},
		{emulateActionTimezone, input.TimezoneID != ""},
		{emulateActionLocale, input.Locale != ""},
		{emulateActionUserAgent, input.UserAgent != nil || input.AcceptLanguage != "" || input.Platform != ""},
		{emulateActionCPUThrottling, input.Rate != nil},
		{emulateActionVisionDeficiency, input.Type != ""},
		{emulateActionMedia, input.Media != "" || input.Features != nil},
		{emulateActionPseudoState, input.Ref != "" || input.Selector != "" || input.Frame != "" || input.States != nil || input.Timeout != 0},
	}
	for _, g := range groups {
		if g.set && g.action != input.Action {
			return fmt.Errorf("action=%q does not accept fields belonging to action=%q", input.Action, g.action)
		}
	}
	return nil
}

func registerEmulationTools(s *mcp.Server, mgr *browser.Manager) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "emulate",
		Description: "Emulate browser/device environment on a tab: viewport metrics ('viewport'), mobile/tablet device presets ('device'), network throttling/offline ('network'), geolocation ('geolocation'), timezone ('timezone'), locale ('locale'), user agent ('user_agent'), CPU throttling ('cpu_throttling'), vision deficiency ('vision_deficiency'), CSS media/color-scheme ('media'), or forced CSS pseudo-states ('pseudo_state', supporting snapshot ref, CSS/'>>>' shadow selector, and same-origin frame).",
		InputSchema: schemaWithEnums[emulateInput](map[string][]string{
			"action": emulateActionValues,
			"device": emulateDeviceValues,
			"type":   visionDeficiencyValues,
			"states": forcePseudoStateValues,
		}),
		Annotations: idempotentWriteAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input emulateInput) (*mcp.CallToolResult, emulateOutput, error) {
		if err := validateEnum("action", input.Action, false, emulateActionValues); err != nil {
			return nil, emulateOutput{}, err
		}
		if err := validateEmulateFields(input); err != nil {
			return nil, emulateOutput{}, err
		}

		switch input.Action {
		case emulateActionViewport:
			if input.Width <= 0 || input.Height <= 0 {
				return nil, emulateOutput{}, fmt.Errorf("width and height must be > 0, got %dx%d", input.Width, input.Height)
			}
			if err := validateNonNegativeFinite("device_scale_factor", input.DeviceScaleFactor); err != nil {
				return nil, emulateOutput{}, err
			}
			t, err := mgr.ResolveTab("", input.Tab)
			if err != nil {
				return nil, emulateOutput{}, err
			}
			tctx, tcancel := tabContext(ctx, t.Context())
			defer tcancel()
			scale := input.DeviceScaleFactor
			if scale == 0 {
				scale = 1.0
			}
			_, err = chromedp.Call(tctx, emulation.SetDeviceMetricsOverride, emulation.SetDeviceMetricsOverrideParams{
				Width:             int64(input.Width),
				Height:            int64(input.Height),
				DeviceScaleFactor: scale,
				Mobile:            input.Mobile,
			})
			return nil, emulateOutput{}, err

		case emulateActionDevice:
			if err := validateEnum("device", input.Device, false, emulateDeviceValues); err != nil {
				return nil, emulateOutput{}, err
			}
			if input.Device == "reset" && input.Landscape {
				return nil, emulateOutput{}, fmt.Errorf("landscape cannot be true when device is 'reset'")
			}
			t, err := mgr.ResolveTab("", input.Tab)
			if err != nil {
				return nil, emulateOutput{}, err
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
					return nil, emulateOutput{}, err
				}
				return nil, emulateOutput{Device: "reset"}, nil
			}

			preset := devicePresetsByName[input.Device]
			dev := preset.portrait
			if input.Landscape {
				dev = preset.landscape
			}
			if err := chromedp.Do(tctx, chromedp.Emulate(dev)); err != nil {
				return nil, emulateOutput{}, err
			}
			info := dev.Device()
			return nil, emulateOutput{
				Device:            preset.name,
				Width:             info.Width,
				Height:            info.Height,
				DeviceScaleFactor: info.Scale,
				Mobile:            info.Mobile,
				Touch:             info.Touch,
				Landscape:         info.Landscape,
				UserAgent:         info.UserAgent,
			}, nil

		case emulateActionNetwork:
			if err := validateNonNegativeFinite("latency", input.Latency); err != nil {
				return nil, emulateOutput{}, err
			}
			if math.IsNaN(input.DownloadThroughput) || math.IsInf(input.DownloadThroughput, 0) || (input.DownloadThroughput < 0 && input.DownloadThroughput != -1) {
				return nil, emulateOutput{}, fmt.Errorf("download_throughput must be -1 or a finite number >= 0, got %v", input.DownloadThroughput)
			}
			if math.IsNaN(input.UploadThroughput) || math.IsInf(input.UploadThroughput, 0) || (input.UploadThroughput < 0 && input.UploadThroughput != -1) {
				return nil, emulateOutput{}, fmt.Errorf("upload_throughput must be -1 or a finite number >= 0, got %v", input.UploadThroughput)
			}
			t, err := mgr.ResolveTab("", input.Tab)
			if err != nil {
				return nil, emulateOutput{}, err
			}
			download := input.DownloadThroughput
			if download == 0 {
				download = -1
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
			return nil, emulateOutput{}, err

		case emulateActionGeolocation:
			if (input.Latitude == nil) != (input.Longitude == nil) {
				return nil, emulateOutput{}, fmt.Errorf("latitude and longitude must both be provided (or both omitted to reset)")
			}
			if input.Latitude != nil {
				lat := *input.Latitude
				if math.IsNaN(lat) || math.IsInf(lat, 0) || lat < -90 || lat > 90 {
					return nil, emulateOutput{}, fmt.Errorf("latitude must be between -90 and 90, got %v", lat)
				}
				lon := *input.Longitude
				if math.IsNaN(lon) || math.IsInf(lon, 0) || lon < -180 || lon > 180 {
					return nil, emulateOutput{}, fmt.Errorf("longitude must be between -180 and 180, got %v", lon)
				}
			} else if input.Accuracy != nil {
				return nil, emulateOutput{}, fmt.Errorf("accuracy requires latitude and longitude")
			}
			if input.Accuracy != nil {
				if err := validateNonNegativeFinite("accuracy", *input.Accuracy); err != nil {
					return nil, emulateOutput{}, err
				}
			}
			t, err := mgr.ResolveTab("", input.Tab)
			if err != nil {
				return nil, emulateOutput{}, err
			}
			params := emulation.SetGeolocationOverrideParams{
				Latitude:  input.Latitude,
				Longitude: input.Longitude,
				Accuracy:  input.Accuracy,
			}
			if params.Accuracy == nil && input.Latitude != nil {
				params.Accuracy = new(1.0)
			}
			tctx, tcancel := tabContext(ctx, t.Context())
			defer tcancel()
			_, err = chromedp.Call(tctx, emulation.SetGeolocationOverride, params)
			return nil, emulateOutput{}, err

		case emulateActionTimezone:
			t, err := mgr.ResolveTab("", input.Tab)
			if err != nil {
				return nil, emulateOutput{}, err
			}
			tctx, tcancel := tabContext(ctx, t.Context())
			defer tcancel()
			_, err = chromedp.Call(tctx, emulation.SetTimezoneOverride, emulation.SetTimezoneOverrideParams{
				TimezoneID: input.TimezoneID,
			})
			return nil, emulateOutput{}, err

		case emulateActionLocale:
			t, err := mgr.ResolveTab("", input.Tab)
			if err != nil {
				return nil, emulateOutput{}, err
			}
			tctx, tcancel := tabContext(ctx, t.Context())
			defer tcancel()
			_, err = chromedp.Call(tctx, emulation.SetLocaleOverride, emulation.SetLocaleOverrideParams{
				Locale: input.Locale,
			})
			return nil, emulateOutput{}, err

		case emulateActionUserAgent:
			if input.UserAgent == nil {
				return nil, emulateOutput{}, fmt.Errorf("user_agent is required for action='user_agent' (empty string resets)")
			}
			t, err := mgr.ResolveTab("", input.Tab)
			if err != nil {
				return nil, emulateOutput{}, err
			}
			tctx, tcancel := tabContext(ctx, t.Context())
			defer tcancel()
			_, err = chromedp.Call(tctx, emulation.SetUserAgentOverride, emulation.SetUserAgentOverrideParams{
				UserAgent:      *input.UserAgent,
				AcceptLanguage: input.AcceptLanguage,
				Platform:       input.Platform,
			})
			return nil, emulateOutput{}, err

		case emulateActionCPUThrottling:
			if input.Rate == nil {
				return nil, emulateOutput{}, fmt.Errorf("rate is required for action='cpu_throttling'")
			}
			if err := validateNonNegativeFinite("rate", *input.Rate); err != nil {
				return nil, emulateOutput{}, err
			}
			t, err := mgr.ResolveTab("", input.Tab)
			if err != nil {
				return nil, emulateOutput{}, err
			}
			rate := *input.Rate
			if rate < 1 {
				rate = 1
			}
			tctx, tcancel := tabContext(ctx, t.Context())
			defer tcancel()
			_, err = chromedp.Call(tctx, emulation.SetCPUThrottlingRate, emulation.SetCPUThrottlingRateParams{
				Rate: rate,
			})
			return nil, emulateOutput{}, err

		case emulateActionVisionDeficiency:
			if err := validateEnum("vision deficiency type", input.Type, false, visionDeficiencyValues); err != nil {
				return nil, emulateOutput{}, err
			}
			t, err := mgr.ResolveTab("", input.Tab)
			if err != nil {
				return nil, emulateOutput{}, err
			}
			tctx, tcancel := tabContext(ctx, t.Context())
			defer tcancel()
			_, err = chromedp.Call(tctx, emulation.SetEmulatedVisionDeficiency, emulation.SetEmulatedVisionDeficiencyParams{
				Type: emulation.SetEmulatedVisionDeficiencyType(input.Type),
			})
			return nil, emulateOutput{}, err

		case emulateActionMedia:
			t, err := mgr.ResolveTab("", input.Tab)
			if err != nil {
				return nil, emulateOutput{}, err
			}
			var features []*emulation.MediaFeature
			if len(input.Features) > 0 {
				features = make([]*emulation.MediaFeature, len(input.Features))
				for i, f := range input.Features {
					features[i] = &emulation.MediaFeature{Name: f.Name, Value: f.Value}
				}
			}
			tctx, tcancel := tabContext(ctx, t.Context())
			defer tcancel()
			_, err = chromedp.Call(tctx, emulation.SetEmulatedMedia, emulation.SetEmulatedMediaParams{
				Media:    input.Media,
				Features: features,
			})
			return nil, emulateOutput{}, err

		default: // emulateActionPseudoState
			if err := validateElementTarget(input.Ref, input.Selector, input.Frame, true); err != nil {
				return nil, emulateOutput{}, err
			}
			if err := validateSelectorTimeout(input.Timeout); err != nil {
				return nil, emulateOutput{}, err
			}
			states := make([]string, 0, len(input.States))
			for _, st := range input.States {
				if err := validateEnum("pseudo state", st, false, forcePseudoStateValues); err != nil {
					return nil, emulateOutput{}, err
				}
				if !slices.Contains(states, st) {
					states = append(states, st)
				}
			}
			t, err := mgr.ResolveTab("", input.Tab)
			if err != nil {
				return nil, emulateOutput{}, err
			}
			tctx, tcancel := tabContext(ctx, t.Context())
			defer tcancel()

			var backendNodeID cdp.BackendNodeID
			err = chromedp.Do(tctx, chromedp.Func(func(ctx context.Context, tgt *chromedp.Target) error {
				el, err := resolveElementObject(ctx, tgt, t, input.Ref, input.Selector, input.Frame, input.Timeout, true)
				if err != nil {
					return err
				}
				defer el.Release(ctx, tgt)

				if el.BackendNodeID == 0 {
					return fmt.Errorf("element %s has no DOM node ID", el.Label)
				}
				backendNodeID = el.BackendNodeID
				return nil
			}))
			if err != nil {
				return nil, emulateOutput{}, err
			}

			applied, err := t.ForcePseudoState(tctx, backendNodeID, states)
			if err != nil {
				return nil, emulateOutput{}, err
			}
			if applied == nil {
				applied = []string{}
			}
			return nil, emulateOutput{States: &applied}, nil
		}
	})
}
