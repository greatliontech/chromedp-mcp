package tools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestIndexedDBLifecyclePaginationIndexAndDelete(t *testing.T) {
	// Dedicated origin guarantees empty initial IndexedDB state.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><html><body><h1>IndexedDB Test</h1></body></html>`))
	}))
	defer srv.Close()

	tabOut := callTool[tabNewOutput](t, "tab_new", map[string]any{})
	tabID := tabOut.TabID
	defer closeTab(t, tabID)

	// Opaque origin (about:blank) must fail cleanly.
	if errText := callToolExpectErr(t, "indexeddb", map[string]any{"action": "get", "tab": tabID}); !strings.Contains(errText, "opaque origin") {
		t.Errorf("indexeddb(get) on about:blank err = %q, want opaque origin", errText)
	}

	callTool[navigateOutput](t, "navigate", map[string]any{"tab": tabID, "url": srv.URL + "/"})

	// Empty origin must serialize databases as [], never null.
	rawEmpty := callToolRaw(t, "indexeddb", map[string]any{"action": "get", "tab": tabID})
	if rawEmpty.IsError {
		t.Fatalf("indexeddb(get) empty error: %s", contentText(rawEmpty))
	}
	if got := contentText(rawEmpty); !strings.Contains(got, `"databases":[]`) {
		t.Fatalf("empty indexeddb(get) = %s, want \"databases\":[]", got)
	}

	// Create database with two object stores, an index, nested objects, arrays, and a Date.
	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": `new Promise((resolve, reject) => {
			const req = indexedDB.open('app_db', 2);
			req.onupgradeneeded = (ev) => {
				const db = ev.target.result;
				const users = db.createObjectStore('users', {keyPath: 'id', autoIncrement: true});
				users.createIndex('by_email', 'email', {unique: true});
				db.createObjectStore('meta');
			};
			req.onerror = () => reject(req.error);
			req.onsuccess = () => {
				const db = req.result;
				const tx = db.transaction(['users', 'meta'], 'readwrite');
				const users = tx.objectStore('users');
				users.add({id: 1, name: 'Charlie', email: 'c@example.com', tags: ['ops', 'dev'], created: new Date('2025-01-15T10:20:30.000Z')});
				users.add({id: 2, name: 'Alice', email: 'a@example.com', tags: ['admin']});
				users.add({id: 3, name: 'Bob', email: 'b@example.com', tags: []});
				users.add({id: 4, name: 'Dana', email: 'd@example.com', tags: ['qa']});
				users.add({id: 5, name: 'Eve', email: 'e@example.com', tags: ['sec']});
				const rich = {
					schema: 'v2',
					re: /abc/gi,
					set: new Set(['x', 'y']),
					map: new Map([['k1', 10]]),
					bytes: new Uint8Array([7, 8, 9]),
					floats: new Float64Array([1.5, NaN, Infinity]),
					big: 42n,
				};
				rich.self = rich;
				tx.objectStore('meta').put(rich, 'config');
				tx.oncomplete = () => { db.close(); resolve('ok'); };
				tx.onerror = () => reject(tx.error);
			};
		})`,
	})

	// Mode 1: list database names.
	listOut := callTool[indexedDBOutput](t, "indexeddb", map[string]any{"action": "get", "tab": tabID})
	if len(listOut.Databases) != 1 || listOut.Databases[0] != "app_db" {
		t.Fatalf("databases = %v, want [app_db]", listOut.Databases)
	}

	// Mode 2: inspect database schema (version, stores, keyPath, indexes).
	dbOut := callTool[indexedDBOutput](t, "indexeddb", map[string]any{
		"action":   "get",
		"tab":      tabID,
		"database": "app_db",
	})
	if dbOut.Database != "app_db" || dbOut.Version != 2 || len(dbOut.Stores) != 2 {
		t.Fatalf("inspect database = %+v, want app_db v2 with 2 stores", dbOut)
	}
	var usersStore *indexedDBStoreInfo
	for i := range dbOut.Stores {
		if dbOut.Stores[i].Name == "users" {
			usersStore = &dbOut.Stores[i]
		}
	}
	if usersStore == nil || usersStore.KeyPath != "id" || !usersStore.AutoIncrement || len(usersStore.Indexes) != 1 ||
		usersStore.Indexes[0].Name != "by_email" || usersStore.Indexes[0].KeyPath != "email" || !usersStore.Indexes[0].Unique {
		t.Fatalf("unexpected users store schema: %+v", usersStore)
	}

	// Mode 3: read page 1 (limit=2) and verify nested object + Date materialization.
	page1 := callTool[indexedDBOutput](t, "indexeddb", map[string]any{
		"action":   "get",
		"tab":      tabID,
		"database": "app_db",
		"store":    "users",
		"limit":    2,
	})
	if len(page1.Entries) != 2 || !page1.HasMore {
		t.Fatalf("page1 len=%d has_more=%v, want 2, true", len(page1.Entries), page1.HasMore)
	}
	var rec1 struct {
		ID      int      `json:"id"`
		Name    string   `json:"name"`
		Email   string   `json:"email"`
		Tags    []string `json:"tags"`
		Created string   `json:"created"`
	}
	rawVal, err := json.Marshal(page1.Entries[0].Value)
	if err != nil {
		t.Fatalf("marshal record 1 value: %v", err)
	}
	if err := json.Unmarshal(rawVal, &rec1); err != nil {
		t.Fatalf("unmarshal record 1 value: %v (raw=%s)", err, rawVal)
	}
	if rec1.ID != 1 || rec1.Name != "Charlie" || len(rec1.Tags) != 2 || rec1.Tags[0] != "ops" || rec1.Created != "2025-01-15T10:20:30.000Z" {
		t.Fatalf("record 1 materialized value = %+v, want Charlie with tags and ISO date", rec1)
	}

	// Page 2 (skip=2, limit=2) and final page (skip=4, limit=2).
	page2 := callTool[indexedDBOutput](t, "indexeddb", map[string]any{
		"action":   "get",
		"tab":      tabID,
		"database": "app_db",
		"store":    "users",
		"skip":     2,
		"limit":    2,
	})
	if len(page2.Entries) != 2 || !page2.HasMore || page2.Entries[0].Key != float64(3) {
		t.Fatalf("page2 = %+v, want 2 entries starting at key 3 with has_more=true", page2)
	}
	page3 := callTool[indexedDBOutput](t, "indexeddb", map[string]any{
		"action":   "get",
		"tab":      tabID,
		"database": "app_db",
		"store":    "users",
		"skip":     4,
		"limit":    2,
	})
	if len(page3.Entries) != 1 || page3.HasMore || page3.Entries[0].Key != float64(5) {
		t.Fatalf("page3 = %+v, want 1 entry key 5 with has_more=false", page3)
	}

	// Query through secondary index (by_email -> sorted a@example.com first, primaryKey=2).
	byEmail := callTool[indexedDBOutput](t, "indexeddb", map[string]any{
		"action":   "get",
		"tab":      tabID,
		"database": "app_db",
		"store":    "users",
		"index":    "by_email",
		"limit":    2,
	})
	if len(byEmail.Entries) != 2 || byEmail.Entries[0].Key != "a@example.com" || byEmail.Entries[0].PrimaryKey != float64(2) {
		t.Fatalf("by_email index entries = %+v, want first key=\"a@example.com\" primaryKey=2", byEmail.Entries)
	}

	// Clear object store "users" — users becomes empty ([]), meta remains intact.
	callTool[indexedDBOutput](t, "indexeddb", map[string]any{
		"action":   "delete",
		"tab":      tabID,
		"database": "app_db",
		"store":    "users",
	})
	rawClearedUsers := callToolRaw(t, "indexeddb", map[string]any{
		"action":   "get",
		"tab":      tabID,
		"database": "app_db",
		"store":    "users",
	})
	if rawClearedUsers.IsError || !strings.Contains(contentText(rawClearedUsers), `"entries":[]`) {
		t.Fatalf("after clearObjectStore, users = %s, want \"entries\":[]", contentText(rawClearedUsers))
	}
	metaOut := callTool[indexedDBOutput](t, "indexeddb", map[string]any{
		"action":   "get",
		"tab":      tabID,
		"database": "app_db",
		"store":    "meta",
	})
	if len(metaOut.Entries) != 1 || metaOut.Entries[0].Key != "config" {
		t.Fatalf("meta store after clearing users = %+v, want intact config record", metaOut.Entries)
	}
	rawMeta, _ := json.Marshal(metaOut.Entries[0].Value)
	var metaRec struct {
		Schema string   `json:"schema"`
		Re     string   `json:"re"`
		Set    []string `json:"set"`
		Map    [][]any  `json:"map"`
		Bytes  []int    `json:"bytes"`
		Floats []any    `json:"floats"`
		Big    string   `json:"big"`
		Self   string   `json:"self"`
	}
	if err := json.Unmarshal(rawMeta, &metaRec); err != nil {
		t.Fatalf("unmarshal structured-clone meta record: %v (raw=%s)", err, rawMeta)
	}
	if metaRec.Schema != "v2" || metaRec.Re != "/abc/gi" || len(metaRec.Set) != 2 || metaRec.Set[0] != "x" ||
		len(metaRec.Map) != 1 || len(metaRec.Bytes) != 3 || metaRec.Bytes[2] != 9 ||
		len(metaRec.Floats) != 3 || metaRec.Floats[0] != 1.5 || metaRec.Floats[1] != "NaN" || metaRec.Floats[2] != "Infinity" ||
		metaRec.Big != "42n" || metaRec.Self != "[Circular]" {
		t.Fatalf("structured-clone meta record = %+v (raw=%s)", metaRec, rawMeta)
	}

	// Concurrent reads on the same tab must not corrupt RemoteObject handles.
	var wg sync.WaitGroup
	errs := make(chan string, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := harness.session.CallTool(harness.ctx, &mcp.CallToolParams{
				Name: "indexeddb",
				Arguments: map[string]any{
					"action":   "get",
					"tab":      tabID,
					"database": "app_db",
					"store":    "meta",
				},
			})
			if err != nil {
				errs <- err.Error()
				return
			}
			if res.IsError || !strings.Contains(contentText(res), `"[Circular]"`) {
				errs <- contentText(res)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Errorf("concurrent indexeddb(get) failed or corrupted value: %s", e)
	}

	// Missing database and missing store must return clear errors.
	if errText := callToolExpectErr(t, "indexeddb", map[string]any{"action": "get", "tab": tabID, "database": "no_such_db"}); !strings.Contains(errText, "not found") {
		t.Errorf("missing database inspect err = %q, want not found", errText)
	}
	if errText := callToolExpectErr(t, "indexeddb", map[string]any{"action": "get", "tab": tabID, "database": "no_such_db", "store": "s"}); !strings.Contains(errText, "not found") {
		t.Errorf("missing database read err = %q, want not found", errText)
	}
	if errText := callToolExpectErr(t, "indexeddb", map[string]any{"action": "get", "tab": tabID, "database": "app_db", "store": "no_such_store"}); errText == "" {
		t.Error("missing object store returned no error")
	}

	// Delete entire database.
	callTool[indexedDBOutput](t, "indexeddb", map[string]any{
		"action":   "delete",
		"tab":      tabID,
		"database": "app_db",
	})
	afterDel := callTool[indexedDBOutput](t, "indexeddb", map[string]any{"action": "get", "tab": tabID})
	if len(afterDel.Databases) != 0 {
		t.Fatalf("after delete, databases = %v, want empty", afterDel.Databases)
	}
}

func TestIndexedDBValidation(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	badCases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"store_without_db", map[string]any{"action": "get", "tab": tabID, "store": "users"}, "requires database"},
		{"index_without_store", map[string]any{"action": "get", "tab": tabID, "database": "db", "index": "idx"}, "requires store"},
		{"negative_skip", map[string]any{"action": "get", "tab": tabID, "database": "db", "store": "s", "skip": -1}, "skip"},
		{"skip_without_store", map[string]any{"action": "get", "tab": tabID, "database": "db", "skip": 1}, "requires store"},
		{"negative_limit", map[string]any{"action": "get", "tab": tabID, "database": "db", "store": "s", "limit": -1}, "limit"},
		{"limit_without_store", map[string]any{"action": "get", "tab": tabID, "database": "db", "limit": 10}, "requires store"},
		{"delete_empty_db", map[string]any{"action": "delete", "tab": tabID, "database": ""}, "database"},
		{"delete_stray_index", map[string]any{"action": "delete", "tab": tabID, "database": "db", "index": "idx"}, "action='delete'"},
		{"delete_stray_limit", map[string]any{"action": "delete", "tab": tabID, "database": "db", "limit": 5}, "action='delete'"},
	}
	for _, tc := range badCases {
		t.Run(tc.name, func(t *testing.T) {
			if errText := callToolExpectErr(t, "indexeddb", tc.args); !strings.Contains(errText, tc.want) {
				t.Errorf("%s err = %q, want substring %q", tc.name, errText, tc.want)
			}
		})
	}
}
