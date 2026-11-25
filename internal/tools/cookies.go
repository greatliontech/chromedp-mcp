package tools

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	cdpstorage "github.com/chromedp/cdproto/storage"
	"github.com/chromedp/chromedp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/browser"
	"github.com/greatliontech/chromedp-mcp/internal/tab"
)

// CookieInfo represents a cookie in tool output.
type CookieInfo struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Domain   string  `json:"domain"`
	Path     string  `json:"path"`
	Expires  float64 `json:"expires,omitempty"`
	Size     int64   `json:"size"`
	HTTPOnly bool    `json:"http_only"`
	Secure   bool    `json:"secure"`
	SameSite string  `json:"same_site,omitempty"`
}

// GetCookiesInput is the input for get_cookies.
type GetCookiesInput struct {
	TabInput
	URLs []string `json:"urls,omitempty" jsonschema:"Filter cookies to these URLs. If omitted returns cookies for the current page URL."`
}

// GetCookiesOutput is the output for get_cookies.
type GetCookiesOutput struct {
	Cookies []CookieInfo `json:"cookies"`
}

// SetCookieInput is the input for set_cookie.
type SetCookieInput struct {
	TabInput
	Name     string  `json:"name" jsonschema:"Cookie name"`
	Value    string  `json:"value" jsonschema:"Cookie value"`
	Domain   string  `json:"domain,omitempty" jsonschema:"Cookie domain"`
	Path     string  `json:"path,omitempty" jsonschema:"Cookie path (default /)"`
	Expires  float64 `json:"expires,omitempty" jsonschema:"Cookie expiration as Unix timestamp. If omitted creates a session cookie."`
	HTTPOnly bool    `json:"http_only,omitempty" jsonschema:"HTTP-only flag (default false)"`
	Secure   bool    `json:"secure,omitempty" jsonschema:"Secure flag (default false)"`
	SameSite string  `json:"same_site,omitempty" jsonschema:"SameSite attribute: Strict Lax None"`
}

// DeleteCookiesInput is the input for delete_cookies.
type DeleteCookiesInput struct {
	TabInput
	Name   string `json:"name,omitempty" jsonschema:"Cookie name to delete. If omitted deletes all cookies."`
	Domain string `json:"domain,omitempty" jsonschema:"Scope deletion to a domain"`
	Path   string `json:"path,omitempty" jsonschema:"Scope deletion to a path"`
}

var cookieSameSiteValues = []string{"Strict", "Lax", "None"}

func registerCookieTools(s *mcp.Server, mgr *browser.Manager) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_cookies",
		Description: "Get browser cookies for the current page or specified URLs.",
		Annotations: readOnlyAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GetCookiesInput) (*mcp.CallToolResult, GetCookiesOutput, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, GetCookiesOutput{}, err
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		res, err := chromedp.Call(tctx, network.GetCookies, network.GetCookiesParams{
			URLs: input.URLs,
		})
		if err != nil {
			return nil, GetCookiesOutput{}, err
		}

		infos := make([]CookieInfo, 0, len(res.Cookies))
		for _, c := range res.Cookies {
			ci := CookieInfo{
				Name:     c.Name,
				Value:    c.Value,
				Domain:   c.Domain,
				Path:     c.Path,
				Expires:  float64(c.Expires),
				Size:     int64(c.Size),
				HTTPOnly: c.HTTPOnly,
				Secure:   c.Secure,
				SameSite: string(c.SameSite),
			}
			infos = append(infos, ci)
		}
		return nil, GetCookiesOutput{Cookies: infos}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "set_cookie",
		Description: "Set a browser cookie.",
		InputSchema: schemaWithEnums[SetCookieInput](map[string][]string{
			"same_site": cookieSameSiteValues,
		}),
		Annotations: idempotentWriteAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input SetCookieInput) (*mcp.CallToolResult, struct{}, error) {
		if input.Name == "" {
			return nil, struct{}{}, fmt.Errorf("cookie name is required")
		}
		domain := strings.TrimSpace(input.Domain)
		if input.Domain != "" && (domain == "" || domain == ".") {
			return nil, struct{}{}, fmt.Errorf("invalid cookie domain %q", input.Domain)
		}
		if err := validateNonNegativeFinite("expires", input.Expires); err != nil {
			return nil, struct{}{}, err
		}
		if err := validateEnum("same_site", input.SameSite, true, cookieSameSiteValues); err != nil {
			return nil, struct{}{}, err
		}

		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		path := input.Path
		if path == "" {
			path = "/"
		}
		params := network.SetCookieParams{
			Name:   input.Name,
			Value:  input.Value,
			Domain: domain,
			Path:   path,
		}
		if params.Domain == "" {
			curURL, err := activeTabHTTPURL(tctx, t)
			if err != nil {
				return nil, struct{}{}, err
			}
			params.URL = curURL
		}
		if input.Expires > 0 {
			params.Expires = cdp.TimeSinceEpoch(input.Expires)
		}
		if input.HTTPOnly {
			params.HTTPOnly = new(true)
		}
		if input.Secure {
			params.Secure = new(true)
		}
		if input.SameSite != "" {
			params.SameSite = network.CookieSameSite(input.SameSite)
		}
		if _, err := chromedp.Call(tctx, network.SetCookie, params); err != nil {
			return nil, struct{}{}, err
		}
		return nil, struct{}{}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "delete_cookies",
		Description: "Delete cookies by name, optionally scoped to a domain and path. If name, domain, and path are all omitted, clears all browser cookies. If name is omitted with domain or path set, deletes only matching cookies.",
		Annotations: destructiveAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input DeleteCookiesInput) (*mcp.CallToolResult, struct{}, error) {
		domain := strings.TrimSpace(input.Domain)
		if input.Domain != "" && (domain == "" || domain == ".") {
			return nil, struct{}{}, fmt.Errorf("invalid cookie domain %q", input.Domain)
		}

		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()

		if input.Name == "" && domain == "" && input.Path == "" {
			// No filter specified — clear all browser cookies.
			if _, err := chromedp.Call(tctx, network.ClearBrowserCookies, cdp.Empty{}); err != nil {
				return nil, struct{}{}, err
			}
			return nil, struct{}{}, nil
		}

		// Filtered delete (by any combination of name, domain, and path):
		// enumerate browser cookies via Storage.getCookies so domain/path
		// matching is consistent across all filter combinations, cookies with
		// a path different from the current page path are still matched, and
		// CHIPS PartitionKey is preserved on delete.
		var implicitHost string
		if domain == "" {
			curURL, uerr := activeTabHTTPURL(tctx, t)
			if uerr != nil {
				return nil, struct{}{}, uerr
			}
			u, _ := url.Parse(curURL)
			implicitHost = u.Hostname()
		}
		res, err := chromedp.Call(tctx, cdpstorage.GetCookies, cdpstorage.GetCookiesParams{})
		if err != nil {
			return nil, struct{}{}, err
		}
		for _, c := range res.Cookies {
			if input.Name != "" && c.Name != input.Name {
				continue
			}
			if domain != "" {
				if !cookieDomainMatches(c.Domain, domain) {
					continue
				}
			} else if !cookieAppliesToHost(c.Domain, implicitHost) {
				continue
			}
			if input.Path != "" && c.Path != input.Path {
				continue
			}
			if _, derr := chromedp.Call(tctx, network.DeleteCookies, network.DeleteCookiesParams{
				Name:         c.Name,
				Domain:       c.Domain,
				Path:         c.Path,
				PartitionKey: c.PartitionKey,
			}); derr != nil {
				return nil, struct{}{}, derr
			}
		}
		return nil, struct{}{}, nil
	})
}

// activeTabHTTPURL returns the active tab's current HTTP(S) URL for cookie
// operations that omit domain.
func activeTabHTTPURL(ctx context.Context, t *tab.Tab) (string, error) {
	rawURL, _, err := t.Info(ctx)
	if err != nil {
		return "", fmt.Errorf("resolve active tab URL for cookie operation: %w", err)
	}
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("domain is required when the tab is not on an HTTP(S) page (current URL: %q)", rawURL)
	}
	return rawURL, nil
}

// normalizeCookieHost lowercases, trims whitespace, and strips brackets from
// IPv6 literals (Chromium stores "[::1]" while url.URL.Hostname returns "::1").
func normalizeCookieHost(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		s = s[1 : len(s)-1]
	}
	return s
}

// cookieDomainMatches reports whether a cookie's Domain matches the requested
// filter domain. A leading-dot filter (e.g. ".example.com") matches the apex
// domain and all subdomains; a plain filter ("example.com") matches exact or
// leading-dot cookies for that host.
func cookieDomainMatches(cookieDomain, filterDomain string) bool {
	cd := normalizeCookieHost(cookieDomain)
	fd := normalizeCookieHost(filterDomain)
	if cd == "" || fd == "" || fd == "." {
		return false
	}
	if strings.HasPrefix(fd, ".") {
		bare := fd[1:]
		return cd == fd || cd == bare || strings.HasSuffix(cd, fd)
	}
	return strings.TrimPrefix(cd, ".") == fd
}

// cookieAppliesToHost reports whether a cookie with cookieDomain applies to
// host according to RFC 6265 domain matching (exact host match, or leading-dot
// domain cookie matching apex or subdomain).
func cookieAppliesToHost(cookieDomain, host string) bool {
	cd := normalizeCookieHost(cookieDomain)
	h := normalizeCookieHost(host)
	if cd == "" || h == "" {
		return false
	}
	if strings.HasPrefix(cd, ".") {
		bare := cd[1:]
		return bare != "" && (h == bare || strings.HasSuffix(h, cd))
	}
	return cd == h
}
