package tools

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
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
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable"} {
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
	port, _ := startRemoteChromeWithKill(t, urls...)
	return port
}

// startRemoteChromeWithKill behaves like startRemoteChrome and additionally
// returns an idempotent kill function for tests that simulate external crashes.
func startRemoteChromeWithKill(t *testing.T, urls ...string) (int, func()) {
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
		"--enable-unsafe-swiftshader",
		fmt.Sprintf("--remote-debugging-port=%d", port),
		fmt.Sprintf("--user-data-dir=%s", userDataDir),
		firstURL,
	}

	cmd := exec.Command(chromeBin, args...)
	configureProcGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start remote Chrome: %v", err)
	}
	var killOnce sync.Once
	kill := func() {
		killOnce.Do(func() {
			killProcGroup(cmd)
		})
	}
	t.Cleanup(kill)

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
	return port, kill
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
	textOut := callTool[GetDOMOutput](t, "get_dom", map[string]any{
		"tab":      activeTab.ID,
		"format":   "text",
		"selector": "h1",
	})
	if strings.TrimSpace(textOut.Content) == "" {
		t.Errorf("expected non-empty h1 text from adopted tab, got %q", textOut.Content)
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
	textOut := callTool[GetDOMOutput](t, "get_dom", map[string]any{
		"tab":      connOut.Tabs[0].ID,
		"format":   "text",
		"selector": "h1",
	})
	if strings.TrimSpace(textOut.Content) == "" {
		t.Errorf("expected h1 text on healthy adopted tab, got %q", textOut.Content)
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

func TestBrowserLaunchListClose(t *testing.T) {
	// Launch a second browser.
	lo := callTool[BrowserLaunchOutput](t, "browser_launch", map[string]any{
		"headless": true,
	})
	if lo.BrowserID == "" {
		t.Fatal("browser_launch returned empty browser_id")
	}

	// List should show at least 2 browsers.
	list := callTool[BrowserListOutput](t, "browser_list", map[string]any{})
	if len(list.Browsers) < 2 {
		t.Errorf("browser_list returned %d browsers, want >= 2", len(list.Browsers))
	}

	// The new browser should be active.
	var foundActive bool
	for _, b := range list.Browsers {
		if b.ID == lo.BrowserID && b.Active {
			foundActive = true
		}
	}
	if !foundActive {
		t.Errorf("newly launched browser %s is not marked active", lo.BrowserID)
	}

	// Close the new browser.
	callTool[struct{}](t, "browser_close", map[string]any{
		"browser": lo.BrowserID,
	})

	// List should now have one fewer.
	list2 := callTool[BrowserListOutput](t, "browser_list", map[string]any{})
	for _, b := range list2.Browsers {
		if b.ID == lo.BrowserID {
			t.Errorf("closed browser %s still in list", lo.BrowserID)
		}
	}
}

func TestBrowserCloseInvalidID(t *testing.T) {
	errText := callToolExpectErr(t, "browser_close", map[string]any{
		"browser": "nonexistent-browser-id",
	})
	if !strings.Contains(errText, "not found") {
		t.Errorf("browser_close error = %q, want 'not found'", errText)
	}
}

func TestBrowserCloseActiveFallsBackToMRU(t *testing.T) {
	// Launch two browsers.
	b1 := callTool[BrowserLaunchOutput](t, "browser_launch", map[string]any{"headless": true})
	b2 := callTool[BrowserLaunchOutput](t, "browser_launch", map[string]any{"headless": true})

	// b2 is now active. Close it without specifying ID (should close active).
	callTool[BrowserCloseOutput](t, "browser_close", map[string]any{})

	// b1 should now be active.
	list := callTool[BrowserListOutput](t, "browser_list", map[string]any{})
	var b1Active, b2Found bool
	for _, b := range list.Browsers {
		if b.ID == b1.BrowserID && b.Active {
			b1Active = true
		}
		if b.ID == b2.BrowserID {
			b2Found = true
		}
	}
	if b2Found {
		t.Error("closed browser b2 should not be in list")
	}
	if !b1Active {
		t.Error("b1 should be active after closing active browser b2")
	}

	// Clean up.
	callTool[BrowserCloseOutput](t, "browser_close", map[string]any{"browser": b1.BrowserID})
}

func TestBrowserListModeField(t *testing.T) {
	list := callTool[BrowserListOutput](t, "browser_list", map[string]any{})
	if len(list.Browsers) == 0 {
		t.Fatal("expected at least one browser")
	}
	for _, b := range list.Browsers {
		if b.Mode != "launch" && b.Mode != "connect" {
			t.Errorf("browser mode = %q, want 'launch' or 'connect'", b.Mode)
		}
	}
}

func TestBrowserListTabsCount(t *testing.T) {
	list := callTool[BrowserListOutput](t, "browser_list", map[string]any{})
	if len(list.Browsers) == 0 {
		t.Fatal("expected at least one browser")
	}
	// The harness pre-launches a browser. There should be at least 0 tabs (tests
	// create/close tabs, but the initial browser should always exist).
	for _, b := range list.Browsers {
		if b.Tabs < 0 {
			t.Errorf("browser tabs count = %d, should be >= 0", b.Tabs)
		}
	}
}

func TestBrowserLaunchCustomDimensions(t *testing.T) {
	b := callTool[BrowserLaunchOutput](t, "browser_launch", map[string]any{
		"headless": true,
		"width":    800,
		"height":   600,
	})
	defer callTool[BrowserCloseOutput](t, "browser_close", map[string]any{
		"browser": b.BrowserID,
	})

	// Create a tab and verify viewport dimensions.
	tab := callTool[TabNewOutput](t, "tab_new", map[string]any{
		"url":     fixtureURL("page2.html"),
		"browser": b.BrowserID,
	})
	defer closeTab(t, tab.TabID)

	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tab.TabID,
		"expression": "window.innerWidth",
	})
	if string(out.Result) != "800" {
		t.Errorf("viewport width = %s, want 800", out.Result)
	}
}

func TestBrowserDoubleClose(t *testing.T) {
	b := callTool[BrowserLaunchOutput](t, "browser_launch", map[string]any{
		"headless": true,
	})

	// First close succeeds.
	callTool[BrowserCloseOutput](t, "browser_close", map[string]any{
		"browser": b.BrowserID,
	})

	// Second close should error.
	errText := callToolExpectErr(t, "browser_close", map[string]any{
		"browser": b.BrowserID,
	})
	if !strings.Contains(errText, "not found") {
		t.Errorf("double close error = %q, want 'not found'", errText)
	}
}

func TestBrowserListNotNil(t *testing.T) {
	list := callTool[BrowserListOutput](t, "browser_list", map[string]any{})
	if list.Browsers == nil {
		t.Error("browser list should be non-nil (empty array at minimum)")
	}
}

// findFreePort returns a free TCP port.
func findFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

// getWSURL retrieves the browser's WebSocket debugger URL from Chrome's
// /json/version endpoint, retrying a few times while Chrome starts up.
func getWSURL(t *testing.T, port int) string {
	t.Helper()
	url := fmt.Sprintf("http://127.0.0.1:%d/json/version", port)
	for i := 0; i < 20; i++ {
		resp, err := http.Get(url)
		if err != nil {
			time.Sleep(250 * time.Millisecond)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var info struct {
			WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
		}
		if err := json.Unmarshal(body, &info); err == nil && info.WebSocketDebuggerURL != "" {
			return info.WebSocketDebuggerURL
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatal("could not get WebSocket URL from Chrome /json/version")
	return ""
}

func TestBrowserConnect(t *testing.T) {
	port := startRemoteChrome(t)
	wsURL := getWSURL(t, port)

	// Now connect via the MCP tool.
	out := callTool[BrowserConnectOutput](t, "browser_connect", map[string]any{
		"url": wsURL,
	})
	if out.BrowserID == "" {
		t.Fatal("browser_connect returned empty browser ID")
	}

	// Verify the connected browser appears in browser_list with mode "connect".
	list := callTool[BrowserListOutput](t, "browser_list", map[string]any{})
	var found bool
	for _, b := range list.Browsers {
		if b.ID == out.BrowserID {
			found = true
			if b.Mode != "connect" {
				t.Errorf("connected browser mode = %q, want 'connect'", b.Mode)
			}
		}
	}
	if !found {
		t.Error("connected browser not found in browser_list")
	}

	// Create a tab on the connected browser and verify we can use it.
	tab := callTool[TabNewOutput](t, "tab_new", map[string]any{
		"url":     fixtureURL("page2.html"),
		"browser": out.BrowserID,
	})

	eval := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tab.TabID,
		"expression": "document.title",
	})
	if !strings.Contains(string(eval.Result), "Page 2") {
		t.Errorf("connected browser tab title = %s, want 'Page 2'", eval.Result)
	}

	closeTab(t, tab.TabID)

	// Close the connected browser (should disconnect, not kill the process).
	callTool[BrowserCloseOutput](t, "browser_close", map[string]any{
		"browser": out.BrowserID,
	})

	// Verify it's removed from the list.
	list2 := callTool[BrowserListOutput](t, "browser_list", map[string]any{})
	for _, b := range list2.Browsers {
		if b.ID == out.BrowserID {
			t.Error("closed connected browser still in list")
		}
	}

	// The Chrome process should still be running (we just disconnected).
	// Verify by hitting the /json/version endpoint again.
	wsURL2 := getWSURL(t, port)
	if wsURL2 == "" {
		t.Error("Chrome process should still be running after disconnect")
	}
}

func TestBrowserConnectInvalidURL(t *testing.T) {
	errText := callToolExpectErr(t, "browser_connect", map[string]any{
		"url": "ws://127.0.0.1:1/devtools/browser/nonexistent",
	})
	if errText == "" {
		t.Error("browser_connect to invalid URL should error")
	}
}

func TestBrowserKilledExternally(t *testing.T) {
	port, kill := startRemoteChromeWithKill(t)
	wsURL := getWSURL(t, port)

	// Connect via MCP tool.
	out := callTool[BrowserConnectOutput](t, "browser_connect", map[string]any{
		"url": wsURL,
	})
	browserID := out.BrowserID
	if browserID == "" {
		t.Fatal("browser_connect returned empty ID")
	}

	// Create a tab so we have a tab ID to reference.
	tabOut := callTool[TabNewOutput](t, "tab_new", map[string]any{
		"browser": browserID,
	})
	tabID := tabOut.TabID

	// Kill Chrome externally.
	kill()

	// Poll until chromedp detects the dead connection and tool calls error.
	var errText string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		result := callToolRaw(t, "get_dom", map[string]any{
			"tab":     tabID,
			"format":  "text",
			"timeout": 500,
		})
		if result.IsError {
			errText = contentText(result)
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if errText == "" {
		t.Error("get_dom on killed browser should error")
	}
	if !strings.Contains(errText, "no longer running") && !strings.Contains(errText, "not found") {
		t.Logf("error text: %s", errText)
	}

	// browser_list should no longer include the dead browser.
	list := callTool[BrowserListOutput](t, "browser_list", map[string]any{})
	for _, b := range list.Browsers {
		if b.ID == browserID {
			t.Error("dead browser should have been pruned from browser_list")
		}
	}
}

func TestBrowserConnectHTTPURL(t *testing.T) {
	port := startRemoteChrome(t)

	// Connect via http:// URL instead of ws:// — browser.Connect resolves
	// /json/version before dialing the WebSocket.
	httpURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	out := callTool[BrowserConnectOutput](t, "browser_connect", map[string]any{
		"url": httpURL,
	})
	if out.BrowserID == "" {
		t.Fatal("browser_connect via http:// returned empty browser ID")
	}

	// Verify it works by creating a tab and evaluating JS.
	tab := callTool[TabNewOutput](t, "tab_new", map[string]any{
		"url":     fixtureURL("index.html"),
		"browser": out.BrowserID,
	})

	eval := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tab.TabID,
		"expression": "document.title",
	})
	if !strings.Contains(string(eval.Result), "Test") {
		t.Errorf("connected via http:// but eval returned unexpected title: %s", eval.Result)
	}

	closeTab(t, tab.TabID)
	callTool[BrowserCloseOutput](t, "browser_close", map[string]any{
		"browser": out.BrowserID,
	})
}

// TestBrowserCloseNoBrowsers verifies that browser_close with no browser ID
// returns a clear error when no browsers exist.
func TestBrowserCloseNoBrowsers(t *testing.T) {
	sess := newIsolatedSession(t, &Options{})

	res, err := sess.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "browser_close",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("browser_close protocol error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected error closing active browser when no browsers exist")
	}
	if text := contentText(res); !strings.Contains(text, "no browser") {
		t.Errorf("browser_close error = %q, want mention of 'no browser'", text)
	}
}

func TestConcurrentBrowserAndTabOperations(t *testing.T) {
	// Launch a dedicated browser for this test.
	b := callTool[BrowserLaunchOutput](t, "browser_launch", map[string]any{
		"headless": true,
	})
	defer callTool[BrowserCloseOutput](t, "browser_close", map[string]any{
		"browser": b.BrowserID,
	})

	// Create several tabs.
	const numTabs = 5
	tabIDs := make([]string, numTabs)
	for i := 0; i < numTabs; i++ {
		out := callTool[TabNewOutput](t, "tab_new", map[string]any{
			"browser": b.BrowserID,
		})
		tabIDs[i] = out.TabID
	}

	// Run concurrent operations: list, activate, and list again.
	// The race detector will catch any lock ordering issues.
	done := make(chan struct{}, 20)
	for i := 0; i < 5; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			callTool[TabListOutput](t, "tab_list", map[string]any{
				"browser": b.BrowserID,
			})
		}()
		go func(idx int) {
			defer func() { done <- struct{}{} }()
			callTool[BrowserListOutput](t, "browser_list", map[string]any{})
		}(i)
	}

	// Wait for all concurrent operations.
	for i := 0; i < 10; i++ {
		<-done
	}

	// Clean up tabs.
	for _, id := range tabIDs {
		closeTab(t, id)
	}
}

func TestNoBrowserError(t *testing.T) {
	sess := newIsolatedSession(t, &Options{})

	// With no browsers running, tab-scoped tools must tell the caller to
	// launch or connect a browser first.
	res, err := sess.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "navigate",
		Arguments: map[string]any{"url": fixtureURL("index.html")},
	})
	if err != nil {
		t.Fatalf("navigate protocol error: %v", err)
	}
	if !res.IsError {
		t.Fatal("navigate without any running browser should error")
	}
	if got := contentText(res); !strings.Contains(got, "browser_launch") {
		t.Errorf("no-browser error = %q, want guidance mentioning browser_launch", got)
	}

	// Explicit reference to a nonexistent tab ID on the shared harness also errors.
	errText := callToolExpectErr(t, "navigate", map[string]any{
		"url": fixtureURL("index.html"),
		"tab": "nonexistent-tab-id",
	})
	if !strings.Contains(errText, "not found") {
		t.Errorf("error should mention 'not found', got: %s", errText)
	}
}
