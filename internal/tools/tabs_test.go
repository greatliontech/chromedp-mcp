package tools

import (
	"strings"
	"testing"
)

func TestTabNewFailedNavigationRollsBackTab(t *testing.T) {
	// Establish a known active tab first.
	existing := navigateToFixture(t, "index.html")
	defer closeTab(t, existing)

	before := callTool[TabListOutput](t, "tab_list", map[string]any{})

	// Creating a tab with an unreachable/invalid URL must fail AND roll back
	// the newly created tab so it neither leaks nor hijacks the active tab.
	errText := callToolExpectErr(t, "tab_new", map[string]any{
		"url": "http://127.0.0.1:1/unreachable",
	})
	if errText == "" {
		t.Fatal("tab_new with unreachable URL should fail")
	}

	after := callTool[TabListOutput](t, "tab_list", map[string]any{})
	if len(after.Tabs) != len(before.Tabs) {
		t.Fatalf("tab count changed after failed tab_new: before=%d after=%d", len(before.Tabs), len(after.Tabs))
	}
	var activeID string
	for _, tb := range after.Tabs {
		if tb.Active {
			activeID = tb.ID
		}
	}
	if activeID != existing {
		t.Errorf("active tab after failed tab_new = %q, want %q", activeID, existing)
	}
}

func TestTabNewRedirectReturnsFinalURL(t *testing.T) {
	out := callTool[TabNewOutput](t, "tab_new", map[string]any{
		"url": fixtureURL("redirect"),
	})
	defer closeTab(t, out.TabID)

	if !strings.HasSuffix(out.URL, "/page2.html") {
		t.Errorf("tab_new redirect URL = %q, want suffix /page2.html", out.URL)
	}
}

func TestMultiBrowserTabRoutingAndActivate(t *testing.T) {
	b1 := callTool[BrowserLaunchOutput](t, "browser_launch", map[string]any{"headless": true})
	defer callTool[BrowserCloseOutput](t, "browser_close", map[string]any{"browser": b1.BrowserID})
	t1 := callTool[TabNewOutput](t, "tab_new", map[string]any{
		"browser": b1.BrowserID,
		"url":     fixtureURL("index.html"),
	})

	b2 := callTool[BrowserLaunchOutput](t, "browser_launch", map[string]any{"headless": true})
	defer callTool[BrowserCloseOutput](t, "browser_close", map[string]any{"browser": b2.BrowserID})
	t2 := callTool[TabNewOutput](t, "tab_new", map[string]any{
		"browser": b2.BrowserID,
		"url":     fixtureURL("page2.html"),
	})
	_ = t2

	// b2 is currently active, but explicit tab=t1 must still reach t1 in b1.
	txt := callTool[GetTextOutput](t, "get_text", map[string]any{
		"tab":      t1.TabID,
		"selector": "#title",
	})
	if strings.TrimSpace(txt.Text) != "Hello World" {
		t.Fatalf("get_text(tab=t1) = %q, want Hello World", txt.Text)
	}

	// Activating t1 must also switch the active browser to b1 so default-tab
	// calls now hit t1 rather than b2's t2.
	callTool[struct{}](t, "tab_activate", map[string]any{"tab": t1.TabID})
	defaultEval := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"expression": "document.title",
	})
	if !strings.Contains(string(defaultEval.Result), "Test Page") {
		t.Errorf("default-tab evaluate after tab_activate(t1) title = %s, want Test Page", defaultEval.Result)
	}
}
