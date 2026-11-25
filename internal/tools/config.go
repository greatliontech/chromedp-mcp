package tools

import (
	"context"
	"fmt"

	cdpbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/emulation"
	cdpnetwork "github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/security"
	"github.com/chromedp/chromedp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/browser"
)

// AddScriptInput is the input for add_script.
type AddScriptInput struct {
	TabInput
	Source string `json:"source" jsonschema:"JavaScript source code to evaluate on every new document."`
	// EvaluateNow controls whether the script also runs once on the
	// document currently loaded in the tab. Required so the LLM declares
	// intent — silently defaulting either way creates real footguns:
	// false misses the common 'instrument now and future' case; true would
	// silently double-execute side-effecting scripts (window.fetch wrappers
	// etc.) on the current page.
	EvaluateNow bool `json:"evaluate_now" jsonschema:"If true, also evaluate the script once on the currently loaded document via Runtime.evaluate. The returned identifier removes only the new-document registration; current-document side effects cannot be undone via remove_script. Required."`
}

// AddScriptOutput is the output for add_script. The identifier always
// refers to the new-document registration (Page.addScriptToEvaluateOnNewDocument).
// Warning is non-empty when evaluate_now=true was requested but the
// current-document evaluation failed — the new-document registration
// stays committed so subsequent loads still install the script.
type AddScriptOutput struct {
	Identifier string `json:"identifier"`
	Warning    string `json:"warning,omitempty"`
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

var permissionSettingValues = []string{
	string(cdpbrowser.PermissionSettingGranted),
	string(cdpbrowser.PermissionSettingDenied),
	string(cdpbrowser.PermissionSettingPrompt),
}

func registerConfigTools(s *mcp.Server, mgr *browser.Manager) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "add_script",
		Description: "Inject JavaScript to run on every new document before any page scripts. Useful for test fixtures, polyfills, disabling animations, or intercepting APIs. The 'evaluate_now' parameter controls whether the script also runs once on the currently loaded document — set true for the common 'instrument now and future loads' case, false to register only for future navigations. Use the 'evaluate' tool if you only want one-shot execution on the current document. Caveat for evaluate_now=true: scripts that wrap globals (window.fetch, XMLHttpRequest.prototype.send, console.log) will run once on top of any existing wrapper and again on every future navigation; guard side-effecting code with `if (!window.__myInstalled) { window.__myInstalled = true; ... }` to keep installation idempotent.",
		// IdempotentHint is false: with evaluate_now=true, successive calls
		// produce additive current-document side effects.
		Annotations: mutatingAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input AddScriptInput) (*mcp.CallToolResult, AddScriptOutput, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, AddScriptOutput{}, err
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()

		// Validate the script parses before doing anything observable.
		// addScriptToEvaluateOnNewDocument doesn't reject syntactically
		// invalid sources at registration — it just throws on every
		// future navigation. Running compileScript first catches parse
		// errors up-front so we never commit a registration that's
		// guaranteed to fail forever, and so the LLM gets a clean error
		// rather than a misleading "registration committed" warning.
		compileRes, err := chromedp.Call(tctx, runtime.CompileScript, runtime.CompileScriptParams{
			Expression:    input.Source,
			SourceURL:     "add_script",
			PersistScript: false,
		})
		if err != nil {
			return nil, AddScriptOutput{}, fmt.Errorf("compile script: %w", err)
		}
		if compileRes.ExceptionDetails != nil {
			return nil, AddScriptOutput{}, fmt.Errorf("script does not parse: %s", (&chromedp.ExceptionError{ExceptionDetails: compileRes.ExceptionDetails}).Error())
		}

		// Register on new documents first. If this fails the registration
		// is not committed and there's nothing to roll back, so we surface
		// the error directly.
		//
		// Note: between this Call and the evaluate_now Do below the page
		// can navigate. If it does, the new-document registration fires
		// on the navigation and the subsequent Runtime.evaluate then
		// re-runs the script on the post-navigation document, producing
		// a double-run. Rare; flagged here so future readers know the
		// pair is not atomic.
		addRes, err := chromedp.Call(tctx, page.AddScriptToEvaluateOnNewDocument, page.AddScriptToEvaluateOnNewDocumentParams{
			Source: input.Source,
		})
		if err != nil {
			return nil, AddScriptOutput{}, err
		}
		identifier := addRes.Identifier

		out := AddScriptOutput{Identifier: string(identifier)}
		if !input.EvaluateNow {
			return nil, out, nil
		}

		// Run the script once on the currently loaded document. Wait for
		// any returned Promise so async failures (Promise.reject, async
		// functions that throw) surface as eval errors rather than
		// silently "succeeding" with an unawaited rejected promise.
		// If this fails, keep the new-document registration committed
		// and surface a warning so the LLM knows the current-doc side of
		// the request didn't take.
		evalErr := chromedp.Do(tctx, chromedp.Evaluate[chromedp.Void](input.Source, chromedp.EvalAwaitPromise))
		if evalErr != nil {
			out.Warning = fmt.Sprintf("evaluate_now=true: current-document evaluation failed: %v. The new-document registration (identifier %s) is still active; remove with remove_script if not wanted.",
				evalErr, identifier)
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "remove_script",
		Description: "Remove an injected script by its identifier (returned by add_script).",
		Annotations: destructiveAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input RemoveScriptInput) (*mcp.CallToolResult, struct{}, error) {
		if input.Identifier == "" {
			return nil, struct{}{}, fmt.Errorf("script identifier is required")
		}
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		_, err = chromedp.Call(tctx, page.RemoveScriptToEvaluateOnNewDocument, page.RemoveScriptToEvaluateOnNewDocumentParams{
			Identifier: page.ScriptIdentifier(input.Identifier),
		})
		return nil, struct{}{}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "set_extra_headers",
		Description: "Inject custom HTTP headers into all requests from this tab. Useful for auth tokens, feature flags, API keys. Pass an empty headers object to clear.",
		Annotations: idempotentWriteAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input SetExtraHeadersInput) (*mcp.CallToolResult, struct{}, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}

		headers := make(cdpnetwork.Headers, len(input.Headers))
		for k, v := range input.Headers {
			headers[k] = v
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		_, err = chromedp.Call(tctx, cdpnetwork.SetExtraHTTPHeaders, cdpnetwork.SetExtraHTTPHeadersParams{
			Headers: headers,
		})
		return nil, struct{}{}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "set_permission",
		Description: "Grant, deny, or reset a browser permission (geolocation, notifications, camera, microphone, clipboard-read, clipboard-write, etc.).",
		InputSchema: schemaWithEnums[SetPermissionInput](map[string][]string{
			"setting": permissionSettingValues,
		}),
		Annotations: idempotentWriteAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input SetPermissionInput) (*mcp.CallToolResult, struct{}, error) {
		if input.Name == "" {
			return nil, struct{}{}, fmt.Errorf("permission name is required")
		}
		if err := validateEnum("permission setting", input.Setting, false, permissionSettingValues); err != nil {
			return nil, struct{}{}, err
		}
		setting := cdpbrowser.PermissionSetting(input.Setting)

		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		_, err = chromedp.Call(tctx, cdpbrowser.SetPermission, cdpbrowser.SetPermissionParams{
			Permission: &cdpbrowser.PermissionDescriptor{Name: input.Name},
			Setting:    setting,
			Origin:     input.Origin,
		})
		return nil, struct{}{}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "set_emulated_media",
		Description: "Override CSS media type and features. Use to test dark mode (prefers-color-scheme: dark), reduced motion (prefers-reduced-motion: reduce), or print styles (media: print). Call with no arguments to reset.",
		Annotations: idempotentWriteAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input SetEmulatedMediaInput) (*mcp.CallToolResult, struct{}, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, struct{}{}, err
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
		return nil, struct{}{}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "set_ignore_certificate_errors",
		Description: "Ignore or enforce TLS certificate errors. Enable to test against local dev servers with self-signed certificates.",
		Annotations: idempotentWriteAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input SetIgnoreCertErrorsInput) (*mcp.CallToolResult, struct{}, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		_, err = chromedp.Call(tctx, security.SetIgnoreCertificateErrors, security.SetIgnoreCertificateErrorsParams{
			Ignore: input.Ignore,
		})
		return nil, struct{}{}, err
	})
}
