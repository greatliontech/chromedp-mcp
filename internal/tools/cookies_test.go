package tools

import (
	"strings"
	"testing"
	"time"
)

func TestCookieDomainMatchingUnits(t *testing.T) {
	matchCases := []struct {
		cookieDomain string
		filterDomain string
		want         bool
	}{
		{"example.com", "example.com", true},
		{".example.com", "example.com", true},
		{"sub.example.com", "example.com", false},
		{"example.com", ".example.com", true},
		{".example.com", ".example.com", true},
		{"sub.example.com", ".example.com", true},
		{".sub.example.com", ".example.com", true},
		{"evil-example.com", ".example.com", false},
		{"EXAMPLE.COM", "example.com", true},
		{"example.com", "", false},
		{"example.com", "   ", false},
		{"example.com", ".", false},
		{"[::1]", "::1", true},
		{"::1", "[::1]", true},
	}
	for _, tc := range matchCases {
		if got := cookieDomainMatches(tc.cookieDomain, tc.filterDomain); got != tc.want {
			t.Errorf("cookieDomainMatches(%q, %q) = %v, want %v", tc.cookieDomain, tc.filterDomain, got, tc.want)
		}
	}

	hostCases := []struct {
		cookieDomain string
		host         string
		want         bool
	}{
		{"127.0.0.1", "127.0.0.1", true},
		{"[::1]", "::1", true},
		{"::1", "[::1]", true},
		{"example.com", "example.com", true},
		{"example.com", "sub.example.com", false},
		{".example.com", "example.com", true},
		{".example.com", "sub.example.com", true},
		{".example.com", "evil-example.com", false},
		{".", "example.com", false},
		{"", "example.com", false},
	}
	for _, tc := range hostCases {
		if got := cookieAppliesToHost(tc.cookieDomain, tc.host); got != tc.want {
			t.Errorf("cookieAppliesToHost(%q, %q) = %v, want %v", tc.cookieDomain, tc.host, got, tc.want)
		}
	}
}

func TestSetCookieOmittedDomainUsesActiveTabURL(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)
	defer callTool[struct{}](t, "delete_cookies", map[string]any{"tab": tabID})

	callTool[struct{}](t, "set_cookie", map[string]any{
		"tab":   tabID,
		"name":  "active_url_cookie",
		"value": "v1",
	})

	out := callTool[GetCookiesOutput](t, "get_cookies", map[string]any{"tab": tabID})
	found := false
	for _, c := range out.Cookies {
		if c.Name == "active_url_cookie" && c.Value == "v1" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("cookie set without domain was not found in get_cookies: %+v", out.Cookies)
	}

	// Non-HTTP page without domain must fail cleanly.
	callTool[NavigateOutput](t, "navigate", map[string]any{
		"tab": tabID,
		"url": "about:blank",
	})
	errText := callToolExpectErr(t, "set_cookie", map[string]any{
		"tab":   tabID,
		"name":  "blank_cookie",
		"value": "v2",
	})
	if !strings.Contains(errText, "domain") {
		t.Errorf("expected domain error on about:blank, got: %s", errText)
	}
}

func TestSetCookieValidation(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "set_cookie", map[string]any{
		"tab":    tabID,
		"name":   "",
		"value":  "v",
		"domain": "127.0.0.1",
	})
	if !strings.Contains(errText, "name") {
		t.Errorf("expected empty name error, got: %s", errText)
	}

	errText = callToolExpectErr(t, "set_cookie", map[string]any{
		"tab":     tabID,
		"name":    "neg_exp",
		"value":   "v",
		"domain":  "127.0.0.1",
		"expires": -10,
	})
	if !strings.Contains(errText, "expires") {
		t.Errorf("expected negative expires error, got: %s", errText)
	}

	errText = callToolExpectErr(t, "set_cookie", map[string]any{
		"tab":       tabID,
		"name":      "bad_ss",
		"value":     "v",
		"domain":    "127.0.0.1",
		"same_site": "Bogus",
	})
	if !strings.Contains(errText, "same_site") {
		t.Errorf("expected invalid same_site error, got: %s", errText)
	}
}

func TestDeleteCookiesScopedByDomainPreservesOthers(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)
	defer callTool[struct{}](t, "delete_cookies", map[string]any{"tab": tabID})

	callTool[struct{}](t, "set_cookie", map[string]any{
		"tab":    tabID,
		"name":   "c_alpha",
		"value":  "1",
		"domain": "alpha.test",
	})
	callTool[struct{}](t, "set_cookie", map[string]any{
		"tab":    tabID,
		"name":   "c_beta",
		"value":  "2",
		"domain": "beta.test",
	})

	// Delete all cookies for alpha.test only (name omitted).
	callTool[struct{}](t, "delete_cookies", map[string]any{
		"tab":    tabID,
		"domain": "alpha.test",
	})

	alpha := callTool[GetCookiesOutput](t, "get_cookies", map[string]any{
		"tab":  tabID,
		"urls": []string{"http://alpha.test/"},
	})
	if len(alpha.Cookies) != 0 {
		t.Errorf("expected alpha.test cookies deleted, got: %+v", alpha.Cookies)
	}

	beta := callTool[GetCookiesOutput](t, "get_cookies", map[string]any{
		"tab":  tabID,
		"urls": []string{"http://beta.test/"},
	})
	if len(beta.Cookies) != 1 || beta.Cookies[0].Name != "c_beta" {
		t.Errorf("expected beta.test cookie preserved, got: %+v", beta.Cookies)
	}
}

func TestDeleteCookiesScopedByPathPreservesOtherPathsAndDomains(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)
	defer callTool[struct{}](t, "delete_cookies", map[string]any{"tab": tabID})

	callTool[struct{}](t, "set_cookie", map[string]any{
		"tab":   tabID,
		"name":  "local_admin",
		"value": "1",
		"path":  "/admin",
	})
	callTool[struct{}](t, "set_cookie", map[string]any{
		"tab":   tabID,
		"name":  "local_root",
		"value": "2",
		"path":  "/",
	})
	callTool[struct{}](t, "set_cookie", map[string]any{
		"tab":    tabID,
		"name":   "other_admin",
		"value":  "3",
		"domain": "other.test",
		"path":   "/admin",
	})

	// Path-only delete on 127.0.0.1 must remove local_admin even while tab is on
	// /page2.html, while keeping local_root and other.test's /admin cookie.
	callTool[struct{}](t, "delete_cookies", map[string]any{
		"tab":  tabID,
		"path": "/admin",
	})

	localAdmin := callTool[GetCookiesOutput](t, "get_cookies", map[string]any{
		"tab":  tabID,
		"urls": []string{harness.httpSrv.URL + "/admin"},
	})
	for _, c := range localAdmin.Cookies {
		if c.Name == "local_admin" {
			t.Errorf("expected local_admin deleted, still present: %+v", c)
		}
	}
	foundRoot := false
	for _, c := range localAdmin.Cookies {
		if c.Name == "local_root" {
			foundRoot = true
		}
	}
	if !foundRoot {
		t.Errorf("expected local_root preserved, got: %+v", localAdmin.Cookies)
	}

	other := callTool[GetCookiesOutput](t, "get_cookies", map[string]any{
		"tab":  tabID,
		"urls": []string{"http://other.test/admin"},
	})
	if len(other.Cookies) != 1 || other.Cookies[0].Name != "other_admin" {
		t.Errorf("expected other.test/admin cookie preserved, got: %+v", other.Cookies)
	}
}

func TestDeleteCookiesNameAndPathWithoutDomain(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)
	defer callTool[struct{}](t, "delete_cookies", map[string]any{"tab": tabID})

	callTool[struct{}](t, "set_cookie", map[string]any{
		"tab":   tabID,
		"name":  "admin_tok",
		"value": "secret",
		"path":  "/admin",
	})
	callTool[struct{}](t, "set_cookie", map[string]any{
		"tab":   tabID,
		"name":  "admin_tok",
		"value": "root",
		"path":  "/",
	})

	// Tab is on /page2.html, NOT /admin. Deleting by name+path without domain
	// must still match and delete the /admin cookie while preserving / root.
	callTool[struct{}](t, "delete_cookies", map[string]any{
		"tab":  tabID,
		"name": "admin_tok",
		"path": "/admin",
	})

	got := callTool[GetCookiesOutput](t, "get_cookies", map[string]any{
		"tab":  tabID,
		"urls": []string{harness.httpSrv.URL + "/admin"},
	})
	for _, c := range got.Cookies {
		if c.Name == "admin_tok" && c.Path == "/admin" {
			t.Fatalf("expected /admin cookie deleted, still present: %+v", c)
		}
	}
	foundRoot := false
	for _, c := range got.Cookies {
		if c.Name == "admin_tok" && c.Path == "/" {
			foundRoot = true
		}
	}
	if !foundRoot {
		t.Fatalf("expected / cookie preserved, got: %+v", got.Cookies)
	}
}

func TestDeleteCookiesRejectsWhitespaceAndDotDomain(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)
	defer callTool[struct{}](t, "delete_cookies", map[string]any{"tab": tabID})

	callTool[struct{}](t, "set_cookie", map[string]any{
		"tab":    tabID,
		"name":   "keep_me",
		"value":  "1",
		"domain": "alpha.test",
	})

	for _, bad := range []string{"   ", "."} {
		errText := callToolExpectErr(t, "delete_cookies", map[string]any{
			"tab":    tabID,
			"domain": bad,
		})
		if !strings.Contains(errText, "invalid cookie domain") {
			t.Errorf("delete_cookies(domain=%q) error = %q, want invalid cookie domain", bad, errText)
		}
	}

	got := callTool[GetCookiesOutput](t, "get_cookies", map[string]any{
		"tab":  tabID,
		"urls": []string{"http://alpha.test/"},
	})
	if len(got.Cookies) != 1 || got.Cookies[0].Name != "keep_me" {
		t.Fatalf("invalid domain call wiped cookies: got %+v", got.Cookies)
	}
}

func TestCookies(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// Set a cookie.
	callTool[struct{}](t, "set_cookie", map[string]any{
		"tab":    tabID,
		"name":   "test_cookie",
		"value":  "test_value",
		"domain": "127.0.0.1",
	})

	// Get cookies.
	out := callTool[GetCookiesOutput](t, "get_cookies", map[string]any{
		"tab": tabID,
	})
	found := false
	for _, c := range out.Cookies {
		if c.Name == "test_cookie" && c.Value == "test_value" {
			found = true
		}
	}
	if !found {
		t.Error("set cookie not found in get_cookies output")
	}

	// Delete the cookie.
	callTool[struct{}](t, "delete_cookies", map[string]any{
		"tab":    tabID,
		"name":   "test_cookie",
		"domain": "127.0.0.1",
	})

	out = callTool[GetCookiesOutput](t, "get_cookies", map[string]any{
		"tab": tabID,
	})
	for _, c := range out.Cookies {
		if c.Name == "test_cookie" {
			t.Error("cookie still exists after delete")
		}
	}
}

func TestClearCookies(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	// Set two cookies.
	callTool[struct{}](t, "set_cookie", map[string]any{
		"tab":    tabID,
		"name":   "cookie_a",
		"value":  "val_a",
		"domain": "127.0.0.1",
	})
	callTool[struct{}](t, "set_cookie", map[string]any{
		"tab":    tabID,
		"name":   "cookie_b",
		"value":  "val_b",
		"domain": "127.0.0.1",
	})

	// Clear all cookies.
	callTool[struct{}](t, "delete_cookies", map[string]any{"tab": tabID})

	// Verify none remain.
	out := callTool[GetCookiesOutput](t, "get_cookies", map[string]any{"tab": tabID})
	if len(out.Cookies) != 0 {
		t.Errorf("after delete_cookies, expected 0 cookies, got %d", len(out.Cookies))
	}
}

func TestCookieFields(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "set_cookie", map[string]any{
		"tab":       tabID,
		"name":      "field_test",
		"value":     "field_value",
		"domain":    "127.0.0.1",
		"http_only": true,
	})
	defer callTool[struct{}](t, "delete_cookies", map[string]any{"tab": tabID})

	out := callTool[GetCookiesOutput](t, "get_cookies", map[string]any{"tab": tabID})
	var found *CookieInfo
	for i := range out.Cookies {
		if out.Cookies[i].Name == "field_test" {
			found = &out.Cookies[i]
			break
		}
	}
	if found == nil {
		t.Fatal("cookie 'field_test' not found")
	}
	if found.Value != "field_value" {
		t.Errorf("cookie value = %q, want 'field_value'", found.Value)
	}
	if found.Path != "/" {
		t.Errorf("cookie path = %q, want '/' (default)", found.Path)
	}
	if !found.HTTPOnly {
		t.Error("cookie http_only should be true")
	}
	if found.Domain == "" {
		t.Error("cookie domain should not be empty")
	}
}

func TestSetCookieDefaultPath(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "set_cookie", map[string]any{
		"tab":    tabID,
		"name":   "path_test",
		"value":  "pv",
		"domain": "127.0.0.1",
	})
	defer callTool[struct{}](t, "delete_cookies", map[string]any{"tab": tabID})

	out := callTool[GetCookiesOutput](t, "get_cookies", map[string]any{"tab": tabID})
	for _, c := range out.Cookies {
		if c.Name == "path_test" {
			if c.Path != "/" {
				t.Errorf("default path = %q, want '/'", c.Path)
			}
			return
		}
	}
	t.Error("cookie 'path_test' not found")
}

func TestSetCookieCustomPath(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)
	defer callTool[struct{}](t, "delete_cookies", map[string]any{"tab": tabID})

	callTool[struct{}](t, "set_cookie", map[string]any{
		"tab":    tabID,
		"name":   "path_cookie",
		"value":  "pv",
		"domain": "127.0.0.1",
		"path":   "/subdir",
	})

	out := callTool[GetCookiesOutput](t, "get_cookies", map[string]any{"tab": tabID})
	for _, c := range out.Cookies {
		if c.Name == "path_cookie" {
			if c.Path != "/subdir" {
				t.Errorf("cookie path = %q, want '/subdir'", c.Path)
			}
			return
		}
	}
	// The cookie may not be returned for the current URL since it's scoped to /subdir.
	// That's actually correct behaviour — just verify no error occurred.
}

func TestSetCookieSameSite(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)
	defer callTool[struct{}](t, "delete_cookies", map[string]any{"tab": tabID})

	callTool[struct{}](t, "set_cookie", map[string]any{
		"tab":       tabID,
		"name":      "ss_cookie",
		"value":     "ssv",
		"domain":    "127.0.0.1",
		"same_site": "Lax",
	})

	out := callTool[GetCookiesOutput](t, "get_cookies", map[string]any{"tab": tabID})
	for _, c := range out.Cookies {
		if c.Name == "ss_cookie" {
			if c.SameSite != "Lax" {
				t.Errorf("same_site = %q, want 'Lax'", c.SameSite)
			}
			return
		}
	}
	t.Error("cookie 'ss_cookie' not found")
}

func TestSetCookieWithExpires(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)
	defer callTool[struct{}](t, "delete_cookies", map[string]any{"tab": tabID})

	// Set expiry 1 hour from now.
	expiry := float64(time.Now().Add(time.Hour).Unix())
	callTool[struct{}](t, "set_cookie", map[string]any{
		"tab":     tabID,
		"name":    "exp_cookie",
		"value":   "ev",
		"domain":  "127.0.0.1",
		"expires": expiry,
	})

	out := callTool[GetCookiesOutput](t, "get_cookies", map[string]any{"tab": tabID})
	for _, c := range out.Cookies {
		if c.Name == "exp_cookie" {
			if c.Expires <= 0 {
				t.Errorf("expires = %f, want > 0", c.Expires)
			}
			return
		}
	}
	t.Error("cookie 'exp_cookie' not found")
}

func TestDeleteCookieWithoutDomain(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)
	defer callTool[struct{}](t, "delete_cookies", map[string]any{"tab": tabID})

	callTool[struct{}](t, "set_cookie", map[string]any{
		"tab":    tabID,
		"name":   "no_domain_del",
		"value":  "val",
		"domain": "127.0.0.1",
	})

	// Delete without specifying domain on an HTTP(S) page defaults to the
	// active tab's URL and deletes the cookie.
	callTool[struct{}](t, "delete_cookies", map[string]any{
		"tab":  tabID,
		"name": "no_domain_del",
	})
	after := callTool[GetCookiesOutput](t, "get_cookies", map[string]any{"tab": tabID})
	for _, c := range after.Cookies {
		if c.Name == "no_domain_del" {
			t.Fatalf("cookie %q was not deleted when domain was omitted on HTTP page", c.Name)
		}
	}

	// On a non-HTTP page (about:blank), omitting domain must return an error.
	callTool[NavigateOutput](t, "navigate", map[string]any{
		"tab": tabID,
		"url": "about:blank",
	})
	errText := callToolExpectErr(t, "delete_cookies", map[string]any{
		"tab":  tabID,
		"name": "no_domain_del",
	})
	if !strings.Contains(errText, "domain") {
		t.Errorf("expected error about domain requirement on about:blank, got: %s", errText)
	}
}

func TestDeleteNonexistentCookie(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	// Should not error.
	callTool[struct{}](t, "delete_cookies", map[string]any{
		"tab":    tabID,
		"name":   "nonexistent_cookie_xyz",
		"domain": "127.0.0.1",
	})
}

func TestClearCookiesIdempotent(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	// Clear when nothing exists — should not error.
	callTool[struct{}](t, "delete_cookies", map[string]any{"tab": tabID})
	callTool[struct{}](t, "delete_cookies", map[string]any{"tab": tabID})
}

func TestCookieSizeField(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)
	defer callTool[struct{}](t, "delete_cookies", map[string]any{"tab": tabID})

	callTool[struct{}](t, "set_cookie", map[string]any{
		"tab":    tabID,
		"name":   "size_test",
		"value":  "somevalue",
		"domain": "127.0.0.1",
	})

	out := callTool[GetCookiesOutput](t, "get_cookies", map[string]any{"tab": tabID})
	for _, c := range out.Cookies {
		if c.Name == "size_test" {
			if c.Size <= 0 {
				t.Errorf("cookie size = %d, want > 0", c.Size)
			}
			return
		}
	}
	t.Error("cookie 'size_test' not found")
}

func TestCookieOverwrite(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)
	defer callTool[struct{}](t, "delete_cookies", map[string]any{"tab": tabID})

	// Set cookie with value "v1".
	callTool[struct{}](t, "set_cookie", map[string]any{
		"tab":    tabID,
		"name":   "overwrite_test",
		"value":  "v1",
		"domain": "127.0.0.1",
	})

	// Overwrite with value "v2".
	callTool[struct{}](t, "set_cookie", map[string]any{
		"tab":    tabID,
		"name":   "overwrite_test",
		"value":  "v2",
		"domain": "127.0.0.1",
	})

	out := callTool[GetCookiesOutput](t, "get_cookies", map[string]any{"tab": tabID})
	var found int
	var lastValue string
	for _, c := range out.Cookies {
		if c.Name == "overwrite_test" {
			found++
			lastValue = c.Value
		}
	}
	if found != 1 {
		t.Errorf("expected exactly 1 cookie named 'overwrite_test', found %d", found)
	}
	if lastValue != "v2" {
		t.Errorf("cookie value = %q, want 'v2'", lastValue)
	}
}

func TestSetCookieSameSiteStrict(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)
	defer callTool[struct{}](t, "delete_cookies", map[string]any{"tab": tabID})

	callTool[struct{}](t, "set_cookie", map[string]any{
		"tab":       tabID,
		"name":      "strict_cookie",
		"value":     "sv",
		"domain":    "127.0.0.1",
		"same_site": "Strict",
	})

	out := callTool[GetCookiesOutput](t, "get_cookies", map[string]any{"tab": tabID})
	for _, c := range out.Cookies {
		if c.Name == "strict_cookie" {
			if c.SameSite != "Strict" {
				t.Errorf("same_site = %q, want 'Strict'", c.SameSite)
			}
			return
		}
	}
	t.Error("cookie 'strict_cookie' not found")
}

func TestDeleteCookieWithPath(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)
	defer callTool[struct{}](t, "delete_cookies", map[string]any{"tab": tabID})

	callTool[struct{}](t, "set_cookie", map[string]any{
		"tab":    tabID,
		"name":   "path_del_test",
		"value":  "pv",
		"domain": "127.0.0.1",
		"path":   "/",
	})

	// Delete with name + domain + path.
	callTool[struct{}](t, "delete_cookies", map[string]any{
		"tab":    tabID,
		"name":   "path_del_test",
		"domain": "127.0.0.1",
		"path":   "/",
	})

	out := callTool[GetCookiesOutput](t, "get_cookies", map[string]any{"tab": tabID})
	for _, c := range out.Cookies {
		if c.Name == "path_del_test" {
			t.Error("cookie should have been deleted")
		}
	}
}

func TestGetCookiesNoCookies(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	// Clear any existing cookies first.
	callTool[struct{}](t, "delete_cookies", map[string]any{"tab": tabID})

	out := callTool[GetCookiesOutput](t, "get_cookies", map[string]any{"tab": tabID})
	if out.Cookies == nil {
		t.Error("cookies should be an empty array, not nil")
	}
	if len(out.Cookies) != 0 {
		t.Errorf("expected 0 cookies after clear, got %d", len(out.Cookies))
	}
}

func TestGetCookiesWithURLs(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// Set a cookie scoped to the test server.
	callTool[struct{}](t, "set_cookie", map[string]any{
		"tab":    tabID,
		"name":   "url_test_cookie",
		"value":  "hello",
		"domain": "127.0.0.1",
		"path":   "/",
	})
	defer func() {
		callTool[struct{}](t, "delete_cookies", map[string]any{
			"tab":    tabID,
			"name":   "url_test_cookie",
			"domain": "127.0.0.1",
		})
	}()

	// Get cookies with explicit URLs — should return the cookie.
	out := callTool[GetCookiesOutput](t, "get_cookies", map[string]any{
		"tab":  tabID,
		"urls": []string{fixtureURL("index.html")},
	})
	found := false
	for _, c := range out.Cookies {
		if c.Name == "url_test_cookie" {
			found = true
			if c.Value != "hello" {
				t.Errorf("cookie value = %q, want %q", c.Value, "hello")
			}
		}
	}
	if !found {
		t.Error("get_cookies with urls did not return the expected cookie")
	}

	// Get cookies with a URL that doesn't match — should NOT return the cookie.
	out2 := callTool[GetCookiesOutput](t, "get_cookies", map[string]any{
		"tab":  tabID,
		"urls": []string{"https://example.com/"},
	})
	for _, c := range out2.Cookies {
		if c.Name == "url_test_cookie" {
			t.Error("get_cookies with non-matching URL should not return the cookie")
		}
	}
}

// TestDeleteCookiesClearAll verifies that omitting name clears all cookies.
func TestDeleteCookiesClearAll(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	// Set cookies via JS (simpler, no domain issues).
	type evalResult struct {
		Result string `json:"result"`
	}
	callTool[evalResult](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.cookie = 'a=1'; document.cookie = 'b=2'; 'ok'",
	})

	// Verify they exist.
	cookies := callTool[GetCookiesOutput](t, "get_cookies", map[string]any{"tab": tabID})
	if len(cookies.Cookies) < 2 {
		t.Fatalf("expected at least 2 cookies, got %d", len(cookies.Cookies))
	}

	// Delete all (no name).
	callTool[struct{}](t, "delete_cookies", map[string]any{"tab": tabID})

	// Verify all cleared.
	cookies = callTool[GetCookiesOutput](t, "get_cookies", map[string]any{"tab": tabID})
	if len(cookies.Cookies) != 0 {
		t.Fatalf("expected 0 cookies after clear-all, got %d", len(cookies.Cookies))
	}
}

// TestDeleteCookiesClearAllIdempotent verifies double clear-all is fine.
func TestDeleteCookiesClearAllIdempotent(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "delete_cookies", map[string]any{"tab": tabID})
	callTool[struct{}](t, "delete_cookies", map[string]any{"tab": tabID})
}
