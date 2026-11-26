package tools

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestStorage_SingleKeyAllEntriesAndMissing(t *testing.T) {
	tabID := navigateToFixture(t, "storage.html")
	defer closeTab(t, tabID)

	out := callTool[storageOutput](t, "storage", map[string]any{
		"action": "get",
		"tab":    tabID,
		"key":    "ls-key1",
	})
	if out.Value == nil || *out.Value != "ls-value1" {
		t.Fatalf("storage(get, ls-key1) = %v, want ls-value1", out.Value)
	}

	all := callTool[storageOutput](t, "storage", map[string]any{
		"action": "get",
		"tab":    tabID,
	})
	if all.Entries == nil {
		t.Fatal("expected non-nil entries")
	}
	for _, key := range []string{"ls-key1", "ls-key2", "ls-json"} {
		if _, ok := all.Entries[key]; !ok {
			t.Errorf("expected key %q in entries", key)
		}
	}

	missing := callTool[storageOutput](t, "storage", map[string]any{
		"action": "get",
		"tab":    tabID,
		"key":    "nonexistent-key",
	})
	if missing.Value != nil {
		t.Errorf("expected nil value for missing key, got %q", *missing.Value)
	}

	ss := callTool[storageOutput](t, "storage", map[string]any{
		"action": "get",
		"tab":    tabID,
		"type":   "session",
		"key":    "ss-key1",
	})
	if ss.Value == nil || *ss.Value != "ss-value1" {
		t.Errorf("session storage ss-key1 = %v, want ss-value1", ss.Value)
	}
}

func TestStorage_SetOverwriteAndDelete(t *testing.T) {
	tabID := navigateToFixture(t, "storage.html")
	defer closeTab(t, tabID)

	// Set new key, overwrite, and set empty string value.
	callTool[storageOutput](t, "storage", map[string]any{
		"action": "set",
		"tab":    tabID,
		"key":    "new-key",
		"value":  "new-value",
	})
	callTool[storageOutput](t, "storage", map[string]any{
		"action": "set",
		"tab":    tabID,
		"key":    "ls-key1",
		"value":  "overwritten",
	})
	callTool[storageOutput](t, "storage", map[string]any{
		"action": "set",
		"tab":    tabID,
		"key":    "empty-val",
		"value":  "",
	})

	if got := callTool[storageOutput](t, "storage", map[string]any{"action": "get", "tab": tabID, "key": "new-key"}); got.Value == nil || *got.Value != "new-value" {
		t.Errorf("new-key = %v, want new-value", got.Value)
	}
	if got := callTool[storageOutput](t, "storage", map[string]any{"action": "get", "tab": tabID, "key": "ls-key1"}); got.Value == nil || *got.Value != "overwritten" {
		t.Errorf("ls-key1 = %v, want overwritten", got.Value)
	}
	if got := callTool[storageOutput](t, "storage", map[string]any{"action": "get", "tab": tabID, "key": "empty-val"}); got.Value == nil || *got.Value != "" {
		t.Errorf("empty-val = %v, want empty string", got.Value)
	}

	// Delete single key preserves siblings.
	callTool[storageOutput](t, "storage", map[string]any{
		"action": "delete",
		"tab":    tabID,
		"key":    "ls-key1",
	})
	if got := callTool[storageOutput](t, "storage", map[string]any{"action": "get", "tab": tabID, "key": "ls-key1"}); got.Value != nil {
		t.Errorf("ls-key1 should be nil after delete, got %q", *got.Value)
	}
	if got := callTool[storageOutput](t, "storage", map[string]any{"action": "get", "tab": tabID, "key": "ls-key2"}); got.Value == nil {
		t.Error("ls-key2 should still exist after deleting ls-key1")
	}

	// Session storage set and delete.
	callTool[storageOutput](t, "storage", map[string]any{
		"action": "set",
		"tab":    tabID,
		"type":   "session",
		"key":    "ss-new",
		"value":  "ss-new-value",
	})
	callTool[storageOutput](t, "storage", map[string]any{
		"action": "delete",
		"tab":    tabID,
		"type":   "session",
		"key":    "ss-key1",
	})
	if got := callTool[storageOutput](t, "storage", map[string]any{"action": "get", "tab": tabID, "type": "session", "key": "ss-new"}); got.Value == nil || *got.Value != "ss-new-value" {
		t.Errorf("ss-new = %v, want ss-new-value", got.Value)
	}
	if got := callTool[storageOutput](t, "storage", map[string]any{"action": "get", "tab": tabID, "type": "session", "key": "ss-key1"}); got.Value != nil {
		t.Errorf("ss-key1 should be nil after delete, got %q", *got.Value)
	}

	// Clear all localStorage.
	callTool[storageOutput](t, "storage", map[string]any{
		"action": "delete",
		"tab":    tabID,
	})
	keysAfter := callTool[storageOutput](t, "storage", map[string]any{
		"action":    "get",
		"tab":       tabID,
		"keys_only": true,
	})
	if len(keysAfter.Keys) != 0 {
		t.Errorf("expected 0 keys after clear, got %d", len(keysAfter.Keys))
	}
}

func TestStorage_KeysOnlyLimitAndMaxBytes(t *testing.T) {
	tabID := navigateToFixture(t, "storage.html")
	defer closeTab(t, tabID)

	// Clear and seed deterministic sessionStorage entries.
	callTool[storageOutput](t, "storage", map[string]any{"action": "delete", "tab": tabID, "type": "session"})
	callTool[storageOutput](t, "storage", map[string]any{"action": "set", "tab": tabID, "type": "session", "key": "a", "value": "11111"})
	callTool[storageOutput](t, "storage", map[string]any{"action": "set", "tab": tabID, "type": "session", "key": "b", "value": "2222222222"})
	callTool[storageOutput](t, "storage", map[string]any{"action": "set", "tab": tabID, "type": "session", "key": "c", "value": "🌍🌍🌍"})

	// keys_only returns sorted key names + byte lengths.
	allKeys := callTool[storageOutput](t, "storage", map[string]any{
		"action":    "get",
		"tab":       tabID,
		"type":      "session",
		"keys_only": true,
	})
	if len(allKeys.Keys) != 3 || allKeys.Total == nil || *allKeys.Total != 3 || allKeys.Truncated {
		t.Fatalf("allKeys = %+v", allKeys)
	}
	if allKeys.Keys[0].Key != "a" || allKeys.Keys[0].Size != 5 ||
		allKeys.Keys[1].Key != "b" || allKeys.Keys[1].Size != 10 ||
		allKeys.Keys[2].Key != "c" || allKeys.Keys[2].Size != 12 {
		t.Fatalf("unexpected keys/sizes: %+v", allKeys.Keys)
	}

	// keys_only with limit=1 reports total=3 and truncated=true.
	limKeys := callTool[storageOutput](t, "storage", map[string]any{
		"action":    "get",
		"tab":       tabID,
		"type":      "session",
		"keys_only": true,
		"limit":     1,
	})
	if len(limKeys.Keys) != 1 || limKeys.Keys[0].Key != "a" || limKeys.Total == nil || *limKeys.Total != 3 || !limKeys.Truncated {
		t.Fatalf("limKeys = %+v, want [a] total=3 truncated=true", limKeys)
	}

	// Single-key max_bytes truncates on a valid UTF-8 rune boundary (4-byte emoji).
	truncVal := callTool[storageOutput](t, "storage", map[string]any{
		"action":    "get",
		"tab":       tabID,
		"type":      "session",
		"key":       "c",
		"max_bytes": 5,
	})
	if truncVal.Value == nil || *truncVal.Value != "🌍" || !utf8.ValidString(*truncVal.Value) || !truncVal.Truncated || truncVal.TotalBytes != 12 {
		t.Fatalf("truncated single key = val=%v truncated=%v total_bytes=%d, want '🌍'/true/12",
			truncVal.Value, truncVal.Truncated, truncVal.TotalBytes)
	}

	// All-entries limit caps returned entries and reports total & truncated.
	limitedAll := callTool[storageOutput](t, "storage", map[string]any{
		"action": "get",
		"tab":    tabID,
		"type":   "session",
		"limit":  2,
	})
	if len(limitedAll.Entries) != 2 || limitedAll.Entries["a"] != "11111" || limitedAll.Entries["b"] != "2222222222" || limitedAll.Total == nil || *limitedAll.Total != 3 || !limitedAll.Truncated {
		t.Fatalf("limitedAll = %+v, want 2 entries (a,b) total=3 truncated=true", limitedAll)
	}

	// All-entries max_bytes stops before exceeding budget and reports total & truncated.
	// Entry "a" takes 1+5=6 bytes; entry "b" takes 1+10=11 bytes (6+11=17 > 10).
	boundedAll := callTool[storageOutput](t, "storage", map[string]any{
		"action":    "get",
		"tab":       tabID,
		"type":      "session",
		"max_bytes": 10,
	})
	if len(boundedAll.Entries) != 1 || boundedAll.Entries["a"] != "11111" || boundedAll.Total == nil || *boundedAll.Total != 3 || !boundedAll.Truncated {
		t.Fatalf("boundedAll = %+v, want {a:11111} total=3 truncated=true", boundedAll)
	}
}

func TestStorage_Validation(t *testing.T) {
	tabID := navigateToFixture(t, "storage.html")
	defer closeTab(t, tabID)

	badCases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"invalid_type", map[string]any{"action": "get", "tab": tabID, "type": "invalid"}, "invalid"},
		{"set_missing_key", map[string]any{"action": "set", "tab": tabID, "value": "something"}, "key"},
		{"set_missing_value", map[string]any{"action": "set", "tab": tabID, "key": "k"}, "value"},
		{"set_stray_keys_only", map[string]any{"action": "set", "tab": tabID, "key": "k", "value": "v", "keys_only": true}, "action='set'"},
		{"get_stray_value", map[string]any{"action": "get", "tab": tabID, "value": ""}, "action='get'"},
		{"get_keys_only_and_key", map[string]any{"action": "get", "tab": tabID, "keys_only": true, "key": "a"}, "keys_only"},
		{"get_keys_only_and_max_bytes", map[string]any{"action": "get", "tab": tabID, "keys_only": true, "max_bytes": 10}, "keys_only"},
		{"get_key_and_limit", map[string]any{"action": "get", "tab": tabID, "key": "a", "limit": 5}, "limit"},
		{"get_neg_max_bytes", map[string]any{"action": "get", "tab": tabID, "max_bytes": -1}, "max_bytes"},
		{"get_neg_limit", map[string]any{"action": "get", "tab": tabID, "keys_only": true, "limit": -1}, "limit"},
		{"delete_stray_value", map[string]any{"action": "delete", "tab": tabID, "value": ""}, "action='delete'"},
		{"delete_stray_keys_only", map[string]any{"action": "delete", "tab": tabID, "keys_only": true}, "action='delete'"},
	}
	for _, tc := range badCases {
		t.Run(tc.name, func(t *testing.T) {
			errText := callToolExpectErr(t, "storage", tc.args)
			if !strings.Contains(errText, tc.want) {
				t.Errorf("%s err = %q, want substring %q", tc.name, errText, tc.want)
			}
		})
	}
}

func TestStorage_CrossTabVisibility(t *testing.T) {
	tab1 := navigateToFixture(t, "storage.html")
	defer closeTab(t, tab1)

	callTool[storageOutput](t, "storage", map[string]any{
		"action": "set",
		"tab":    tab1,
		"key":    "cross-tab-key",
		"value":  "cross-tab-value",
	})

	tab2 := navigateToFixture(t, "index.html")
	defer closeTab(t, tab2)

	out := callTool[storageOutput](t, "storage", map[string]any{
		"action": "get",
		"tab":    tab2,
		"key":    "cross-tab-key",
	})
	if out.Value == nil || *out.Value != "cross-tab-value" {
		t.Errorf("cross-tab storage(get) = %v, want cross-tab-value", out.Value)
	}
}

func TestStorage_EmptyAllEntriesVsMissingKeyJSON(t *testing.T) {
	tabID := navigateToFixture(t, "storage.html")
	defer closeTab(t, tabID)

	callTool[storageOutput](t, "storage", map[string]any{
		"action": "delete",
		"tab":    tabID,
		"type":   "session",
	})

	allRaw := callToolRaw(t, "storage", map[string]any{
		"action": "get",
		"tab":    tabID,
		"type":   "session",
	})
	if allRaw.IsError {
		t.Fatalf("storage(get) all entries error: %s", contentText(allRaw))
	}
	allJSON := contentText(allRaw)
	if !strings.Contains(allJSON, `"entries":{}`) {
		t.Errorf("empty all-entries JSON = %s, want '\"entries\":{}'", allJSON)
	}
	if strings.Contains(allJSON, `"value"`) {
		t.Errorf("all-entries JSON should omit 'value', got: %s", allJSON)
	}

	missingRaw := callToolRaw(t, "storage", map[string]any{
		"action": "get",
		"tab":    tabID,
		"type":   "session",
		"key":    "definitely-missing",
	})
	if missingRaw.IsError {
		t.Fatalf("storage(get) missing key error: %s", contentText(missingRaw))
	}
	missingJSON := contentText(missingRaw)
	if !strings.Contains(missingJSON, `"value":null`) {
		t.Errorf("missing single-key JSON = %s, want '\"value\":null'", missingJSON)
	}
	if strings.Contains(missingJSON, `"entries"`) {
		t.Errorf("single-key JSON should omit 'entries', got: %s", missingJSON)
	}
}

func TestIsLocalStorage(t *testing.T) {
	tests := []struct {
		input   string
		isLocal bool
		wantErr bool
	}{
		{input: "", isLocal: true, wantErr: false},
		{input: "local", isLocal: true, wantErr: false},
		{input: "session", isLocal: false, wantErr: false},
		{input: "invalid", isLocal: false, wantErr: true},
		{input: "Local", isLocal: false, wantErr: true},
		{input: "SESSION", isLocal: false, wantErr: true},
		{input: "indexeddb", isLocal: false, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := isLocalStorage(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("isLocalStorage(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.isLocal {
				t.Errorf("isLocalStorage(%q) = %v, want %v", tt.input, got, tt.isLocal)
			}
			if tt.wantErr && err != nil && !strings.Contains(err.Error(), "invalid storage type") {
				t.Errorf("isLocalStorage(%q) error = %q, want it to contain 'invalid storage type'", tt.input, err)
			}
		})
	}
}
