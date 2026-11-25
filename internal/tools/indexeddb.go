package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/indexeddb"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/browser"
)

const defaultIndexedDBPageSize = 50

// GetIndexedDBInput is the input for get_indexeddb.
type GetIndexedDBInput struct {
	TabInput
	Database string `json:"database,omitempty" jsonschema:"Database name. If omitted, lists all IndexedDB database names for the current page's origin."`
	Store    string `json:"store,omitempty" jsonschema:"Object store name (requires database). If omitted with database set, returns the database schema (version and object stores). If set, returns paginated records from the store."`
	Index    string `json:"index,omitempty" jsonschema:"Optional index name within the object store to read records through (requires store)."`
	Skip     int    `json:"skip,omitempty" jsonschema:"Number of records to skip for pagination (default 0, requires store)."`
	Limit    int    `json:"limit,omitempty" jsonschema:"Maximum records to return (default 50, requires store)."`
}

// IndexedDBIndexInfo describes an index on an IndexedDB object store.
type IndexedDBIndexInfo struct {
	Name       string `json:"name"`
	KeyPath    any    `json:"key_path,omitempty"`
	Unique     bool   `json:"unique"`
	MultiEntry bool   `json:"multi_entry"`
}

// IndexedDBStoreInfo describes an object store inside an IndexedDB database.
type IndexedDBStoreInfo struct {
	Name          string               `json:"name"`
	KeyPath       any                  `json:"key_path,omitempty"`
	AutoIncrement bool                 `json:"auto_increment"`
	Indexes       []IndexedDBIndexInfo `json:"indexes"`
}

// IndexedDBEntry represents a single record read from an IndexedDB object store.
// Key, PrimaryKey, and Value hold arbitrary decoded JSON values (numbers,
// strings, arrays, objects, booleans, or null).
type IndexedDBEntry struct {
	Key        any `json:"key"`
	PrimaryKey any `json:"primary_key"`
	Value      any `json:"value"`
}

// GetIndexedDBOutput is the mode-specific output for get_indexeddb.
type GetIndexedDBOutput struct {
	Databases []string             `json:"databases,omitempty"`
	Database  string               `json:"database,omitempty"`
	Version   float64              `json:"version,omitempty"`
	Stores    []IndexedDBStoreInfo `json:"stores,omitempty"`
	Store     string               `json:"store,omitempty"`
	Index     string               `json:"index,omitempty"`
	Entries   []IndexedDBEntry     `json:"entries,omitempty"`
	HasMore   bool                 `json:"has_more,omitempty"`
}

// MarshalJSON emits clean per-mode JSON with non-null empty arrays ([]):
//   - list-databases mode:  {"databases": [...]}
//   - inspect-database mode: {"database": "...", "version": N, "stores": [...]}
//   - read-records mode:    {"database": "...", "store": "...", "index"?: "...", "entries": [...], "has_more": bool}
func (o GetIndexedDBOutput) MarshalJSON() ([]byte, error) {
	if o.Databases != nil {
		return json.Marshal(struct {
			Databases []string `json:"databases"`
		}{Databases: o.Databases})
	}
	if o.Stores != nil {
		return json.Marshal(struct {
			Database string               `json:"database"`
			Version  float64              `json:"version"`
			Stores   []IndexedDBStoreInfo `json:"stores"`
		}{
			Database: o.Database,
			Version:  o.Version,
			Stores:   o.Stores,
		})
	}
	entries := o.Entries
	if entries == nil {
		entries = []IndexedDBEntry{}
	}
	return json.Marshal(struct {
		Database string           `json:"database"`
		Store    string           `json:"store"`
		Index    string           `json:"index,omitempty"`
		Entries  []IndexedDBEntry `json:"entries"`
		HasMore  bool             `json:"has_more"`
	}{
		Database: o.Database,
		Store:    o.Store,
		Index:    o.Index,
		Entries:  entries,
		HasMore:  o.HasMore,
	})
}

// DeleteIndexedDBInput is the input for delete_indexeddb.
type DeleteIndexedDBInput struct {
	TabInput
	Database string `json:"database" jsonschema:"Database name to delete or clear a store within. Required."`
	Store    string `json:"store,omitempty" jsonschema:"Optional object store name. If provided, clears all records in that store; if omitted, deletes the entire database."`
}

func formatKeyPath(kp *indexeddb.KeyPath) any {
	if kp == nil {
		return nil
	}
	switch kp.Type {
	case indexeddb.KeyPathTypeString:
		return kp.String
	case indexeddb.KeyPathTypeArray:
		out := make([]string, len(kp.Array))
		copy(out, kp.Array)
		return out
	default:
		return nil
	}
}

// serializeRemoteObject converts a CDP runtime.RemoteObject (returned by
// IndexedDB.requestData) into a JSON-encodable Go value. Transport/context
// errors are propagated rather than silently degraded; caller is responsible
// for releasing all RemoteObject handles returned by RequestData.
func serializeRemoteObject(ctx context.Context, tgt *chromedp.Target, ro *runtime.RemoteObject) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if ro == nil || ro.Type == runtime.RemoteObjectTypeUndefined || (ro.Type == runtime.RemoteObjectTypeObject && ro.Subtype == runtime.RemoteObjectSubtypeNull) {
		return nil, nil
	}
	if len(ro.Value) > 0 {
		return decodeRawJSON(ro.Value), nil
	}
	if ro.UnserializableValue != "" {
		return string(ro.UnserializableValue), nil
	}
	if ro.ObjectID != "" {
		// Serialize structured-clone JS values (including Date, RegExp, Map,
		// Set, Error, Blob, ArrayBuffer, TypedArrays, BigInt, and cyclic
		// graphs) cleanly to JSON-compatible values in a single CDP call.
		const fn = `function() {
			const seen = new WeakSet();
			function ser(v) {
				if (v === null || v === undefined) return null;
				const t = typeof v;
				if (t === 'boolean' || t === 'string') return v;
				if (t === 'number') return Number.isFinite(v) ? v : String(v);
				if (t === 'bigint') return v.toString() + 'n';
				if (v instanceof Date) return isNaN(v.getTime()) ? 'Invalid Date' : v.toISOString();
				if (v instanceof RegExp) return v.toString();
				if (v instanceof Error) return {name: v.name, message: v.message};
				if (typeof Blob !== 'undefined' && v instanceof Blob) return {type: 'Blob', size: v.size, mime: v.type};
				if (v instanceof ArrayBuffer) return {type: 'ArrayBuffer', byteLength: v.byteLength};
				if (ArrayBuffer.isView(v)) {
					if (v instanceof DataView) return Array.from(new Uint8Array(v.buffer, v.byteOffset, v.byteLength));
					if (typeof BigInt64Array !== 'undefined' && (v instanceof BigInt64Array || v instanceof BigUint64Array)) {
						return Array.from(v, x => x.toString() + 'n');
					}
					return Array.from(v, ser);
				}
				if (t !== 'object') return String(v);
				if (seen.has(v)) return '[Circular]';
				seen.add(v);
				try {
					if (Array.isArray(v)) return v.map(ser);
					if (v instanceof Set) return Array.from(v, ser);
					if (v instanceof Map) return Array.from(v.entries(), ([k, val]) => [ser(k), ser(val)]);
					const out = {};
					for (const k of Object.keys(v)) out[k] = ser(v[k]);
					return out;
				} finally {
					seen.delete(v);
				}
			}
			return ser(this);
		}`
		res, err := cdp.Call(ctx, tgt, runtime.CallFunctionOn, runtime.CallFunctionOnParams{
			ObjectID:            ro.ObjectID,
			FunctionDeclaration: fn,
			ReturnByValue:       new(true),
			Silent:              new(true),
		})
		if err != nil {
			return nil, fmt.Errorf("serialize IndexedDB value: %w", err)
		}
		if res.ExceptionDetails == nil && res.Result != nil && len(res.Result.Value) > 0 {
			return decodeRawJSON(res.Result.Value), nil
		}
	}
	if ro.Description != "" {
		return ro.Description, nil
	}
	return nil, nil
}

// releaseDataEntries releases every non-empty RemoteObject handle across
// entries under a bounded cleanup context so V8 inspector handles never leak
// even when serialization exits early on cancellation or error.
func releaseDataEntries(ctx context.Context, tgt *chromedp.Target, entries []*indexeddb.DataEntry) {
	var ids []runtime.RemoteObjectID
	for _, e := range entries {
		if e == nil {
			continue
		}
		for _, ro := range []*runtime.RemoteObject{e.Key, e.PrimaryKey, e.Value} {
			if ro != nil && ro.ObjectID != "" {
				ids = append(ids, ro.ObjectID)
			}
		}
	}
	if len(ids) == 0 {
		return
	}
	cleanCtx, cancel := cleanupContext(ctx)
	defer cancel()
	for _, id := range ids {
		_, _ = cdp.Call(cleanCtx, tgt, runtime.ReleaseObject, runtime.ReleaseObjectParams{
			ObjectID: id,
		})
	}
}

func decodeRawJSON(raw []byte) any {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	return v
}

func registerIndexedDBTools(s *mcp.Server, mgr *browser.Manager) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_indexeddb",
		Description: "Inspect IndexedDB on the current page's origin. Omit database to list database names; pass database to inspect its object stores and indexes; pass database and store to read paginated records.",
		Annotations: readOnlyAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GetIndexedDBInput) (*mcp.CallToolResult, GetIndexedDBOutput, error) {
		if input.Store != "" && input.Database == "" {
			return nil, GetIndexedDBOutput{}, fmt.Errorf("store requires database")
		}
		if input.Index != "" && input.Store == "" {
			return nil, GetIndexedDBOutput{}, fmt.Errorf("index requires store")
		}
		if input.Skip < 0 {
			return nil, GetIndexedDBOutput{}, fmt.Errorf("skip must be >= 0, got %d", input.Skip)
		}
		if input.Skip > 0 && input.Store == "" {
			return nil, GetIndexedDBOutput{}, fmt.Errorf("skip requires store")
		}
		if err := validateLimit(input.Limit); err != nil {
			return nil, GetIndexedDBOutput{}, err
		}
		if input.Limit > 0 && input.Store == "" {
			return nil, GetIndexedDBOutput{}, fmt.Errorf("limit requires store")
		}

		t, err := mgr.ResolveTab("", input.Tab)
		if err != nil {
			return nil, GetIndexedDBOutput{}, err
		}
		tctx, cancel := tabContext(ctx, t.Context())
		defer cancel()

		var out GetIndexedDBOutput
		err = chromedp.Do(tctx, chromedp.Func(func(ctx context.Context, tgt *chromedp.Target) error {
			origin, err := resolveOriginTarget(ctx, tgt)
			if err != nil {
				return err
			}
			// IndexedDB commands work directly without per-call Enable/Disable
			// toggling (which avoids a race where IndexedDB.disable releases
			// the shared kIndexedDBObjectGroup while a concurrent read is
			// materializing RemoteObjects).
			namesRes, err := cdp.Call(ctx, tgt, indexeddb.RequestDatabaseNames, indexeddb.RequestDatabaseNamesParams{
				StorageKey:     origin.storageKey,
				SecurityOrigin: origin.securityOrigin,
			})
			if err != nil {
				return err
			}

			// Mode 1: list database names.
			if input.Database == "" {
				names := namesRes.DatabaseNames
				if names == nil {
					names = []string{}
				}
				out.Databases = names
				return nil
			}

			// Verify existence up front so missing databases return a clear
			// not-found error instead of Chromium's cryptic "Aborted upgrade.".
			if !slices.Contains(namesRes.DatabaseNames, input.Database) {
				return fmt.Errorf("database %q not found", input.Database)
			}

			// Mode 2: inspect database schema (object stores and indexes).
			if input.Store == "" {
				res, err := cdp.Call(ctx, tgt, indexeddb.RequestDatabase, indexeddb.RequestDatabaseParams{
					StorageKey:     origin.storageKey,
					SecurityOrigin: origin.securityOrigin,
					DatabaseName:   input.Database,
				})
				if err != nil {
					return err
				}
				db := res.DatabaseWithObjectStores
				if db == nil {
					return fmt.Errorf("database %q not found", input.Database)
				}
				stores := make([]IndexedDBStoreInfo, 0, len(db.ObjectStores))
				for _, st := range db.ObjectStores {
					if st == nil {
						continue
					}
					indexes := make([]IndexedDBIndexInfo, 0, len(st.Indexes))
					for _, idx := range st.Indexes {
						if idx == nil {
							continue
						}
						indexes = append(indexes, IndexedDBIndexInfo{
							Name:       idx.Name,
							KeyPath:    formatKeyPath(idx.KeyPath),
							Unique:     idx.Unique,
							MultiEntry: idx.MultiEntry,
						})
					}
					stores = append(stores, IndexedDBStoreInfo{
						Name:          st.Name,
						KeyPath:       formatKeyPath(st.KeyPath),
						AutoIncrement: st.AutoIncrement,
						Indexes:       indexes,
					})
				}
				out.Database = db.Name
				out.Version = db.Version
				out.Stores = stores
				return nil
			}

			// Mode 3: read paginated records from an object store or index.
			pageSize := input.Limit
			if pageSize == 0 {
				pageSize = defaultIndexedDBPageSize
			}
			res, err := cdp.Call(ctx, tgt, indexeddb.RequestData, indexeddb.RequestDataParams{
				StorageKey:      origin.storageKey,
				SecurityOrigin:  origin.securityOrigin,
				DatabaseName:    input.Database,
				ObjectStoreName: input.Store,
				IndexName:       input.Index,
				SkipCount:       int64(input.Skip),
				PageSize:        int64(pageSize),
			})
			if err != nil {
				return err
			}
			defer releaseDataEntries(ctx, tgt, res.ObjectStoreDataEntries)

			entries := make([]IndexedDBEntry, 0, len(res.ObjectStoreDataEntries))
			for _, e := range res.ObjectStoreDataEntries {
				if e == nil {
					continue
				}
				keyVal, err := serializeRemoteObject(ctx, tgt, e.Key)
				if err != nil {
					return err
				}
				pkVal, err := serializeRemoteObject(ctx, tgt, e.PrimaryKey)
				if err != nil {
					return err
				}
				val, err := serializeRemoteObject(ctx, tgt, e.Value)
				if err != nil {
					return err
				}
				entries = append(entries, IndexedDBEntry{
					Key:        keyVal,
					PrimaryKey: pkVal,
					Value:      val,
				})
			}
			out.Database = input.Database
			out.Store = input.Store
			out.Index = input.Index
			out.Entries = entries
			out.HasMore = res.HasMore
			return nil
		}))
		if err != nil {
			return nil, GetIndexedDBOutput{}, fmt.Errorf("get_indexeddb: %w", err)
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "delete_indexeddb",
		Description: "Delete an IndexedDB database or clear all records from one of its object stores on the current page's origin.",
		Annotations: destructiveAnnotations(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input DeleteIndexedDBInput) (*mcp.CallToolResult, struct{}, error) {
		if input.Database == "" {
			return nil, struct{}{}, fmt.Errorf("database is required")
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
			if input.Store != "" {
				_, err = cdp.Call(ctx, tgt, indexeddb.ClearObjectStore, indexeddb.ClearObjectStoreParams{
					StorageKey:      origin.storageKey,
					SecurityOrigin:  origin.securityOrigin,
					DatabaseName:    input.Database,
					ObjectStoreName: input.Store,
				})
				return err
			}
			_, err = cdp.Call(ctx, tgt, indexeddb.DeleteDatabase, indexeddb.DeleteDatabaseParams{
				StorageKey:     origin.storageKey,
				SecurityOrigin: origin.securityOrigin,
				DatabaseName:   input.Database,
			})
			return err
		}))
		if err != nil {
			return nil, struct{}{}, fmt.Errorf("delete_indexeddb: %w", err)
		}
		return nil, struct{}{}, nil
	})
}
