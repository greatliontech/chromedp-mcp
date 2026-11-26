package tools

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/domstorage"
	"github.com/chromedp/cdproto/page"
	cdpstorage "github.com/chromedp/cdproto/storage"
	"github.com/chromedp/chromedp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/browser"
	"github.com/greatliontech/chromedp-mcp/internal/collector"
)

const defaultStorageMaxBytes = 65536

var (
	storageActionValues = []string{"get", "set", "delete"}
	storageTypeValues   = []string{"local", "session"}
)

// isLocalStorage validates the type parameter and returns true for local,
// false for session.
func isLocalStorage(typ string) (bool, error) {
	if err := validateEnum("storage type", typ, true, storageTypeValues); err != nil {
		return false, err
	}
	return typ != "session", nil
}

// resolveMainFrame returns the current page's main frame from Page.getFrameTree.
func resolveMainFrame(ctx context.Context, tgt *chromedp.Target) (*cdp.Frame, error) {
	res, err := cdp.Call(ctx, tgt, page.GetFrameTree, cdp.Empty{})
	if err != nil {
		return nil, err
	}
	if res.FrameTree == nil || res.FrameTree.Frame == nil {
		return nil, fmt.Errorf("empty frame tree")
	}
	return res.FrameTree.Frame, nil
}

// resolveStorageID builds a domstorage.StorageID for the current page.
func resolveStorageID(ctx context.Context, tgt *chromedp.Target, isLocal bool) (*domstorage.StorageID, error) {
	top, err := resolveMainFrame(ctx, tgt)
	if err != nil {
		return nil, fmt.Errorf("resolve storage origin: %w", err)
	}
	return &domstorage.StorageID{
		SecurityOrigin: top.SecurityOrigin,
		IsLocalStorage: isLocal,
	}, nil
}

// originTarget identifies the current page's origin for storage CDP domains
// (IndexedDB, CacheStorage) that require exactly one of storageKey or
// securityOrigin.
type originTarget struct {
	storageKey     string
	securityOrigin string
}

// resolveOriginTarget resolves the main frame's storage key (preferred on
// modern Chromium) or security origin, rejecting opaque origins such as
// about:blank or data: URLs.
func resolveOriginTarget(ctx context.Context, tgt *chromedp.Target) (originTarget, error) {
	top, err := resolveMainFrame(ctx, tgt)
	if err != nil {
		return originTarget{}, fmt.Errorf("resolve page origin: %w", err)
	}
	if top.SecurityOrigin == "" || top.SecurityOrigin == "://" || top.SecurityOrigin == "null" {
		return originTarget{}, fmt.Errorf("current page has an opaque origin (%q); navigate to an http/https page first", top.SecurityOrigin)
	}
	if skRes, err := cdp.Call(ctx, tgt, cdpstorage.GetStorageKey, cdpstorage.GetStorageKeyParams{
		FrameID: top.ID,
	}); err == nil && skRes.StorageKey != "" {
		return originTarget{storageKey: string(skRes.StorageKey)}, nil
	}
	return originTarget{securityOrigin: top.SecurityOrigin}, nil
}

// storageKeyInfo holds a key name and the byte length of its value.
type storageKeyInfo struct {
	Key  string `json:"key"`
	Size int    `json:"size"`
}

// storageInput is the input for storage.
type storageInput struct {
	tabInput
	Action   string  `json:"action" jsonschema:"Storage action: 'get', 'set', or 'delete'. Required."`
	Type     string  `json:"type,omitempty" jsonschema:"Storage type: 'local' (default) or 'session'."`
	Key      string  `json:"key,omitempty" jsonschema:"Key to read, write (required for action='set'), or delete (omit on delete to clear all items)."`
	Value    *string `json:"value,omitempty" jsonschema:"Value to write (required for action='set'; empty string allowed)."`
	KeysOnly bool    `json:"keys_only,omitempty" jsonschema:"For action='get': list key names and value byte sizes without returning values (cannot combine with key or max_bytes)."`
	Limit    int     `json:"limit,omitempty" jsonschema:"For action='get' (without key): max keys/entries to return (default all)."`
	MaxBytes int     `json:"max_bytes,omitempty" jsonschema:"For action='get' (when keys_only=false): max UTF-8 bytes of returned data (value bytes for single-key, key+value sum for all-entries; default 65536)."`
}

// storageOutput is the mode-specific output for storage.
type storageOutput struct {
	// Single-key mode: {"value": "..." | null, "total_bytes"?: N, "truncated"?: true}
	hasValue bool
	Value    *string `json:"value,omitempty"`

	// All-entries mode: {"entries": {...}, "total": N, "truncated"?: true}
	Entries map[string]string `json:"entries,omitempty"`

	// Keys-only mode: {"keys": [...], "total": N, "truncated"?: true}
	Keys []storageKeyInfo `json:"keys,omitempty"`

	Total      *int `json:"total,omitempty"`
	TotalBytes int  `json:"total_bytes,omitempty"`
	Truncated  bool `json:"truncated,omitempty"`
}

// MarshalJSON emits clean per-mode JSON:
//   - set/delete:      {}
//   - get(keys_only):  {"keys": [...], "total": N, "truncated"?: true}
//   - get(key):        {"value": "..." | null, "total_bytes"?: N, "truncated"?: true}
//   - get(all):        {"entries": {...}, "total": N, "truncated"?: true}
func (o storageOutput) MarshalJSON() ([]byte, error) {
	if o.Keys != nil {
		return json.Marshal(struct {
			Keys      []storageKeyInfo `json:"keys"`
			Total     *int             `json:"total,omitempty"`
			Truncated bool             `json:"truncated,omitempty"`
		}{
			Keys:      o.Keys,
			Total:     o.Total,
			Truncated: o.Truncated,
		})
	}
	if o.Entries != nil {
		return json.Marshal(struct {
			Entries   map[string]string `json:"entries"`
			Total     *int              `json:"total,omitempty"`
			Truncated bool              `json:"truncated,omitempty"`
		}{
			Entries:   o.Entries,
			Total:     o.Total,
			Truncated: o.Truncated,
		})
	}
	if o.hasValue {
		return json.Marshal(struct {
			Value      *string `json:"value"`
			TotalBytes int     `json:"total_bytes,omitempty"`
			Truncated  bool    `json:"truncated,omitempty"`
		}{
			Value:      o.Value,
			TotalBytes: o.TotalBytes,
			Truncated:  o.Truncated,
		})
	}
	return []byte("{}"), nil
}

func registerStorageTools(s *mcp.Server, mgr *browser.Manager) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "storage",
		Description: "Read, write, or delete items in localStorage or sessionStorage. action='get' reads a single key, lists all entries (bounded by max_bytes, default 65536), or lists key names and byte sizes with keys_only=true; action='set' writes a key/value pair; action='delete' removes one key or clears all items when key is omitted.",
		InputSchema: schemaWithEnums[storageInput](map[string][]string{
			"action": storageActionValues,
			"type":   storageTypeValues,
		}),
		Annotations: destructiveAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input storageInput) (*mcp.CallToolResult, storageOutput, error) {
		if err := validateEnum("action", input.Action, false, storageActionValues); err != nil {
			return nil, storageOutput{}, err
		}
		isLocal, err := isLocalStorage(input.Type)
		if err != nil {
			return nil, storageOutput{}, err
		}

		switch input.Action {
		case "get":
			if input.Value != nil {
				return nil, storageOutput{}, fmt.Errorf("action='get' does not accept value")
			}
			if err := validateLimit(input.Limit); err != nil {
				return nil, storageOutput{}, err
			}
			if input.MaxBytes < 0 {
				return nil, storageOutput{}, fmt.Errorf("max_bytes must be >= 0, got %d", input.MaxBytes)
			}
			if input.KeysOnly && input.Key != "" {
				return nil, storageOutput{}, fmt.Errorf("keys_only cannot be combined with key")
			}
			if input.KeysOnly && input.MaxBytes > 0 {
				return nil, storageOutput{}, fmt.Errorf("keys_only cannot be combined with max_bytes")
			}
			if input.Key != "" && input.Limit > 0 {
				return nil, storageOutput{}, fmt.Errorf("limit cannot be combined with key")
			}

			t, err := mgr.ResolveTab("", input.Tab)
			if err != nil {
				return nil, storageOutput{}, err
			}
			tctx, cancel := tabContext(ctx, t.Context())
			defer cancel()

			var out storageOutput
			err = chromedp.Do(tctx, chromedp.Func(func(ctx context.Context, tgt *chromedp.Target) error {
				sid, err := resolveStorageID(ctx, tgt, isLocal)
				if err != nil {
					return err
				}
				res, err := cdp.Call(ctx, tgt, domstorage.GetDOMStorageItems, domstorage.GetDOMStorageItemsParams{StorageID: sid})
				if err != nil {
					return err
				}

				type kv struct{ k, v string }
				pairs := make([]kv, 0, len(res.Entries))
				for _, item := range res.Entries {
					if len(item) >= 2 {
						pairs = append(pairs, kv{k: item[0], v: item[1]})
					}
				}
				slices.SortFunc(pairs, func(a, b kv) int { return cmp.Compare(a.k, b.k) })

				if input.KeysOnly {
					total := len(pairs)
					shown := total
					if input.Limit > 0 && input.Limit < shown {
						shown = input.Limit
					}
					keys := make([]storageKeyInfo, 0, shown)
					for _, p := range pairs[:shown] {
						keys = append(keys, storageKeyInfo{Key: p.k, Size: len(p.v)})
					}
					out.Keys = keys
					out.Total = &total
					out.Truncated = shown < total
					return nil
				}

				maxBytes := input.MaxBytes
				if maxBytes == 0 {
					maxBytes = defaultStorageMaxBytes
				}

				if input.Key != "" {
					out.hasValue = true
					for _, p := range pairs {
						if p.k == input.Key {
							val, truncated := collector.TruncateUTF8(p.v, maxBytes)
							out.Value = &val
							if truncated {
								out.TotalBytes = len(p.v)
								out.Truncated = true
							}
							return nil
						}
					}
					// Key not found — Value stays nil, hasValue emits {"value":null}.
					return nil
				}

				// All-entries mode: deterministic key order, bounded by limit and maxBytes.
				total := len(pairs)
				entries := make(map[string]string, total)
				usedBytes := 0
				truncated := false
				for i, p := range pairs {
					if input.Limit > 0 && i >= input.Limit {
						truncated = true
						break
					}
					entryBytes := len(p.k) + len(p.v)
					if usedBytes+entryBytes > maxBytes {
						truncated = true
						break
					}
					entries[p.k] = p.v
					usedBytes += entryBytes
				}
				out.Entries = entries
				out.Total = &total
				out.Truncated = truncated
				return nil
			}))
			if err != nil {
				return nil, storageOutput{}, fmt.Errorf("storage: %w", err)
			}
			return nil, out, nil

		case "set":
			if input.KeysOnly || input.Limit != 0 || input.MaxBytes != 0 {
				return nil, storageOutput{}, fmt.Errorf("action='set' does not accept keys_only, limit, or max_bytes")
			}
			if input.Key == "" {
				return nil, storageOutput{}, fmt.Errorf("key is required for action='set'")
			}
			if input.Value == nil {
				return nil, storageOutput{}, fmt.Errorf("value is required for action='set'")
			}
			t, err := mgr.ResolveTab("", input.Tab)
			if err != nil {
				return nil, storageOutput{}, err
			}
			tctx, cancel := tabContext(ctx, t.Context())
			defer cancel()

			err = chromedp.Do(tctx, chromedp.Func(func(ctx context.Context, tgt *chromedp.Target) error {
				sid, err := resolveStorageID(ctx, tgt, isLocal)
				if err != nil {
					return err
				}
				_, err = cdp.Call(ctx, tgt, domstorage.SetDOMStorageItem, domstorage.SetDOMStorageItemParams{
					StorageID: sid,
					Key:       input.Key,
					Value:     *input.Value,
				})
				return err
			}))
			if err != nil {
				return nil, storageOutput{}, fmt.Errorf("storage: %w", err)
			}
			return nil, storageOutput{}, nil

		default: // "delete"
			if input.Value != nil || input.KeysOnly || input.Limit != 0 || input.MaxBytes != 0 {
				return nil, storageOutput{}, fmt.Errorf("action='delete' does not accept value, keys_only, limit, or max_bytes")
			}
			t, err := mgr.ResolveTab("", input.Tab)
			if err != nil {
				return nil, storageOutput{}, err
			}
			tctx, cancel := tabContext(ctx, t.Context())
			defer cancel()

			err = chromedp.Do(tctx, chromedp.Func(func(ctx context.Context, tgt *chromedp.Target) error {
				sid, err := resolveStorageID(ctx, tgt, isLocal)
				if err != nil {
					return err
				}
				if input.Key != "" {
					_, err = cdp.Call(ctx, tgt, domstorage.RemoveDOMStorageItem, domstorage.RemoveDOMStorageItemParams{
						StorageID: sid,
						Key:       input.Key,
					})
					return err
				}
				_, err = cdp.Call(ctx, tgt, domstorage.Clear, domstorage.ClearParams{StorageID: sid})
				return err
			}))
			if err != nil {
				return nil, storageOutput{}, fmt.Errorf("storage: %w", err)
			}
			return nil, storageOutput{}, nil
		}
	})
}
