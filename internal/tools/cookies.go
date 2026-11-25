package tools

import (
	"context"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/browser"
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

func registerCookieTools(s *mcp.Server, mgr *browser.Manager) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_cookies",
		Description: "Get browser cookies for the current page or specified URLs.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:   true,
			IdempotentHint: true,
		},
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
		Annotations: &mcp.ToolAnnotations{
			IdempotentHint: true,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input SetCookieInput) (*mcp.CallToolResult, struct{}, error) {
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
			Domain: input.Domain,
			Path:   path,
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
		Description: "Delete cookies by name, optionally scoped to a domain and path. If name is omitted, deletes all cookies.",
		Annotations: &mcp.ToolAnnotations{
			DestructiveHint: new(true),
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input DeleteCookiesInput) (*mcp.CallToolResult, struct{}, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		if input.Name == "" {
			// No name specified — clear all browser cookies.
			_, err = chromedp.Call(tctx, network.ClearBrowserCookies, cdp.Empty{})
		} else {
			_, err = chromedp.Call(tctx, network.DeleteCookies, network.DeleteCookiesParams{
				Name:   input.Name,
				Domain: input.Domain,
				Path:   input.Path,
			})
		}
		if err != nil {
			return nil, struct{}{}, err
		}
		return nil, struct{}{}, nil
	})
}
