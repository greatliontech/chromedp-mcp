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

// BodyRangeInput is the shared input for get_response_body and get_request_body.
type BodyRangeInput struct {
	TabInput
	RequestID  string `json:"request_id" jsonschema:"The request ID from get_network_requests"`
	RangeStart int64  `json:"range_start,omitempty" jsonschema:"Inclusive byte offset to start returning from (default 0, must be >= 0)."`
	RangeEnd   int64  `json:"range_end,omitempty" jsonschema:"Exclusive byte offset to stop at (default 0 = end of body, must be 0 or >= range_start). When base64_encoded=true, offsets address the decoded binary, not the base64 text."`
}

// BodySliceOutput is the shared output for get_response_body and get_request_body.
type BodySliceOutput struct {
	Body          string `json:"body"`
	Base64Encoded bool   `json:"base64_encoded"`
	// TotalBytes is the size of the full body (decoded for base64
	// payloads) so the caller can plan range fetches.
	TotalBytes int64 `json:"total_bytes"`
	// Truncated is true when range_start/range_end caused the returned
	// body to be a strict subset of the full body.
	Truncated bool `json:"truncated,omitempty"`
}

// GetResponseBodyInput is an alias for BodyRangeInput.
type GetResponseBodyInput = BodyRangeInput

// GetResponseBodyOutput is an alias for BodySliceOutput.
type GetResponseBodyOutput = BodySliceOutput

// GetRequestBodyInput is an alias for BodyRangeInput.
type GetRequestBodyInput = BodyRangeInput

// GetRequestBodyOutput is an alias for BodySliceOutput.
type GetRequestBodyOutput = BodySliceOutput

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

var wsDirectionValues = []string{"sent", "received", "both"}

func registerNetworkTools(s *mcp.Server, mgr *browser.Manager) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_network_requests",
		Description: "Get captured network requests (HTTP and WebSocket connections) with their URLs, methods, status codes, timing, and headers. WebSocket entries include frame counts; use get_websocket_frames to read the actual frames. 'mode' must be 'peek' (keep entries in the HTTP buffer) or 'drain' (consume entries that match the filter). WebSocket connections are long-lived and always returned by snapshot regardless of mode. Pass 'fields' (e.g. ['id','method','url','status']) to project each entry to a subset of JSON keys — useful when listing many entries.",
		InputSchema: schemaWithEnums[GetNetworkRequestsInput](map[string][]string{
			"mode":   {ModePeek, ModeDrain},
			"fields": sortedKeys(networkEntryFieldSet),
		}),
		OutputSchema: networkRequestsOutputSchema(),
		Annotations:  mutatingAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GetNetworkRequestsInput) (*mcp.CallToolResult, any, error) {
		if err := validateMode(input.Mode, ModePeek, ModeDrain); err != nil {
			return nil, nil, err
		}
		if input.Limit < 0 {
			return nil, nil, fmt.Errorf("limit must be >= 0, got %d", input.Limit)
		}
		if input.StatusMin < 0 || input.StatusMax < 0 {
			return nil, nil, fmt.Errorf("status_min and status_max must be >= 0, got %d..%d", input.StatusMin, input.StatusMax)
		}
		if input.StatusMax > 0 && input.StatusMin > input.StatusMax {
			return nil, nil, fmt.Errorf("status_max (%d) must be >= status_min (%d)", input.StatusMax, input.StatusMin)
		}
		var allowedFields map[string]struct{}
		if len(input.Fields) > 0 {
			var err error
			allowedFields, err = validateNetworkFields(input.Fields)
			if err != nil {
				return nil, nil, err
			}
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

		// Pass input.Limit directly to the HTTP buffer so mode="drain"
		// removes ONLY the entries actually returned — excess matching HTTP
		// entries stay buffered for subsequent paginated drain calls.
		// Remaining slots up to input.Limit (or all of them when limit <= 0
		// or type="websocket") are filled with matching WebSocket snapshots.
		var httpEntries []collector.NetworkEntry
		if !strings.EqualFold(input.Type, "websocket") {
			if input.Mode == ModePeek {
				httpEntries = t.Network.Peek(f, input.Limit)
			} else {
				httpEntries = t.Network.Drain(f, input.Limit)
			}
		}
		wsEntries := websocketEntries(t.WebSocket.Connections(), f)
		if input.Limit > 0 {
			rem := max(input.Limit-len(httpEntries), 0)
			if len(wsEntries) > rem {
				wsEntries = wsEntries[:rem]
			}
		}

		requests := make([]collector.NetworkEntry, 0, len(httpEntries)+len(wsEntries))
		requests = append(requests, httpEntries...)
		requests = append(requests, wsEntries...)

		if len(allowedFields) == 0 {
			return nil, GetNetworkRequestsOutput{Requests: requests}, nil
		}
		projected, err := projectEntries(requests, allowedFields)
		if err != nil {
			return nil, nil, err
		}
		return nil, ProjectedNetworkRequestsOutput{Requests: projected}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_response_body",
		Description: "Get the response body of a specific network request by its request ID. Use range_start/range_end to fetch a slice of large bodies; total_bytes always reports the full body size so you can plan paging.",
		Annotations: readOnlyAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GetResponseBodyInput) (*mcp.CallToolResult, GetResponseBodyOutput, error) {
		if input.RequestID == "" {
			return nil, GetResponseBodyOutput{}, fmt.Errorf("request_id is required")
		}
		if err := validateRange(input.RangeStart, input.RangeEnd); err != nil {
			return nil, GetResponseBodyOutput{}, err
		}
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, GetResponseBodyOutput{}, err
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		res, err := chromedp.Call(tctx, cdpnetwork.GetResponseBody, cdpnetwork.GetResponseBodyParams{
			RequestID: cdpnetwork.RequestID(input.RequestID),
		})
		if err != nil {
			return nil, GetResponseBodyOutput{}, err
		}

		out := encodeBodySlice(res.Body, input.RangeStart, input.RangeEnd)
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_request_body",
		Description: "Get the full request body (POST/PUT/PATCH/DELETE payload) of a specific network request by its request ID. Use when get_network_requests returned has_request_body=true and the inline request_body was empty or truncated. Use range_start/range_end to fetch a slice of large bodies; total_bytes always reports the full body size.",
		Annotations: readOnlyAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GetRequestBodyInput) (*mcp.CallToolResult, GetRequestBodyOutput, error) {
		if input.RequestID == "" {
			return nil, GetRequestBodyOutput{}, fmt.Errorf("request_id is required")
		}
		if err := validateRange(input.RangeStart, input.RangeEnd); err != nil {
			return nil, GetRequestBodyOutput{}, err
		}
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, GetRequestBodyOutput{}, err
		}

		tctx, tcancel := tabContext(ctx, t.Context())
		defer tcancel()
		res, err := chromedp.Call(tctx, cdpnetwork.GetRequestPostData, cdpnetwork.GetRequestPostDataParams{
			RequestID: cdpnetwork.RequestID(input.RequestID),
		})
		if err != nil {
			return nil, GetRequestBodyOutput{}, err
		}

		// Multipart form uploads with binary parts (files) are excluded by
		// Chrome — see CDP docs: "Request body string, omitting files from
		// multipart requests". cdproto decodes base64 when base64Encoded=true.
		return nil, encodeBodySlice(res.PostData, input.RangeStart, input.RangeEnd), nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_websocket_frames",
		Description: "Get WebSocket frames sent and/or received on a specific connection. Use the request ID returned for entries with type=websocket from get_network_requests. 'mode' must be 'peek' (keep frames buffered) or 'drain' (consume them).",
		InputSchema: schemaWithEnums[GetWebSocketFramesInput](map[string][]string{
			"mode":      {ModePeek, ModeDrain},
			"direction": wsDirectionValues,
		}),
		Annotations: mutatingAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GetWebSocketFramesInput) (*mcp.CallToolResult, GetWebSocketFramesOutput, error) {
		if err := validateMode(input.Mode, ModePeek, ModeDrain); err != nil {
			return nil, GetWebSocketFramesOutput{}, err
		}
		if input.RequestID == "" {
			return nil, GetWebSocketFramesOutput{}, fmt.Errorf("request_id is required")
		}
		if input.Limit < 0 {
			return nil, GetWebSocketFramesOutput{}, fmt.Errorf("limit must be >= 0, got %d", input.Limit)
		}
		if err := validateEnum("direction", input.Direction, true, wsDirectionValues); err != nil {
			return nil, GetWebSocketFramesOutput{}, err
		}
		direction := input.Direction
		if direction == "" {
			direction = "both"
		}
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, GetWebSocketFramesOutput{}, err
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

// encodeBodySlice slices a body to the given byte range and returns the
// shape get_response_body / get_request_body produce. Text vs binary is
// classified on the *full* body so a UTF-8 body sliced mid-codepoint
// stays text — the slice is trimmed back to the last valid rune
// boundary on either end. Binary bodies are base64-encoded as before.
func encodeBodySlice(body []byte, start, end int64) BodySliceOutput {
	total := int64(len(body))
	isText := utf8.Valid(body)
	sliced, truncated := applyByteRange(body, start, end)
	out := BodySliceOutput{TotalBytes: total, Truncated: truncated}
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

// validateNetworkFields validates caller-supplied projection keys against
// networkEntryFieldSet before any buffer drain occurs.
func validateNetworkFields(fields []string) (map[string]struct{}, error) {
	allowed := make(map[string]struct{}, len(fields))
	for _, f := range fields {
		if _, ok := networkEntryFieldSet[f]; !ok {
			return nil, fmt.Errorf("unknown field %q in fields list; available: %v",
				f, sortedKeys(networkEntryFieldSet))
		}
		allowed[f] = struct{}{}
	}
	return allowed, nil
}

// projectEntries serializes each NetworkEntry to a generic JSON map and
// strips keys not in the validated allowed set. The map shape (rather
// than reflective struct projection) handles every JSON tag — including
// the conditionally-emitted WebSocket fields and request-body fields —
// without per-field special cases.
func projectEntries(entries []collector.NetworkEntry, allowed map[string]struct{}) ([]map[string]any, error) {
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
		BlockedReason:        "x",
		CorsError:            "x",
		Canceled:             true,
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
		return body[total:total], total > 0
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
// negative, or when a non-zero range_end precedes range_start.
func validateRange(start, end int64) error {
	if start < 0 {
		return fmt.Errorf("range_start must be >= 0; got %d (negative offsets are not supported)", start)
	}
	if end < 0 {
		return fmt.Errorf("range_end must be >= 0; got %d (use 0 to mean end of body)", end)
	}
	if end > 0 && end < start {
		return fmt.Errorf("range_end (%d) must be >= range_start (%d) (use 0 for end of body)", end, start)
	}
	return nil
}

// trimByteSliceToValidUTF8 trims partial multi-byte UTF-8 runes from either
// end of a slice cut from a known-valid UTF-8 body in O(1) time (at most 3
// bytes per end since UTF-8 codepoints are at most 4 bytes long).
func trimByteSliceToValidUTF8(b []byte) []byte {
	for len(b) > 0 && !utf8.RuneStart(b[0]) {
		b = b[1:]
	}
	for i := 1; i <= 3 && i <= len(b); i++ {
		if utf8.RuneStart(b[len(b)-i]) {
			if !utf8.FullRune(b[len(b)-i:]) {
				b = b[:len(b)-i]
			}
			break
		}
	}
	return b
}
