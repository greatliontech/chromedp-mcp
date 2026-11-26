package tools

import (
	"context"
	"strings"
	"testing"
	"time"

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
	txt := callTool[GetDOMOutput](t, "get_dom", map[string]any{
		"tab":      t1.TabID,
		"format":   "text",
		"selector": "#title",
	})
	if strings.TrimSpace(txt.Content) != "Hello World" {
		t.Fatalf("get_dom(tab=t1) = %q, want Hello World", txt.Content)
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

func TestTabManagement(t *testing.T) {
	// Create a new tab.
	out := callTool[TabNewOutput](t, "tab_new", map[string]any{
		"url": fixtureURL("index.html"),
	})
	tab1 := out.TabID

	// Create another tab.
	out = callTool[TabNewOutput](t, "tab_new", map[string]any{
		"url": fixtureURL("page2.html"),
	})
	tab2 := out.TabID

	// List tabs.
	list := callTool[TabListOutput](t, "tab_list", map[string]any{})
	if len(list.Tabs) < 2 {
		t.Errorf("tab_list returned %d tabs, want >= 2", len(list.Tabs))
	}

	// Activate first tab.
	callTool[struct{}](t, "tab_activate", map[string]any{"tab": tab1})

	// Close both.
	closeTab(t, tab1)
	closeTab(t, tab2)
}

func TestTabActiveTabSwitch(t *testing.T) {
	// Create tab A.
	a := callTool[TabNewOutput](t, "tab_new", map[string]any{
		"url": fixtureURL("page2.html"),
	})
	defer closeTab(t, a.TabID)

	// Create tab B — should now be active.
	b := callTool[TabNewOutput](t, "tab_new", map[string]any{
		"url": fixtureURL("forms.html"),
	})
	defer closeTab(t, b.TabID)

	// List tabs and verify B is active.
	type tabListOut struct {
		Tabs []struct {
			ID     string `json:"id"`
			Active bool   `json:"active"`
		} `json:"tabs"`
	}
	list := callTool[tabListOut](t, "tab_list", map[string]any{})
	for _, tab := range list.Tabs {
		if tab.ID == b.TabID && !tab.Active {
			t.Error("newly created tab B should be active")
		}
		if tab.ID == a.TabID && tab.Active {
			t.Error("tab A should not be active after creating B")
		}
	}

	// Activate A explicitly.
	callTool[struct{}](t, "tab_activate", map[string]any{"tab": a.TabID})

	list = callTool[tabListOut](t, "tab_list", map[string]any{})
	for _, tab := range list.Tabs {
		if tab.ID == a.TabID && !tab.Active {
			t.Error("tab A should be active after tab_activate")
		}
	}
}

func TestTabCloseActiveSwitches(t *testing.T) {
	// Create two tabs.
	a := callTool[TabNewOutput](t, "tab_new", map[string]any{
		"url": fixtureURL("page2.html"),
	})
	b := callTool[TabNewOutput](t, "tab_new", map[string]any{
		"url": fixtureURL("forms.html"),
	})
	// B is active. Close B — A (MRU) should become active.
	closeTab(t, b.TabID)
	defer closeTab(t, a.TabID)

	type tabListOut struct {
		Tabs []struct {
			ID     string `json:"id"`
			Active bool   `json:"active"`
		} `json:"tabs"`
	}
	list := callTool[tabListOut](t, "tab_list", map[string]any{})
	for _, tab := range list.Tabs {
		if tab.ID == a.TabID && !tab.Active {
			t.Error("after closing active tab, MRU tab should become active")
		}
	}
}

func TestTabInvalidID(t *testing.T) {
	errText := callToolExpectErr(t, "tab_activate", map[string]any{
		"tab": "nonexistent-id",
	})
	if !strings.Contains(errText, "not found") {
		t.Errorf("expected 'not found' error, got: %s", errText)
	}
}

func TestTabCloseInvalidID(t *testing.T) {
	errText := callToolExpectErr(t, "tab_close", map[string]any{
		"tab": "nonexistent-tab-id",
	})
	if !strings.Contains(errText, "not found") {
		t.Errorf("tab_close error = %q, want 'not found'", errText)
	}
}

func TestTabNewWithoutURL(t *testing.T) {
	out := callTool[TabNewOutput](t, "tab_new", map[string]any{})
	defer closeTab(t, out.TabID)

	if out.TabID == "" {
		t.Fatal("tab_new returned empty tab_id")
	}
	if out.URL != "" {
		t.Errorf("tab_new without URL should have empty URL field, got %q", out.URL)
	}
}

func TestActiveTabDefault(t *testing.T) {
	// Create a tab and navigate it — it becomes the active tab.
	tabOut := callTool[TabNewOutput](t, "tab_new", map[string]any{
		"url": fixtureURL("page2.html"),
	})
	defer closeTab(t, tabOut.TabID)

	// Call get_dom WITHOUT the tab parameter — should use the active tab.
	out := callTool[GetDOMOutput](t, "get_dom", map[string]any{"format": "text"})
	if !strings.Contains(out.Content, "Page Two") {
		t.Errorf("get_dom without tab param = %q, want to contain 'Page Two'", out.Content)
	}
}

func TestConcurrentToolCallsOnDifferentTabs(t *testing.T) {
	tab1 := navigateToFixture(t, "index.html")
	defer closeTab(t, tab1)
	tab2 := navigateToFixture(t, "page2.html")
	defer closeTab(t, tab2)

	// Run evaluate on both tabs concurrently.
	type result struct {
		tab   string
		title string
	}
	ch := make(chan result, 2)

	go func() {
		out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
			"tab":        tab1,
			"expression": "document.title",
		})
		ch <- result{tab: "tab1", title: string(out.Result)}
	}()
	go func() {
		out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
			"tab":        tab2,
			"expression": "document.title",
		})
		ch <- result{tab: "tab2", title: string(out.Result)}
	}()

	results := make(map[string]string)
	for i := 0; i < 2; i++ {
		select {
		case r := <-ch:
			results[r.tab] = r.title
		case <-time.After(10 * time.Second):
			t.Fatal("timed out waiting for concurrent calls")
		}
	}

	if !strings.Contains(results["tab1"], "Test Page") {
		t.Errorf("tab1 title = %s, want 'Test Page'", results["tab1"])
	}
	if !strings.Contains(results["tab2"], "Page 2") {
		t.Errorf("tab2 title = %s, want 'Page 2'", results["tab2"])
	}
}

func TestTabListFieldsURLAndTitle(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	list := callTool[TabListOutput](t, "tab_list", map[string]any{})
	var found bool
	for _, tab := range list.Tabs {
		if tab.ID == tabID {
			found = true
			if tab.URL == "" {
				t.Error("tab URL should not be empty")
			}
			if tab.Title == "" {
				t.Error("tab Title should not be empty")
			}
		}
	}
	if !found {
		t.Errorf("tab %s not found in tab_list", tabID)
	}
}

func TestTabActivateIdempotent(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	// Activate the same tab twice — should not error.
	callTool[struct{}](t, "tab_activate", map[string]any{"tab": tabID})
	callTool[struct{}](t, "tab_activate", map[string]any{"tab": tabID})
}

func TestTabNewReturnsURL(t *testing.T) {
	url := fixtureURL("forms.html")
	out := callTool[TabNewOutput](t, "tab_new", map[string]any{
		"url": url,
	})
	defer closeTab(t, out.TabID)

	if out.URL != url {
		t.Errorf("tab_new URL = %q, want %q", out.URL, url)
	}
}

func TestPerTabCollectorIsolation(t *testing.T) {
	tab1 := navigateToFixture(t, "index.html")
	defer closeTab(t, tab1)
	tab2 := navigateToFixture(t, "errors.html")
	defer closeTab(t, tab2)
	waitForConsole(t, tab1)
	waitForJSErrors(t, tab2)

	// Tab1 has console logs (log, warn, error).
	// Tab2 has JS errors.

	// Tab1 should have console logs.
	logs := callTool[GetConsoleLogsOutput](t, "get_console_logs", map[string]any{
		"tab":  tab1,
		"mode": "peek",
	})
	if len(logs.Logs) == 0 {
		t.Error("tab1 should have console logs from index.html")
	}

	// Tab1 should NOT have JS errors (those are only from errors.html on tab2).
	errs := callTool[GetJSErrorsOutput](t, "get_js_errors", map[string]any{
		"tab":  tab1,
		"mode": "peek",
	})
	// index.html doesn't throw JS errors.
	for _, e := range errs.Errors {
		if strings.Contains(e.Message, "test error from page") {
			t.Error("tab1 should not have JS errors from tab2's errors.html")
		}
	}

	// Tab2 should have its own JS errors.
	errs2 := callTool[GetJSErrorsOutput](t, "get_js_errors", map[string]any{
		"tab":  tab2,
		"mode": "peek",
	})
	if len(errs2.Errors) == 0 {
		t.Error("tab2 should have JS errors from errors.html")
	}
}

func TestPerTabNetworkIsolation(t *testing.T) {
	tab1 := navigateToFixture(t, "network.html")
	defer closeTab(t, tab1)
	tab2 := navigateToFixture(t, "page2.html")
	defer closeTab(t, tab2)
	waitForNetwork(t, tab1, "/api/data")

	// Tab1 made fetch calls; tab2 did not.
	net1 := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":         tab1,
		"mode":        "peek",
		"url_pattern": "/api/data",
	})
	if len(net1.Requests) == 0 {
		t.Error("tab1 should have /api/data request")
	}

	// Tab2 should not have /api/data requests.
	net2 := callTool[GetNetworkRequestsOutput](t, "get_network_requests", map[string]any{
		"tab":         tab2,
		"mode":        "peek",
		"url_pattern": "/api/data",
	})
	if len(net2.Requests) != 0 {
		t.Errorf("tab2 should not have /api/data requests, got %d", len(net2.Requests))
	}
}

func TestTabNewInvalidBrowserID(t *testing.T) {
	errText := callToolExpectErr(t, "tab_new", map[string]any{
		"browser": "nonexistent-browser-id",
	})
	if !strings.Contains(errText, "not found") {
		t.Errorf("error = %q, want to contain 'not found'", errText)
	}
}

func TestTabCloseLastErrors(t *testing.T) {
	// Capture active harness browser to restore afterward.
	initList := callTool[BrowserListOutput](t, "browser_list", map[string]any{})
	var origBrowser string
	for _, br := range initList.Browsers {
		if br.Active {
			origBrowser = br.ID
			break
		}
	}

	// Launch a separate browser so we don't mess with the shared harness browser.
	b := callTool[BrowserLaunchOutput](t, "browser_launch", map[string]any{
		"headless": true,
	})
	defer func() {
		callTool[BrowserCloseOutput](t, "browser_close", map[string]any{
			"browser": b.BrowserID,
		})
		if origBrowser != "" {
			callTool[struct{}](t, "browser_activate", map[string]any{"browser": origBrowser})
		}
	}()

	if len(b.Tabs) != 1 {
		t.Fatalf("browser_launch returned %d tabs, want 1 default tab", len(b.Tabs))
	}
	defaultTabID := b.Tabs[0].ID

	// Close the only (default) tab.
	callTool[struct{}](t, "tab_close", map[string]any{"tab": defaultTabID})

	// tab_list should show 0 tabs.
	list := callTool[TabListOutput](t, "tab_list", map[string]any{
		"browser": b.BrowserID,
	})
	if len(list.Tabs) != 0 {
		t.Errorf("after closing last tab, tab_list shows %d tabs, want 0", len(list.Tabs))
	}

	// Default-tab tool call must error rather than silently auto-creating a tab.
	if errText := callToolExpectErr(t, "evaluate", map[string]any{
		"expression": "1+1",
	}); !strings.Contains(errText, "no open tabs") {
		t.Errorf("default-tab evaluate with 0 tabs err = %q, want 'no open tabs'", errText)
	}

	// A tool call referencing the closed tab ID should also error.
	if errText := callToolExpectErr(t, "evaluate", map[string]any{
		"tab":        defaultTabID,
		"expression": "1+1",
	}); errText == "" {
		t.Error("evaluate on closed tab should error")
	}
}

func TestConcurrentSameTab(t *testing.T) {
	tabID := navigateToFixture(t, "index.html")
	defer closeTab(t, tabID)

	done := make(chan struct{}, 3)

	// Run 3 concurrent evaluate calls on the same tab.
	for i := 0; i < 3; i++ {
		go func(n int) {
			defer func() { done <- struct{}{} }()
			callTool[EvaluateOutput](t, "evaluate", map[string]any{
				"tab":        tabID,
				"expression": "document.title",
			})
		}(i)
	}

	for i := 0; i < 3; i++ {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("concurrent same-tab calls timed out")
		}
	}
}

func TestTabMRUOrder(t *testing.T) {
	// Create three tabs: A, B, C.
	tabA := callTool[TabNewOutput](t, "tab_new", map[string]any{
		"url": fixtureURL("index.html"),
	})
	tabB := callTool[TabNewOutput](t, "tab_new", map[string]any{
		"url": fixtureURL("page2.html"),
	})
	tabC := callTool[TabNewOutput](t, "tab_new", map[string]any{
		"url": fixtureURL("forms.html"),
	})

	// MRU order should now be: A, B, C (C is most recent / active).

	// Activate A (MRU becomes: B, C, A).
	callTool[struct{}](t, "tab_activate", map[string]any{
		"tab": tabA.TabID,
	})

	// Close A. MRU fallback should go to C (the next most recent).
	closeTab(t, tabA.TabID)

	// Check which tab is now active.
	tabs := callTool[TabListOutput](t, "tab_list", map[string]any{})
	var activeID string
	for _, tab := range tabs.Tabs {
		if tab.Active {
			activeID = tab.ID
		}
	}

	if activeID != tabC.TabID {
		t.Errorf("after closing A, active tab = %q, want %q (C, the MRU fallback)", activeID, tabC.TabID)
		t.Logf("tabs: B=%s, C=%s", tabB.TabID, tabC.TabID)
	}

	// Clean up.
	closeTab(t, tabB.TabID)
	closeTab(t, tabC.TabID)
}

func TestTabMRUOrderComplexSequence(t *testing.T) {
	// Create tabs A, B, C, D.
	tabA := callTool[TabNewOutput](t, "tab_new", map[string]any{
		"url": fixtureURL("index.html"),
	})
	tabB := callTool[TabNewOutput](t, "tab_new", map[string]any{
		"url": fixtureURL("page2.html"),
	})
	tabC := callTool[TabNewOutput](t, "tab_new", map[string]any{
		"url": fixtureURL("forms.html"),
	})
	tabD := callTool[TabNewOutput](t, "tab_new", map[string]any{
		"url": fixtureURL("interaction.html"),
	})

	// MRU: A, B, C, D (D is active)

	// Activate B → MRU: A, C, D, B
	callTool[struct{}](t, "tab_activate", map[string]any{"tab": tabB.TabID})
	// Activate A → MRU: C, D, B, A
	callTool[struct{}](t, "tab_activate", map[string]any{"tab": tabA.TabID})
	// Activate C → MRU: D, B, A, C
	callTool[struct{}](t, "tab_activate", map[string]any{"tab": tabC.TabID})

	// Close C. Fallback should be A (MRU: D, B, A).
	closeTab(t, tabC.TabID)

	tabs := callTool[TabListOutput](t, "tab_list", map[string]any{})
	var activeAfterCloseC string
	for _, tab := range tabs.Tabs {
		if tab.Active {
			activeAfterCloseC = tab.ID
		}
	}
	if activeAfterCloseC != tabA.TabID {
		t.Errorf("after closing C, active = %q, want %q (A)", activeAfterCloseC, tabA.TabID)
	}

	// Close A. Fallback should be B (MRU: D, B).
	closeTab(t, tabA.TabID)
	tabs2 := callTool[TabListOutput](t, "tab_list", map[string]any{})
	var activeAfterCloseA string
	for _, tab := range tabs2.Tabs {
		if tab.Active {
			activeAfterCloseA = tab.ID
		}
	}
	if activeAfterCloseA != tabB.TabID {
		t.Errorf("after closing A, active = %q, want %q (B)", activeAfterCloseA, tabB.TabID)
	}

	// Clean up.
	closeTab(t, tabB.TabID)
	closeTab(t, tabD.TabID)
}

func TestTabContextCancelledByMCPContext(t *testing.T) {
	// Create a cancelled MCP context.
	reqCtx, reqCancel := context.WithCancel(context.Background())
	reqCancel() // cancel immediately

	// Create a long-lived "tab" context.
	tctx, tcancel := context.WithCancel(context.Background())
	defer tcancel()

	// tabContext should return a context that is already done.
	child, childCancel := tabContext(reqCtx, tctx)
	defer childCancel()

	select {
	case <-child.Done():
		// Good — the child was cancelled because reqCtx was cancelled.
	case <-time.After(1 * time.Second):
		t.Error("tabContext child was not cancelled when MCP request context was cancelled")
	}
}

func TestTabContextCancelledByTabContext(t *testing.T) {
	// Create a live MCP context.
	reqCtx, reqCancel := context.WithCancel(context.Background())
	defer reqCancel()

	// Create a tab context that we'll cancel.
	tctx, tcancel := context.WithCancel(context.Background())

	child, childCancel := tabContext(reqCtx, tctx)
	defer childCancel()

	// Cancel the tab context.
	tcancel()

	select {
	case <-child.Done():
		// Good — the child was cancelled because tctx was cancelled.
	case <-time.After(1 * time.Second):
		t.Error("tabContext child was not cancelled when tab context was cancelled")
	}
}

func TestTabContextNormalCleanup(t *testing.T) {
	// Both contexts live — child should only be done after cleanup.
	reqCtx, reqCancel := context.WithCancel(context.Background())
	defer reqCancel()

	tctx, tcancel := context.WithCancel(context.Background())
	defer tcancel()

	child, childCancel := tabContext(reqCtx, tctx)

	// Child should not be done yet.
	select {
	case <-child.Done():
		t.Fatal("tabContext child should not be cancelled before cleanup")
	default:
		// Good.
	}

	// Calling the cancel func should clean up.
	childCancel()
	select {
	case <-child.Done():
		// Good.
	case <-time.After(1 * time.Second):
		t.Error("tabContext child not cancelled after childCancel()")
	}
}
