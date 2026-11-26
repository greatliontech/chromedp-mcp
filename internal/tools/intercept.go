package tools

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"

	"github.com/chromedp/cdproto/network"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/browser"
	"github.com/greatliontech/chromedp-mcp/internal/collector"
)

const (
	interceptOpRemove = "remove"
	interceptOpList   = "list"
	interceptOpClear  = "clear"
)

// interceptInput is the input for intercept.
type interceptInput struct {
	tabInput
	Action string `json:"action" jsonschema:"Interception action: 'fulfill' (add mock response rule), 'modify' (add request rewrite rule), 'fail' (add request block/abort rule), 'remove' (remove one rule by id), 'list' (list active rules), or 'clear' (remove all rules). Required."`

	// Rule ID (for action='remove')
	ID string `json:"id,omitempty" jsonschema:"Rule ID to remove (required for action='remove')."`

	// Match criteria (for action='fulfill'|'modify'|'fail')
	URLPattern   string `json:"url_pattern,omitempty" jsonschema:"Glob pattern matching request URLs ('*' matches any characters including '/', '?' matches one char, '\\' escapes; e.g. '*/api/*', '*.png', '*'). Required for fulfill/modify/fail."`
	Method       string `json:"method,omitempty" jsonschema:"Optional HTTP method filter (case-insensitive, e.g. GET, POST)."`
	ResourceType string `json:"resource_type,omitempty" jsonschema:"Optional CDP resource type filter (e.g. xhr, document, script, image, stylesheet; Chrome's Fetch domain reports 'xhr' for both fetch() and XMLHttpRequest)."`

	// Fulfill fields
	Status     int    `json:"status,omitempty" jsonschema:"HTTP status code for action='fulfill' (100-599, default 200)."`
	Body       string `json:"body,omitempty" jsonschema:"Response body for action='fulfill' (UTF-8 text, or base64 when body_base64=true)."`
	BodyBase64 bool   `json:"body_base64,omitempty" jsonschema:"When true, decode 'body' from base64 as raw response bytes (action='fulfill' only)."`

	// Shared headers field (response headers for fulfill, request header overrides for modify)
	Headers map[string]string `json:"headers,omitempty" jsonschema:"Response headers for action='fulfill', or request header overrides merged case-insensitively onto original headers for action='modify'."`

	// Modify fields
	URL            string `json:"url,omitempty" jsonschema:"Replacement absolute URL for action='modify'."`
	OverrideMethod string `json:"override_method,omitempty" jsonschema:"Replacement HTTP method for action='modify'."`
	PostData       string `json:"post_data,omitempty" jsonschema:"Replacement request body for action='modify' (UTF-8 text, or base64 when post_data_base64=true)."`
	PostDataBase64 bool   `json:"post_data_base64,omitempty" jsonschema:"When true, decode 'post_data' from base64 as raw request bytes (action='modify' only)."`

	// Fail field
	ErrorReason string `json:"error_reason,omitempty" jsonschema:"CDP network error reason for action='fail' (default 'Failed'; e.g. 'BlockedByClient' to block URLs)."`
}

// interceptOutput is the output for intercept.
type interceptOutput struct {
	ID      string                         `json:"id,omitempty"`
	Rules   *[]collector.InterceptRuleInfo `json:"rules,omitempty"`
	Removed *int                           `json:"removed,omitempty"`
}

var (
	interceptToolActionValues = []string{
		string(collector.InterceptActionFulfill),
		string(collector.InterceptActionModify),
		string(collector.InterceptActionFail),
		interceptOpRemove,
		interceptOpList,
		interceptOpClear,
	}
	interceptErrorReasonValues = []string{
		string(network.ErrorReasonFailed),
		string(network.ErrorReasonAborted),
		string(network.ErrorReasonTimedOut),
		string(network.ErrorReasonAccessDenied),
		string(network.ErrorReasonConnectionClosed),
		string(network.ErrorReasonConnectionReset),
		string(network.ErrorReasonConnectionRefused),
		string(network.ErrorReasonConnectionAborted),
		string(network.ErrorReasonConnectionFailed),
		string(network.ErrorReasonNameNotResolved),
		string(network.ErrorReasonInternetDisconnected),
		string(network.ErrorReasonAddressUnreachable),
		string(network.ErrorReasonBlockedByClient),
		string(network.ErrorReasonBlockedByResponse),
	}
)

// isHTTPToken reports whether s is a valid RFC 9110 HTTP token (header name or method).
func isHTTPToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			continue
		}
		switch c {
		case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
			continue
		default:
			return false
		}
	}
	return true
}

// isValidHeaderValue rejects control bytes (CR, LF, NUL, DEL, and CTLs other
// than HTAB) that Chromium's Fetch handler rejects at runtime.
func isValidHeaderValue(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 0x20 && c != '\t') || c == 0x7f {
			return false
		}
	}
	return true
}

// validateHTTPHeaders validates header names (RFC 9110 tokens), values (no
// CR/LF/NUL/control bytes), and case-insensitive uniqueness.
func validateHTTPHeaders(headers map[string]string) error {
	seenHeaders := make(map[string]string, len(headers))
	for k, v := range headers {
		if !isHTTPToken(k) {
			return fmt.Errorf("invalid header name %q: must be a non-empty HTTP token", k)
		}
		if !isValidHeaderValue(v) {
			return fmt.Errorf("invalid header value for %q: control characters (CR/LF/NUL) are not allowed", k)
		}
		lower := strings.ToLower(k)
		if prev, dup := seenHeaders[lower]; dup {
			return fmt.Errorf("duplicate header names differing only in case: %q and %q", prev, k)
		}
		seenHeaders[lower] = k
	}
	return nil
}

func isBodylessStatus(status int) bool {
	return (status >= 100 && status <= 199) || status == 204 || status == 205 || status == 304
}

func hasInterceptRuleFields(input interceptInput) bool {
	return input.URLPattern != "" ||
		input.Method != "" ||
		input.ResourceType != "" ||
		input.Status != 0 ||
		input.Body != "" ||
		input.BodyBase64 ||
		input.Headers != nil ||
		input.URL != "" ||
		input.OverrideMethod != "" ||
		input.PostData != "" ||
		input.PostDataBase64 ||
		input.ErrorReason != ""
}

func buildInterceptRule(input interceptInput) (collector.InterceptRule, error) {
	if input.ID != "" {
		return collector.InterceptRule{}, fmt.Errorf("id is only valid when action is 'remove'")
	}
	if input.URLPattern == "" {
		return collector.InterceptRule{}, fmt.Errorf("url_pattern is required")
	}
	if err := validateEnum("resource_type", input.ResourceType, true, networkResourceTypeValues); err != nil {
		return collector.InterceptRule{}, err
	}
	if err := validateEnum("error_reason", input.ErrorReason, true, interceptErrorReasonValues); err != nil {
		return collector.InterceptRule{}, err
	}
	if err := validateHTTPHeaders(input.Headers); err != nil {
		return collector.InterceptRule{}, err
	}

	rule := collector.InterceptRule{
		URLPattern:   input.URLPattern,
		Method:       strings.TrimSpace(input.Method),
		ResourceType: strings.TrimSpace(input.ResourceType),
		Action:       collector.InterceptAction(input.Action),
	}

	switch rule.Action {
	case collector.InterceptActionFulfill:
		if input.URL != "" || input.OverrideMethod != "" || input.PostData != "" || input.PostDataBase64 || input.ErrorReason != "" {
			return collector.InterceptRule{}, fmt.Errorf("action='fulfill' does not accept url, override_method, post_data, post_data_base64, or error_reason")
		}
		status := input.Status
		if status == 0 {
			status = 200
		}
		if status < 100 || status > 599 {
			return collector.InterceptRule{}, fmt.Errorf("status must be between 100 and 599, got %d", input.Status)
		}
		rule.Status = status
		rule.Headers = input.Headers
		if input.BodyBase64 {
			decoded, err := base64.StdEncoding.DecodeString(input.Body)
			if err != nil {
				return collector.InterceptRule{}, fmt.Errorf("invalid base64 body: %w", err)
			}
			rule.Body = decoded
		} else if input.Body != "" {
			rule.Body = []byte(input.Body)
		}
		if len(rule.Body) > 0 && isBodylessStatus(status) {
			return collector.InterceptRule{}, fmt.Errorf("status %d forbids a non-empty response body", status)
		}

	case collector.InterceptActionModify:
		if input.Status != 0 || input.Body != "" || input.BodyBase64 || input.ErrorReason != "" {
			return collector.InterceptRule{}, fmt.Errorf("action='modify' does not accept status, body, body_base64, or error_reason")
		}
		overrideMethod := strings.TrimSpace(input.OverrideMethod)
		if input.OverrideMethod != "" && !isHTTPToken(overrideMethod) {
			return collector.InterceptRule{}, fmt.Errorf("invalid override_method %q: must be a valid HTTP method token", input.OverrideMethod)
		}
		if input.PostDataBase64 && input.PostData == "" {
			return collector.InterceptRule{}, fmt.Errorf("post_data must be non-empty when post_data_base64 is true")
		}
		if input.URL == "" && overrideMethod == "" && len(input.Headers) == 0 && input.PostData == "" {
			return collector.InterceptRule{}, fmt.Errorf("action='modify' requires at least one of url, override_method, headers, or post_data")
		}
		if input.URL != "" {
			u, err := url.Parse(input.URL)
			if err != nil || u.Scheme == "" || (u.Host == "" && u.Scheme != "data" && u.Scheme != "about") {
				return collector.InterceptRule{}, fmt.Errorf("invalid replacement url %q: must be an absolute URL", input.URL)
			}
			rule.URL = input.URL
		}
		rule.OverrideMethod = overrideMethod
		rule.Headers = input.Headers
		if input.PostDataBase64 {
			decoded, err := base64.StdEncoding.DecodeString(input.PostData)
			if err != nil {
				return collector.InterceptRule{}, fmt.Errorf("invalid base64 post_data: %w", err)
			}
			if len(decoded) == 0 {
				return collector.InterceptRule{}, fmt.Errorf("post_data decoded to empty bytes; CDP Fetch.continueRequest omits empty postData")
			}
			rule.PostData = decoded
		} else if input.PostData != "" {
			rule.PostData = []byte(input.PostData)
		}

	case collector.InterceptActionFail:
		if input.Status != 0 || input.Body != "" || input.BodyBase64 || input.URL != "" || input.OverrideMethod != "" || input.Headers != nil || input.PostData != "" || input.PostDataBase64 {
			return collector.InterceptRule{}, fmt.Errorf("action='fail' only accepts error_reason (not status, body, headers, url, override_method, or post_data)")
		}
		if input.ErrorReason == "" {
			rule.ErrorReason = network.ErrorReasonFailed
		} else {
			rule.ErrorReason = network.ErrorReason(input.ErrorReason)
		}
	}

	return rule, nil
}

func registerInterceptTools(s *mcp.Server, mgr *browser.Manager) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "intercept",
		Description: "Manage network request interception rules on a tab (via CDP Fetch domain). Add rules with action='fulfill' (mock), 'modify' (rewrite), or 'fail' (block/abort); remove one with 'remove'; inspect with 'list'; or remove all with 'clear'. Rules evaluate in insertion order (first match wins); Fetch interception enables lazily on the first rule and disables automatically when none remain.",
		InputSchema: schemaWithEnums[interceptInput](map[string][]string{
			"action":        interceptToolActionValues,
			"resource_type": networkResourceTypeValues,
			"error_reason":  interceptErrorReasonValues,
		}),
		Annotations: destructiveAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input interceptInput) (*mcp.CallToolResult, interceptOutput, error) {
		if err := validateEnum("action", input.Action, false, interceptToolActionValues); err != nil {
			return nil, interceptOutput{}, err
		}

		switch input.Action {
		case interceptOpRemove:
			if input.ID == "" {
				return nil, interceptOutput{}, fmt.Errorf("rule id is required for action='remove'")
			}
			if hasInterceptRuleFields(input) {
				return nil, interceptOutput{}, fmt.Errorf("action='remove' only accepts id (not rule definition fields)")
			}
			t, err := mgr.ResolveTab("", input.Tab)
			if err != nil {
				return nil, interceptOutput{}, err
			}
			if err := t.RemoveInterceptRule(ctx, input.ID); err != nil {
				return nil, interceptOutput{}, err
			}
			return nil, interceptOutput{}, nil

		case interceptOpList:
			if input.ID != "" || hasInterceptRuleFields(input) {
				return nil, interceptOutput{}, fmt.Errorf("action='list' does not accept id or rule definition fields")
			}
			t, err := mgr.ResolveTab("", input.Tab)
			if err != nil {
				return nil, interceptOutput{}, err
			}
			rules := t.ListInterceptRules()
			if rules == nil {
				rules = []collector.InterceptRuleInfo{}
			}
			return nil, interceptOutput{Rules: &rules}, nil

		case interceptOpClear:
			if input.ID != "" || hasInterceptRuleFields(input) {
				return nil, interceptOutput{}, fmt.Errorf("action='clear' does not accept id or rule definition fields")
			}
			t, err := mgr.ResolveTab("", input.Tab)
			if err != nil {
				return nil, interceptOutput{}, err
			}
			removed, err := t.ClearInterceptRules(ctx)
			if err != nil {
				return nil, interceptOutput{}, err
			}
			return nil, interceptOutput{Removed: &removed}, nil

		default:
			rule, err := buildInterceptRule(input)
			if err != nil {
				return nil, interceptOutput{}, err
			}
			t, err := mgr.ResolveTab("", input.Tab)
			if err != nil {
				return nil, interceptOutput{}, err
			}
			ruleID, err := t.AddInterceptRule(ctx, rule)
			if err != nil {
				return nil, interceptOutput{}, err
			}
			return nil, interceptOutput{ID: ruleID}, nil
		}
	})
}
