package tools

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"strings"
	"testing"
	"time"
)

type devtoolsTargetInfo struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	URL   string `json:"url"`
	Title string `json:"title"`
}

func listDevToolsPageTargets(t *testing.T, port int) []devtoolsTargetInfo {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/json/list", port))
	if err != nil {
		t.Fatalf("GET /json/list: %v", err)
	}
	defer resp.Body.Close()
	var all []devtoolsTargetInfo
	if err := json.NewDecoder(resp.Body).Decode(&all); err != nil {
		t.Fatalf("decode /json/list: %v", err)
	}
	var pages []devtoolsTargetInfo
	for _, info := range all {
		if info.Type == "page" {
			pages = append(pages, info)
		}
	}
	return pages
}

func findChrome(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"chromium", "google-chrome", "chromium-browser"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	t.Skip("chromium/google-chrome not found in PATH")
	return ""
}

// startRemoteChrome launches an external headless Chrome process in its own
// process group with remote debugging enabled and opens urls. t.Cleanup kills
// the entire process group before t.TempDir removes the user-data-dir so
// Chrome's child helper processes cannot race TempDir cleanup.
func startRemoteChrome(t *testing.T, urls ...string) int {
	t.Helper()
	chromeBin := findChrome(t)
	port := findFreePort(t)
	userDataDir := t.TempDir()

	firstURL := "about:blank"
	if len(urls) > 0 {
		firstURL = urls[0]
	}
	args := []string{
		"--headless=new",
		"--no-sandbox",
		"--disable-gpu",
		fmt.Sprintf("--remote-debugging-port=%d", port),
		fmt.Sprintf("--user-data-dir=%s", userDataDir),
		firstURL,
	}

	cmd := exec.Command(chromeBin, args...)
	configureProcGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start remote Chrome: %v", err)
	}
	t.Cleanup(func() {
		killProcGroup(cmd)
	})

	_ = getWSURL(t, port)

	// Headless Chrome only accepts one CLI startup URL; open additional tabs
	// via DevTools PUT /json/new?<url>.
	for _, u := range urls[min(1, len(urls)):] {
		req, err := http.NewRequest(http.MethodPut, fmt.Sprintf("http://127.0.0.1:%d/json/new?%s", port, url.QueryEscape(u)), nil)
		if err != nil {
			t.Fatalf("build /json/new request: %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("PUT /json/new for %s: %v", u, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("PUT /json/new for %s returned status %d", u, resp.StatusCode)
		}
	}
	return port
}

func TestBrowserConnectAdoptsExistingTabsWithoutRogueBlankTab(t *testing.T) {
	url1 := fixtureURL("forms.html")
	url2 := fixtureURL("page2.html")
	port := startRemoteChrome(t, url1, url2)

	// Wait for both initial tabs to finish navigating.
	deadline := time.Now().Add(5 * time.Second)
	var beforePages []devtoolsTargetInfo
	for time.Now().Before(deadline) {
		beforePages = listDevToolsPageTargets(t, port)
		if len(beforePages) == 2 &&
			((strings.HasSuffix(beforePages[0].URL, "/forms.html") && strings.HasSuffix(beforePages[1].URL, "/page2.html")) ||
				(strings.HasSuffix(beforePages[0].URL, "/page2.html") && strings.HasSuffix(beforePages[1].URL, "/forms.html"))) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(beforePages) != 2 {
		t.Fatalf("precondition: expected 2 remote page targets, got %+v", beforePages)
	}

	// Connect via browser_connect — must adopt both pages and NOT spawn a blank tab.
	connOut := callTool[BrowserConnectOutput](t, "browser_connect", map[string]any{
		"url": fmt.Sprintf("http://127.0.0.1:%d", port),
	})
	if connOut.BrowserID == "" {
		t.Fatal("browser_connect returned empty browser_id")
	}
	if len(connOut.Tabs) != 2 {
		t.Fatalf("browser_connect adopted %d tabs, want 2: %+v", len(connOut.Tabs), connOut.Tabs)
	}
	activeTab := connOut.Tabs[len(connOut.Tabs)-1]
	if !activeTab.Active {
		t.Errorf("MRU tail adopted tab should be active: %+v", activeTab)
	}
	if connOut.Tabs[0].Active {
		t.Errorf("older adopted tab should not be active: %+v", connOut.Tabs[0])
	}
	gotURLs := map[string]bool{
		connOut.Tabs[0].URL: true,
		connOut.Tabs[1].URL: true,
	}
	if !gotURLs[url1] || !gotURLs[url2] {
		t.Errorf("adopted tab URLs = %v, want both %q and %q", gotURLs, url1, url2)
	}

	// Chrome must still have exactly 2 page targets (no rogue blank tab created).
	afterConnectPages := listDevToolsPageTargets(t, port)
	if len(afterConnectPages) != 2 {
		t.Fatalf("browser_connect spawned rogue tab(s): before=%d after=%d (%+v)", len(beforePages), len(afterConnectPages), afterConnectPages)
	}

	// Immediately inspect an adopted tab without calling tab_new or navigate.
	textOut := callTool[GetTextOutput](t, "get_text", map[string]any{
		"tab":      activeTab.ID,
		"selector": "h1",
	})
	if strings.TrimSpace(textOut.Text) == "" {
		t.Errorf("expected non-empty h1 text from adopted tab, got %q", textOut.Text)
	}

	// Disconnect via browser_close — must detach and leave both user tabs open.
	callTool[BrowserCloseOutput](t, "browser_close", map[string]any{
		"browser": connOut.BrowserID,
	})
	afterDisconnectPages := listDevToolsPageTargets(t, port)
	if len(afterDisconnectPages) != 2 {
		t.Fatalf("disconnecting closed or altered user's tabs: %+v", afterDisconnectPages)
	}

	// Re-connect and verify explicit tab_close closes the adopted target in
	// Chrome synchronously (without needing an external retry loop).
	connOut2 := callTool[BrowserConnectOutput](t, "browser_connect", map[string]any{
		"url": fmt.Sprintf("http://127.0.0.1:%d", port),
	})
	t.Cleanup(func() {
		callTool[BrowserCloseOutput](t, "browser_close", map[string]any{
			"browser": connOut2.BrowserID,
		})
	})
	if len(connOut2.Tabs) != 2 {
		t.Fatalf("reconnect adopted %d tabs, want 2", len(connOut2.Tabs))
	}
	closedURL := connOut2.Tabs[0].URL
	callTool[struct{}](t, "tab_close", map[string]any{"tab": connOut2.Tabs[0].ID})

	afterClosePages := listDevToolsPageTargets(t, port)
	if len(afterClosePages) != 1 || afterClosePages[0].URL == closedURL {
		t.Errorf("explicit tab_close did not synchronously close adopted target %q: remaining=%+v", closedURL, afterClosePages)
	}
}

func TestBrowserConnectSkipsWedgedTabWithoutHanging(t *testing.T) {
	// An infinite JS loop wedges the renderer's main thread without needing an
	// already-attached CDP session, so chromedp's Runtime.evaluate("self")
	// during AttachTarget would hang forever without adoptTargetTimeout.
	wedgedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>Wedged</title></head><body><script>for(;;){}</script></body></html>`))
	}))
	defer wedgedSrv.Close()

	healthyURL := fixtureURL("page2.html")
	port := startRemoteChrome(t, wedgedSrv.URL, healthyURL)

	// Wait until both targets appear and the wedged tab enters its infinite loop.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		pages := listDevToolsPageTargets(t, port)
		if len(pages) == 2 &&
			((strings.HasPrefix(pages[0].URL, wedgedSrv.URL) && strings.HasSuffix(pages[1].URL, "/page2.html")) ||
				(strings.HasPrefix(pages[1].URL, wedgedSrv.URL) && strings.HasSuffix(pages[0].URL, "/page2.html"))) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(250 * time.Millisecond)

	start := time.Now()
	connOut := callTool[BrowserConnectOutput](t, "browser_connect", map[string]any{
		"url": fmt.Sprintf("http://127.0.0.1:%d", port),
	})
	elapsed := time.Since(start)
	t.Cleanup(func() {
		callTool[BrowserCloseOutput](t, "browser_close", map[string]any{
			"browser": connOut.BrowserID,
		})
	})

	if elapsed > 8*time.Second {
		t.Fatalf("browser_connect took %v with a wedged tab, expected bounded per-target skip", elapsed)
	}
	// The wedged tab must have been skipped, leaving only the healthy page2.html tab.
	if len(connOut.Tabs) != 1 || !strings.HasSuffix(connOut.Tabs[0].URL, "/page2.html") {
		t.Fatalf("expected only healthy page2.html tab to be adopted, got %+v", connOut.Tabs)
	}
	textOut := callTool[GetTextOutput](t, "get_text", map[string]any{
		"tab":      connOut.Tabs[0].ID,
		"selector": "h1",
	})
	if strings.TrimSpace(textOut.Text) == "" {
		t.Errorf("expected h1 text on healthy adopted tab, got %q", textOut.Text)
	}
}

func TestBrowserActivateSwitchesActiveBrowser(t *testing.T) {
	// Capture the initial harness browser ID.
	initList := callTool[BrowserListOutput](t, "browser_list", map[string]any{})
	if len(initList.Browsers) == 0 {
		t.Fatal("expected at least one harness browser")
	}
	b1 := initList.Browsers[0].ID

	// Create a tab in b1 navigated to forms.html.
	t1 := callTool[TabNewOutput](t, "tab_new", map[string]any{
		"browser": b1,
		"url":     fixtureURL("forms.html"),
	})
	defer closeTab(t, t1.TabID)

	// Launch a second browser b2 (which becomes active) with a tab on page2.html.
	b2Out := callTool[BrowserLaunchOutput](t, "browser_launch", map[string]any{
		"headless": true,
	})
	b2 := b2Out.BrowserID
	defer callTool[BrowserCloseOutput](t, "browser_close", map[string]any{"browser": b2})

	t2 := callTool[TabNewOutput](t, "tab_new", map[string]any{
		"browser": b2,
		"url":     fixtureURL("page2.html"),
	})
	defer closeTab(t, t2.TabID)

	// Verify b2 is currently active.
	urlOut := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"expression": `location.pathname`,
	})
	if !strings.Contains(string(urlOut.Result), "page2.html") {
		t.Fatalf("expected active browser b2 on page2.html, got %s", urlOut.Result)
	}

	// Switch active browser back to b1 via browser_activate.
	callTool[struct{}](t, "browser_activate", map[string]any{"browser": b1})

	list := callTool[BrowserListOutput](t, "browser_list", map[string]any{})
	var b1Active, b2Active bool
	for _, b := range list.Browsers {
		if b.ID == b1 {
			b1Active = b.Active
		}
		if b.ID == b2 {
			b2Active = b.Active
		}
	}
	if !b1Active || b2Active {
		t.Errorf("after browser_activate(b1): b1Active=%v b2Active=%v, want true/false", b1Active, b2Active)
	}

	// Default-tab evaluate (no tab param) must now target b1's active tab (forms.html).
	urlOut = callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"expression": `location.pathname`,
	})
	if !strings.Contains(string(urlOut.Result), "forms.html") {
		t.Errorf("after browser_activate(b1), default evaluate returned %s, want forms.html", urlOut.Result)
	}

	// Validation: empty and unknown browser IDs fail cleanly.
	if err := callToolExpectErr(t, "browser_activate", map[string]any{"browser": ""}); !strings.Contains(err, "browser ID") {
		t.Errorf("empty browser_activate err = %q", err)
	}
	if err := callToolExpectErr(t, "browser_activate", map[string]any{"browser": "nonexistent-browser"}); !strings.Contains(err, "not found") {
		t.Errorf("unknown browser_activate err = %q", err)
	}
}
