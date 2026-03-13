package tools

import (
	"context"
	"fmt"

	"github.com/chromedp/cdproto/domstorage"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/browser"
)

// isLocalStorage validates the type parameter and returns true for local,
// false for session.
func isLocalStorage(typ string) (bool, error) {
	switch typ {
	case "", "local":
		return true, nil
	case "session":
		return false, nil
	default:
		return false, fmt.Errorf("invalid storage type %q: must be \"local\" or \"session\"", typ)
	}
}

// resolveStorageID builds a domstorage.StorageID for the current page.
// Must be called inside a chromedp.ActionFunc.
func resolveStorageID(ctx context.Context, isLocal bool) (*domstorage.StorageID, error) {
	tree, err := page.GetFrameTree().Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve storage origin: %w", err)
	}
	return &domstorage.StorageID{
		SecurityOrigin: tree.Frame.SecurityOrigin,
		IsLocalStorage: isLocal,
	}, nil
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
	// does not exist.
	Value *string `json:"value"`
	// Entries is set when no key is specified — all key-value pairs.
	Entries map[string]string `json:"entries,omitempty"`
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
	// get_storage
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_storage",
		Description: "Read one or all items from localStorage or sessionStorage.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:   true,
			IdempotentHint: true,
		},
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
		err = chromedp.Run(tctx, chromedp.ActionFunc(func(ctx context.Context) error {
			sid, err := resolveStorageID(ctx, isLocal)
			if err != nil {
				return err
			}
			items, err := domstorage.GetDOMStorageItems(sid).Do(ctx)
			if err != nil {
				return err
			}

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
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: false,
		},
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

		err = chromedp.Run(tctx, chromedp.ActionFunc(func(ctx context.Context) error {
			sid, err := resolveStorageID(ctx, isLocal)
			if err != nil {
				return err
			}
			return domstorage.SetDOMStorageItem(sid, input.Key, input.Value).Do(ctx)
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
		Annotations: &mcp.ToolAnnotations{
			DestructiveHint: ptrBool(true),
		},
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

		err = chromedp.Run(tctx, chromedp.ActionFunc(func(ctx context.Context) error {
			sid, err := resolveStorageID(ctx, isLocal)
			if err != nil {
				return err
			}
			if input.Key != "" {
				return domstorage.RemoveDOMStorageItem(sid, input.Key).Do(ctx)
			}
			return domstorage.Clear(sid).Do(ctx)
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
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:   true,
			IdempotentHint: true,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GetStorageKeysInput) (*mcp.CallToolResult, GetStorageKeysOutput, error) {
		isLocal, err := isLocalStorage(input.Type)
		if err != nil {
			return nil, GetStorageKeysOutput{}, err
		}
		tab, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, GetStorageKeysOutput{}, err
		}
		tctx, cancel := tabContext(ctx, tab.Context())
		defer cancel()

		var out GetStorageKeysOutput
		err = chromedp.Run(tctx, chromedp.ActionFunc(func(ctx context.Context) error {
			sid, err := resolveStorageID(ctx, isLocal)
			if err != nil {
				return err
			}
			items, err := domstorage.GetDOMStorageItems(sid).Do(ctx)
			if err != nil {
				return err
			}

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
