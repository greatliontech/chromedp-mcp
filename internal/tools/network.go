package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	cdpnetwork "github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/browser"
	"github.com/greatliontech/chromedp-mcp/internal/collector"
)

// GetNetworkRequestsInput is the input for get_network_requests.
type GetNetworkRequestsInput struct {
	TabInput
	Mode       string   `json:"mode" jsonschema:"Read mode: 'peek' to keep entries in the HTTP buffer, 'drain' to consume entries that match the filter. Required. WebSocket connections are never drained (long-lived); they are always returned by snapshot."`
	Limit      int      `json:"limit,omitempty" jsonschema:"Max entries to return (default all)"`
	Type       string   `json:"type,omitempty" jsonschema:"Filter by resource type: document stylesheet script image xhr fetch websocket other"`
	StatusMin  int      `json:"status_min,omitempty" jsonschema:"Filter by minimum HTTP status code"`
	StatusMax  int      `json:"status_max,omitempty" jsonschema:"Filter by maximum HTTP status code"`
	URLPattern string   `json:"url_pattern,omitempty" jsonschema:"Filter by URL substring match"`
	FailedOnly bool     `json:"failed_only,omitempty" jsonschema:"Return only failed requests (default false)"`
	Fields     []string `json:"fields,omitempty" jsonschema:"Project each entry to this set of JSON field names (e.g. ['id','method','url','status']). When omitted or empty, every field is returned. Useful to keep responses tiny when listing many entries."`
}

// GetNetworkRequestsOutput is the output for get_network_requests when
// no fields projection is requested.
type GetNetworkRequestsOutput struct {
	Requests []collector.NetworkEntry `json:"requests"`
}

// ProjectedNetworkRequestsOutput is the output for get_network_requests
// when the caller asked for a fields projection. Each request is a map
// containing only the requested keys.
type ProjectedNetworkRequestsOutput struct {
	Requests []map[string]any `json:"requests"`
}

// GetResponseBodyInput is the input for get_response_body.
type GetResponseBodyInput struct {
	TabInput
	RequestID  string `json:"request_id" jsonschema:"The request ID from get_network_requests"`
	RangeStart int64  `json:"range_start,omitempty" jsonschema:"Inclusive byte offset to start returning from (default 0). Negative values are treated as 0."`
	RangeEnd   int64  `json:"range_end,omitempty" jsonschema:"Exclusive byte offset to stop at (default 0 = end of body). When base64_encoded=true, offsets address the decoded binary, not the base64 text."`
}

// GetResponseBodyOutput is the output for get_response_body.
type GetResponseBodyOutput struct {
	Body          string `json:"body"`
	Base64Encoded bool   `json:"base64_encoded"`
	// TotalBytes is the size of the full body (decoded for base64
	// payloads) so the caller can plan range fetches.
	TotalBytes int64 `json:"total_bytes"`
	// Truncated is true when range_start/range_end caused the returned
	// body to be a strict subset of the full body.
	Truncated bool `json:"truncated,omitempty"`
}

// GetRequestBodyInput is the input for get_request_body.
type GetRequestBodyInput struct {
	TabInput
	RequestID  string `json:"request_id" jsonschema:"The request ID from get_network_requests"`
	RangeStart int64  `json:"range_start,omitempty" jsonschema:"Inclusive byte offset to start returning from (default 0). Negative values are treated as 0."`
	RangeEnd   int64  `json:"range_end,omitempty" jsonschema:"Exclusive byte offset to stop at (default 0 = end of body). When base64_encoded=true, offsets address the decoded binary, not the base64 text."`
}

// GetRequestBodyOutput is the output for get_request_body.
type GetRequestBodyOutput struct {
	Body          string `json:"body"`
	Base64Encoded bool   `json:"base64_encoded"`
	TotalBytes    int64  `json:"total_bytes"`
	Truncated     bool   `json:"truncated,omitempty"`
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
		Name:         "get_network_requests",
		Description:  "Get captured network requests (HTTP and WebSocket connections) with their URLs, methods, status codes, timing, and headers. WebSocket entries include frame counts; use get_websocket_frames to read the actual frames. 'mode' must be 'peek' (keep entries in the HTTP buffer) or 'drain' (consume entries that match the filter). WebSocket connections are long-lived and always returned by snapshot regardless of mode. Pass 'fields' (e.g. ['id','method','url','status']) to project each entry to a subset of JSON keys — useful when listing many entries.",
		InputSchema:  modeSchemaFor[GetNetworkRequestsInput](ModePeek, ModeDrain),
		OutputSchema: networkRequestsOutputSchema(),
		Annotations:  &mcp.ToolAnnotations{},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GetNetworkRequestsInput) (*mcp.CallToolResult, any, error) {
		if err := validateMode(input.Mode, ModePeek, ModeDrain); err != nil {
			return nil, nil, err
		}
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, nil, err
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

		if len(input.Fields) == 0 {
			return nil, GetNetworkRequestsOutput{Requests: requests}, nil
		}
		projected, err := projectEntries(requests, input.Fields)
		if err != nil {
			return nil, nil, err
		}
		return nil, ProjectedNetworkRequestsOutput{Requests: projected}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_response_body",
		Description: "Get the response body of a specific network request by its request ID. Use range_start/range_end to fetch a slice of large bodies; total_bytes always reports the full body size so you can plan paging.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:   true,
			IdempotentHint: true,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GetResponseBodyInput) (*mcp.CallToolResult, GetResponseBodyOutput, error) {
		if err := validateRange(input.RangeStart, input.RangeEnd); err != nil {
			return nil, GetResponseBodyOutput{}, err
		}
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

		out := encodeBodySlice(bodyBytes, input.RangeStart, input.RangeEnd)
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_request_body",
		Description: "Get the full request body (POST/PUT/PATCH/DELETE payload) of a specific network request by its request ID. Use when get_network_requests returned has_request_body=true and the inline request_body was empty or truncated. Use range_start/range_end to fetch a slice of large bodies; total_bytes always reports the full body size.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:   true,
			IdempotentHint: true,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GetRequestBodyInput) (*mcp.CallToolResult, GetRequestBodyOutput, error) {
		if err := validateRange(input.RangeStart, input.RangeEnd); err != nil {
			return nil, GetRequestBodyOutput{}, err
		}
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
		resp := encodeBodySlice([]byte(postData), input.RangeStart, input.RangeEnd)
		return nil, GetRequestBodyOutput{
			Body:          resp.Body,
			Base64Encoded: resp.Base64Encoded,
			TotalBytes:    resp.TotalBytes,
			Truncated:     resp.Truncated,
		}, nil
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

// encodeBodySlice slices a body to the given byte range and returns the
// shape get_response_body / get_request_body produce. Text vs binary is
// classified on the *full* body so a UTF-8 body sliced mid-codepoint
// stays text — the slice is trimmed back to the last valid rune
// boundary on either end. Binary bodies are base64-encoded as before.
func encodeBodySlice(body []byte, start, end int64) GetResponseBodyOutput {
	total := int64(len(body))
	isText := utf8.Valid(body)
	sliced, truncated := applyByteRange(body, start, end)
	out := GetResponseBodyOutput{TotalBytes: total, Truncated: truncated}
	if isText {
		text := trimByteSliceToValidUTF8(sliced)
		out.Body = string(text)
		return out
	}
	out.Body = base64.StdEncoding.EncodeToString(sliced)
	out.Base64Encoded = true
	return out
}

// networkRequestsOutputSchema declares an OutputSchema that's loose
// enough to cover both response shapes get_network_requests produces:
// the typed default (entries with the full NetworkEntry field set) and
// the projected case (entries with only the requested fields). Both
// shapes serialize to {requests: [object, ...]}.
//
// Without this, the handler's `Out: any` return type would leave
// OutputSchema nil and schema-aware MCP clients lose any structural
// hint about the response shape.
func networkRequestsOutputSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"requests": {
				Type:        "array",
				Description: "Network entries (HTTP requests and WebSocket connections). Each entry is an object whose keys depend on whether 'fields' was passed: by default, every NetworkEntry field is present; when 'fields' is passed, only the requested keys appear.",
				Items:       &jsonschema.Schema{Type: "object"},
			},
		},
		Required: []string{"requests"},
	}
}

// projectEntries serializes each NetworkEntry to a generic JSON map and
// strips keys not in the requested fields list. The map shape (rather
// than reflective struct projection) handles every JSON tag — including
// the conditionally-emitted WebSocket fields and request-body fields —
// without per-field special cases.
//
// Returns an error if any requested field name doesn't appear on the
// entry shape, so a typo from the LLM doesn't silently produce empty
// rows.
func projectEntries(entries []collector.NetworkEntry, fields []string) ([]map[string]any, error) {
	allowed := make(map[string]struct{}, len(fields))
	for _, f := range fields {
		allowed[f] = struct{}{}
	}
	for f := range allowed {
		if _, ok := networkEntryFieldSet[f]; !ok {
			return nil, fmt.Errorf("unknown field %q in fields list; available: %v",
				f, sortedKeys(networkEntryFieldSet))
		}
	}

	out := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		blob, err := json.Marshal(e)
		if err != nil {
			return nil, err
		}
		var m map[string]any
		if err := json.Unmarshal(blob, &m); err != nil {
			return nil, err
		}
		for k := range m {
			if _, ok := allowed[k]; !ok {
				delete(m, k)
			}
		}
		out = append(out, m)
	}
	return out, nil
}

// networkEntryFieldSet is the set of JSON keys producible by a
// NetworkEntry's serialization, used to validate caller-supplied field
// names. Computed eagerly at package init from a probe entry whose
// every field is set to a non-zero value so omitempty doesn't drop
// keys from the set. Eager (rather than lazy via sync.OnceValue) so a
// future struct change introducing an unmarshalable type fails the
// server at startup rather than mid-LLM-session — a successful binary
// startup proves the probe serializes cleanly.
var networkEntryFieldSet = buildNetworkEntryFieldSet()

func buildNetworkEntryFieldSet() map[string]struct{} {
	one := int64(1)
	probe := collector.NetworkEntry{
		ID:                   "x",
		URL:                  "x",
		Method:               "x",
		Status:               1,
		Type:                 "x",
		RequestHeaders:       map[string]string{"x": "x"},
		ResponseHeaders:      map[string]string{"x": "x"},
		Size:                 1,
		Timing:               &collector.TimingInfo{TotalTime: 1},
		Error:                "x",
		StartTime:            time.Unix(1, 0),
		EndTime:              time.Unix(1, 0),
		Failed:               true,
		HasRequestBody:       true,
		RequestBody:          "x",
		RequestBodyBase64:    true,
		RequestBodyTruncated: true,
		FramesSentCount:      &one,
		FramesReceivedCount:  &one,
	}
	blob, err := json.Marshal(probe)
	if err != nil {
		// json.Marshal of a struct with no funky types is infallible;
		// if this ever fires it's a programmer error and crashing at
		// init() is exactly the signal we want.
		panic(fmt.Sprintf("buildNetworkEntryFieldSet: probe marshal: %v", err))
	}
	var m map[string]any
	if err := json.Unmarshal(blob, &m); err != nil {
		panic(fmt.Sprintf("buildNetworkEntryFieldSet: probe unmarshal: %v", err))
	}
	out := make(map[string]struct{}, len(m))
	for k := range m {
		out[k] = struct{}{}
	}
	return out
}

// sortedKeys returns the keys of a set in stable order for predictable
// error messages.
func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// applyByteRange returns the slice of body addressed by [start, end).
// end == 0 means "to the end of body"; end > len(body) is clamped to
// len(body); start > len(body) returns empty.
//
// Negative start/end are rejected by the caller (validateRange) so this
// helper assumes both are non-negative.
//
// The returned truncated flag is true iff the slice is shorter than the
// original body.
func applyByteRange(body []byte, start, end int64) (slice []byte, truncated bool) {
	total := int64(len(body))
	if start > total {
		return body[total:total], true
	}
	if end == 0 || end > total {
		end = total
	}
	if end < start {
		end = start
	}
	out := body[start:end]
	return out, int64(len(out)) != total
}

// validateRange returns an error when range_start or range_end are
// negative. Negative offsets are silently masked by Python-style
// indexing in some languages — we reject them so the LLM gets a clear
// "you typed -5, did you mean omit the field?" signal instead of
// quietly receiving the whole body.
func validateRange(start, end int64) error {
	if start < 0 {
		return fmt.Errorf("range_start must be >= 0; got %d (negative offsets are not supported)", start)
	}
	if end < 0 {
		return fmt.Errorf("range_end must be >= 0; got %d (use 0 to mean end of body)", end)
	}
	return nil
}

// trimByteSliceToValidUTF8 walks back the trailing bytes of a slice
// that are part of a partial multi-byte rune, so a slice cut from a
// known-text body stays valid UTF-8 instead of being misclassified as
// binary and base64-encoded. Same for a leading byte that's a UTF-8
// continuation (0x80–0xBF, no leading-byte marker).
//
// At most 3 bytes are removed from each end since UTF-8 codepoints are
// at most 4 bytes long.
func trimByteSliceToValidUTF8(b []byte) []byte {
	for len(b) > 0 && !utf8.RuneStart(b[0]) {
		b = b[1:]
	}
	for len(b) > 0 && !utf8.Valid(b) {
		b = b[:len(b)-1]
	}
	return b
}
