// Package tools registers all MCP tools on the server.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/browser"
)

// Options configures optional behavior for the MCP tools.
type Options struct {
	// DownloadDir is the directory for saving screenshots, PDFs, and
	// downloads. When empty, save-to-disk is unavailable and the tools
	// return binary data inline only.
	DownloadDir string
	// AllowedProfiles is the list of Chrome profiles (by display name)
	// that the LLM is permitted to use. When non-empty, the
	// browser_list_profiles tool is registered and browser_launch
	// accepts a profile parameter.
	AllowedProfiles []string
	// UserDataDir overrides the Chrome/Chromium user data directory
	// used for profile discovery. When empty, the default platform
	// location is auto-detected (e.g. ~/.config/google-chrome on
	// Linux). Only relevant when AllowedProfiles is non-empty.
	UserDataDir string

	// resolved at registration time by registerProfileTools
	userDataDir string          // effective user data dir (explicit or auto-detected)
	allowedSet  map[string]bool // allowed profile names for fast lookup
}

// Register registers all tools on the given MCP server.
func Register(s *mcp.Server, mgr *browser.Manager, opts *Options) {
	if opts == nil {
		opts = &Options{}
	}
	registerBrowserTools(s, mgr, opts)
	registerProfileTools(s, mgr, opts)
	registerTabTools(s, mgr)
	registerNavigationTools(s, mgr)
	registerVisualTools(s, mgr, opts)
	registerDOMTools(s, mgr)
	registerFrameTools(s, mgr)
	registerConsoleTools(s, mgr)
	registerBufferTools(s, mgr)
	registerNetworkTools(s, mgr)
	registerInterceptTools(s, mgr)
	registerJSTools(s, mgr)
	registerInteractionTools(s, mgr)
	registerCookieTools(s, mgr)
	registerStorageTools(s, mgr)
	registerIndexedDBTools(s, mgr)
	registerCacheStorageTools(s, mgr)
	registerPerformanceTools(s, mgr)
	registerDownloadTools(s, mgr)
	registerConfigTools(s, mgr)
	registerEmulationTools(s, mgr)
}

// Buffer-read modes for tools that consume per-tab event buffers
// (get_console_logs, get_js_errors, get_network_requests,
// get_websocket_frames, get_layout_shifts, get_lcp, get_downloads).
//
// Mode is a required parameter on those tools to make the destructive
// vs. non-destructive choice explicit at every call site — the previous
// shape (a single boolean defaulting to drain) silently destroyed data
// the LLM hadn't intended to consume.
const (
	ModePeek  = "peek"
	ModeDrain = "drain"
)

// Type-tool modes (used by the `type` tool only). Required for the same
// reason: the previous shape (`clear: bool`, default false) silently
// concatenated typed text onto whatever was already in the field.
const (
	TypeModeReplace = "replace"
	TypeModeAppend  = "append"
)

// validateMode returns an error when mode is not in the allowed set.
// Empty is rejected so the caller cannot silently default into
// destructive behavior. This is defense-in-depth — the schema-level
// rejection (required field + enum, see modeSchemaFor) fires first for
// well-behaved MCP clients, but validateMode catches handler invocations
// that bypass schema validation.
func validateMode(mode string, allowed ...string) error {
	for _, a := range allowed {
		if mode == a {
			return nil
		}
	}
	if mode == "" {
		return fmt.Errorf("mode is required: must be one of %v", allowed)
	}
	return fmt.Errorf("mode must be one of %v, got %q", allowed, mode)
}

// schemaWithEnums returns the JSON Schema inferred from the input type In
// with enum constraints applied to the named properties. For scalar string
// properties, Enum is set on the property; for string-array properties, Enum
// is set on the array's Items schema.
//
// Inference or wiring failures (including non-string or nullable *string
// properties) are programmer errors and panic at registration time rather than
// ship a misconfigured tool.
func schemaWithEnums[In any](enums map[string][]string) *jsonschema.Schema {
	s, err := jsonschema.For[In](nil)
	if err != nil {
		panic(fmt.Sprintf("schemaWithEnums: %v", err))
	}
	for name, allowed := range enums {
		if len(allowed) == 0 {
			panic(fmt.Sprintf("schemaWithEnums: empty enum for property %q", name))
		}
		prop, ok := s.Properties[name]
		if !ok {
			panic(fmt.Sprintf("schemaWithEnums: input type has no %q property", name))
		}
		target := prop
		if prop.Items != nil {
			target = prop.Items
		}
		if target.Type != "string" {
			panic(fmt.Sprintf("schemaWithEnums: property %q target type is %q (types=%v), want \"string\"", name, target.Type, target.Types))
		}
		vals := make([]any, len(allowed))
		for i, v := range allowed {
			vals[i] = v
		}
		target.Enum = vals
	}
	return s
}

// validateEnum validates val against allowed values as defense-in-depth behind
// schema-level enum validation.
func validateEnum(param, val string, allowEmpty bool, allowed []string) error {
	if val == "" && allowEmpty {
		return nil
	}
	if slices.Contains(allowed, val) {
		return nil
	}
	return fmt.Errorf("invalid %s %q: must be one of %v", param, val, allowed)
}

// validateSelectorTimeout rejects negative timeout values on SelectorInput tools.
func validateSelectorTimeout(timeoutMs int) error {
	if timeoutMs < 0 {
		return fmt.Errorf("timeout must be >= 0, got %d", timeoutMs)
	}
	return nil
}

// validateNonNegativeFinite rejects NaN, +/-Inf, and negative values.
func validateNonNegativeFinite(param string, v float64) error {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return fmt.Errorf("%s must be a finite number >= 0, got %v", param, v)
	}
	return nil
}

// validateLimit rejects negative entry limits (0 means default/unlimited).
func validateLimit(limit int) error {
	if limit < 0 {
		return fmt.Errorf("limit must be >= 0, got %d", limit)
	}
	return nil
}

// modeSchemaFor returns the JSON Schema inferred from the input type In
// with its "mode" property constrained to the given enum values.
func modeSchemaFor[In any](allowed ...string) *jsonschema.Schema {
	return schemaWithEnums[In](map[string][]string{"mode": allowed})
}

// readOnlyAnnotations returns MCP ToolAnnotations for tools that never mutate
// browser, tab, or buffer state.
func readOnlyAnnotations() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{
		ReadOnlyHint:    true,
		DestructiveHint: new(false),
		IdempotentHint:  true,
	}
}

// idempotentWriteAnnotations returns MCP ToolAnnotations for non-destructive
// mutating tools whose repeated invocation with the same inputs produces the
// same resulting state.
func idempotentWriteAnnotations() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{
		ReadOnlyHint:    false,
		DestructiveHint: new(false),
		IdempotentHint:  true,
	}
}

// mutatingAnnotations returns MCP ToolAnnotations for non-destructive mutating
// tools that are not idempotent (including peek/drain buffer readers).
func mutatingAnnotations() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{
		ReadOnlyHint:    false,
		DestructiveHint: new(false),
		IdempotentHint:  false,
	}
}

// destructiveAnnotations returns MCP ToolAnnotations for tools that close
// resources or delete state.
func destructiveAnnotations() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{
		ReadOnlyHint:    false,
		DestructiveHint: new(true),
		IdempotentHint:  false,
	}
}

// TabInput is embedded by tool inputs that operate on a tab but do not
// involve CSS selector polling.
type TabInput struct {
	Tab string `json:"tab,omitempty" jsonschema:"Tab ID. If omitted uses the active tab."`
}

// SelectorInput is embedded by tool inputs that involve CSS selector polling
// with a configurable timeout. It extends TabInput with a Timeout field.
type SelectorInput struct {
	TabInput
	Timeout int `json:"timeout,omitempty" jsonschema:"Max time in milliseconds to wait for selectors (default 5000). Set lower for elements known to be present."`
}

// defaultSelectorTimeout is the default timeout for selector-based chromedp
// actions. chromedp polls with a 5ms retry loop until the element appears or
// the context times out. This default gives dynamic elements a reasonable
// window to appear while keeping failures bounded.
const defaultSelectorTimeout = 5 * time.Second

// defaultLifecycleTimeout is the maximum time to wait for a navigation
// lifecycle event (e.g. networkIdle, DOMContentLoaded) after the initial
// page load. Pages with persistent WebSocket connections or long-polling
// may never reach networkIdle, so this prevents indefinite hangs.
const defaultLifecycleTimeout = 30 * time.Second

// tabContext creates a child of the tab's context (tctx) that is also
// cancelled when the MCP request context (reqCtx) is done. This ensures
// that if the MCP client disconnects or cancels a request, in-flight
// CDP operations are terminated. The returned cancel must always be called.
//
// The child is derived from tctx (not reqCtx) because chromedp stores
// internal target state keyed by context — the context hierarchy must
// descend from the tab's context for chromedp.Run to work.
func tabContext(reqCtx, tctx context.Context) (context.Context, context.CancelFunc) {
	child, cancel := context.WithCancelCause(tctx)
	stop := context.AfterFunc(reqCtx, func() {
		cancel(context.Cause(reqCtx))
	})
	return child, func() {
		stop()
		cancel(nil)
	}
}

// selectorContext returns a context bounded by the user-specified timeout
// (in milliseconds) or the default selector timeout.
func selectorContext(ctx context.Context, timeoutMs int) (context.Context, context.CancelFunc) {
	d := defaultSelectorTimeout
	if timeoutMs > 0 {
		d = time.Duration(timeoutMs) * time.Millisecond
	}
	return context.WithTimeout(ctx, d)
}

// selectorError wraps a selector timeout error with a more descriptive
// message. When the error is a context deadline exceeded, it checks the
// DOM to distinguish "element not found" from "element exists but not
// visible". parentCtx must be the tab's context (not the timed-out one).
func selectorError(parentCtx context.Context, selector string, err error) error {
	if err == nil {
		return nil
	}
	if ctx_err := context.Cause(parentCtx); ctx_err != nil {
		// Parent context is dead (browser killed, etc.) — return as-is.
		return err
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	// The selector timed out. Check if the element exists in the DOM.
	// We use JS document.querySelector here rather than CDP DOM.querySelector
	// because DOM.getDocument() invalidates chromedp's internal DOM tracking,
	// which can break subsequent operations on the same tab.
	checkCtx, cancel := context.WithTimeout(parentCtx, 500*time.Millisecond)
	defer cancel()
	js := fmt.Sprintf("document.querySelector(%s) !== null", jsQuote(selector))
	exists, evalErr := chromedp.Run(checkCtx, chromedp.Evaluate[bool](js))
	if evalErr != nil {
		return err // Can't check, return original error.
	}
	if exists {
		return fmt.Errorf("element %q exists but is not visible (timed out waiting for it to become visible)", selector)
	}
	return fmt.Errorf("element %q not found in the DOM (timed out waiting for it to appear)", selector)
}

// cleanupContext derives a short bounded context for deferred CDP teardown
// calls (e.g., ReleaseObject, Profiler.disable, CSS.disable) that must still
// run even if the caller's request or selector context expired. chromedp still
// short-circuits immediately if the target detaches or the browser dies.
func cleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
}

var jsLineSepReplacer = strings.NewReplacer(
	"\u2028", `\u2028`,
	"\u2029", `\u2029`,
)

// jsQuote encodes s as a double-quoted JavaScript string literal.
// Unlike Go's %q (which emits Go-only escapes like \a, \v, \xNN, \UXXXXXXXX),
// JSON encoding plus U+2028/U+2029 escaping is valid across all JS engines.
func jsQuote(s string) string {
	b, _ := json.Marshal(s)
	return jsLineSepReplacer.Replace(string(b))
}
