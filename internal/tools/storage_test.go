package tools

import (
	"strings"
	"testing"
)

// --- get_storage ---

func TestGetStorage_SingleKey(t *testing.T) {
	tabID := navigateToFixture(t, "storage.html")
	defer closeTab(t, tabID)

	out := callTool[GetStorageOutput](t, "get_storage", map[string]any{
		"tab": tabID,
		"key": "ls-key1",
	})
	if out.Value == nil {
		t.Fatal("expected non-nil value for ls-key1")
	}
	if *out.Value != "ls-value1" {
		t.Errorf("get_storage value = %q, want %q", *out.Value, "ls-value1")
	}
}

func TestGetStorage_AllEntries(t *testing.T) {
	tabID := navigateToFixture(t, "storage.html")
	defer closeTab(t, tabID)

	out := callTool[GetStorageOutput](t, "get_storage", map[string]any{
		"tab": tabID,
	})
	if out.Entries == nil {
		t.Fatal("expected non-nil entries")
	}
	// The fixture sets ls-key1, ls-key2, ls-json. There may be more
	// from other tests sharing the origin, so check at least these.
	for _, key := range []string{"ls-key1", "ls-key2", "ls-json"} {
		if _, ok := out.Entries[key]; !ok {
			t.Errorf("expected key %q in entries", key)
		}
	}
	if out.Entries["ls-key1"] != "ls-value1" {
		t.Errorf("entries[ls-key1] = %q, want %q", out.Entries["ls-key1"], "ls-value1")
	}
}

func TestGetStorage_MissingKey(t *testing.T) {
	tabID := navigateToFixture(t, "storage.html")
	defer closeTab(t, tabID)

	out := callTool[GetStorageOutput](t, "get_storage", map[string]any{
		"tab": tabID,
		"key": "nonexistent-key",
	})
	if out.Value != nil {
		t.Errorf("expected nil value for missing key, got %q", *out.Value)
	}
}

func TestGetStorage_SessionStorage(t *testing.T) {
	tabID := navigateToFixture(t, "storage.html")
	defer closeTab(t, tabID)

	out := callTool[GetStorageOutput](t, "get_storage", map[string]any{
		"tab":  tabID,
		"type": "session",
		"key":  "ss-key1",
	})
	if out.Value == nil {
		t.Fatal("expected non-nil value for ss-key1")
	}
	if *out.Value != "ss-value1" {
		t.Errorf("get_storage value = %q, want %q", *out.Value, "ss-value1")
	}
}

// --- set_storage ---

func TestSetStorage(t *testing.T) {
	tabID := navigateToFixture(t, "storage.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "set_storage", map[string]any{
		"tab":   tabID,
		"key":   "new-key",
		"value": "new-value",
	})

	// Read it back.
	out := callTool[GetStorageOutput](t, "get_storage", map[string]any{
		"tab": tabID,
		"key": "new-key",
	})
	if out.Value == nil || *out.Value != "new-value" {
		t.Errorf("after set, get_storage = %v, want %q", out.Value, "new-value")
	}
}

func TestSetStorage_Overwrite(t *testing.T) {
	tabID := navigateToFixture(t, "storage.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "set_storage", map[string]any{
		"tab":   tabID,
		"key":   "ls-key1",
		"value": "overwritten",
	})

	out := callTool[GetStorageOutput](t, "get_storage", map[string]any{
		"tab": tabID,
		"key": "ls-key1",
	})
	if out.Value == nil || *out.Value != "overwritten" {
		t.Errorf("after overwrite, get_storage = %v, want %q", out.Value, "overwritten")
	}
}

func TestSetStorage_SessionStorage(t *testing.T) {
	tabID := navigateToFixture(t, "storage.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "set_storage", map[string]any{
		"tab":   tabID,
		"type":  "session",
		"key":   "ss-new",
		"value": "ss-new-value",
	})

	out := callTool[GetStorageOutput](t, "get_storage", map[string]any{
		"tab":  tabID,
		"type": "session",
		"key":  "ss-new",
	})
	if out.Value == nil || *out.Value != "ss-new-value" {
		t.Errorf("after set session, get_storage = %v, want %q", out.Value, "ss-new-value")
	}
}

// --- delete_storage ---

func TestDeleteStorage_SingleKey(t *testing.T) {
	tabID := navigateToFixture(t, "storage.html")
	defer closeTab(t, tabID)

	// Verify key exists first.
	out := callTool[GetStorageOutput](t, "get_storage", map[string]any{
		"tab": tabID,
		"key": "ls-key1",
	})
	if out.Value == nil {
		t.Fatal("ls-key1 should exist before delete")
	}

	// Delete it.
	callTool[struct{}](t, "delete_storage", map[string]any{
		"tab": tabID,
		"key": "ls-key1",
	})

	// Verify it's gone.
	out = callTool[GetStorageOutput](t, "get_storage", map[string]any{
		"tab": tabID,
		"key": "ls-key1",
	})
	if out.Value != nil {
		t.Errorf("ls-key1 should be nil after delete, got %q", *out.Value)
	}

	// Verify other keys still exist.
	out = callTool[GetStorageOutput](t, "get_storage", map[string]any{
		"tab": tabID,
		"key": "ls-key2",
	})
	if out.Value == nil {
		t.Error("ls-key2 should still exist after deleting ls-key1")
	}
}

func TestDeleteStorage_ClearAll(t *testing.T) {
	tabID := navigateToFixture(t, "storage.html")
	defer closeTab(t, tabID)

	// Clear all localStorage.
	callTool[struct{}](t, "delete_storage", map[string]any{
		"tab": tabID,
	})

	// Verify it's empty.
	out := callTool[GetStorageKeysOutput](t, "get_storage_keys", map[string]any{
		"tab": tabID,
	})
	if len(out.Keys) != 0 {
		t.Errorf("expected 0 keys after clear, got %d", len(out.Keys))
	}
}

func TestDeleteStorage_SessionStorage(t *testing.T) {
	tabID := navigateToFixture(t, "storage.html")
	defer closeTab(t, tabID)

	callTool[struct{}](t, "delete_storage", map[string]any{
		"tab":  tabID,
		"type": "session",
		"key":  "ss-key1",
	})

	out := callTool[GetStorageOutput](t, "get_storage", map[string]any{
		"tab":  tabID,
		"type": "session",
		"key":  "ss-key1",
	})
	if out.Value != nil {
		t.Errorf("ss-key1 should be nil after delete, got %q", *out.Value)
	}

	// Other session key still exists.
	out = callTool[GetStorageOutput](t, "get_storage", map[string]any{
		"tab":  tabID,
		"type": "session",
		"key":  "ss-key2",
	})
	if out.Value == nil {
		t.Error("ss-key2 should still exist")
	}
}

// --- get_storage_keys ---

func TestGetStorageKeys(t *testing.T) {
	tabID := navigateToFixture(t, "storage.html")
	defer closeTab(t, tabID)

	out := callTool[GetStorageKeysOutput](t, "get_storage_keys", map[string]any{
		"tab": tabID,
	})
	if len(out.Keys) < 3 {
		t.Fatalf("expected at least 3 keys, got %d", len(out.Keys))
	}

	keyMap := make(map[string]int)
	for _, k := range out.Keys {
		keyMap[k.Key] = k.Size
	}

	// ls-key1 = "ls-value1" (9 chars)
	if size, ok := keyMap["ls-key1"]; !ok {
		t.Error("expected ls-key1 in keys")
	} else if size != 9 {
		t.Errorf("ls-key1 size = %d, want 9", size)
	}

	// ls-json = '{"a":1}' (7 chars)
	if size, ok := keyMap["ls-json"]; !ok {
		t.Error("expected ls-json in keys")
	} else if size != 7 {
		t.Errorf("ls-json size = %d, want 7", size)
	}
}

func TestGetStorageKeys_Limit(t *testing.T) {
	tabID := navigateToFixture(t, "storage.html")
	defer closeTab(t, tabID)

	out := callTool[GetStorageKeysOutput](t, "get_storage_keys", map[string]any{
		"tab":   tabID,
		"limit": 1,
	})
	if len(out.Keys) != 1 {
		t.Errorf("expected 1 key with limit=1, got %d", len(out.Keys))
	}
}

func TestGetStorageKeys_SessionStorage(t *testing.T) {
	tabID := navigateToFixture(t, "storage.html")
	defer closeTab(t, tabID)

	out := callTool[GetStorageKeysOutput](t, "get_storage_keys", map[string]any{
		"tab":  tabID,
		"type": "session",
	})
	if len(out.Keys) < 2 {
		t.Fatalf("expected at least 2 session keys, got %d", len(out.Keys))
	}

	keyMap := make(map[string]bool)
	for _, k := range out.Keys {
		keyMap[k.Key] = true
	}
	for _, key := range []string{"ss-key1", "ss-key2"} {
		if !keyMap[key] {
			t.Errorf("expected %q in session storage keys", key)
		}
	}
}

// --- error cases ---

func TestStorage_InvalidType(t *testing.T) {
	tabID := navigateToFixture(t, "storage.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "get_storage", map[string]any{
		"tab":  tabID,
		"type": "invalid",
	})
	if !strings.Contains(errText, "invalid storage type") {
		t.Errorf("expected invalid type error, got: %s", errText)
	}
}

func TestSetStorage_MissingKey(t *testing.T) {
	tabID := navigateToFixture(t, "storage.html")
	defer closeTab(t, tabID)

	errText := callToolExpectErr(t, "set_storage", map[string]any{
		"tab":   tabID,
		"value": "something",
	})
	// The MCP SDK validates required fields before our handler runs.
	if !strings.Contains(errText, "key") {
		t.Errorf("expected error about missing key, got: %s", errText)
	}
}

// --- cross-tab behavior ---

func TestStorage_CrossTabVisibility(t *testing.T) {
	// localStorage is per-origin, so a value set in one tab should be
	// visible from another tab on the same origin.
	tab1 := navigateToFixture(t, "storage.html")
	defer closeTab(t, tab1)

	// Set a unique key in tab1.
	callTool[struct{}](t, "set_storage", map[string]any{
		"tab":   tab1,
		"key":   "cross-tab-key",
		"value": "cross-tab-value",
	})

	// Open a second tab on the same origin.
	tab2 := navigateToFixture(t, "index.html")
	defer closeTab(t, tab2)

	// Read from tab2.
	out := callTool[GetStorageOutput](t, "get_storage", map[string]any{
		"tab": tab2,
		"key": "cross-tab-key",
	})
	if out.Value == nil || *out.Value != "cross-tab-value" {
		t.Errorf("cross-tab get_storage = %v, want %q", out.Value, "cross-tab-value")
	}
}
