package tools

import (
	"encoding/base64"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCacheStorageLifecyclePaginationBinaryAndDelete(t *testing.T) {
	// Dedicated 127.0.0.1 origin (Secure Context for window.caches) with clean state.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><html><body><h1>Cache Storage Test</h1></body></html>`))
	}))
	defer srv.Close()

	tabOut := callTool[TabNewOutput](t, "tab_new", map[string]any{})
	tabID := tabOut.TabID
	defer closeTab(t, tabID)

	if errText := callToolExpectErr(t, "get_cache_storage", map[string]any{"tab": tabID}); !strings.Contains(errText, "opaque origin") {
		t.Errorf("get_cache_storage on about:blank err = %q, want opaque origin", errText)
	}

	callTool[NavigateOutput](t, "navigate", map[string]any{"tab": tabID, "url": srv.URL + "/"})

	// Empty origin serializes caches as [], never null.
	rawEmpty := callToolRaw(t, "get_cache_storage", map[string]any{"tab": tabID})
	if rawEmpty.IsError || !strings.Contains(contentText(rawEmpty), `"caches":[]`) {
		t.Fatalf("empty get_cache_storage = %s (isErr=%v), want \"caches\":[]", contentText(rawEmpty), rawEmpty.IsError)
	}

	// Populate two caches: v1-assets (4 entries incl. binary) and v2-empty (0 entries).
	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": `(async function() {
			const c1 = await caches.open('v1-assets');
			await c1.put('/api/hello', new Response('{"msg":"hello world"}', {
				status: 200,
				statusText: 'OK',
				headers: {'Content-Type': 'application/json', 'X-Cache-Tag': 'greeting'},
			}));
			await c1.put('/api/item-1', new Response('item-one', {status: 200}));
			await c1.put('/api/item-2', new Response('item-two', {status: 200}));
			await c1.put('/bin/raw', new Response(new Uint8Array([0x00, 0x01, 0xfe, 0xff]), {
				status: 200,
				headers: {'Content-Type': 'application/octet-stream'},
			}));
			await caches.open('v2-empty');
			return 'ok';
		})()`,
	})

	// Mode 1: list caches.
	listOut := callTool[GetCacheStorageOutput](t, "get_cache_storage", map[string]any{"tab": tabID})
	if len(listOut.Caches) != 2 {
		t.Fatalf("caches = %+v, want 2 caches", listOut.Caches)
	}

	// Mode 2 on empty cache: entries must be [], never null.
	rawEmptyCache := callToolRaw(t, "get_cache_storage", map[string]any{
		"tab":        tabID,
		"cache_name": "v2-empty",
	})
	if rawEmptyCache.IsError || !strings.Contains(contentText(rawEmptyCache), `"entries":[]`) {
		t.Fatalf("empty cache entries = %s, want \"entries\":[]", contentText(rawEmptyCache))
	}

	// Mode 2 pagination on v1-assets (4 entries).
	page1 := callTool[GetCacheStorageOutput](t, "get_cache_storage", map[string]any{
		"tab":        tabID,
		"cache_name": "v1-assets",
		"limit":      2,
	})
	if len(page1.Entries) != 2 || page1.Total != 4 || !page1.HasMore {
		t.Fatalf("page1 len=%d total=%d has_more=%v, want 2, 4, true", len(page1.Entries), page1.Total, page1.HasMore)
	}
	page2 := callTool[GetCacheStorageOutput](t, "get_cache_storage", map[string]any{
		"tab":        tabID,
		"cache_name": "v1-assets",
		"skip":       2,
		"limit":      2,
	})
	if len(page2.Entries) != 2 || page2.Total != 4 || page2.HasMore {
		t.Fatalf("page2 len=%d total=%d has_more=%v, want 2, 4, false", len(page2.Entries), page2.Total, page2.HasMore)
	}

	// skip > total must return empty entries cleanly without crashing Chromium's renderer.
	rawPastEnd := callToolRaw(t, "get_cache_storage", map[string]any{
		"tab":        tabID,
		"cache_name": "v1-assets",
		"skip":       10,
		"limit":      5,
	})
	if rawPastEnd.IsError || !strings.Contains(contentText(rawPastEnd), `"entries":[]`) {
		t.Fatalf("skip > total returned %s (err=%v), want empty entries []", contentText(rawPastEnd), rawPastEnd.IsError)
	}
	pastEnd := callTool[GetCacheStorageOutput](t, "get_cache_storage", map[string]any{
		"tab":        tabID,
		"cache_name": "v1-assets",
		"skip":       10,
		"limit":      5,
	})
	if len(pastEnd.Entries) != 0 || pastEnd.Total != 4 || pastEnd.HasMore {
		t.Fatalf("skip > total = %+v, want 0 entries total=4 has_more=false", pastEnd)
	}

	// Large limit with non-zero skip must clamp cleanly to the end of the cache.
	hugeLimit := callTool[GetCacheStorageOutput](t, "get_cache_storage", map[string]any{
		"tab":        tabID,
		"cache_name": "v1-assets",
		"skip":       1,
		"limit":      1_000_000_000,
	})
	if len(hugeLimit.Entries) != 3 || hugeLimit.Total != 4 || hugeLimit.HasMore {
		t.Fatalf("skip=1 huge limit = %+v, want 3 entries total=4 has_more=false", hugeLimit)
	}

	// Mode 2 with path_filter.
	filtered := callTool[GetCacheStorageOutput](t, "get_cache_storage", map[string]any{
		"tab":         tabID,
		"cache_name":  "v1-assets",
		"path_filter": "/api/item-",
	})
	if len(filtered.Entries) != 2 || filtered.Total != 2 || filtered.HasMore {
		t.Fatalf("filtered entries = %+v (total=%d has_more=%v), want 2", filtered.Entries, filtered.Total, filtered.HasMore)
	}

	// Mode 3: read UTF-8 response body + metadata.
	helloURL := srv.URL + "/api/hello"
	respOut := callTool[GetCacheStorageOutput](t, "get_cache_storage", map[string]any{
		"tab":         tabID,
		"cache_name":  "v1-assets",
		"request_url": helloURL,
	})
	if respOut.Status != 200 || respOut.Body != `{"msg":"hello world"}` || respOut.Base64Encoded || respOut.TotalBytes != 21 || respOut.Truncated {
		t.Fatalf("hello cached response = %+v", respOut)
	}
	if respOut.ResponseHeaders["X-Cache-Tag"] != "greeting" {
		t.Errorf("response_headers = %+v, want X-Cache-Tag=greeting", respOut.ResponseHeaders)
	}

	// Mode 3: byte range slicing.
	sliced := callTool[GetCacheStorageOutput](t, "get_cache_storage", map[string]any{
		"tab":         tabID,
		"cache_name":  "v1-assets",
		"request_url": helloURL,
		"range_start": 8,
		"range_end":   19,
	})
	if sliced.Body != "hello world" || !sliced.Truncated || sliced.TotalBytes != 21 {
		t.Fatalf("sliced cached response = %+v, want body='hello world' truncated=true total_bytes=21", sliced)
	}

	// Mode 3: binary response must be base64-encoded with exact bytes.
	binOut := callTool[GetCacheStorageOutput](t, "get_cache_storage", map[string]any{
		"tab":         tabID,
		"cache_name":  "v1-assets",
		"request_url": srv.URL + "/bin/raw",
	})
	if !binOut.Base64Encoded || binOut.TotalBytes != 4 {
		t.Fatalf("binary cached response = %+v, want base64_encoded=true total_bytes=4", binOut)
	}
	decoded, err := base64.StdEncoding.DecodeString(binOut.Body)
	if err != nil || len(decoded) != 4 || decoded[0] != 0x00 || decoded[1] != 0x01 || decoded[2] != 0xfe || decoded[3] != 0xff {
		t.Fatalf("decoded binary cache body = %x (err=%v), want 0001feff", decoded, err)
	}

	// Missing cache & missing entry return clear errors.
	if errText := callToolExpectErr(t, "get_cache_storage", map[string]any{"tab": tabID, "cache_name": "no-such-cache"}); !strings.Contains(errText, "not found") {
		t.Errorf("missing cache err = %q, want not found", errText)
	}
	if errText := callToolExpectErr(t, "get_cache_storage", map[string]any{"tab": tabID, "cache_name": "v1-assets", "request_url": srv.URL + "/nope"}); !strings.Contains(errText, "not found") {
		t.Errorf("missing entry err = %q, want not found", errText)
	}

	// Delete a single entry.
	callTool[struct{}](t, "delete_cache_storage", map[string]any{
		"tab":         tabID,
		"cache_name":  "v1-assets",
		"request_url": srv.URL + "/api/item-1",
	})
	afterEntryDel := callTool[GetCacheStorageOutput](t, "get_cache_storage", map[string]any{
		"tab":        tabID,
		"cache_name": "v1-assets",
	})
	if afterEntryDel.Total != 3 {
		t.Fatalf("after deleting 1 entry, total = %d, want 3", afterEntryDel.Total)
	}

	// Delete entire cache v1-assets; v2-empty must remain.
	callTool[struct{}](t, "delete_cache_storage", map[string]any{
		"tab":        tabID,
		"cache_name": "v1-assets",
	})
	afterCacheDel := callTool[GetCacheStorageOutput](t, "get_cache_storage", map[string]any{"tab": tabID})
	if len(afterCacheDel.Caches) != 1 || afterCacheDel.Caches[0].Name != "v2-empty" {
		t.Fatalf("after deleting v1-assets, caches = %+v, want [v2-empty]", afterCacheDel.Caches)
	}
}

func TestCacheStorageValidation(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	badGet := []struct {
		name string
		args map[string]any
		want string
	}{
		{"request_url_without_cache", map[string]any{"tab": tabID, "request_url": "https://example.com"}, "requires cache_name"},
		{"path_filter_without_cache", map[string]any{"tab": tabID, "path_filter": "/a"}, "require cache_name"},
		{"skip_without_cache", map[string]any{"tab": tabID, "skip": 1}, "require cache_name"},
		{"limit_without_cache", map[string]any{"tab": tabID, "limit": 10}, "require cache_name"},
		{"negative_skip", map[string]any{"tab": tabID, "cache_name": "c", "skip": -1}, "skip"},
		{"negative_limit", map[string]any{"tab": tabID, "cache_name": "c", "limit": -1}, "limit"},
		{"range_without_request_url", map[string]any{"tab": tabID, "cache_name": "c", "range_start": 1}, "require request_url"},
		{"bad_range_order", map[string]any{"tab": tabID, "cache_name": "c", "request_url": "https://example.com", "range_start": 10, "range_end": 5}, "range_end"},
		{"request_url_with_limit", map[string]any{"tab": tabID, "cache_name": "c", "request_url": "https://example.com", "limit": 5}, "cannot be combined"},
		{"request_url_with_path_filter", map[string]any{"tab": tabID, "cache_name": "c", "request_url": "https://example.com", "path_filter": "/a"}, "cannot be combined"},
	}
	for _, tc := range badGet {
		t.Run(tc.name, func(t *testing.T) {
			if errText := callToolExpectErr(t, "get_cache_storage", tc.args); !strings.Contains(errText, tc.want) {
				t.Errorf("%s err = %q, want substring %q", tc.name, errText, tc.want)
			}
		})
	}
	if errText := callToolExpectErr(t, "delete_cache_storage", map[string]any{"tab": tabID, "cache_name": ""}); !strings.Contains(errText, "cache_name") {
		t.Errorf("delete_cache_storage empty cache_name err = %q, want cache_name", errText)
	}
}

func TestPaginateRangeNoOverflow(t *testing.T) {
	start, end, hasMore := paginateRange(4, 1, math.MaxInt)
	if start != 1 || end != 4 || hasMore {
		t.Fatalf("paginateRange(4, 1, MaxInt) = (%d, %d, %v), want (1, 4, false)", start, end, hasMore)
	}
	start, end, hasMore = paginateRange(4, 10, math.MaxInt)
	if start != 4 || end != 4 || hasMore {
		t.Fatalf("paginateRange(4, 10, MaxInt) = (%d, %d, %v), want (4, 4, false)", start, end, hasMore)
	}
}
