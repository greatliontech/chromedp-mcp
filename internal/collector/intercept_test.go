package collector

import (
	"errors"
	"sync"
	"testing"

	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
)

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		pattern string
		input   string
		want    bool
	}{
		{"*", "https://example.com/a/b?c=1", true},
		{"*", "", true},
		{"", "", true},
		{"", "a", false},
		{"*/api/*", "https://example.com/api/users/42", true},
		{"*/api/*", "https://example.com/static/app.js", false},
		{"*.png", "https://cdn.example.com/img/logo.png", true},
		{"*.png", "https://cdn.example.com/img/logo.png?v=1", false},
		{"https://example.com/v?/items", "https://example.com/v2/items", true},
		{"https://example.com/v?/items", "https://example.com/v10/items", false},
		{`https://example.com/search\?q=*`, "https://example.com/search?q=go", true},
		{`https://example.com/search\?q=*`, "https://example.com/searchXq=go", false},
		{`https://example.com/a\*b`, "https://example.com/a*b", true},
		{`https://example.com/a\*b`, "https://example.com/axxb", false},
		{"https://example.com/exact", "https://example.com/exact", true},
		{"https://example.com/exact", "https://example.com/exact2", false},
		{"**a**b**", "xxayybzz", true},
		// Unicode single-rune '?' matching.
		{"*/caf?", "https://example.com/café", true},
		// Trailing literal backslash.
		{`a\`, `a\`, true},
	}
	for _, tc := range cases {
		if got := MatchGlob(tc.pattern, tc.input); got != tc.want {
			t.Errorf("MatchGlob(%q, %q) = %v, want %v", tc.pattern, tc.input, got, tc.want)
		}
	}
}

func TestInterceptRuleLifecyclePrecedenceAndCopies(t *testing.T) {
	ic := NewIntercept()
	if ic.Len() != 0 || len(ic.List()) != 0 {
		t.Fatal("new Intercept should be empty")
	}

	origHeaders := map[string]string{"X-One": "1"}
	id1 := ic.Add(InterceptRule{
		URLPattern:   "*/api/*",
		Method:       "post",
		ResourceType: "fetch",
		Action:       InterceptActionFulfill,
		Status:       201,
		Headers:      origHeaders,
		Body:         []byte(`{"ok":true}`),
	})
	id2 := ic.Add(InterceptRule{
		URLPattern: "*/api/*",
		Action:     InterceptActionFail,
		ErrorReason: network.ErrorReasonTimedOut,
	})
	if id1 == "" || id2 == "" || id1 == id2 {
		t.Fatalf("expected distinct non-empty IDs, got %q and %q", id1, id2)
	}

	// Mutating caller map after Add must not affect stored rule.
	origHeaders["X-One"] = "tampered"

	// POST + Fetch matches first rule (id1), not fallback id2.
	matched, ok := ic.Match(&fetch.EventRequestPaused{
		Request:      &network.Request{URL: "https://example.com/api/items", Method: "POST"},
		ResourceType: network.ResourceTypeFetch,
	})
	if !ok || matched.ID != id1 || matched.Status != 201 || matched.Headers["X-One"] != "1" {
		t.Fatalf("POST/Fetch match = (%+v, %v), want rule %q with X-One=1", matched, ok, id1)
	}
	// Mutating returned match copy must not affect internal rule.
	matched.Headers["X-One"] = "mutated"
	matched.Body[0] = '!'

	// POST + XHR must NOT match id1 (which requires ResourceType "fetch"),
	// so it falls through to id2.
	matchedXHR, ok := ic.Match(&fetch.EventRequestPaused{
		Request:      &network.Request{URL: "https://example.com/api/items", Method: "POST"},
		ResourceType: network.ResourceTypeXHR,
	})
	if !ok || matchedXHR.ID != id2 {
		t.Fatalf("POST/XHR match = (%+v, %v), want fallback rule %q", matchedXHR, ok, id2)
	}

	// GET matches second rule (id2) because id1 requires POST.
	matched2, ok := ic.Match(&fetch.EventRequestPaused{
		Request:      &network.Request{URL: "https://example.com/api/items", Method: "GET"},
		ResourceType: network.ResourceTypeFetch,
	})
	if !ok || matched2.ID != id2 || matched2.ErrorReason != network.ErrorReasonTimedOut {
		t.Fatalf("GET match = (%+v, %v), want rule %q", matched2, ok, id2)
	}

	// Non-matching URL returns false.
	if _, ok := ic.Match(&fetch.EventRequestPaused{
		Request: &network.Request{URL: "https://example.com/index.html", Method: "GET"},
	}); ok {
		t.Fatal("non-matching URL should return ok=false")
	}
	// Nil event / nil request returns false.
	if _, ok := ic.Match(nil); ok {
		t.Fatal("nil event should return ok=false")
	}

	list := ic.List()
	if len(list) != 2 || list[0].ID != id1 || list[0].Matches != 1 || list[0].Headers["X-One"] != "1" || list[1].ID != id2 || list[1].Matches != 2 {
		t.Fatalf("unexpected List(): %+v", list)
	}

	// Remove missing rule returns ErrInterceptRuleNotFound.
	if rem, err := ic.Remove("missing"); !errors.Is(err, ErrInterceptRuleNotFound) || rem != 2 {
		t.Fatalf("Remove(missing) = (%d, %v), want (2, ErrInterceptRuleNotFound)", rem, err)
	}

	// Remove id1 leaves id2.
	if rem, err := ic.Remove(id1); err != nil || rem != 1 {
		t.Fatalf("Remove(id1) = (%d, %v), want (1, nil)", rem, err)
	}
	if n := ic.Clear(); n != 1 || ic.Len() != 0 {
		t.Fatalf("Clear() = %d (Len=%d), want 1 (0)", n, ic.Len())
	}
}

func TestMergeRequestHeadersCaseInsensitiveOverride(t *testing.T) {
	orig := network.Headers{
		"Accept":       "application/json",
		"Content-Type": "text/plain",
		"X-Keep":       "yes",
		"X-Multi":      "val2\nval1",
	}
	overrides := map[string]string{
		"content-type": "application/json",
		"X-Added":      "123",
	}
	merged := MergeRequestHeaders(orig, overrides)
	if len(merged) != 6 {
		t.Fatalf("merged len = %d, want 6: %+v", len(merged), merged)
	}
	got := make(map[string][]string, len(merged))
	for _, h := range merged {
		got[h.Name] = append(got[h.Name], h.Value)
	}
	if len(got["Accept"]) != 1 || got["Accept"][0] != "application/json" ||
		len(got["X-Keep"]) != 1 || got["X-Keep"][0] != "yes" ||
		len(got["content-type"]) != 1 || got["content-type"][0] != "application/json" ||
		len(got["X-Added"]) != 1 || got["X-Added"][0] != "123" {
		t.Errorf("unexpected merged headers: %+v", got)
	}
	// Multi-value CDP header joined with '\n' must split into separate entries
	// while preserving original relative value order (RFC 9110 §5.3).
	if len(got["X-Multi"]) != 2 || got["X-Multi"][0] != "val2" || got["X-Multi"][1] != "val1" {
		t.Errorf("X-Multi = %v, want [val2 val1]", got["X-Multi"])
	}
	if _, dup := got["Content-Type"]; dup {
		t.Errorf("case-insensitive override left duplicate Content-Type: %+v", got)
	}
}

func TestInterceptConcurrentAccess(t *testing.T) {
	ic := NewIntercept()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				id := ic.Add(InterceptRule{URLPattern: "*/api/*", Action: InterceptActionFail, ErrorReason: network.ErrorReasonFailed})
				_, _ = ic.Match(&fetch.EventRequestPaused{Request: &network.Request{URL: "https://example.com/api/x", Method: "GET"}})
				_ = ic.List()
				_, _ = ic.Remove(id)
			}
		}()
	}
	wg.Wait()
}
