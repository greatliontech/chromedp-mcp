package tools

import (
	"strings"
	"testing"
	"time"
)

func getCookiesHelper(t *testing.T, args map[string]any) cookiesOutput {
	t.Helper()
	m := map[string]any{"action": "get"}
	for k, v := range args {
		m[k] = v
	}
	return callTool[cookiesOutput](t, "cookies", m)
}

func setCookieHelper(t *testing.T, args map[string]any) {
	t.Helper()
	m := map[string]any{"action": "set"}
	for k, v := range args {
		m[k] = v
	}
	callTool[cookiesOutput](t, "cookies", m)
}

func deleteCookiesHelper(t *testing.T, args map[string]any) {
	t.Helper()
	m := map[string]any{"action": "delete"}
	for k, v := range args {
		m[k] = v
	}
	callTool[cookiesOutput](t, "cookies", m)
}

func cookieSlice(out cookiesOutput) []cookieInfo {
	if out.Cookies == nil {
		return nil
	}
	return *out.Cookies
}

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
	defer deleteCookiesHelper(t, map[string]any{"tab": tabID})

	setCookieHelper(t, map[string]any{
		"tab":   tabID,
		"name":  "active_url_cookie",
		"value": "v1",
	})

	out := getCookiesHelper(t, map[string]any{"tab": tabID})
	found := false
	for _, c := range cookieSlice(out) {
		if c.Name == "active_url_cookie" && c.Value == "v1" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("cookie set without domain was not found: %+v", cookieSlice(out))
	}

	// Non-HTTP page without domain must fail cleanly.
	callTool[navigateOutput](t, "navigate", map[string]any{
		"tab": tabID,
		"url": "about:blank",
	})
	errText := callToolExpectErr(t, "cookies", map[string]any{
		"action": "set",
		"tab":    tabID,
		"name":   "blank_cookie",
		"value":  "v2",
	})
	if !strings.Contains(errText, "domain") {
		t.Errorf("expected domain error on about:blank, got: %s", errText)
	}
}

func TestSetCookieValidation(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	badCases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"empty_name", map[string]any{"action": "set", "tab": tabID, "name": "", "value": "v", "domain": "127.0.0.1"}, "name"},
		{"missing_value", map[string]any{"action": "set", "tab": tabID, "name": "k", "domain": "127.0.0.1"}, "value"},
		{"neg_expires", map[string]any{"action": "set", "tab": tabID, "name": "neg_exp", "value": "v", "domain": "127.0.0.1", "expires": -10}, "expires"},
		{"bad_same_site", map[string]any{"action": "set", "tab": tabID, "name": "bad_ss", "value": "v", "domain": "127.0.0.1", "same_site": "Bogus"}, "same_site"},
		{"get_stray_name", map[string]any{"action": "get", "tab": tabID, "name": "x"}, "action='get'"},
		{"get_stray_value", map[string]any{"action": "get", "tab": tabID, "value": ""}, "action='get'"},
		{"get_neg_limit", map[string]any{"action": "get", "tab": tabID, "limit": -1}, "limit"},
		{"set_stray_urls", map[string]any{"action": "set", "tab": tabID, "name": "k", "value": "v", "urls": []string{}}, "does not accept"},
		{"delete_stray_value", map[string]any{"action": "delete", "tab": tabID, "value": ""}, "action='delete'"},
		{"delete_stray_urls", map[string]any{"action": "delete", "tab": tabID, "urls": []string{}}, "action='delete'"},
	}
	for _, tc := range badCases {
		t.Run(tc.name, func(t *testing.T) {
			errText := callToolExpectErr(t, "cookies", tc.args)
			if !strings.Contains(errText, tc.want) {
				t.Errorf("%s err = %q, want substring %q", tc.name, errText, tc.want)
			}
		})
	}
}

func TestCookiesLimitAndTotalReporting(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)
	deleteCookiesHelper(t, map[string]any{"tab": tabID})
	defer deleteCookiesHelper(t, map[string]any{"tab": tabID})

	for _, name := range []string{"c1", "c2", "c3"} {
		setCookieHelper(t, map[string]any{
			"tab":    tabID,
			"name":   name,
			"value":  "val-" + name,
			"domain": "127.0.0.1",
		})
	}
	// Empty string value is valid for action='set'.
	setCookieHelper(t, map[string]any{
		"tab":    tabID,
		"name":   "c4_empty",
		"value":  "",
		"domain": "127.0.0.1",
	})

	capped := getCookiesHelper(t, map[string]any{"tab": tabID, "limit": 2})
	if len(cookieSlice(capped)) != 2 || capped.Total == nil || *capped.Total != 4 || !capped.Truncated {
		t.Fatalf("capped cookies = len=%d total=%v truncated=%v, want 2/4/true",
			len(cookieSlice(capped)), capped.Total, capped.Truncated)
	}

	all := getCookiesHelper(t, map[string]any{"tab": tabID})
	if len(cookieSlice(all)) != 4 || all.Total == nil || *all.Total != 4 || all.Truncated {
		t.Fatalf("all cookies = len=%d total=%v truncated=%v, want 4/4/false",
			len(cookieSlice(all)), all.Total, all.Truncated)
	}
}

func TestDeleteCookiesScopedByDomainPreservesOthers(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)
	defer deleteCookiesHelper(t, map[string]any{"tab": tabID})

	setCookieHelper(t, map[string]any{"tab": tabID, "name": "c_alpha", "value": "1", "domain": "alpha.test"})
	setCookieHelper(t, map[string]any{"tab": tabID, "name": "c_beta", "value": "2", "domain": "beta.test"})

	deleteCookiesHelper(t, map[string]any{"tab": tabID, "domain": "alpha.test"})

	alpha := getCookiesHelper(t, map[string]any{"tab": tabID, "urls": []string{"http://alpha.test/"}})
	if len(cookieSlice(alpha)) != 0 {
		t.Errorf("expected alpha.test cookies deleted, got: %+v", cookieSlice(alpha))
	}

	beta := getCookiesHelper(t, map[string]any{"tab": tabID, "urls": []string{"http://beta.test/"}})
	if len(cookieSlice(beta)) != 1 || cookieSlice(beta)[0].Name != "c_beta" {
		t.Errorf("expected beta.test cookie preserved, got: %+v", cookieSlice(beta))
	}
}

func TestDeleteCookiesScopedByPathPreservesOtherPathsAndDomains(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)
	defer deleteCookiesHelper(t, map[string]any{"tab": tabID})

	setCookieHelper(t, map[string]any{"tab": tabID, "name": "local_admin", "value": "1", "path": "/admin"})
	setCookieHelper(t, map[string]any{"tab": tabID, "name": "local_root", "value": "2", "path": "/"})
	setCookieHelper(t, map[string]any{"tab": tabID, "name": "other_admin", "value": "3", "domain": "other.test", "path": "/admin"})

	deleteCookiesHelper(t, map[string]any{"tab": tabID, "path": "/admin"})

	localAdmin := getCookiesHelper(t, map[string]any{"tab": tabID, "urls": []string{ensureHarness(t).httpSrv.URL + "/admin"}})
	for _, c := range cookieSlice(localAdmin) {
		if c.Name == "local_admin" {
			t.Errorf("expected local_admin deleted, still present: %+v", c)
		}
	}
	foundRoot := false
	for _, c := range cookieSlice(localAdmin) {
		if c.Name == "local_root" {
			foundRoot = true
		}
	}
	if !foundRoot {
		t.Errorf("expected local_root preserved, got: %+v", cookieSlice(localAdmin))
	}

	other := getCookiesHelper(t, map[string]any{"tab": tabID, "urls": []string{"http://other.test/admin"}})
	if len(cookieSlice(other)) != 1 || cookieSlice(other)[0].Name != "other_admin" {
		t.Errorf("expected other.test/admin cookie preserved, got: %+v", cookieSlice(other))
	}
}

func TestDeleteCookiesNameAndPathWithoutDomain(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)
	defer deleteCookiesHelper(t, map[string]any{"tab": tabID})

	setCookieHelper(t, map[string]any{"tab": tabID, "name": "admin_tok", "value": "secret", "path": "/admin"})
	setCookieHelper(t, map[string]any{"tab": tabID, "name": "admin_tok", "value": "root", "path": "/"})

	deleteCookiesHelper(t, map[string]any{"tab": tabID, "name": "admin_tok", "path": "/admin"})

	got := getCookiesHelper(t, map[string]any{"tab": tabID, "urls": []string{ensureHarness(t).httpSrv.URL + "/admin"}})
	for _, c := range cookieSlice(got) {
		if c.Name == "admin_tok" && c.Path == "/admin" {
			t.Fatalf("expected /admin cookie deleted, still present: %+v", c)
		}
	}
	foundRoot := false
	for _, c := range cookieSlice(got) {
		if c.Name == "admin_tok" && c.Path == "/" {
			foundRoot = true
		}
	}
	if !foundRoot {
		t.Fatalf("expected / cookie preserved, got: %+v", cookieSlice(got))
	}
}

func TestDeleteCookiesRejectsWhitespaceAndDotDomain(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)
	defer deleteCookiesHelper(t, map[string]any{"tab": tabID})

	setCookieHelper(t, map[string]any{"tab": tabID, "name": "keep_me", "value": "1", "domain": "alpha.test"})

	for _, bad := range []string{"   ", "."} {
		errText := callToolExpectErr(t, "cookies", map[string]any{
			"action": "delete",
			"tab":    tabID,
			"domain": bad,
		})
		if !strings.Contains(errText, "invalid cookie domain") {
			t.Errorf("cookies(delete, domain=%q) error = %q, want invalid cookie domain", bad, errText)
		}
	}

	got := getCookiesHelper(t, map[string]any{"tab": tabID, "urls": []string{"http://alpha.test/"}})
	if len(cookieSlice(got)) != 1 || cookieSlice(got)[0].Name != "keep_me" {
		t.Fatalf("invalid domain call wiped cookies: got %+v", cookieSlice(got))
	}
}

func TestCookies(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	setCookieHelper(t, map[string]any{
		"tab":    tabID,
		"name":   "test_cookie",
		"value":  "test_value",
		"domain": "127.0.0.1",
	})

	out := getCookiesHelper(t, map[string]any{"tab": tabID})
	found := false
	for _, c := range cookieSlice(out) {
		if c.Name == "test_cookie" && c.Value == "test_value" {
			found = true
		}
	}
	if !found {
		t.Error("set cookie not found in cookies(get) output")
	}

	deleteCookiesHelper(t, map[string]any{
		"tab":    tabID,
		"name":   "test_cookie",
		"domain": "127.0.0.1",
	})

	out = getCookiesHelper(t, map[string]any{"tab": tabID})
	for _, c := range cookieSlice(out) {
		if c.Name == "test_cookie" {
			t.Error("cookie still exists after delete")
		}
	}
}

func TestClearCookies(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	setCookieHelper(t, map[string]any{"tab": tabID, "name": "cookie_a", "value": "val_a", "domain": "127.0.0.1"})
	setCookieHelper(t, map[string]any{"tab": tabID, "name": "cookie_b", "value": "val_b", "domain": "127.0.0.1"})

	deleteCookiesHelper(t, map[string]any{"tab": tabID})

	out := getCookiesHelper(t, map[string]any{"tab": tabID})
	if len(cookieSlice(out)) != 0 {
		t.Errorf("after clear, expected 0 cookies, got %d", len(cookieSlice(out)))
	}
}

func TestCookieFields(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	setCookieHelper(t, map[string]any{
		"tab":       tabID,
		"name":      "field_test",
		"value":     "field_value",
		"domain":    "127.0.0.1",
		"http_only": true,
	})
	defer deleteCookiesHelper(t, map[string]any{"tab": tabID})

	out := getCookiesHelper(t, map[string]any{"tab": tabID})
	var found *cookieInfo
	list := cookieSlice(out)
	for i := range list {
		if list[i].Name == "field_test" {
			found = &list[i]
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

	setCookieHelper(t, map[string]any{"tab": tabID, "name": "path_test", "value": "pv", "domain": "127.0.0.1"})
	defer deleteCookiesHelper(t, map[string]any{"tab": tabID})

	out := getCookiesHelper(t, map[string]any{"tab": tabID})
	for _, c := range cookieSlice(out) {
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
	defer deleteCookiesHelper(t, map[string]any{"tab": tabID})

	setCookieHelper(t, map[string]any{
		"tab":    tabID,
		"name":   "path_cookie",
		"value":  "pv",
		"domain": "127.0.0.1",
		"path":   "/subdir",
	})

	out := getCookiesHelper(t, map[string]any{"tab": tabID, "urls": []string{ensureHarness(t).httpSrv.URL + "/subdir"}})
	for _, c := range cookieSlice(out) {
		if c.Name == "path_cookie" {
			if c.Path != "/subdir" {
				t.Errorf("cookie path = %q, want '/subdir'", c.Path)
			}
			return
		}
	}
	t.Error("cookie 'path_cookie' not found for /subdir")
}

func TestSetCookieSameSiteAndExpires(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)
	defer deleteCookiesHelper(t, map[string]any{"tab": tabID})

	expiry := float64(time.Now().Add(time.Hour).Unix())
	setCookieHelper(t, map[string]any{
		"tab":       tabID,
		"name":      "ss_lax",
		"value":     "ssv",
		"domain":    "127.0.0.1",
		"same_site": "Lax",
		"expires":   expiry,
	})
	setCookieHelper(t, map[string]any{
		"tab":       tabID,
		"name":      "ss_strict",
		"value":     "sv",
		"domain":    "127.0.0.1",
		"same_site": "Strict",
	})

	out := getCookiesHelper(t, map[string]any{"tab": tabID})
	seenLax, seenStrict := false, false
	for _, c := range cookieSlice(out) {
		switch c.Name {
		case "ss_lax":
			seenLax = true
			if c.SameSite != "Lax" || c.Expires <= 0 || c.Size <= 0 {
				t.Errorf("ss_lax = %+v", c)
			}
		case "ss_strict":
			seenStrict = true
			if c.SameSite != "Strict" {
				t.Errorf("ss_strict same_site = %q, want Strict", c.SameSite)
			}
		}
	}
	if !seenLax || !seenStrict {
		t.Fatalf("missing expected cookies: lax=%v strict=%v", seenLax, seenStrict)
	}
}

func TestDeleteCookieWithoutDomain(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)
	defer deleteCookiesHelper(t, map[string]any{"tab": tabID})

	setCookieHelper(t, map[string]any{"tab": tabID, "name": "no_domain_del", "value": "val", "domain": "127.0.0.1"})
	deleteCookiesHelper(t, map[string]any{"tab": tabID, "name": "no_domain_del"})

	after := getCookiesHelper(t, map[string]any{"tab": tabID})
	for _, c := range cookieSlice(after) {
		if c.Name == "no_domain_del" {
			t.Fatalf("cookie %q was not deleted when domain was omitted on HTTP page", c.Name)
		}
	}

	callTool[navigateOutput](t, "navigate", map[string]any{"tab": tabID, "url": "about:blank"})
	errText := callToolExpectErr(t, "cookies", map[string]any{
		"action": "delete",
		"tab":    tabID,
		"name":   "no_domain_del",
	})
	if !strings.Contains(errText, "domain") {
		t.Errorf("expected error about domain requirement on about:blank, got: %s", errText)
	}
}

func TestDeleteNonexistentAndIdempotentClear(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	deleteCookiesHelper(t, map[string]any{"tab": tabID, "name": "nonexistent_cookie_xyz", "domain": "127.0.0.1"})
	deleteCookiesHelper(t, map[string]any{"tab": tabID})
	deleteCookiesHelper(t, map[string]any{"tab": tabID})
}

func TestCookieOverwriteAndPathDelete(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)
	defer deleteCookiesHelper(t, map[string]any{"tab": tabID})

	setCookieHelper(t, map[string]any{"tab": tabID, "name": "overwrite_test", "value": "v1", "domain": "127.0.0.1"})
	setCookieHelper(t, map[string]any{"tab": tabID, "name": "overwrite_test", "value": "v2", "domain": "127.0.0.1"})

	out := getCookiesHelper(t, map[string]any{"tab": tabID})
	var found int
	var lastValue string
	for _, c := range cookieSlice(out) {
		if c.Name == "overwrite_test" {
			found++
			lastValue = c.Value
		}
	}
	if found != 1 || lastValue != "v2" {
		t.Errorf("overwrite_test found=%d val=%q, want 1/v2", found, lastValue)
	}

	deleteCookiesHelper(t, map[string]any{"tab": tabID, "name": "overwrite_test", "domain": "127.0.0.1", "path": "/"})
	out = getCookiesHelper(t, map[string]any{"tab": tabID})
	for _, c := range cookieSlice(out) {
		if c.Name == "overwrite_test" {
			t.Error("cookie should have been deleted")
		}
	}
}

func TestGetCookiesNoCookiesAndURLFilter(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	deleteCookiesHelper(t, map[string]any{"tab": tabID})

	rawEmpty := callToolRaw(t, "cookies", map[string]any{"action": "get", "tab": tabID})
	if rawEmpty.IsError || !strings.Contains(contentText(rawEmpty), `"cookies":[]`) {
		t.Fatalf("empty cookies(get) = %s (err=%v), want \"cookies\":[]", contentText(rawEmpty), rawEmpty.IsError)
	}

	setCookieHelper(t, map[string]any{"tab": tabID, "name": "url_test_cookie", "value": "hello", "domain": "127.0.0.1", "path": "/"})
	defer deleteCookiesHelper(t, map[string]any{"tab": tabID, "name": "url_test_cookie", "domain": "127.0.0.1"})

	out := getCookiesHelper(t, map[string]any{"tab": tabID, "urls": []string{fixtureURL(t, "index.html")}})
	found := false
	for _, c := range cookieSlice(out) {
		if c.Name == "url_test_cookie" && c.Value == "hello" {
			found = true
		}
	}
	if !found {
		t.Error("cookies(get) with matching urls did not return url_test_cookie")
	}

	out2 := getCookiesHelper(t, map[string]any{"tab": tabID, "urls": []string{"https://example.com/"}})
	for _, c := range cookieSlice(out2) {
		if c.Name == "url_test_cookie" {
			t.Error("cookies(get) with non-matching URL should not return url_test_cookie")
		}
	}
}
