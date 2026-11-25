package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/chromedp/cdproto/cachestorage"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/chromedp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/browser"
)

const defaultCacheStoragePageSize = 50

// GetCacheStorageInput is the input for get_cache_storage.
type GetCacheStorageInput struct {
	TabInput
	CacheName  string `json:"cache_name,omitempty" jsonschema:"Cache name. Omit to list all caches on the current page's origin."`
	RequestURL string `json:"request_url,omitempty" jsonschema:"Exact request URL of a cached entry to read its response body (requires cache_name)."`
	PathFilter string `json:"path_filter,omitempty" jsonschema:"Optional case-insensitive substring filter on entry URL paths when listing entries (requires cache_name; cannot be combined with request_url)."`
	Skip       int    `json:"skip,omitempty" jsonschema:"Number of entries to skip when listing entries (default 0, requires cache_name; cannot be combined with request_url)."`
	Limit      int    `json:"limit,omitempty" jsonschema:"Maximum entries to return when listing entries (default 50, requires cache_name; cannot be combined with request_url)."`
	RangeStart int64  `json:"range_start,omitempty" jsonschema:"Byte offset to start slicing the cached response body (default 0, requires request_url)."`
	RangeEnd   int64  `json:"range_end,omitempty" jsonschema:"Byte offset (exclusive) to end slicing the cached response body (0 = end of body, requires request_url)."`
}

// CacheStorageInfo describes a single Cache Storage cache on the origin.
type CacheStorageInfo struct {
	Name           string `json:"name"`
	SecurityOrigin string `json:"security_origin,omitempty"`
}

// CacheStorageEntryInfo describes metadata for one cached entry.
type CacheStorageEntryInfo struct {
	RequestURL      string            `json:"request_url"`
	RequestMethod   string            `json:"request_method"`
	ResponseStatus  int64             `json:"response_status"`
	StatusText      string            `json:"status_text,omitempty"`
	ResponseType    string            `json:"response_type,omitempty"`
	ResponseTime    float64           `json:"response_time,omitempty"`
	RequestHeaders  map[string]string `json:"request_headers,omitempty"`
	ResponseHeaders map[string]string `json:"response_headers,omitempty"`
}

// GetCacheStorageOutput is the mode-specific output for get_cache_storage.
type GetCacheStorageOutput struct {
	Caches []CacheStorageInfo `json:"caches,omitempty"`

	CacheName string                  `json:"cache_name,omitempty"`
	Entries   []CacheStorageEntryInfo `json:"entries,omitempty"`
	Total     int64                   `json:"total,omitempty"`
	HasMore   bool                    `json:"has_more,omitempty"`

	RequestURL      string            `json:"request_url,omitempty"`
	Status          int64             `json:"status,omitempty"`
	StatusText      string            `json:"status_text,omitempty"`
	ResponseType    string            `json:"response_type,omitempty"`
	ResponseHeaders map[string]string `json:"response_headers,omitempty"`
	Body            string            `json:"body,omitempty"`
	Base64Encoded   bool              `json:"base64_encoded,omitempty"`
	TotalBytes      int64             `json:"total_bytes,omitempty"`
	Truncated       bool              `json:"truncated,omitempty"`
}

// MarshalJSON emits clean per-mode JSON with non-null empty arrays ([]):
//   - list-caches mode:  {"caches": [...]}
//   - list-entries mode: {"cache_name": "...", "entries": [...], "total": N, "has_more": bool}
//   - read-response mode: {"cache_name": "...", "request_url": "...", "status": N, ..., "body": "...", "base64_encoded": bool, "total_bytes": N, "truncated": bool}
func (o GetCacheStorageOutput) MarshalJSON() ([]byte, error) {
	if o.Caches != nil {
		return json.Marshal(struct {
			Caches []CacheStorageInfo `json:"caches"`
		}{Caches: o.Caches})
	}
	if o.Entries != nil {
		return json.Marshal(struct {
			CacheName string                  `json:"cache_name"`
			Entries   []CacheStorageEntryInfo `json:"entries"`
			Total     int64                   `json:"total"`
			HasMore   bool                    `json:"has_more"`
		}{
			CacheName: o.CacheName,
			Entries:   o.Entries,
			Total:     o.Total,
			HasMore:   o.HasMore,
		})
	}
	return json.Marshal(struct {
		CacheName       string            `json:"cache_name"`
		RequestURL      string            `json:"request_url"`
		Status          int64             `json:"status"`
		StatusText      string            `json:"status_text,omitempty"`
		ResponseType    string            `json:"response_type,omitempty"`
		ResponseHeaders map[string]string `json:"response_headers,omitempty"`
		Body            string            `json:"body"`
		Base64Encoded   bool              `json:"base64_encoded"`
		TotalBytes      int64             `json:"total_bytes"`
		Truncated       bool              `json:"truncated"`
	}{
		CacheName:       o.CacheName,
		RequestURL:      o.RequestURL,
		Status:          o.Status,
		StatusText:      o.StatusText,
		ResponseType:    o.ResponseType,
		ResponseHeaders: o.ResponseHeaders,
		Body:            o.Body,
		Base64Encoded:   o.Base64Encoded,
		TotalBytes:      o.TotalBytes,
		Truncated:       o.Truncated,
	})
}

// DeleteCacheStorageInput is the input for delete_cache_storage.
type DeleteCacheStorageInput struct {
	TabInput
	CacheName  string `json:"cache_name" jsonschema:"Cache name to delete or remove an entry from. Required."`
	RequestURL string `json:"request_url,omitempty" jsonschema:"Optional exact request URL to delete from the cache. Omit to delete the entire cache."`
}

func cacheHeadersToMap(headers []*cachestorage.Header) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	out := make(map[string]string, len(headers))
	for _, h := range headers {
		if h == nil {
			continue
		}
		if prev, exists := out[h.Name]; exists {
			out[h.Name] = prev + ", " + h.Value
		} else {
			out[h.Name] = h.Value
		}
	}
	return out
}

func findCacheByName(ctx context.Context, tgt *chromedp.Target, origin originTarget, name string) (*cachestorage.Cache, error) {
	res, err := cdp.Call(ctx, tgt, cachestorage.RequestCacheNames, cachestorage.RequestCacheNamesParams{
		StorageKey:     origin.storageKey,
		SecurityOrigin: origin.securityOrigin,
	})
	if err != nil {
		return nil, err
	}
	for _, c := range res.Caches {
		if c != nil && c.CacheName == name {
			return c, nil
		}
	}
	return nil, fmt.Errorf("cache %q not found", name)
}

func paginateRange(total, skip, pageSize int) (start, end int, hasMore bool) {
	start = min(skip, total)
	end = start + min(pageSize, total-start)
	return start, end, end < total
}

func registerCacheStorageTools(s *mcp.Server, mgr *browser.Manager) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_cache_storage",
		Description: "Inspect Cache Storage (window.caches) on the current page's origin. Omit cache_name to list caches; pass cache_name to list entries; pass cache_name and request_url to read a cached response body (supports range_start/range_end slicing).",
		Annotations: readOnlyAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GetCacheStorageInput) (*mcp.CallToolResult, GetCacheStorageOutput, error) {
		if input.RequestURL != "" && input.CacheName == "" {
			return nil, GetCacheStorageOutput{}, fmt.Errorf("request_url requires cache_name")
		}
		if (input.PathFilter != "" || input.Skip > 0 || input.Limit > 0) && input.CacheName == "" {
			return nil, GetCacheStorageOutput{}, fmt.Errorf("path_filter, skip, and limit require cache_name")
		}
		if input.Skip < 0 {
			return nil, GetCacheStorageOutput{}, fmt.Errorf("skip must be >= 0, got %d", input.Skip)
		}
		if err := validateLimit(input.Limit); err != nil {
			return nil, GetCacheStorageOutput{}, err
		}
		if err := validateRange(input.RangeStart, input.RangeEnd); err != nil {
			return nil, GetCacheStorageOutput{}, err
		}
		if (input.RangeStart > 0 || input.RangeEnd > 0) && input.RequestURL == "" {
			return nil, GetCacheStorageOutput{}, fmt.Errorf("range_start and range_end require request_url")
		}
		if input.RequestURL != "" && (input.PathFilter != "" || input.Skip > 0 || input.Limit > 0) {
			return nil, GetCacheStorageOutput{}, fmt.Errorf("request_url cannot be combined with path_filter, skip, or limit")
		}

		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, GetCacheStorageOutput{}, err
		}
		tctx, cancel := tabContext(ctx, t.Context())
		defer cancel()

		var out GetCacheStorageOutput
		err = chromedp.Do(tctx, chromedp.Func(func(ctx context.Context, tgt *chromedp.Target) error {
			origin, err := resolveOriginTarget(ctx, tgt)
			if err != nil {
				return err
			}

			// Mode 1: list caches.
			if input.CacheName == "" {
				res, err := cdp.Call(ctx, tgt, cachestorage.RequestCacheNames, cachestorage.RequestCacheNamesParams{
					StorageKey:     origin.storageKey,
					SecurityOrigin: origin.securityOrigin,
				})
				if err != nil {
					return err
				}
				caches := make([]CacheStorageInfo, 0, len(res.Caches))
				for _, c := range res.Caches {
					if c == nil {
						continue
					}
					caches = append(caches, CacheStorageInfo{
						Name:           c.CacheName,
						SecurityOrigin: c.SecurityOrigin,
					})
				}
				out.Caches = caches
				return nil
			}

			cache, err := findCacheByName(ctx, tgt, origin, input.CacheName)
			if err != nil {
				return err
			}

			// Mode 2: list paginated entries in a cache.
			// Omit SkipCount/PageSize in the CDP call and paginate in Go:
			// Chromium's InspectorCacheStorageAgent::requestEntries passes
			// skipCount directly to Vector::EraseAt without clamping, crashing
			// the renderer when skipCount > entries.size().
			if input.RequestURL == "" {
				pageSize := input.Limit
				if pageSize == 0 {
					pageSize = defaultCacheStoragePageSize
				}
				res, err := cdp.Call(ctx, tgt, cachestorage.RequestEntries, cachestorage.RequestEntriesParams{
					CacheID:    cache.CacheID,
					PathFilter: input.PathFilter,
				})
				if err != nil {
					return err
				}
				all := res.CacheDataEntries
				total := int64(len(all))
				start, end, hasMore := paginateRange(len(all), input.Skip, pageSize)
				window := all[start:end]

				entries := make([]CacheStorageEntryInfo, 0, len(window))
				for _, e := range window {
					if e == nil {
						continue
					}
					entries = append(entries, CacheStorageEntryInfo{
						RequestURL:      e.RequestURL,
						RequestMethod:   e.RequestMethod,
						ResponseStatus:  e.ResponseStatus,
						StatusText:      e.ResponseStatusText,
						ResponseType:    string(e.ResponseType),
						ResponseTime:    e.ResponseTime,
						RequestHeaders:  cacheHeadersToMap(e.RequestHeaders),
						ResponseHeaders: cacheHeadersToMap(e.ResponseHeaders),
					})
				}
				out.CacheName = cache.CacheName
				out.Entries = entries
				out.Total = total
				out.HasMore = hasMore
				return nil
			}

			// Mode 3: read cached response body for a specific request_url.
			// Fetch the entry's stored request headers first so Vary-matched
			// responses resolve accurately and missing entries return a clear error.
			entriesRes, err := cdp.Call(ctx, tgt, cachestorage.RequestEntries, cachestorage.RequestEntriesParams{
				CacheID: cache.CacheID,
			})
			if err != nil {
				return err
			}
			var matched *cachestorage.DataEntry
			for _, e := range entriesRes.CacheDataEntries {
				if e != nil && e.RequestURL == input.RequestURL {
					matched = e
					break
				}
			}
			if matched == nil {
				return fmt.Errorf("entry %q not found in cache %q", input.RequestURL, cache.CacheName)
			}
			reqHeaders := matched.RequestHeaders
			if reqHeaders == nil {
				reqHeaders = []*cachestorage.Header{}
			}
			respRes, err := cdp.Call(ctx, tgt, cachestorage.RequestCachedResponse, cachestorage.RequestCachedResponseParams{
				CacheID:        cache.CacheID,
				RequestURL:     matched.RequestURL,
				RequestHeaders: reqHeaders,
			})
			if err != nil {
				return err
			}
			var body []byte
			if respRes.Response != nil {
				body = respRes.Response.Body
			}
			sliced := encodeBodySlice(body, input.RangeStart, input.RangeEnd)
			out.CacheName = cache.CacheName
			out.RequestURL = matched.RequestURL
			out.Status = matched.ResponseStatus
			out.StatusText = matched.ResponseStatusText
			out.ResponseType = string(matched.ResponseType)
			out.ResponseHeaders = cacheHeadersToMap(matched.ResponseHeaders)
			out.Body = sliced.Body
			out.Base64Encoded = sliced.Base64Encoded
			out.TotalBytes = sliced.TotalBytes
			out.Truncated = sliced.Truncated
			return nil
		}))
		if err != nil {
			return nil, GetCacheStorageOutput{}, fmt.Errorf("get_cache_storage: %w", err)
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "delete_cache_storage",
		Description: "Delete an entire Cache Storage cache or a single cached request entry on the current page's origin.",
		Annotations: destructiveAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input DeleteCacheStorageInput) (*mcp.CallToolResult, struct{}, error) {
		if input.CacheName == "" {
			return nil, struct{}{}, fmt.Errorf("cache_name is required")
		}
		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}
		tctx, cancel := tabContext(ctx, t.Context())
		defer cancel()

		err = chromedp.Do(tctx, chromedp.Func(func(ctx context.Context, tgt *chromedp.Target) error {
			origin, err := resolveOriginTarget(ctx, tgt)
			if err != nil {
				return err
			}
			cache, err := findCacheByName(ctx, tgt, origin, input.CacheName)
			if err != nil {
				return err
			}
			if input.RequestURL != "" {
				_, err = cdp.Call(ctx, tgt, cachestorage.DeleteEntry, cachestorage.DeleteEntryParams{
					CacheID: cache.CacheID,
					Request: input.RequestURL,
				})
				return err
			}
			_, err = cdp.Call(ctx, tgt, cachestorage.DeleteCache, cachestorage.DeleteCacheParams{
				CacheID: cache.CacheID,
			})
			return err
		}))
		if err != nil {
			return nil, struct{}{}, fmt.Errorf("delete_cache_storage: %w", err)
		}
		return nil, struct{}{}, nil
	})
}
