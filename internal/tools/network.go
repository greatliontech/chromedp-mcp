package tools

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"unicode/utf8"

	cdpnetwork "github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/browser"
	"github.com/greatliontech/chromedp-mcp/internal/collector"
)

// GetNetworkRequestsInput is the input for get_network_requests.
type GetNetworkRequestsInput struct {
	TabInput
	Mode       string `json:"mode" jsonschema:"Read mode: 'peek' to keep entries in the HTTP buffer, 'drain' to consume entries that match the filter. Required. WebSocket connections are never drained (long-lived); they are always returned by snapshot."`
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

// GetRequestBodyInput is the input for get_request_body.
type GetRequestBodyInput struct {
	TabInput
	RequestID string `json:"request_id" jsonschema:"The request ID from get_network_requests"`
}

// GetRequestBodyOutput is the output for get_request_body.
type GetRequestBodyOutput struct {
	Body          string `json:"body"`
	Base64Encoded bool   `json:"base64_encoded"`
}

// GetWebSocketFramesInput is the input for get_websocket_frames.
type GetWebSocketFramesInput struct {
	TabInput
	RequestID string `json:"request_id" jsonschema:"The websocket connection's request ID from get_network_requests"`
	Direction string `json:"direction,omitempty" jsonschema:"Frame direction: sent received or both (default both)"`
	Limit     int    `json:"limit,omitempty" jsonschema:"Max frames to return per direction (default all buffered)"`
	Mode      string `json:"mode" jsonschema:"Read mode: 'peek' to keep frames in the buffer, 'drain' to consume them. Required."`
}

// GetWebSocketFramesOutput is the output for get_websocket_frames.
type GetWebSocketFramesOutput struct {
	Sent     []collector.WSFrame `json:"sent"`
	Received []collector.WSFrame `json:"received"`
}

func registerNetworkTools(s *mcp.Server, mgr *browser.Manager) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_network_requests",
		Description: "Get captured network requests (HTTP and WebSocket connections) with their URLs, methods, status codes, timing, and headers. WebSocket entries include frame counts; use get_websocket_frames to read the actual frames. 'mode' must be 'peek' (keep entries in the HTTP buffer) or 'drain' (consume entries that match the filter). WebSocket connections are long-lived and always returned by snapshot regardless of mode.",
		InputSchema: modeSchemaFor[GetNetworkRequestsInput](ModePeek, ModeDrain),
		Annotations: &mcp.ToolAnnotations{},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GetNetworkRequestsInput) (*mcp.CallToolResult, GetNetworkRequestsOutput, error) {
		if err := validateMode(input.Mode, ModePeek, ModeDrain); err != nil {
			return nil, GetNetworkRequestsOutput{}, err
		}
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

		// Merge HTTP and WebSocket entries before applying limit so a
		// large HTTP buffer cannot starve WS visibility (a small limit
		// must not silently hide active sockets). The HTTP collector is
		// asked for everything matching its filter; WS connections are
		// projected into NetworkEntry shape and run through the same
		// filter via collector.MatchesFilter so semantics are identical.
		var httpEntries []collector.NetworkEntry
		if !strings.EqualFold(input.Type, "websocket") {
			// type=websocket would filter out every HTTP entry anyway;
			// skip the work (and avoid touching the HTTP buffer at all
			// when mode=drain).
			if input.Mode == ModePeek {
				httpEntries = t.Network.Peek(f, 0)
			} else {
				httpEntries = t.Network.Drain(f, 0)
			}
		}
		// WebSocket connections are long-lived; always peek so a single
		// get_network_requests call doesn't lose visibility into open
		// sockets, regardless of input.Peek.
		wsEntries := websocketEntries(t.WebSocket.Connections(), f)

		requests := make([]collector.NetworkEntry, 0, len(httpEntries)+len(wsEntries))
		requests = append(requests, httpEntries...)
		requests = append(requests, wsEntries...)
		if input.Limit > 0 && len(requests) > input.Limit {
			requests = requests[:input.Limit]
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

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		var bodyBytes []byte
		err = chromedp.Run(tctx, chromedp.ActionFunc(func(ctx context.Context) error {
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

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_request_body",
		Description: "Get the full request body (POST/PUT/PATCH/DELETE payload) of a specific network request by its request ID. Use when get_network_requests returned has_request_body=true and the inline request_body was empty or truncated.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:   true,
			IdempotentHint: true,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GetRequestBodyInput) (*mcp.CallToolResult, GetRequestBodyOutput, error) {
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, GetRequestBodyOutput{}, err
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		var postData string
		err = chromedp.Run(tctx, chromedp.ActionFunc(func(ctx context.Context) error {
			var err error
			postData, err = cdpnetwork.GetRequestPostData(cdpnetwork.RequestID(input.RequestID)).Do(ctx)
			return err
		}))
		if err != nil {
			return nil, GetRequestBodyOutput{}, err
		}

		// CDP returns postData as a string. Multipart form uploads with
		// binary parts (files) are excluded by Chrome — see CDP docs:
		// "Request body string, omitting files from multipart requests".
		// Detect non-UTF8 anyway for safety.
		if !isValidUTF8(postData) {
			return nil, GetRequestBodyOutput{
				Body:          base64.StdEncoding.EncodeToString([]byte(postData)),
				Base64Encoded: true,
			}, nil
		}
		return nil, GetRequestBodyOutput{Body: postData, Base64Encoded: false}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_websocket_frames",
		Description: "Get WebSocket frames sent and/or received on a specific connection. Use the request ID returned for entries with type=websocket from get_network_requests. 'mode' must be 'peek' (keep frames buffered) or 'drain' (consume them).",
		InputSchema: modeSchemaFor[GetWebSocketFramesInput](ModePeek, ModeDrain),
		Annotations: &mcp.ToolAnnotations{},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GetWebSocketFramesInput) (*mcp.CallToolResult, GetWebSocketFramesOutput, error) {
		if err := validateMode(input.Mode, ModePeek, ModeDrain); err != nil {
			return nil, GetWebSocketFramesOutput{}, err
		}
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, GetWebSocketFramesOutput{}, err
		}

		direction := strings.ToLower(strings.TrimSpace(input.Direction))
		if direction == "" {
			direction = "both"
		}
		switch direction {
		case "sent", "received", "both":
		default:
			return nil, GetWebSocketFramesOutput{}, fmt.Errorf("direction must be sent, received, or both")
		}

		peek := input.Mode == ModePeek
		out := GetWebSocketFramesOutput{
			Sent:     []collector.WSFrame{},
			Received: []collector.WSFrame{},
		}
		var found bool
		if direction == "sent" || direction == "both" {
			frames, ok := t.WebSocket.Frames(input.RequestID, collector.WSDirectionSent, input.Limit, peek)
			if ok {
				found = true
				if frames != nil {
					out.Sent = frames
				}
			}
		}
		if direction == "received" || direction == "both" {
			frames, ok := t.WebSocket.Frames(input.RequestID, collector.WSDirectionReceived, input.Limit, peek)
			if ok {
				found = true
				if frames != nil {
					out.Received = frames
				}
			}
		}
		if !found {
			return nil, GetWebSocketFramesOutput{}, fmt.Errorf("no websocket connection with request_id %q", input.RequestID)
		}
		return nil, out, nil
	})
}

// websocketEntries projects WSConnections into NetworkEntry shape so
// get_network_requests can return a single unified list, then applies the
// shared collector filter so HTTP and WS entries are filtered identically.
func websocketEntries(conns []collector.WSConnection, f *collector.NetworkFilter) []collector.NetworkEntry {
	out := make([]collector.NetworkEntry, 0, len(conns))
	for _, c := range conns {
		sent := c.FramesSentCount
		received := c.FramesReceivedCount
		// Failed covers two cases: (a) a CDP-reported frame error, or (b)
		// a handshake that returned any non-101 HTTP status (401, 403,
		// 404, etc. mean the upgrade was rejected). Status==0 is "still
		// pending" and not a failure.
		failed := c.Error != "" ||
			(c.ResponseStatus != 0 && c.ResponseStatus != 101)
		entry := collector.NetworkEntry{
			ID:                  c.ID,
			URL:                 c.URL,
			Method:              "GET", // WebSocket upgrades are HTTP GET.
			Status:              c.ResponseStatus,
			Type:                "websocket",
			RequestHeaders:      c.RequestHeaders,
			ResponseHeaders:     c.ResponseHeaders,
			StartTime:           c.StartTime,
			EndTime:             c.EndTime,
			Failed:              failed,
			Error:               c.Error,
			FramesSentCount:     &sent,
			FramesReceivedCount: &received,
		}
		if !collector.MatchesFilter(f, &entry) {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// isValidUTF8 checks if a byte slice is valid UTF-8 text.
func isValidUTF8(s string) bool {
	return utf8.ValidString(s)
}
