package tools

import (
	"context"
	"fmt"

	cdpbrowser "github.com/chromedp/cdproto/browser"
	cdpnetwork "github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/security"
	"github.com/chromedp/chromedp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/browser"
)

const (
	configActionAddScript           = "add_script"
	configActionRemoveScript        = "remove_script"
	configActionSetHeaders          = "set_headers"
	configActionSetPermission       = "set_permission"
	configActionSetIgnoreCertErrors = "set_ignore_cert_errors"
)

var (
	configurePageActionValues = []string{
		configActionAddScript,
		configActionRemoveScript,
		configActionSetHeaders,
		configActionSetPermission,
		configActionSetIgnoreCertErrors,
	}
	permissionSettingValues = []string{
		string(cdpbrowser.PermissionSettingGranted),
		string(cdpbrowser.PermissionSettingDenied),
		string(cdpbrowser.PermissionSettingPrompt),
	}
)

// configurePageInput is the input for configure_page.
type configurePageInput struct {
	tabInput
	Action string `json:"action" jsonschema:"Page configuration action: 'add_script', 'remove_script', 'set_headers', 'set_permission', or 'set_ignore_cert_errors'. Required."`

	// add_script
	Source      string `json:"source,omitempty" jsonschema:"For action='add_script': JavaScript source code to evaluate on every new document."`
	EvaluateNow *bool  `json:"evaluate_now,omitempty" jsonschema:"For action='add_script' (required): if true, also evaluate once on the currently loaded document. Guard side-effecting wrappers (e.g. window.fetch) for idempotency."`

	// remove_script
	Identifier string `json:"identifier,omitempty" jsonschema:"For action='remove_script': script identifier returned by action='add_script'."`

	// set_headers
	Headers map[string]string `json:"headers,omitempty" jsonschema:"For action='set_headers': custom HTTP headers injected into all requests on this tab. Pass {} to clear."`

	// set_permission
	Name    string `json:"name,omitempty" jsonschema:"For action='set_permission': permission name (e.g. geolocation, notifications, camera, microphone, clipboard-read, clipboard-write)."`
	Setting string `json:"setting,omitempty" jsonschema:"For action='set_permission': granted, denied, or prompt."`
	Origin  string `json:"origin,omitempty" jsonschema:"For action='set_permission': optional origin scope (if omitted applies to all origins)."`

	// set_ignore_cert_errors
	Ignore *bool `json:"ignore,omitempty" jsonschema:"For action='set_ignore_cert_errors' (required): if true, ignore TLS certificate errors."`
}

// configurePageOutput is the output for configure_page.
type configurePageOutput struct {
	Identifier string `json:"identifier,omitempty"`
	Warning    string `json:"warning,omitempty"`
}

func validateConfigurePageFields(input configurePageInput) error {
	groups := []struct {
		action string
		set    bool
	}{
		{configActionAddScript, input.Source != "" || input.EvaluateNow != nil},
		{configActionRemoveScript, input.Identifier != ""},
		{configActionSetHeaders, input.Headers != nil},
		{configActionSetPermission, input.Name != "" || input.Setting != "" || input.Origin != ""},
		{configActionSetIgnoreCertErrors, input.Ignore != nil},
	}
	for _, g := range groups {
		if g.set && g.action != input.Action {
			return fmt.Errorf("action=%q does not accept fields belonging to action=%q", input.Action, g.action)
		}
	}
	return nil
}

func registerConfigTools(s *mcp.Server, mgr *browser.Manager) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "configure_page",
		Description: "Configure tab-level page behavior: inject/remove new-document scripts ('add_script', 'remove_script'), inject custom HTTP request headers ('set_headers'), override browser permissions ('set_permission'), or toggle TLS certificate error ignoring ('set_ignore_cert_errors').",
		InputSchema: schemaWithEnums[configurePageInput](map[string][]string{
			"action":  configurePageActionValues,
			"setting": permissionSettingValues,
		}),
		Annotations: destructiveAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input configurePageInput) (*mcp.CallToolResult, configurePageOutput, error) {
		if err := validateEnum("action", input.Action, false, configurePageActionValues); err != nil {
			return nil, configurePageOutput{}, err
		}
		if err := validateConfigurePageFields(input); err != nil {
			return nil, configurePageOutput{}, err
		}

		switch input.Action {
		case configActionAddScript:
			if input.Source == "" {
				return nil, configurePageOutput{}, fmt.Errorf("source is required for action='add_script'")
			}
			if input.EvaluateNow == nil {
				return nil, configurePageOutput{}, fmt.Errorf("evaluate_now is required for action='add_script'")
			}
			t, err := mgr.ResolveTab("", input.Tab)
			if err != nil {
				return nil, configurePageOutput{}, err
			}
			tctx, tcancel := tabContext(ctx, t.Context())
			defer tcancel()

			// Validate the script parses before committing a new-document registration.
			compileRes, err := chromedp.Call(tctx, runtime.CompileScript, runtime.CompileScriptParams{
				Expression:    input.Source,
				SourceURL:     "add_script",
				PersistScript: false,
			})
			if err != nil {
				return nil, configurePageOutput{}, fmt.Errorf("compile script: %w", err)
			}
			if compileRes.ExceptionDetails != nil {
				return nil, configurePageOutput{}, fmt.Errorf("script does not parse: %s", (&chromedp.ExceptionError{ExceptionDetails: compileRes.ExceptionDetails}).Error())
			}

			addRes, err := chromedp.Call(tctx, page.AddScriptToEvaluateOnNewDocument, page.AddScriptToEvaluateOnNewDocumentParams{
				Source: input.Source,
			})
			if err != nil {
				return nil, configurePageOutput{}, err
			}
			identifier := addRes.Identifier

			out := configurePageOutput{Identifier: string(identifier)}
			if !*input.EvaluateNow {
				return nil, out, nil
			}

			evalErr := chromedp.Do(tctx, chromedp.Evaluate[chromedp.Void](input.Source, chromedp.EvalAwaitPromise))
			if evalErr != nil {
				out.Warning = fmt.Sprintf("evaluate_now=true: current-document evaluation failed: %v. The new-document registration (identifier %s) is still active; remove with configure_page(action='remove_script') if not wanted.",
					evalErr, identifier)
			}
			return nil, out, nil

		case configActionRemoveScript:
			if input.Identifier == "" {
				return nil, configurePageOutput{}, fmt.Errorf("script identifier is required for action='remove_script'")
			}
			t, err := mgr.ResolveTab("", input.Tab)
			if err != nil {
				return nil, configurePageOutput{}, err
			}
			tctx, tcancel := tabContext(ctx, t.Context())
			defer tcancel()
			_, err = chromedp.Call(tctx, page.RemoveScriptToEvaluateOnNewDocument, page.RemoveScriptToEvaluateOnNewDocumentParams{
				Identifier: page.ScriptIdentifier(input.Identifier),
			})
			return nil, configurePageOutput{}, err

		case configActionSetHeaders:
			if input.Headers == nil {
				return nil, configurePageOutput{}, fmt.Errorf("headers object is required for action='set_headers' (pass {} to clear)")
			}
			if err := validateHTTPHeaders(input.Headers); err != nil {
				return nil, configurePageOutput{}, err
			}
			t, err := mgr.ResolveTab("", input.Tab)
			if err != nil {
				return nil, configurePageOutput{}, err
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
			return nil, configurePageOutput{}, err

		case configActionSetPermission:
			if input.Name == "" {
				return nil, configurePageOutput{}, fmt.Errorf("permission name is required for action='set_permission'")
			}
			if err := validateEnum("permission setting", input.Setting, false, permissionSettingValues); err != nil {
				return nil, configurePageOutput{}, err
			}
			t, err := mgr.ResolveTab("", input.Tab)
			if err != nil {
				return nil, configurePageOutput{}, err
			}
			tctx, tcancel := tabContext(ctx, t.Context())
			defer tcancel()
			_, err = chromedp.Call(tctx, cdpbrowser.SetPermission, cdpbrowser.SetPermissionParams{
				Permission: &cdpbrowser.PermissionDescriptor{Name: input.Name},
				Setting:    cdpbrowser.PermissionSetting(input.Setting),
				Origin:     input.Origin,
			})
			return nil, configurePageOutput{}, err

		default: // configActionSetIgnoreCertErrors
			if input.Ignore == nil {
				return nil, configurePageOutput{}, fmt.Errorf("ignore is required for action='set_ignore_cert_errors'")
			}
			t, err := mgr.ResolveTab("", input.Tab)
			if err != nil {
				return nil, configurePageOutput{}, err
			}
			tctx, tcancel := tabContext(ctx, t.Context())
			defer tcancel()
			_, err = chromedp.Call(tctx, security.SetIgnoreCertificateErrors, security.SetIgnoreCertificateErrorsParams{
				Ignore: *input.Ignore,
			})
			return nil, configurePageOutput{}, err
		}
	})
}
