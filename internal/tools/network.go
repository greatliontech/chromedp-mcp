package tools

import (
	"context"
	"encoding/base64"
	"unicode/utf8"

	cdpnetwork "github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/thegrumpylion/chromedp-mcp/internal/browser"
	"github.com/thegrumpylion/chromedp-mcp/internal/collector"
)

// GetNetworkRequestsInput is the input for get_network_requests.
type GetNetworkRequestsInput struct {
	TabInput
	Peek       bool   `json:"peek,omitempty" jsonschema:"If true do not clear the buffer (default false)"`
	Limit      int    `json:"limit,omitempty" jsonschema:"Max entries to return (default all)"`
	Type       string `json:"type,omitempty" jsonschema:"Filter by resource type: document stylesheet script image xhr fetch websocket other"`
	StatusMin  int    `json:"status_min,omitempty" jsonschema:"Filter by minimum HTTP status code"`
	StatusMax  int    `json:"status_max,omitempty" jsonschema:"Filter by maximum HTTP status code"`
	URLPattern string `json:"url_pattern,omitempty" jsonschema:"Filter by URL substring match"`
	FailedOnly bool   `json:"failed_only,omitempty" jsonschema:"Return only failed requests (default false)"`
}

// GetNetworkRequestsOutput is the output for get_network_requests.
type GetNetworkRequestsOutput struct {
	Requests []collector.NetworkEntry `json:"requests"`
}

// GetResponseBodyInput is the input for get_response_body.
type GetResponseBodyInput struct {
	TabInput
	RequestID string `json:"request_id" jsonschema:"The request ID from get_network_requests"`
}

// GetResponseBodyOutput is the output for get_response_body.
type GetResponseBodyOutput struct {
	Body          string `json:"body"`
	Base64Encoded bool   `json:"base64_encoded"`
}

func registerNetworkTools(s *mcp.Server, mgr *browser.Manager) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_network_requests",
		Description: "Get captured network requests with their URLs, methods, status codes, timing, and headers. By default drains the buffer.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GetNetworkRequestsInput) (*mcp.CallToolResult, GetNetworkRequestsOutput, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, GetNetworkRequestsOutput{}, err
		}

		f := &collector.NetworkFilter{
			Type:       input.Type,
			StatusMin:  input.StatusMin,
			StatusMax:  input.StatusMax,
			URLPattern: input.URLPattern,
			FailedOnly: input.FailedOnly,
		}

		var requests []collector.NetworkEntry
		if input.Peek {
			requests = t.Network.Peek(f, input.Limit)
		} else {
			requests = t.Network.Drain(f, input.Limit)
		}
		if requests == nil {
			requests = []collector.NetworkEntry{}
		}
		return nil, GetNetworkRequestsOutput{Requests: requests}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_response_body",
		Description: "Get the response body of a specific network request by its request ID.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:   true,
			IdempotentHint: true,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GetResponseBodyInput) (*mcp.CallToolResult, GetResponseBodyOutput, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, GetResponseBodyOutput{}, err
		}

		var bodyBytes []byte
		err = chromedp.Run(t.Context(), chromedp.ActionFunc(func(ctx context.Context) error {
			var err error
			bodyBytes, err = cdpnetwork.GetResponseBody(cdpnetwork.RequestID(input.RequestID)).Do(ctx)
			return err
		}))
		if err != nil {
			return nil, GetResponseBodyOutput{}, err
		}

		body := string(bodyBytes)
		// If the body contains non-UTF8 data, base64 encode it.
		if !isValidUTF8(body) {
			return nil, GetResponseBodyOutput{
				Body:          base64.StdEncoding.EncodeToString(bodyBytes),
				Base64Encoded: true,
			}, nil
		}
		return nil, GetResponseBodyOutput{Body: body, Base64Encoded: false}, nil
	})
}

// isValidUTF8 checks if a byte slice is valid UTF-8 text.
func isValidUTF8(s string) bool {
	return utf8.ValidString(s)
}
