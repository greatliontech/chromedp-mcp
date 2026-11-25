package tools

import (
	"slices"
	"strings"
	"testing"
	"time"

	cdpbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/performancetimeline"
)

func TestClearBuffersSelectivePreservesOtherBuffers(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	b, tb, err := harness.mgr.ResolveTabWithBrowser("", tabID)
	if err != nil {
		t.Fatalf("ResolveTabWithBrowser: %v", err)
	}
	// Reset all buffers for a clean baseline.
	callTool[ClearBuffersOutput](t, "clear_buffers", map[string]any{"tab": tabID})

	// Populate all 7 buffers:
	// 1. console + 2. errors + 3. network + 6. url_changes via page execution.
	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": `(async function() {
			console.log('buf-msg');
			setTimeout(function() { throw new Error('buf-err'); }, 0);
			await fetch('/api/data');
			history.pushState({}, '', '/step1');
			return true;
		})()`,
	})
	// Wait briefly for the async console, error, network, and URL-change events to settle.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(tb.Console.Peek("", 0)) > 0 && len(tb.JSErrors.Peek(0)) > 0 &&
			len(tb.Network.Peek(nil, 0)) > 0 && tb.URLChanges.CountSince(time.Time{}) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(tb.Console.Peek("", 0)) == 0 || len(tb.JSErrors.Peek(0)) == 0 ||
		len(tb.Network.Peek(nil, 0)) == 0 || tb.URLChanges.CountSince(time.Time{}) == 0 {
		t.Fatalf("precondition buffers not populated: console=%d errors=%d network=%d url_changes=%d",
			len(tb.Console.Peek("", 0)), len(tb.JSErrors.Peek(0)),
			len(tb.Network.Peek(nil, 0)), tb.URLChanges.CountSince(time.Time{}))
	}

	// 4. websocket: one open connection with a buffered frame + one closed connection.
	tb.WebSocket.HandleCreated(&network.EventWebSocketCreated{
		RequestID: network.RequestID("ws-open"),
		URL:       "ws://127.0.0.1/open",
	})
	tb.WebSocket.HandleFrameReceived(&network.EventWebSocketFrameReceived{
		RequestID: network.RequestID("ws-open"),
		Response:  &network.WebSocketFrame{Opcode: 1, PayloadData: []byte("pre-clear")},
	})
	tb.WebSocket.HandleCreated(&network.EventWebSocketCreated{
		RequestID: network.RequestID("ws-closed"),
		URL:       "ws://127.0.0.1/closed",
	})
	tb.WebSocket.HandleClosed(&network.EventWebSocketClosed{
		RequestID: network.RequestID("ws-closed"),
	})
	// 5. performance (both layout shift and LCP)
	tb.Performance.HandleTimelineEvent(&performancetimeline.EventTimelineEventAdded{
		Event: &performancetimeline.TimelineEvent{
			Type:               "layout-shift",
			Time:               cdp.TimeSinceEpoch(1700000200),
			LayoutShiftDetails: &performancetimeline.LayoutShift{Value: 0.1},
		},
	})
	tb.Performance.HandleTimelineEvent(&performancetimeline.EventTimelineEventAdded{
		Event: &performancetimeline.TimelineEvent{
			Type:       "largest-contentful-paint",
			Time:       cdp.TimeSinceEpoch(1700000201),
			LcpDetails: &performancetimeline.LargestContentfulPaint{Size: 500},
		},
	})
	// 7. downloads (browser-scoped)
	b.Downloads.HandleDownloadWillBegin(&cdpbrowser.EventDownloadWillBegin{
		GUID:              "dl-buf-1",
		URL:               "https://example.com/file.bin",
		SuggestedFilename: "file.bin",
	})
	b.Downloads.HandleDownloadProgress(&cdpbrowser.EventDownloadProgress{
		GUID:          "dl-buf-1",
		TotalBytes:    10,
		ReceivedBytes: 10,
		State:         cdpbrowser.DownloadProgressStateCompleted,
	})

	// Clear ONLY console and network (passing duplicates to verify dedup & canonical order).
	out := callTool[ClearBuffersOutput](t, "clear_buffers", map[string]any{
		"tab":     tabID,
		"targets": []string{"network", "console", "console"},
	})
	wantCleared := []string{"console", "network"}
	if !slices.Equal(out.Cleared, wantCleared) {
		t.Fatalf("Cleared = %v, want %v", out.Cleared, wantCleared)
	}
	if len(tb.Console.Peek("", 0)) != 0 {
		t.Errorf("console buffer not cleared: %+v", tb.Console.Peek("", 0))
	}
	if len(tb.Network.Peek(nil, 0)) != 0 {
		t.Errorf("network buffer not cleared: %+v", tb.Network.Peek(nil, 0))
	}
	// All other 5 buffers must remain intact.
	if len(tb.JSErrors.Peek(0)) == 0 {
		t.Error("errors buffer was unexpectedly cleared")
	}
	if len(tb.WebSocket.Connections()) == 0 {
		t.Error("websocket buffer was unexpectedly cleared")
	}
	if len(tb.Performance.PeekLayoutShifts(0)) == 0 || len(tb.Performance.PeekLCP(0)) == 0 {
		t.Error("performance buffer was unexpectedly cleared")
	}
	if tb.URLChanges.CountSince(time.Time{}) == 0 {
		t.Error("url_changes buffer was unexpectedly cleared")
	}
	if len(b.Downloads.Peek(0)) == 0 {
		t.Error("downloads buffer was unexpectedly cleared")
	}

	// Clear errors, websocket, performance, url_changes — downloads still preserved.
	out2 := callTool[ClearBuffersOutput](t, "clear_buffers", map[string]any{
		"tab":     tabID,
		"targets": []string{"url_changes", "performance", "websocket", "errors"},
	})
	wantCleared2 := []string{"errors", "websocket", "performance", "url_changes"}
	if !slices.Equal(out2.Cleared, wantCleared2) {
		t.Fatalf("second Cleared = %v, want %v", out2.Cleared, wantCleared2)
	}
	if len(tb.JSErrors.Peek(0)) != 0 ||
		len(tb.Performance.PeekLayoutShifts(0)) != 0 || len(tb.Performance.PeekLCP(0)) != 0 ||
		tb.URLChanges.CountSince(time.Time{}) != 0 {
		t.Error("one of errors/performance/url_changes was not cleared")
	}
	// WebSocket clear drops closed connections and clears open-socket frame rings
	// while keeping open connections tracked for future frames.
	wsConns := tb.WebSocket.Connections()
	if len(wsConns) != 1 || wsConns[0].ID != "ws-open" {
		t.Fatalf("after websocket clear, Connections = %+v, want only [ws-open]", wsConns)
	}
	if frames, ok := tb.WebSocket.Frames("ws-open", "received", 0, true); !ok || len(frames) != 0 {
		t.Errorf("after websocket clear, ws-open received frames = %+v (ok=%v), want empty ok=true", frames, ok)
	}
	if len(b.Downloads.Peek(0)) == 0 {
		t.Error("downloads buffer should still be intact")
	}

	// Verify url_changes preserved its baseline URL (/step1) and main frame ID
	// after clear: if baseline were wiped, pushState would be dropped and count
	// would stay 0.
	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `history.pushState({}, '', '/step2'); true`,
	})
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if tb.URLChanges.CountSince(time.Time{}) > 0 {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if got := tb.URLChanges.CountSince(time.Time{}); got != 1 {
		t.Errorf("url_changes count after clear + pushState = %d, want 1 (baseline preserved)", got)
	}
}

func TestClearBuffersAllDefaultAndPreservesLiveDialog(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	b, tb, err := harness.mgr.ResolveTabWithBrowser("", tabID)
	if err != nil {
		t.Fatalf("ResolveTabWithBrowser: %v", err)
	}

	// Seed console, downloads, and open a live alert dialog.
	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `console.log('before-all'); setTimeout(function() { alert('still-open'); }, 10); true`,
	})
	defer handleDialog(t, tabID, map[string]any{"accept": true})
	b.Downloads.HandleDownloadWillBegin(&cdpbrowser.EventDownloadWillBegin{
		GUID:              "dl-all-1",
		URL:               "https://example.com/a.bin",
		SuggestedFilename: "a.bin",
	})
	b.Downloads.HandleDownloadProgress(&cdpbrowser.EventDownloadProgress{
		GUID:  "dl-all-1",
		State: cdpbrowser.DownloadProgressStateCompleted,
	})

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if callTool[GetDialogOutput](t, "get_dialog", map[string]any{"tab": tabID}).Open {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if !callTool[GetDialogOutput](t, "get_dialog", map[string]any{"tab": tabID}).Open {
		t.Fatal("timed out waiting for alert dialog")
	}

	// Omitted targets clears all 7 buffers in canonical order.
	out := callTool[ClearBuffersOutput](t, "clear_buffers", map[string]any{"tab": tabID})
	if !slices.Equal(out.Cleared, clearBufferOrder) {
		t.Fatalf("default Cleared = %v, want %v", out.Cleared, clearBufferOrder)
	}
	if len(tb.Console.Peek("", 0)) != 0 || len(b.Downloads.Peek(0)) != 0 {
		t.Error("default clear_buffers did not clear console and downloads")
	}
	// Live dialog state MUST NOT be cleared by clear_buffers.
	if d := callTool[GetDialogOutput](t, "get_dialog", map[string]any{"tab": tabID}); !d.Open {
		t.Fatal("clear_buffers corrupted open live dialog state")
	}

	// Explicit ["all", "console"] clears all 7 without duplicates.
	outAll := callTool[ClearBuffersOutput](t, "clear_buffers", map[string]any{
		"tab":     tabID,
		"targets": []string{"all", "console"},
	})
	if !slices.Equal(outAll.Cleared, clearBufferOrder) {
		t.Fatalf("['all','console'] Cleared = %v, want %v", outAll.Cleared, clearBufferOrder)
	}
}

func TestClearBuffersDownloadsOnlyWithoutOpenTabAndValidation(t *testing.T) {
	// Capture harness browser ID and one of its tabs before launching b2.
	initList := callTool[BrowserListOutput](t, "browser_list", map[string]any{})
	b1ID := initList.Browsers[0].ID
	tabInB1 := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabInB1)

	// Launch a second browser with 0 open tabs.
	b2Out := callTool[BrowserLaunchOutput](t, "browser_launch", map[string]any{"headless": true})
	b2ID := b2Out.BrowserID
	defer func() {
		callTool[BrowserCloseOutput](t, "browser_close", map[string]any{"browser": b2ID})
		callTool[struct{}](t, "browser_activate", map[string]any{"browser": b1ID})
	}()

	b2, err := harness.mgr.Resolve(b2ID)
	if err != nil {
		t.Fatalf("Resolve(b2): %v", err)
	}
	b2.Downloads.HandleDownloadWillBegin(&cdpbrowser.EventDownloadWillBegin{
		GUID:              "dl-notab",
		URL:               "https://example.com/notab.bin",
		SuggestedFilename: "notab.bin",
	})
	b2.Downloads.HandleDownloadProgress(&cdpbrowser.EventDownloadProgress{
		GUID:  "dl-notab",
		State: cdpbrowser.DownloadProgressStateCompleted,
	})

	// Clearing only ["downloads"] must succeed even with 0 open tabs.
	out := callTool[ClearBuffersOutput](t, "clear_buffers", map[string]any{
		"browser": b2ID,
		"targets": []string{"downloads"},
	})
	if !slices.Equal(out.Cleared, []string{"downloads"}) {
		t.Fatalf("Cleared = %v, want [downloads]", out.Cleared)
	}
	if len(b2.Downloads.Peek(0)) != 0 {
		t.Error("b2 downloads buffer was not cleared")
	}

	// Clearing tab-scoped buffers (or default all) on b2 with 0 tabs must error.
	if errText := callToolExpectErr(t, "clear_buffers", map[string]any{"browser": b2ID}); errText == "" {
		t.Error("expected error when clearing default all buffers on browser with 0 tabs")
	}

	// Passing a tab ID from b1 with browser=b2 must reject the mismatch even for downloads.
	if errText := callToolExpectErr(t, "clear_buffers", map[string]any{
		"browser": b2ID,
		"tab":     tabInB1,
		"targets": []string{"downloads"},
	}); !strings.Contains(errText, "not found") {
		t.Errorf("mismatched browser/tab err = %q, want not found", errText)
	}

	// Passing tab from non-active browser b1 WITHOUT browser must resolve b1
	// (the tab's owning browser) for downloads and leave active b2's downloads intact.
	b1, err := harness.mgr.Resolve(b1ID)
	if err != nil {
		t.Fatalf("Resolve(b1): %v", err)
	}
	b1.Downloads.HandleDownloadWillBegin(&cdpbrowser.EventDownloadWillBegin{
		GUID: "dl-b1-owner", URL: "https://example.com/b1.bin", SuggestedFilename: "b1.bin",
	})
	b1.Downloads.HandleDownloadProgress(&cdpbrowser.EventDownloadProgress{
		GUID: "dl-b1-owner", State: cdpbrowser.DownloadProgressStateCompleted,
	})
	b2.Downloads.HandleDownloadWillBegin(&cdpbrowser.EventDownloadWillBegin{
		GUID: "dl-b2-active", URL: "https://example.com/b2.bin", SuggestedFilename: "b2.bin",
	})
	b2.Downloads.HandleDownloadProgress(&cdpbrowser.EventDownloadProgress{
		GUID: "dl-b2-active", State: cdpbrowser.DownloadProgressStateCompleted,
	})
	callTool[ClearBuffersOutput](t, "clear_buffers", map[string]any{
		"tab":     tabInB1,
		"targets": []string{"downloads"},
	})
	if len(b1.Downloads.Peek(0)) != 0 {
		t.Errorf("tab-only clear_buffers(downloads) did not clear owning browser b1")
	}
	if len(b2.Downloads.Peek(0)) != 1 {
		t.Errorf("tab-only clear_buffers(downloads) unexpectedly cleared active browser b2")
	}

	// Validation is atomic: explicit empty [] and mixed valid+invalid targets must
	// reject cleanly and NOT clear valid buffers.
	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabInB1,
		"expression": `console.log('must-survive'); true`,
	})
	tb1, err := harness.mgr.ResolveTab(b1ID, tabInB1)
	if err != nil {
		t.Fatalf("ResolveTab: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(tb1.Console.Peek("", 0)) > 0 {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if len(tb1.Console.Peek("", 0)) == 0 {
		t.Fatal("precondition: console message did not arrive")
	}
	if errText := callToolExpectErr(t, "clear_buffers", map[string]any{
		"tab":     tabInB1,
		"targets": []string{},
	}); !strings.Contains(errText, "must not be empty") {
		t.Errorf("explicit empty targets [] err = %q, want 'must not be empty'", errText)
	}
	if len(tb1.Console.Peek("", 0)) == 0 {
		t.Error("explicit empty targets [] cleared console buffer")
	}
	if errText := callToolExpectErr(t, "clear_buffers", map[string]any{
		"tab":     tabInB1,
		"targets": []string{"console", "bogus"},
	}); errText == "" {
		t.Error("expected error for invalid buffer target")
	}
	if len(tb1.Console.Peek("", 0)) == 0 {
		t.Error("invalid clear_buffers call partially cleared console buffer before failing")
	}
}
