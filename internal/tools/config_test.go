package tools

import (
	"strings"
	"testing"
)

func TestAddAndRemoveScript(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	out := callTool[configurePageOutput](t, "configure_page", map[string]any{
		"action":       "add_script",
		"tab":          tabID,
		"source":       "window.__injected = 'hello from injected script';",
		"evaluate_now": false,
	})
	if out.Identifier == "" {
		t.Fatal("expected non-empty script identifier")
	}
	if out.Warning != "" {
		t.Errorf("Warning = %q, want empty for evaluate_now=false", out.Warning)
	}

	type evalResult struct {
		Result string `json:"result"`
	}
	pre := callTool[evalResult](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "window.__injected || 'not set'",
	})
	if pre.Result != "not set" {
		t.Fatalf("evaluate_now=false should not run on current doc; got %q", pre.Result)
	}

	callTool[struct{}](t, "navigate", map[string]any{
		"tab": tabID,
		"url": fixtureURL(t, "config.html"),
	})

	result := callTool[evalResult](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "window.__injected || 'not set'",
	})
	if result.Result != "hello from injected script" {
		t.Fatalf("expected injected value, got %q", result.Result)
	}

	callTool[configurePageOutput](t, "configure_page", map[string]any{
		"action":     "remove_script",
		"tab":        tabID,
		"identifier": out.Identifier,
	})

	callTool[struct{}](t, "navigate", map[string]any{
		"tab": tabID,
		"url": fixtureURL(t, "index.html"),
	})

	result = callTool[evalResult](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "window.__injected || 'not set'",
	})
	if result.Result != "not set" {
		t.Fatalf("expected script removed, but got %q", result.Result)
	}
}

func TestAddScriptEvaluateNowRunsOnCurrentDoc(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	out := callTool[configurePageOutput](t, "configure_page", map[string]any{
		"action":       "add_script",
		"tab":          tabID,
		"source":       "window.__pingedAt = Date.now();",
		"evaluate_now": true,
	})
	if out.Identifier == "" || out.Warning != "" {
		t.Fatalf("unexpected add_script output: %+v", out)
	}

	type evalResult struct {
		Result string `json:"result"`
	}
	res := callTool[evalResult](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "typeof window.__pingedAt === 'number' ? 'set' : 'not set'",
	})
	if res.Result != "set" {
		t.Errorf("evaluate_now=true should run on current doc; got %q", res.Result)
	}

	callTool[configurePageOutput](t, "configure_page", map[string]any{
		"action":     "remove_script",
		"tab":        tabID,
		"identifier": out.Identifier,
	})
}

func TestAddScriptEvaluateNowFailureKeepsRegistration(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	src := `
		if (location.pathname.indexOf('page2') === -1) {
			throw new Error('not page2: ' + location.pathname);
		}
		window.__landedOnPage2 = true;
	`
	out := callTool[configurePageOutput](t, "configure_page", map[string]any{
		"action":       "add_script",
		"tab":          tabID,
		"source":       src,
		"evaluate_now": true,
	})
	if out.Identifier == "" {
		t.Fatal("registration should not be rolled back on current-doc eval failure")
	}
	if out.Warning == "" {
		t.Error("Warning must be set when current-doc evaluation fails")
	}

	callTool[struct{}](t, "navigate", map[string]any{
		"tab": tabID,
		"url": fixtureURL(t, "page2.html"),
	})

	type evalResult struct {
		Result string `json:"result"`
	}
	res := callTool[evalResult](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "window.__landedOnPage2 === true ? 'set' : 'not set'",
	})
	if res.Result != "set" {
		t.Errorf("after navigation, registration should have run on Page 2; got %q", res.Result)
	}

	callTool[configurePageOutput](t, "configure_page", map[string]any{
		"action":     "remove_script",
		"tab":        tabID,
		"identifier": out.Identifier,
	})
}

func TestAddScriptEvaluateNowAwaitsPromiseRejection(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	out := callTool[configurePageOutput](t, "configure_page", map[string]any{
		"action":       "add_script",
		"tab":          tabID,
		"source":       "(async () => { throw new Error('async-failure'); })()",
		"evaluate_now": true,
	})
	if out.Identifier == "" || out.Warning == "" || !strings.Contains(out.Warning, "async-failure") {
		t.Fatalf("unexpected async failure output: %+v", out)
	}

	callTool[configurePageOutput](t, "configure_page", map[string]any{
		"action":     "remove_script",
		"tab":        tabID,
		"identifier": out.Identifier,
	})
}

func TestAddScriptParseErrorRejectsEarly(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	for _, evalNow := range []bool{false, true} {
		errText := callToolExpectErr(t, "configure_page", map[string]any{
			"action":       "add_script",
			"tab":          tabID,
			"source":       "var x = 'unterminated;",
			"evaluate_now": evalNow,
		})
		if !strings.Contains(strings.ToLower(errText), "parse") && !strings.Contains(errText, "SyntaxError") {
			t.Errorf("evaluate_now=%v: error %q should mention 'parse' or 'SyntaxError'", evalNow, errText)
		}
	}

	callTool[struct{}](t, "navigate", map[string]any{
		"tab": tabID,
		"url": fixtureURL(t, "page2.html"),
	})
	logs := callTool[getJSErrorsOutput](t, "get_js_errors", map[string]any{
		"tab":  tabID,
		"mode": "drain",
	})
	for _, e := range logs.Errors {
		if strings.Contains(e.Message, "unterminated") || strings.Contains(e.Message, "SyntaxError") {
			t.Errorf("orphan registration ran on new doc: %s", e.Message)
		}
	}
}

func TestSetExtraHeadersAndRFC9110Validation(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	callTool[configurePageOutput](t, "configure_page", map[string]any{
		"action": "set_headers",
		"tab":    tabID,
		"headers": map[string]string{
			"X-Custom-Auth":  "Bearer test-token-123",
			"X-Feature-Flag": "dark-mode-enabled",
		},
	})

	type evalResult struct {
		Result string `json:"result"`
	}
	result := callTool[evalResult](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "fetch('/api/headers').then(r => r.text())",
	})
	if !strings.Contains(result.Result, "Bearer test-token-123") || !strings.Contains(result.Result, "dark-mode-enabled") {
		t.Fatalf("expected custom headers in response, got: %s", result.Result)
	}

	// Clear headers with empty map {}.
	callTool[configurePageOutput](t, "configure_page", map[string]any{
		"action":  "set_headers",
		"tab":     tabID,
		"headers": map[string]string{},
	})
	result = callTool[evalResult](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "fetch('/api/headers').then(r => r.text())",
	})
	if strings.Contains(result.Result, "Bearer test-token-123") {
		t.Fatal("expected custom header to be cleared")
	}

	// RFC 9110 header validation rejects bad names, CRLF/NUL values, and case duplicates.
	badHeaders := []struct {
		name    string
		headers map[string]string
		want    string
	}{
		{"bad_name_space", map[string]string{"Bad Name": "1"}, "invalid header name"},
		{"crlf_value", map[string]string{"X-Test": "a\r\nX-Inject: b"}, "invalid header value"},
		{"nul_value", map[string]string{"X-Test": "a\x00b"}, "invalid header value"},
		{"case_duplicate", map[string]string{"X-Foo": "1", "x-foo": "2"}, "duplicate header names"},
	}
	for _, tc := range badHeaders {
		t.Run(tc.name, func(t *testing.T) {
			errText := callToolExpectErr(t, "configure_page", map[string]any{
				"action":  "set_headers",
				"tab":     tabID,
				"headers": tc.headers,
			})
			if !strings.Contains(errText, tc.want) {
				t.Errorf("%s err = %q, want substring %q", tc.name, errText, tc.want)
			}
		})
	}
}

func TestSetPermission(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	callTool[configurePageOutput](t, "configure_page", map[string]any{
		"action":  "set_permission",
		"tab":     tabID,
		"name":    "geolocation",
		"setting": "granted",
	})

	type evalResult struct {
		Result string `json:"result"`
	}
	result := callTool[evalResult](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "navigator.permissions.query({name:'geolocation'}).then(p => p.state)",
	})
	if result.Result != "granted" {
		t.Fatalf("expected geolocation granted, got %q", result.Result)
	}

	callTool[configurePageOutput](t, "configure_page", map[string]any{
		"action":  "set_permission",
		"tab":     tabID,
		"name":    "geolocation",
		"setting": "denied",
	})
	result = callTool[evalResult](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "navigator.permissions.query({name:'geolocation'}).then(p => p.state)",
	})
	if result.Result != "denied" {
		t.Fatalf("expected geolocation denied, got %q", result.Result)
	}
}

func TestSetIgnoreCertificateErrors(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	callTool[configurePageOutput](t, "configure_page", map[string]any{
		"action": "set_ignore_cert_errors",
		"tab":    tabID,
		"ignore": true,
	})
	callTool[configurePageOutput](t, "configure_page", map[string]any{
		"action": "set_ignore_cert_errors",
		"tab":    tabID,
		"ignore": false,
	})
}

func TestConfigurePageValidation(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	badCases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"add_script_missing_source", map[string]any{"action": "add_script", "tab": tabID, "evaluate_now": false}, "source"},
		{"add_script_missing_eval_now", map[string]any{"action": "add_script", "tab": tabID, "source": "1"}, "evaluate_now"},
		{"remove_script_empty_id", map[string]any{"action": "remove_script", "tab": tabID, "identifier": ""}, "identifier"},
		{"set_headers_missing", map[string]any{"action": "set_headers", "tab": tabID}, "headers"},
		{"set_permission_empty_name", map[string]any{"action": "set_permission", "tab": tabID, "name": "", "setting": "granted"}, "permission name"},
		{"set_permission_bad_setting", map[string]any{"action": "set_permission", "tab": tabID, "name": "geolocation", "setting": "invalid"}, "invalid"},
		{"set_ignore_missing", map[string]any{"action": "set_ignore_cert_errors", "tab": tabID}, "ignore"},
		{"cross_field_headers_on_script", map[string]any{"action": "add_script", "tab": tabID, "source": "1", "evaluate_now": false, "headers": map[string]string{}}, "does not accept"},
		{"cross_field_ignore_on_perm", map[string]any{"action": "set_permission", "tab": tabID, "name": "geolocation", "setting": "granted", "ignore": true}, "does not accept"},
	}
	for _, tc := range badCases {
		t.Run(tc.name, func(t *testing.T) {
			errText := callToolExpectErr(t, "configure_page", tc.args)
			if !strings.Contains(errText, tc.want) {
				t.Errorf("%s err = %q, want substring %q", tc.name, errText, tc.want)
			}
		})
	}
}
