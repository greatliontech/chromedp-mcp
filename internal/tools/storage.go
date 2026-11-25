package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/domstorage"
	"github.com/chromedp/cdproto/page"
	cdpstorage "github.com/chromedp/cdproto/storage"
	"github.com/chromedp/chromedp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/browser"
)

var storageTypeValues = []string{"local", "session"}

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

// --- get_storage ---

// GetStorageInput is the input for get_storage.
type GetStorageInput struct {
	TabInput
	Type string `json:"type,omitempty" jsonschema:"Storage type: local (default) or session"`
	Key  string `json:"key,omitempty" jsonschema:"Key to retrieve. If omitted returns all key-value pairs."`
}

// GetStorageOutput is the output for get_storage.
type GetStorageOutput struct {
	// Value is set when a specific key is requested. Null when the key
	// does not exist. Omitted in all-entries mode.
	Value *string `json:"value,omitempty"`
	// Entries is set when no key is specified — all key-value pairs
	// (empty object {} when storage is empty). Omitted in single-key mode.
	Entries map[string]string `json:"entries,omitzero"`
}

// MarshalJSON ensures single-key mode always emits {"value": ...} (including
// explicit null when the key is missing) and all-entries mode always emits
// {"entries": {...}} (including {} when empty, without a spurious "value": null).
func (o GetStorageOutput) MarshalJSON() ([]byte, error) {
	if o.Entries != nil {
		return json.Marshal(struct {
			Entries map[string]string `json:"entries"`
		}{Entries: o.Entries})
	}
	return json.Marshal(struct {
		Value *string `json:"value"`
	}{Value: o.Value})
}

// --- set_storage ---

// SetStorageInput is the input for set_storage.
type SetStorageInput struct {
	TabInput
	Type  string `json:"type,omitempty" jsonschema:"Storage type: local (default) or session"`
	Key   string `json:"key" jsonschema:"Key to set"`
	Value string `json:"value" jsonschema:"Value to set"`
}

// --- delete_storage ---

// DeleteStorageInput is the input for delete_storage.
type DeleteStorageInput struct {
	TabInput
	Type string `json:"type,omitempty" jsonschema:"Storage type: local (default) or session"`
	Key  string `json:"key,omitempty" jsonschema:"Key to delete. If omitted clears all items."`
}

// --- get_storage_keys ---

// GetStorageKeysInput is the input for get_storage_keys.
type GetStorageKeysInput struct {
	TabInput
	Type  string `json:"type,omitempty" jsonschema:"Storage type: local (default) or session"`
	Limit int    `json:"limit,omitempty" jsonschema:"Max keys to return (default all)"`
}

// StorageKeyInfo holds a key name and the byte length of its value.
type StorageKeyInfo struct {
	Key  string `json:"key"`
	Size int    `json:"size"`
}

// GetStorageKeysOutput is the output for get_storage_keys.
type GetStorageKeysOutput struct {
	Keys []StorageKeyInfo `json:"keys"`
}

func registerStorageTools(s *mcp.Server, mgr *browser.Manager) {
	storageTypeEnum := map[string][]string{"type": storageTypeValues}

	// get_storage
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_storage",
		Description: "Read one or all items from localStorage or sessionStorage.",
		InputSchema: schemaWithEnums[GetStorageInput](storageTypeEnum),
		Annotations: readOnlyAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GetStorageInput) (*mcp.CallToolResult, GetStorageOutput, error) {
		isLocal, err := isLocalStorage(input.Type)
		if err != nil {
			return nil, GetStorageOutput{}, err
		}
		tab, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, GetStorageOutput{}, err
		}
		tctx, cancel := tabContext(ctx, tab.Context())
		defer cancel()

		var out GetStorageOutput
		err = chromedp.Do(tctx, chromedp.Func(func(ctx context.Context, tgt *chromedp.Target) error {
			sid, err := resolveStorageID(ctx, tgt, isLocal)
			if err != nil {
				return err
			}
			res, err := cdp.Call(ctx, tgt, domstorage.GetDOMStorageItems, domstorage.GetDOMStorageItemsParams{StorageID: sid})
			if err != nil {
				return err
			}
			items := res.Entries

			if input.Key != "" {
				for _, item := range items {
					if len(item) >= 2 && item[0] == input.Key {
						v := item[1]
						out.Value = &v
						return nil
					}
				}
				// Key not found — Value stays nil.
				return nil
			}

			// All entries.
			out.Entries = make(map[string]string, len(items))
			for _, item := range items {
				if len(item) >= 2 {
					out.Entries[item[0]] = item[1]
				}
			}
			return nil
		}))
		if err != nil {
			return nil, GetStorageOutput{}, fmt.Errorf("get_storage: %w", err)
		}
		return nil, out, nil
	})

	// set_storage
	mcp.AddTool(s, &mcp.Tool{
		Name:        "set_storage",
		Description: "Write a key-value pair to localStorage or sessionStorage.",
		InputSchema: schemaWithEnums[SetStorageInput](storageTypeEnum),
		Annotations: idempotentWriteAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input SetStorageInput) (*mcp.CallToolResult, struct{}, error) {
		isLocal, err := isLocalStorage(input.Type)
		if err != nil {
			return nil, struct{}{}, err
		}
		if input.Key == "" {
			return nil, struct{}{}, fmt.Errorf("set_storage: key is required")
		}
		tab, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}
		tctx, cancel := tabContext(ctx, tab.Context())
		defer cancel()

		err = chromedp.Do(tctx, chromedp.Func(func(ctx context.Context, tgt *chromedp.Target) error {
			sid, err := resolveStorageID(ctx, tgt, isLocal)
			if err != nil {
				return err
			}
			_, err = cdp.Call(ctx, tgt, domstorage.SetDOMStorageItem, domstorage.SetDOMStorageItemParams{
				StorageID: sid,
				Key:       input.Key,
				Value:     input.Value,
			})
			return err
		}))
		if err != nil {
			return nil, struct{}{}, fmt.Errorf("set_storage: %w", err)
		}
		return nil, struct{}{}, nil
	})

	// delete_storage
	mcp.AddTool(s, &mcp.Tool{
		Name:        "delete_storage",
		Description: "Delete one or all items from localStorage or sessionStorage. If key is omitted, clears all items.",
		InputSchema: schemaWithEnums[DeleteStorageInput](storageTypeEnum),
		Annotations: destructiveAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input DeleteStorageInput) (*mcp.CallToolResult, struct{}, error) {
		isLocal, err := isLocalStorage(input.Type)
		if err != nil {
			return nil, struct{}{}, err
		}
		tab, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, struct{}{}, err
		}
		tctx, cancel := tabContext(ctx, tab.Context())
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
			return nil, struct{}{}, fmt.Errorf("delete_storage: %w", err)
		}
		return nil, struct{}{}, nil
	})

	// get_storage_keys
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_storage_keys",
		Description: "List all keys in localStorage or sessionStorage with value sizes. Useful for discovering stored data without retrieving potentially large values.",
		InputSchema: schemaWithEnums[GetStorageKeysInput](storageTypeEnum),
		Annotations: readOnlyAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GetStorageKeysInput) (*mcp.CallToolResult, GetStorageKeysOutput, error) {
		isLocal, err := isLocalStorage(input.Type)
		if err != nil {
			return nil, GetStorageKeysOutput{}, err
		}
		if err := validateLimit(input.Limit); err != nil {
			return nil, GetStorageKeysOutput{}, err
		}
		tab, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, GetStorageKeysOutput{}, err
		}
		tctx, cancel := tabContext(ctx, tab.Context())
		defer cancel()

		var out GetStorageKeysOutput
		err = chromedp.Do(tctx, chromedp.Func(func(ctx context.Context, tgt *chromedp.Target) error {
			sid, err := resolveStorageID(ctx, tgt, isLocal)
			if err != nil {
				return err
			}
			res, err := cdp.Call(ctx, tgt, domstorage.GetDOMStorageItems, domstorage.GetDOMStorageItemsParams{StorageID: sid})
			if err != nil {
				return err
			}
			items := res.Entries

			limit := input.Limit
			if limit <= 0 {
				limit = len(items)
			}

			out.Keys = make([]StorageKeyInfo, 0, min(len(items), limit))
			for i, item := range items {
				if i >= limit {
					break
				}
				if len(item) >= 2 {
					out.Keys = append(out.Keys, StorageKeyInfo{
						Key:  item[0],
						Size: len(item[1]),
					})
				}
			}
			return nil
		}))
		if err != nil {
			return nil, GetStorageKeysOutput{}, fmt.Errorf("get_storage_keys: %w", err)
		}
		return nil, out, nil
	})
}
