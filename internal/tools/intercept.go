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

// AddInterceptRuleInput is the input for add_intercept_rule.
type AddInterceptRuleInput struct {
	TabInput
	URLPattern   string `json:"url_pattern" jsonschema:"Glob pattern matching request URLs ('*' matches any characters including '/', '?' matches any single character, '\\' escapes; e.g. '*/api/*', '*.png', '*'). Required."`
	Method       string `json:"method,omitempty" jsonschema:"Optional HTTP method filter (case-insensitive, e.g. GET, POST)."`
	ResourceType string `json:"resource_type,omitempty" jsonschema:"Optional CDP resource type filter (case-insensitive, e.g. XHR, Document, Script, Image, Stylesheet; note Chrome's Fetch domain reports 'XHR' for both fetch() and XMLHttpRequest)."`
	Action       string `json:"action" jsonschema:"Action to apply to matching requests: 'fulfill' (mock response), 'modify' (rewrite request before forwarding), or 'fail' (abort with network error). Required."`

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
	ErrorReason string `json:"error_reason,omitempty" jsonschema:"CDP network error reason for action='fail' (default 'Failed')."`
}

// AddInterceptRuleOutput is the output for add_intercept_rule.
type AddInterceptRuleOutput struct {
	ID string `json:"id"`
}

// RemoveInterceptRuleInput is the input for remove_intercept_rule.
type RemoveInterceptRuleInput struct {
	TabInput
	ID string `json:"id" jsonschema:"Rule ID returned by add_intercept_rule"`
}

// ListInterceptRulesInput is the input for list_intercept_rules.
type ListInterceptRulesInput struct {
	TabInput
}

// ListInterceptRulesOutput is the output for list_intercept_rules.
type ListInterceptRulesOutput struct {
	Rules []collector.InterceptRuleInfo `json:"rules"`
}

// ClearInterceptRulesInput is the input for clear_intercept_rules.
type ClearInterceptRulesInput struct {
	TabInput
}

// ClearInterceptRulesOutput is the output for clear_intercept_rules.
type ClearInterceptRulesOutput struct {
	Removed int `json:"removed"`
}

var (
	interceptActionValues = []string{
		string(collector.InterceptActionFulfill),
		string(collector.InterceptActionModify),
		string(collector.InterceptActionFail),
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

func isBodylessStatus(status int) bool {
	return (status >= 100 && status <= 199) || status == 204 || status == 205 || status == 304
}

func buildInterceptRule(input AddInterceptRuleInput) (collector.InterceptRule, error) {
	if input.URLPattern == "" {
		return collector.InterceptRule{}, fmt.Errorf("url_pattern is required")
	}
	if err := validateEnum("action", input.Action, false, interceptActionValues); err != nil {
		return collector.InterceptRule{}, err
	}
	if err := validateEnum("error_reason", input.ErrorReason, true, interceptErrorReasonValues); err != nil {
		return collector.InterceptRule{}, err
	}
	seenHeaders := make(map[string]string, len(input.Headers))
	for k, v := range input.Headers {
		if !isHTTPToken(k) {
			return collector.InterceptRule{}, fmt.Errorf("invalid header name %q: must be a non-empty HTTP token", k)
		}
		if !isValidHeaderValue(v) {
			return collector.InterceptRule{}, fmt.Errorf("invalid header value for %q: control characters (CR/LF/NUL) are not allowed", k)
		}
		lower := strings.ToLower(k)
		if prev, dup := seenHeaders[lower]; dup {
			return collector.InterceptRule{}, fmt.Errorf("duplicate header names differing only in case: %q and %q", prev, k)
		}
		seenHeaders[lower] = k
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
		if input.Status != 0 || input.Body != "" || input.BodyBase64 || input.URL != "" || input.OverrideMethod != "" || len(input.Headers) > 0 || input.PostData != "" || input.PostDataBase64 {
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
		Name:        "add_intercept_rule",
		Description: "Add a network request interception rule to a tab (via CDP Fetch domain). Rules are evaluated in insertion order (first match wins); non-matching requests pass through untouched. Lazily enables Fetch interception on the first rule.",
		InputSchema: schemaWithEnums[AddInterceptRuleInput](map[string][]string{
			"action":       interceptActionValues,
			"error_reason": interceptErrorReasonValues,
		}),
		Annotations: mutatingAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input AddInterceptRuleInput) (*mcp.CallToolResult, AddInterceptRuleOutput, error) {
		rule, err := buildInterceptRule(input)
		if err != nil {
			return nil, AddInterceptRuleOutput{}, err
		}
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, AddInterceptRuleOutput{}, err
		}
		ruleID, err := t.AddInterceptRule(ctx, rule)
		if err != nil {
			return nil, AddInterceptRuleOutput{}, err
		}
		return nil, AddInterceptRuleOutput{ID: ruleID}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "remove_intercept_rule",
		Description: "Remove a network request interception rule by ID. Disables CDP Fetch interception automatically when the last rule is removed.",
		Annotations: destructiveAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input RemoveInterceptRuleInput) (*mcp.CallToolResult, struct{}, error) {
		if input.ID == "" {
			return nil, struct{}{}, fmt.Errorf("rule id is required")
		}
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}
		if err := t.RemoveInterceptRule(ctx, input.ID); err != nil {
			return nil, struct{}{}, err
		}
		return nil, struct{}{}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_intercept_rules",
		Description: "List active network request interception rules on a tab in evaluation order, including match counts.",
		Annotations: readOnlyAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ListInterceptRulesInput) (*mcp.CallToolResult, ListInterceptRulesOutput, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, ListInterceptRulesOutput{}, err
		}
		rules := t.ListInterceptRules()
		if rules == nil {
			rules = []collector.InterceptRuleInfo{}
		}
		return nil, ListInterceptRulesOutput{Rules: rules}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "clear_intercept_rules",
		Description: "Remove all network request interception rules on a tab and disable CDP Fetch interception.",
		Annotations: idempotentWriteAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ClearInterceptRulesInput) (*mcp.CallToolResult, ClearInterceptRulesOutput, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, ClearInterceptRulesOutput{}, err
		}
		removed, err := t.ClearInterceptRules(ctx)
		if err != nil {
			return nil, ClearInterceptRulesOutput{}, err
		}
		return nil, ClearInterceptRulesOutput{Removed: removed}, nil
	})
}
