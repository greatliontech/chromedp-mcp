package tools

import (
	"strings"
	"testing"

	"github.com/chromedp/chromedp"
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

func TestBrowserLaunchLeveragesDefaultTabWithoutTabNew(t *testing.T) {
	// Capture active harness browser to restore afterward.
	initList := callTool[BrowserListOutput](t, "browser_list", map[string]any{})
	var origBrowser string
	for _, b := range initList.Browsers {
		if b.Active {
			origBrowser = b.ID
			break
		}
	}

	bOut := callTool[BrowserLaunchOutput](t, "browser_launch", map[string]any{"headless": true})
	defer func() {
		callTool[BrowserCloseOutput](t, "browser_close", map[string]any{"browser": bOut.BrowserID})
		if origBrowser != "" {
			callTool[struct{}](t, "browser_activate", map[string]any{"browser": origBrowser})
		}
	}()

	// Launch must attach Chrome's initial default tab (exactly 1 active tab,
	// no rogue untracked blank tab alongside it).
	if len(bOut.Tabs) != 1 || bOut.Tabs[0].ID == "" || !bOut.Tabs[0].Active {
		t.Fatalf("browser_launch Tabs = %+v, want 1 active default tab", bOut.Tabs)
	}
	defaultTabID := bOut.Tabs[0].ID

	// Immediately call navigate WITHOUT tab_new or explicit tab ID.
	nav := callTool[NavigateOutput](t, "navigate", map[string]any{
		"url": fixtureURL("page2.html"),
	})
	if !strings.HasSuffix(nav.URL, "/page2.html") || nav.Title != "Page 2" {
		t.Fatalf("immediate navigate after browser_launch = %+v, want /page2.html 'Page 2'", nav)
	}

	// Verify tab_list reports the same single tab now at /page2.html.
	tabs := callTool[TabListOutput](t, "tab_list", map[string]any{"browser": bOut.BrowserID})
	if len(tabs.Tabs) != 1 || tabs.Tabs[0].ID != defaultTabID || !strings.HasSuffix(tabs.Tabs[0].URL, "/page2.html") {
		t.Fatalf("tab_list after immediate navigate = %+v, want single tab %q at /page2.html", tabs.Tabs, defaultTabID)
	}

	// Verify Chrome itself only has 1 page target (no extra untracked blank tab).
	bObj, err := harness.mgr.Resolve(bOut.BrowserID)
	if err != nil {
		t.Fatalf("Resolve(%q): %v", bOut.BrowserID, err)
	}
	rawTargets, err := chromedp.Targets(bObj.Tabs.Active().Context())
	if err != nil {
		t.Fatalf("chromedp.Targets: %v", err)
	}
	pageTargets := 0
	for _, info := range rawTargets {
		if info != nil && info.Type == "page" && info.Subtype == "" {
			pageTargets++
		}
	}
	if pageTargets != 1 {
		t.Errorf("Chrome page target count = %d, want 1 (no extra untracked blank tab)", pageTargets)
	}

	// Verify collectors work on the adopted default tab (console + network).
	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"expression": `console.log('default-tab-log'); fetch('/api/data').then(r => r.json())`,
	})
	logs := callTool[GetConsoleLogsOutput](t, "get_console_logs", map[string]any{"mode": "drain"})
	if len(logs.Logs) == 0 || !strings.Contains(logs.Logs[0].Text, "default-tab-log") {
		t.Errorf("default tab console collector missed log: %+v", logs.Logs)
	}
	waitForNetwork(t, defaultTabID, "/api/data")
	netOut := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"mode":        "drain",
		"url_pattern": "/api/data",
	})
	if len(netOut.Requests) == 0 {
		t.Errorf("default tab network collector missed /api/data request")
	}

	// Closing the default tab leaves 0 open tabs — calling navigate now must
	// still error ("no open tabs") rather than silently auto-creating, AND
	// tab_new must still work on the same browser!
	callTool[struct{}](t, "tab_close", map[string]any{"tab": defaultTabID})
	if errText := callToolExpectErr(t, "navigate", map[string]any{"url": fixtureURL("forms.html")}); !strings.Contains(errText, "no open tabs") {
		t.Errorf("navigate after closing last tab err = %q, want 'no open tabs'", errText)
	}
	reopened := callTool[TabNewOutput](t, "tab_new", map[string]any{
		"browser": bOut.BrowserID,
		"url":     fixtureURL("forms.html"),
	})
	if reopened.TabID == "" || !strings.HasSuffix(reopened.URL, "/forms.html") {
		t.Errorf("tab_new after closing default tab = %+v, want live tab at /forms.html", reopened)
	}
}
