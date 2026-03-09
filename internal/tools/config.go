package tools

import (
	"context"
	"fmt"

	cdpbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/emulation"
	cdpnetwork "github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/security"
	"github.com/chromedp/chromedp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/thegrumpylion/chromedp-mcp/internal/browser"
)

// AddScriptInput is the input for add_script.
type AddScriptInput struct {
	TabInput
	Source string `json:"source" jsonschema:"JavaScript source code to evaluate on every new document."`
}

// AddScriptOutput is the output for add_script.
type AddScriptOutput struct {
	Identifier string `json:"identifier"`
}

// RemoveScriptInput is the input for remove_script.
type RemoveScriptInput struct {
	TabInput
	Identifier string `json:"identifier" jsonschema:"Script identifier returned by add_script."`
}

// SetExtraHeadersInput is the input for set_extra_headers.
type SetExtraHeadersInput struct {
	TabInput
	Headers map[string]string `json:"headers" jsonschema:"HTTP headers to inject into all requests. Pass an empty object to clear."`
}

// SetPermissionInput is the input for set_permission.
type SetPermissionInput struct {
	TabInput
	Name    string `json:"name" jsonschema:"Permission name: geolocation, notifications, camera, microphone, clipboard-read, clipboard-write, etc."`
	Setting string `json:"setting" jsonschema:"Permission setting: granted, denied, or prompt"`
	Origin  string `json:"origin,omitempty" jsonschema:"Scope to a specific origin. If omitted applies to all origins."`
}

// MediaFeatureInput represents a single media feature override.
type MediaFeatureInput struct {
	Name  string `json:"name" jsonschema:"Media feature name (e.g. prefers-color-scheme, prefers-reduced-motion)"`
	Value string `json:"value" jsonschema:"Media feature value (e.g. dark, light, reduce, no-preference)"`
}

// SetEmulatedMediaInput is the input for set_emulated_media.
type SetEmulatedMediaInput struct {
	TabInput
	Media    string              `json:"media,omitempty" jsonschema:"Media type to emulate: screen, print. Empty string resets to default."`
	Features []MediaFeatureInput `json:"features,omitempty" jsonschema:"Media features to override. Common: prefers-color-scheme (dark/light), prefers-reduced-motion (reduce/no-preference)."`
}

// SetIgnoreCertErrorsInput is the input for set_ignore_certificate_errors.
type SetIgnoreCertErrorsInput struct {
	TabInput
	Ignore bool `json:"ignore" jsonschema:"If true, all certificate errors will be ignored."`
}

func registerConfigTools(s *mcp.Server, mgr *browser.Manager) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "add_script",
		Description: "Inject JavaScript to run on every new document before any page scripts. Useful for test fixtures, polyfills, disabling animations, or intercepting APIs. Returns an identifier for removal.",
		Annotations: &mcp.ToolAnnotations{
			IdempotentHint: true,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input AddScriptInput) (*mcp.CallToolResult, AddScriptOutput, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, AddScriptOutput{}, err
		}

		var identifier page.ScriptIdentifier
		err = chromedp.Run(t.Context(), chromedp.ActionFunc(func(ctx context.Context) error {
			var e error
			identifier, e = page.AddScriptToEvaluateOnNewDocument(input.Source).Do(ctx)
			return e
		}))
		if err != nil {
			return nil, AddScriptOutput{}, err
		}
		return nil, AddScriptOutput{Identifier: string(identifier)}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "remove_script",
		Description: "Remove an injected script by its identifier (returned by add_script).",
		Annotations: &mcp.ToolAnnotations{
			DestructiveHint: ptrBool(true),
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input RemoveScriptInput) (*mcp.CallToolResult, struct{}, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}

		err = chromedp.Run(t.Context(), chromedp.ActionFunc(func(ctx context.Context) error {
			return page.RemoveScriptToEvaluateOnNewDocument(page.ScriptIdentifier(input.Identifier)).Do(ctx)
		}))
		return nil, struct{}{}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "set_extra_headers",
		Description: "Inject custom HTTP headers into all requests from this tab. Useful for auth tokens, feature flags, API keys. Pass an empty headers object to clear.",
		Annotations: &mcp.ToolAnnotations{
			IdempotentHint: true,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input SetExtraHeadersInput) (*mcp.CallToolResult, struct{}, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}

		headers := make(cdpnetwork.Headers, len(input.Headers))
		for k, v := range input.Headers {
			headers[k] = v
		}

		err = chromedp.Run(t.Context(), cdpnetwork.SetExtraHTTPHeaders(headers))
		return nil, struct{}{}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "set_permission",
		Description: "Grant, deny, or reset a browser permission (geolocation, notifications, camera, microphone, clipboard-read, clipboard-write, etc.).",
		Annotations: &mcp.ToolAnnotations{
			IdempotentHint: true,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input SetPermissionInput) (*mcp.CallToolResult, struct{}, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}

		setting := cdpbrowser.PermissionSetting(input.Setting)
		switch setting {
		case cdpbrowser.PermissionSettingGranted, cdpbrowser.PermissionSettingDenied, cdpbrowser.PermissionSettingPrompt:
			// valid
		default:
			return nil, struct{}{}, fmt.Errorf("invalid permission setting %q: must be granted, denied, or prompt", input.Setting)
		}

		params := cdpbrowser.SetPermission(
			&cdpbrowser.PermissionDescriptor{Name: input.Name},
			setting,
		)
		if input.Origin != "" {
			params = params.WithOrigin(input.Origin)
		}

		err = chromedp.Run(t.Context(), params)
		return nil, struct{}{}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "set_emulated_media",
		Description: "Override CSS media type and features. Use to test dark mode (prefers-color-scheme: dark), reduced motion (prefers-reduced-motion: reduce), or print styles (media: print). Call with no arguments to reset.",
		Annotations: &mcp.ToolAnnotations{
			IdempotentHint: true,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input SetEmulatedMediaInput) (*mcp.CallToolResult, struct{}, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}

		params := emulation.SetEmulatedMedia()
		if input.Media != "" {
			params = params.WithMedia(input.Media)
		}
		if len(input.Features) > 0 {
			features := make([]*emulation.MediaFeature, len(input.Features))
			for i, f := range input.Features {
				features[i] = &emulation.MediaFeature{Name: f.Name, Value: f.Value}
			}
			params = params.WithFeatures(features)
		}

		err = chromedp.Run(t.Context(), params)
		return nil, struct{}{}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "set_ignore_certificate_errors",
		Description: "Ignore or enforce TLS certificate errors. Enable to test against local dev servers with self-signed certificates.",
		Annotations: &mcp.ToolAnnotations{
			IdempotentHint: true,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input SetIgnoreCertErrorsInput) (*mcp.CallToolResult, struct{}, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}

		err = chromedp.Run(t.Context(), security.SetIgnoreCertificateErrors(input.Ignore))
		return nil, struct{}{}, err
	})
}
