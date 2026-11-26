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

const defaultCookiesLimit = 100

// cookieInfo represents a cookie in tool output.
type cookieInfo struct {
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

// cookiesInput is the input for cookies.
type cookiesInput struct {
	tabInput
	Action string `json:"action" jsonschema:"Cookie action: 'get', 'set', or 'delete'. Required."`

	// Get fields
	URLs  []string `json:"urls,omitempty" jsonschema:"For action='get': filter cookies to these URLs. If omitted returns cookies for the current page URL."`
	Limit int      `json:"limit,omitempty" jsonschema:"For action='get': max cookies to return (default 100)."`

	// Set & Delete fields
	Name     string  `json:"name,omitempty" jsonschema:"Cookie name (required for action='set'; optional filter for action='delete')."`
	Value    *string `json:"value,omitempty" jsonschema:"Cookie value (required for action='set'; empty string allowed)."`
	Domain   string  `json:"domain,omitempty" jsonschema:"Cookie domain (defaults to current HTTP(S) URL for action='set'; optional scope for action='delete')."`
	Path     string  `json:"path,omitempty" jsonschema:"Cookie path (default '/' for action='set'; optional scope for action='delete')."`
	Expires  float64 `json:"expires,omitempty" jsonschema:"For action='set': expiration as Unix timestamp. Omit for a session cookie."`
	HTTPOnly bool    `json:"http_only,omitempty" jsonschema:"For action='set': HTTP-only flag (default false)."`
	Secure   bool    `json:"secure,omitempty" jsonschema:"For action='set': Secure flag (default false)."`
	SameSite string  `json:"same_site,omitempty" jsonschema:"For action='set': SameSite attribute (Strict, Lax, or None)."`
}

// cookiesOutput is the output for cookies.
type cookiesOutput struct {
	Cookies   *[]cookieInfo `json:"cookies,omitempty"`
	Total     *int          `json:"total,omitempty"`
	Truncated bool          `json:"truncated,omitempty"`
}

var (
	cookiesActionValues  = []string{"get", "set", "delete"}
	cookieSameSiteValues = []string{"Strict", "Lax", "None"}
)

func registerCookieTools(s *mcp.Server, mgr *browser.Manager) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "cookies",
		Description: "Get, set, or delete browser cookies. action='get' returns cookies for the current page or urls (capped by limit, default 100, with total count); action='set' sets one cookie; action='delete' deletes matching cookies or clears all browser cookies when name, domain, and path are all omitted.",
		InputSchema: schemaWithEnums[cookiesInput](map[string][]string{
			"action":    cookiesActionValues,
			"same_site": cookieSameSiteValues,
		}),
		Annotations: destructiveAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input cookiesInput) (*mcp.CallToolResult, cookiesOutput, error) {
		if err := validateEnum("action", input.Action, false, cookiesActionValues); err != nil {
			return nil, cookiesOutput{}, err
		}

		switch input.Action {
		case "get":
			if input.Name != "" || input.Value != nil || input.Domain != "" || input.Path != "" ||
				input.Expires != 0 || input.HTTPOnly || input.Secure || input.SameSite != "" {
				return nil, cookiesOutput{}, fmt.Errorf("action='get' only accepts urls and limit (not cookie mutation fields)")
			}
			if err := validateLimit(input.Limit); err != nil {
				return nil, cookiesOutput{}, err
			}
			t, err := mgr.ResolveTab("", input.Tab)
			if err != nil {
				return nil, cookiesOutput{}, err
			}
			tctx, tcancel := tabContext(ctx, t.Context())
			defer tcancel()

			res, err := chromedp.Call(tctx, network.GetCookies, network.GetCookiesParams{
				URLs: input.URLs,
			})
			if err != nil {
				return nil, cookiesOutput{}, err
			}

			limit := input.Limit
			if limit == 0 {
				limit = defaultCookiesLimit
			}
			total := len(res.Cookies)
			truncated := total > limit
			shown := min(total, limit)

			infos := make([]cookieInfo, 0, shown)
			for _, c := range res.Cookies[:shown] {
				infos = append(infos, cookieInfo{
					Name:     c.Name,
					Value:    c.Value,
					Domain:   c.Domain,
					Path:     c.Path,
					Expires:  float64(c.Expires),
					Size:     int64(c.Size),
					HTTPOnly: c.HTTPOnly,
					Secure:   c.Secure,
					SameSite: string(c.SameSite),
				})
			}
			return nil, cookiesOutput{
				Cookies:   &infos,
				Total:     &total,
				Truncated: truncated,
			}, nil

		case "set":
			if input.URLs != nil || input.Limit != 0 {
				return nil, cookiesOutput{}, fmt.Errorf("action='set' does not accept urls or limit")
			}
			if input.Name == "" {
				return nil, cookiesOutput{}, fmt.Errorf("cookie name is required for action='set'")
			}
			if input.Value == nil {
				return nil, cookiesOutput{}, fmt.Errorf("cookie value is required for action='set'")
			}
			domain := strings.TrimSpace(input.Domain)
			if input.Domain != "" && (domain == "" || domain == ".") {
				return nil, cookiesOutput{}, fmt.Errorf("invalid cookie domain %q", input.Domain)
			}
			if err := validateNonNegativeFinite("expires", input.Expires); err != nil {
				return nil, cookiesOutput{}, err
			}
			if err := validateEnum("same_site", input.SameSite, true, cookieSameSiteValues); err != nil {
				return nil, cookiesOutput{}, err
			}

			t, err := mgr.ResolveTab("", input.Tab)
			if err != nil {
				return nil, cookiesOutput{}, err
			}
			tctx, tcancel := tabContext(ctx, t.Context())
			defer tcancel()

			path := input.Path
			if path == "" {
				path = "/"
			}
			params := network.SetCookieParams{
				Name:   input.Name,
				Value:  *input.Value,
				Domain: domain,
				Path:   path,
			}
			if params.Domain == "" {
				curURL, err := activeTabHTTPURL(tctx, t)
				if err != nil {
					return nil, cookiesOutput{}, err
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
				return nil, cookiesOutput{}, err
			}
			return nil, cookiesOutput{}, nil

		default: // "delete"
			if input.URLs != nil || input.Limit != 0 || input.Value != nil ||
				input.Expires != 0 || input.HTTPOnly || input.Secure || input.SameSite != "" {
				return nil, cookiesOutput{}, fmt.Errorf("action='delete' only accepts name, domain, and path")
			}
			domain := strings.TrimSpace(input.Domain)
			if input.Domain != "" && (domain == "" || domain == ".") {
				return nil, cookiesOutput{}, fmt.Errorf("invalid cookie domain %q", input.Domain)
			}

			t, err := mgr.ResolveTab("", input.Tab)
			if err != nil {
				return nil, cookiesOutput{}, err
			}
			tctx, tcancel := tabContext(ctx, t.Context())
			defer tcancel()

			if input.Name == "" && domain == "" && input.Path == "" {
				if _, err := chromedp.Call(tctx, network.ClearBrowserCookies, cdp.Empty{}); err != nil {
					return nil, cookiesOutput{}, err
				}
				return nil, cookiesOutput{}, nil
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
					return nil, cookiesOutput{}, uerr
				}
				u, _ := url.Parse(curURL)
				implicitHost = u.Hostname()
			}
			res, err := chromedp.Call(tctx, cdpstorage.GetCookies, cdpstorage.GetCookiesParams{})
			if err != nil {
				return nil, cookiesOutput{}, err
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
					return nil, cookiesOutput{}, derr
				}
			}
			return nil, cookiesOutput{}, nil
		}
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
