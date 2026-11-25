package tools

import (
	"strings"
	"testing"
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
