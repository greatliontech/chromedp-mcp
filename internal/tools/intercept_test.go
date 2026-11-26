package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/chromedp"
)

func attachPausedCounter(t *testing.T, tabID string) (*atomic.Int64, func()) {
	t.Helper()
	tb, err := harness.mgr.ResolveTab("", tabID)
	if err != nil {
		t.Fatalf("ResolveTab(%q): %v", tabID, err)
	}
	listenCtx, cancel := context.WithCancel(tb.Context())
	var count atomic.Int64
	if err := chromedp.Listen(listenCtx, func(ev any) {
		if _, ok := ev.(*fetch.EventRequestPaused); ok {
			count.Add(1)
		}
	}); err != nil {
		cancel()
		t.Fatalf("chromedp.Listen: %v", err)
	}
	return &count, cancel
}

func TestInterceptFulfillTextBinaryAndPassThrough(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	pausedCount, stopListen := attachPausedCounter(t, tabID)
	defer stopListen()

	// Empty list must serialize as {"rules":[]}, never null.
	rawList := callToolRaw(t, "intercept", map[string]any{"tab": tabID, "action": "list"})
	if rawList.IsError {
		t.Fatalf("intercept(list) error: %s", contentText(rawList))
	}
	if !strings.Contains(contentText(rawList), `"rules":[]`) {
		t.Fatalf("intercept(list) empty JSON = %s, want \"rules\":[]", contentText(rawList))
	}

	// Rule 1: mock JSON API response.
	r1 := callTool[interceptOutput](t, "intercept", map[string]any{
		"tab":         tabID,
		"url_pattern": "*/api/mock",
		"action":      "fulfill",
		"status":      201,
		"headers": map[string]string{
			"Content-Type": "application/json",
			"X-Mock":       "yes",
		},
		"body": `{"mocked":true}`,
	})
	if r1.ID == "" {
		t.Fatal("intercept(fulfill) returned empty ID")
	}

	// Rule 2: mock binary response via body_base64.
	wantBytes := []byte{0x00, 0x01, 0xfe, 0xff}
	r2 := callTool[interceptOutput](t, "intercept", map[string]any{
		"tab":         tabID,
		"url_pattern": "*/api/bin",
		"action":      "fulfill",
		"headers":     map[string]string{"Content-Type": "application/octet-stream"},
		"body":        base64.StdEncoding.EncodeToString(wantBytes),
		"body_base64": true,
	})

	// Rule 3: valid 204 No Content with empty body must succeed.
	r3 := callTool[interceptOutput](t, "intercept", map[string]any{
		"tab":         tabID,
		"url_pattern": "*/api/empty",
		"action":      "fulfill",
		"status":      204,
	})

	// clear_buffers must NOT disturb active intercept rules (configuration, not event buffers).
	callTool[clearBuffersOutput](t, "clear_buffers", map[string]any{"tab": tabID, "targets": []string{"all"}})

	// Fetch mocked JSON endpoint.
	ev := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": `(async function() {
			const r = await fetch('/api/mock');
			return {status: r.status, xmock: r.headers.get('X-Mock'), body: await r.json()};
		})()`,
	})
	var mockRes struct {
		Status int    `json:"status"`
		XMock  string `json:"xmock"`
		Body   struct {
			Mocked bool `json:"mocked"`
		} `json:"body"`
	}
	if err := json.Unmarshal(ev.Result, &mockRes); err != nil {
		t.Fatalf("unmarshal mock fetch: %v (raw=%s)", err, ev.Result)
	}
	if mockRes.Status != 201 || mockRes.XMock != "yes" || !mockRes.Body.Mocked {
		t.Fatalf("unexpected mock response: %+v", mockRes)
	}

	// Fetch mocked binary endpoint and verify exact bytes.
	evBin := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": `(async function() {
			const r = await fetch('/api/bin');
			return Array.from(new Uint8Array(await r.arrayBuffer()));
		})()`,
	})
	var gotBytes []int
	if err := json.Unmarshal(evBin.Result, &gotBytes); err != nil {
		t.Fatalf("unmarshal binary fetch: %v (raw=%s)", err, evBin.Result)
	}
	if len(gotBytes) != 4 || gotBytes[0] != 0 || gotBytes[1] != 1 || gotBytes[2] != 254 || gotBytes[3] != 255 {
		t.Fatalf("binary mock bytes = %v, want [0 1 254 255]", gotBytes)
	}

	// Fetch 204 No Content mock.
	evEmpty := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `fetch('/api/empty').then(r => r.status)`,
	})
	if string(evEmpty.Result) != "204" {
		t.Fatalf("204 mock status = %s, want 204", evEmpty.Result)
	}

	// Non-matching request must pass through untouched to the real server.
	evReal := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `fetch('/api/data').then(r => r.json())`,
	})
	if !strings.Contains(string(evReal.Result), `"value":42`) {
		t.Fatalf("non-matching pass-through returned %s, want value:42", evReal.Result)
	}

	if pausedCount.Load() < 4 {
		t.Fatalf("expected >=4 EventRequestPaused events while rules active, got %d", pausedCount.Load())
	}

	// Verify intercept(list) reports order and match counts.
	list := callTool[interceptOutput](t, "intercept", map[string]any{"tab": tabID, "action": "list"})
	if list.Rules == nil || len(*list.Rules) != 3 ||
		(*list.Rules)[0].ID != r1.ID || (*list.Rules)[0].Matches != 1 ||
		(*list.Rules)[1].ID != r2.ID || (*list.Rules)[1].Matches != 1 ||
		(*list.Rules)[2].ID != r3.ID || (*list.Rules)[2].Matches != 1 {
		t.Fatalf("unexpected intercept(list): %+v", list.Rules)
	}

	// Remove r1; /api/mock must now hit the real server (404).
	callTool[interceptOutput](t, "intercept", map[string]any{"tab": tabID, "action": "remove", "id": r1.ID})
	if errText := callToolExpectErr(t, "intercept", map[string]any{"tab": tabID, "action": "remove", "id": r1.ID}); !strings.Contains(errText, "not found") {
		t.Errorf("removing already-removed rule err = %q, want not found", errText)
	}
	evAfterRemove := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `fetch('/api/mock').then(r => r.status)`,
	})
	if string(evAfterRemove.Result) != "404" {
		t.Errorf("after removing r1, /api/mock status = %s, want 404", evAfterRemove.Result)
	}

	// Clear remaining rules and verify idempotency + explicit removed:0 + actual Fetch.disable teardown.
	cleared := callTool[interceptOutput](t, "intercept", map[string]any{"tab": tabID, "action": "clear"})
	if cleared.Removed == nil || *cleared.Removed != 2 {
		t.Errorf("intercept(clear) Removed = %v, want 2", cleared.Removed)
	}
	rawCleared2 := callToolRaw(t, "intercept", map[string]any{"tab": tabID, "action": "clear"})
	if rawCleared2.IsError || !strings.Contains(contentText(rawCleared2), `"removed":0`) {
		t.Errorf("second intercept(clear) raw = %s (err=%v), want \"removed\":0", contentText(rawCleared2), rawCleared2.IsError)
	}

	beforeClearFetch := pausedCount.Load()
	evAfterClear := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `fetch('/api/data').then(r => r.status)`,
	})
	if string(evAfterClear.Result) != "200" {
		t.Fatalf("post-clear fetch status = %s, want 200", evAfterClear.Result)
	}
	if after := pausedCount.Load(); after != beforeClearFetch {
		t.Errorf("Fetch.disable did not stop EventRequestPaused after intercept(clear): count went %d -> %d", beforeClearFetch, after)
	}
}

func TestInterceptModifyPreservesHeadersAndRewritesRequest(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/page", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><html><body><h1>Modify Test</h1></body></html>`))
	})
	echoHandler := func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"path":       r.URL.Path,
			"method":     r.Method,
			"x_injected": r.Header.Get("X-Injected"),
			"x_client":   r.Header.Get("X-Client"),
			"x_keep":     r.Header.Get("X-Keep"),
			"body":       string(body),
			"body_b64":   base64.StdEncoding.EncodeToString(body),
		})
	}
	mux.HandleFunc("/orig", echoHandler)
	mux.HandleFunc("/rewritten", echoHandler)
	mux.HandleFunc("/bin-upload", echoHandler)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)
	callTool[navigateOutput](t, "navigate", map[string]any{"tab": tabID, "url": srv.URL + "/page"})

	pausedCount, stopListen := attachPausedCounter(t, tabID)
	defer stopListen()

	rule := callTool[interceptOutput](t, "intercept", map[string]any{
		"tab":             tabID,
		"url_pattern":     "*/orig",
		"action":          "modify",
		"url":             srv.URL + "/rewritten",
		"override_method": "PUT",
		"headers": map[string]string{
			"X-Injected": "hello",
			"x-client":   "overridden",
		},
		"post_data": "rewritten-body",
	})

	wantBinPost := []byte{0x00, 0x01, 0x7f, 0xfe, 0xff}
	binRule := callTool[interceptOutput](t, "intercept", map[string]any{
		"tab":              tabID,
		"url_pattern":      "*/bin-upload",
		"action":           "modify",
		"post_data":        base64.StdEncoding.EncodeToString(wantBinPost),
		"post_data_base64": true,
	})

	ev := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": `fetch('/orig', {
			method: 'POST',
			headers: {'X-Client': 'orig', 'X-Keep': 'preserved'},
			body: 'orig-body'
		}).then(r => r.json())`,
	})
	var got struct {
		Path      string `json:"path"`
		Method    string `json:"method"`
		XInjected string `json:"x_injected"`
		XClient   string `json:"x_client"`
		XKeep     string `json:"x_keep"`
		Body      string `json:"body"`
		BodyB64   string `json:"body_b64"`
	}
	if err := json.Unmarshal(ev.Result, &got); err != nil {
		t.Fatalf("unmarshal echo response: %v (raw=%s)", err, ev.Result)
	}
	if got.Path != "/rewritten" || got.Method != "PUT" || got.XInjected != "hello" || got.XClient != "overridden" || got.XKeep != "preserved" || got.Body != "rewritten-body" {
		t.Fatalf("modified request echo = %+v, want path=/rewritten method=PUT X-Injected=hello X-Client=overridden X-Keep=preserved body=rewritten-body", got)
	}

	// Verify post_data_base64: true delivers exact decoded binary bytes.
	evBin := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `fetch('/bin-upload', {method: 'POST', body: 'placeholder'}).then(r => r.json())`,
	})
	if err := json.Unmarshal(evBin.Result, &got); err != nil {
		t.Fatalf("unmarshal binary echo response: %v (raw=%s)", err, evBin.Result)
	}
	wantB64 := base64.StdEncoding.EncodeToString(wantBinPost)
	if got.BodyB64 != wantB64 {
		t.Fatalf("post_data_base64 echo body_b64 = %q, want %q", got.BodyB64, wantB64)
	}

	if pausedCount.Load() < 2 {
		t.Fatalf("expected >=2 EventRequestPaused events while modify rules active, got %d", pausedCount.Load())
	}

	// Removing all rules via intercept(remove) disables Fetch cleanly;
	// /orig hits original path unmodified AND emits zero new EventRequestPaused events.
	callTool[interceptOutput](t, "intercept", map[string]any{"tab": tabID, "action": "remove", "id": binRule.ID})
	callTool[interceptOutput](t, "intercept", map[string]any{"tab": tabID, "action": "remove", "id": rule.ID})

	beforeRemoveFetch := pausedCount.Load()
	evAfter := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": `fetch('/orig', {
			method: 'POST',
			headers: {'X-Client': 'orig', 'X-Keep': 'preserved'},
			body: 'orig-body'
		}).then(r => r.json())`,
	})
	if err := json.Unmarshal(evAfter.Result, &got); err != nil {
		t.Fatalf("unmarshal post-remove echo: %v", err)
	}
	if got.Path != "/orig" || got.Method != "POST" || got.XClient != "orig" || got.XInjected != "" || got.Body != "orig-body" {
		t.Fatalf("post-remove request echo = %+v, want unmodified /orig POST", got)
	}
	if after := pausedCount.Load(); after != beforeRemoveFetch {
		t.Errorf("Fetch.disable did not stop EventRequestPaused after removing last rule: count went %d -> %d", beforeRemoveFetch, after)
	}
}

func TestInterceptResourceTypeFilter(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	// Rule 1 targets image only; a fetch() request (reported by Chrome's Fetch
	// domain as resource type "XHR") must bypass it.
	imgRule := callTool[interceptOutput](t, "intercept", map[string]any{
		"tab":           tabID,
		"url_pattern":   "*/api/data",
		"resource_type": "image",
		"action":        "fulfill",
		"body":          "image-mocked",
	})

	evPass := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `fetch('/api/data').then(r => r.json())`,
	})
	if !strings.Contains(string(evPass.Result), `"value":42`) {
		t.Fatalf("fetch() request matched image-only rule; got %s, want real /api/data", evPass.Result)
	}

	// Rule 2 targets xhr; fetch() must now match rule 2, not rule 1.
	xhrRule := callTool[interceptOutput](t, "intercept", map[string]any{
		"tab":           tabID,
		"url_pattern":   "*/api/data",
		"resource_type": "xhr",
		"action":        "fulfill",
		"body":          "xhr-mocked",
	})

	evMatch := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `fetch('/api/data').then(r => r.text())`,
	})
	if string(evMatch.Result) != `"xhr-mocked"` {
		t.Fatalf("fetch() with resource_type=xhr rule returned %s, want \"xhr-mocked\"", evMatch.Result)
	}

	// Trigger an actual Image request to /api/data and confirm it matches rule 1 (image), not rule 2.
	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": `new Promise(resolve => {
			const img = new Image();
			img.onload = () => resolve('load');
			img.onerror = () => resolve('error');
			img.src = '/api/data';
		})`,
	})

	list := callTool[interceptOutput](t, "intercept", map[string]any{"tab": tabID, "action": "list"})
	if list.Rules == nil || len(*list.Rules) != 2 ||
		(*list.Rules)[0].ID != imgRule.ID || (*list.Rules)[0].Matches != 1 ||
		(*list.Rules)[1].ID != xhrRule.ID || (*list.Rules)[1].Matches != 1 {
		t.Fatalf("unexpected resource_type rule match counts: %+v", list.Rules)
	}

	callTool[interceptOutput](t, "intercept", map[string]any{"tab": tabID, "action": "clear"})
}

func TestInterceptFailBlockedByClientAndConcurrentFetches(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	callTool[clearBuffersOutput](t, "clear_buffers", map[string]any{"tab": tabID, "targets": []string{"network"}})

	// Rule 1: fail POST /api/data with TimedOut.
	callTool[interceptOutput](t, "intercept", map[string]any{
		"tab":          tabID,
		"url_pattern":  "*/api/data",
		"method":       "POST",
		"action":       "fail",
		"error_reason": "TimedOut",
	})
	// Rule 2: fulfill GET /api/data with custom body (first-match precedence).
	callTool[interceptOutput](t, "intercept", map[string]any{
		"tab":         tabID,
		"url_pattern": "*/api/data",
		"method":      "GET",
		"action":      "fulfill",
		"body":        "get-mocked",
	})
	// Rule 3: block *.png via BlockedByClient (supersedes removed block_urls).
	callTool[interceptOutput](t, "intercept", map[string]any{
		"tab":          tabID,
		"url_pattern":  "*.png",
		"action":       "fail",
		"error_reason": "BlockedByClient",
	})
	// Rule 4: broader fallback fail that must NOT shadow Rule 2 for GET /api/data.
	callTool[interceptOutput](t, "intercept", map[string]any{
		"tab":         tabID,
		"url_pattern": "*/api/data",
		"action":      "fail",
	})

	// Fire concurrent requests mixing fail, blocked, fulfill, and pass-through to verify
	// async EventRequestPaused dispatch never deadlocks or races.
	ev := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": `(async function() {
			const tasks = [
				fetch('/api/data', {method: 'POST'}).then(() => 'unexpected-ok', () => 'post-failed'),
				fetch('/api/data', {method: 'POST'}).then(() => 'unexpected-ok', () => 'post-failed'),
				fetch('/api/data', {method: 'GET'}).then(r => r.text()),
				fetch('/api/data', {method: 'GET'}).then(r => r.text()),
				fetch('/submit', {method: 'POST'}).then(r => r.text()),
				fetch('/submit', {method: 'POST'}).then(r => r.text()),
				fetch('/page2.html').then(r => r.status),
				fetch('/image.png').then(() => 'unexpected-ok', () => 'png-blocked'),
			];
			return await Promise.all(tasks);
		})()`,
	})
	var results []any
	if err := json.Unmarshal(ev.Result, &results); err != nil {
		t.Fatalf("unmarshal concurrent fetches: %v (raw=%s)", err, ev.Result)
	}
	if len(results) != 8 ||
		results[0] != "post-failed" || results[1] != "post-failed" ||
		results[2] != "get-mocked" || results[3] != "get-mocked" ||
		results[4] != "submitted" || results[5] != "submitted" ||
		results[6] != float64(200) || results[7] != "png-blocked" {
		t.Fatalf("concurrent fetch results = %#v", results)
	}

	// Verify exact error reasons (TimedOut -> ERR_TIMED_OUT, BlockedByClient -> ERR_BLOCKED_BY_CLIENT).
	netOut := callTool[getNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":         tabID,
		"mode":        "peek",
		"failed_only": true,
	})
	timedOutCount := 0
	blockedCount := 0
	for _, r := range netOut.Requests {
		if strings.HasSuffix(r.URL, "/api/data") && r.Method == "POST" && strings.Contains(r.Error, "ERR_TIMED_OUT") {
			timedOutCount++
		}
		if strings.HasSuffix(r.URL, "/image.png") && strings.Contains(r.Error, "ERR_BLOCKED_BY_CLIENT") {
			blockedCount++
		}
	}
	if timedOutCount != 2 {
		t.Fatalf("expected 2 failed POST /api/data requests with ERR_TIMED_OUT, got %d (entries=%+v)", timedOutCount, netOut.Requests)
	}
	if blockedCount != 1 {
		t.Fatalf("expected 1 blocked /image.png request with ERR_BLOCKED_BY_CLIENT, got %d (entries=%+v)", blockedCount, netOut.Requests)
	}

	callTool[interceptOutput](t, "intercept", map[string]any{"tab": tabID, "action": "clear"})
}

func TestInterceptValidation(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	badCases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"empty_pattern", map[string]any{"tab": tabID, "url_pattern": "", "action": "fail"}, "url_pattern"},
		{"fulfill_with_id", map[string]any{"tab": tabID, "url_pattern": "*", "action": "fulfill", "id": "r1"}, "id is only valid"},
		{"invalid_resource_type", map[string]any{"tab": tabID, "url_pattern": "*", "action": "fail", "resource_type": "bogus"}, "resource_type"},
		{"remove_missing_id", map[string]any{"tab": tabID, "action": "remove"}, "rule id"},
		{"remove_stray_pattern", map[string]any{"tab": tabID, "action": "remove", "id": "r1", "url_pattern": "*"}, "only accepts id"},
		{"remove_empty_headers", map[string]any{"tab": tabID, "action": "remove", "id": "r1", "headers": map[string]string{}}, "only accepts id"},
		{"list_stray_id", map[string]any{"tab": tabID, "action": "list", "id": "r1"}, "does not accept"},
		{"list_stray_pattern", map[string]any{"tab": tabID, "action": "list", "url_pattern": "*"}, "does not accept"},
		{"list_empty_headers", map[string]any{"tab": tabID, "action": "list", "headers": map[string]string{}}, "does not accept"},
		{"clear_stray_id", map[string]any{"tab": tabID, "action": "clear", "id": "r1"}, "does not accept"},
		{"clear_stray_headers", map[string]any{"tab": tabID, "action": "clear", "headers": map[string]string{"X-A": "1"}}, "does not accept"},
		{"clear_empty_headers", map[string]any{"tab": tabID, "action": "clear", "headers": map[string]string{}}, "does not accept"},
		{"status_low", map[string]any{"tab": tabID, "url_pattern": "*", "action": "fulfill", "status": 99}, "status"},
		{"status_high", map[string]any{"tab": tabID, "url_pattern": "*", "action": "fulfill", "status": 600}, "status"},
		{"fulfill_100_with_body", map[string]any{"tab": tabID, "url_pattern": "*", "action": "fulfill", "status": 100, "body": "x"}, "forbids a non-empty response body"},
		{"fulfill_204_with_body", map[string]any{"tab": tabID, "url_pattern": "*", "action": "fulfill", "status": 204, "body": "x"}, "forbids a non-empty response body"},
		{"fulfill_304_with_body", map[string]any{"tab": tabID, "url_pattern": "*", "action": "fulfill", "status": 304, "body": "x"}, "forbids a non-empty response body"},
		{"bad_body_b64", map[string]any{"tab": tabID, "url_pattern": "*", "action": "fulfill", "body": "!!!", "body_base64": true}, "base64"},
		{"fulfill_stray_url", map[string]any{"tab": tabID, "url_pattern": "*", "action": "fulfill", "url": "https://example.com"}, "fulfill"},
		{"modify_empty", map[string]any{"tab": tabID, "url_pattern": "*", "action": "modify"}, "requires at least one"},
		{"modify_whitespace_override_method", map[string]any{"tab": tabID, "url_pattern": "*", "action": "modify", "override_method": "   "}, "invalid override_method"},
		{"modify_invalid_override_method", map[string]any{"tab": tabID, "url_pattern": "*", "action": "modify", "override_method": "BAD METHOD"}, "invalid override_method"},
		{"modify_bad_url", map[string]any{"tab": tabID, "url_pattern": "*", "action": "modify", "url": "/relative"}, "absolute URL"},
		{"modify_empty_post_data_b64", map[string]any{"tab": tabID, "url_pattern": "*", "action": "modify", "post_data": "", "post_data_base64": true}, "non-empty"},
		{"modify_empty_post_data_b64_with_override", map[string]any{"tab": tabID, "url_pattern": "*", "action": "modify", "override_method": "PUT", "post_data": "", "post_data_base64": true}, "non-empty"},
		{"modify_bad_b64", map[string]any{"tab": tabID, "url_pattern": "*", "action": "modify", "post_data": "!!!", "post_data_base64": true}, "base64"},
		{"modify_stray_status", map[string]any{"tab": tabID, "url_pattern": "*", "action": "modify", "override_method": "PUT", "status": 200}, "modify"},
		{"fail_stray_body", map[string]any{"tab": tabID, "url_pattern": "*", "action": "fail", "body": "nope"}, "fail"},
		{"fail_stray_headers", map[string]any{"tab": tabID, "url_pattern": "*", "action": "fail", "headers": map[string]string{"X-A": "1"}}, "fail"},
		{"fail_empty_headers", map[string]any{"tab": tabID, "url_pattern": "*", "action": "fail", "headers": map[string]string{}}, "fail"},
		{"blank_header_name", map[string]any{"tab": tabID, "url_pattern": "*", "action": "fulfill", "headers": map[string]string{"  ": "v"}}, "invalid header name"},
		{"invalid_header_name_space", map[string]any{"tab": tabID, "url_pattern": "*", "action": "fulfill", "headers": map[string]string{"Bad Name": "v"}}, "invalid header name"},
		{"invalid_header_value_crlf", map[string]any{"tab": tabID, "url_pattern": "*", "action": "fulfill", "headers": map[string]string{"X-A": "1\r\nX-B: 2"}}, "invalid header value"},
		{"invalid_header_value_nul", map[string]any{"tab": tabID, "url_pattern": "*", "action": "modify", "headers": map[string]string{"X-A": "1\x002"}}, "invalid header value"},
		{"case_colliding_headers_fulfill", map[string]any{"tab": tabID, "url_pattern": "*", "action": "fulfill", "headers": map[string]string{"X-Foo": "1", "x-foo": "2"}}, "duplicate header names"},
		{"case_colliding_headers_modify", map[string]any{"tab": tabID, "url_pattern": "*", "action": "modify", "headers": map[string]string{"X-Foo": "1", "x-foo": "2"}}, "duplicate header names"},
	}
	for _, tc := range badCases {
		t.Run(tc.name, func(t *testing.T) {
			errText := callToolExpectErr(t, "intercept", tc.args)
			if !strings.Contains(errText, tc.want) {
				t.Errorf("%s err = %q, want substring %q", tc.name, errText, tc.want)
			}
		})
	}

	// Ensure no rules were added by failed calls.
	list := callTool[interceptOutput](t, "intercept", map[string]any{"tab": tabID, "action": "list"})
	if list.Rules == nil || len(*list.Rules) != 0 {
		t.Errorf("failed intercept calls leaked rules: %+v", list.Rules)
	}
}
