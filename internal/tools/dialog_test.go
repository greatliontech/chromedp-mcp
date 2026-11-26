package tools

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/greatliontech/chromedp-mcp/internal/collector"
)

type asyncEvalResult struct {
	result string
	err    error
}

// evalAsync runs evaluate in a background goroutine without calling testing.T
// off the test goroutine, so blocking JS dialogs can be handled concurrently.
func evalAsync(h *testHarness, tabID, expr string) <-chan asyncEvalResult {
	ch := make(chan asyncEvalResult, 1)
	go func() {
		out, err := callToolAsync[evaluateOutput](h, "evaluate", map[string]any{
			"tab":        tabID,
			"expression": expr,
		})
		ch <- asyncEvalResult{result: string(out.Result), err: err}
	}()
	return ch
}

func TestHandleDialogPromptTextAndEmptyString(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	// Case 1: explicit prompt text is returned to window.prompt.
	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `setTimeout(function() { window.__promptVal = prompt('Name?', 'default'); }, 20); true`,
	})
	handleDialog(t, tabID, map[string]any{"action": "accept", "text": "alice"})

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `window.__promptVal`,
	})
	if string(out.Result) != `"alice"` {
		t.Errorf("prompt result = %s, want %q", out.Result, "alice")
	}

	// Case 2: explicit empty string submits "".
	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `setTimeout(function() { window.__promptEmpty = prompt('Name?', 'default'); }, 20); true`,
	})
	handleDialog(t, tabID, map[string]any{"action": "accept", "text": ""})

	out = callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `window.__promptEmpty`,
	})
	if string(out.Result) != `""` {
		t.Errorf("empty text prompt result = %s, want %q", out.Result, "")
	}
}

func TestGetDialogAndHandleDialogPromptDefaultVsEmpty(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	waitDialogOpen := func(wantType string) (dialogOutput, string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			rawRes := callToolRaw(t, "dialog", map[string]any{"tab": tabID, "action": "get"})
			if rawRes.IsError {
				t.Fatalf("dialog(get) returned error: %+v", rawRes.Content)
			}
			var rawJSON string
			for _, c := range rawRes.Content {
				if tc, ok := c.(*mcp.TextContent); ok {
					rawJSON = tc.Text
					break
				}
			}
			var d dialogOutput
			if err := json.Unmarshal([]byte(rawJSON), &d); err != nil {
				t.Fatalf("unmarshal dialog(get): %v", err)
			}
			if d.Open {
				if d.Dialog == nil {
					t.Fatalf("dialog(get) Open=true with nil Dialog: %s", rawJSON)
				}
				if d.Dialog.Type != wantType {
					t.Fatalf("dialog(get) type = %q, want %q", d.Dialog.Type, wantType)
				}
				return d, rawJSON
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %q dialog to appear in dialog(get)", wantType)
		return dialogOutput{}, ""
	}

	// Initially no dialog is open.
	initState := callTool[dialogOutput](t, "dialog", map[string]any{"tab": tabID, "action": "get"})
	if initState.Open || initState.Dialog != nil {
		t.Fatalf("expected Open=false Dialog=nil initially, got %+v", initState)
	}

	// 1. Alert dialog with empty message verifies both message:"" and
	// has_browser_handler:false are preserved in serialized JSON (not dropped
	// by omitempty), and immediate post-accept dialog(get) sees open=false
	// without polling.
	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `setTimeout(function() { alert(''); }, 20); true`,
	})
	d, rawJSON := waitDialogOpen("alert")
	if d.Dialog.Message != "" || !strings.HasSuffix(d.Dialog.URL, "/page2.html") || d.Dialog.OpenedAt.IsZero() {
		t.Errorf("unexpected alert dialog state: %+v", d.Dialog)
	}
	if !strings.Contains(rawJSON, `"message":""`) {
		t.Errorf("dialog(get) JSON omitted empty message: %s", rawJSON)
	}
	if !strings.Contains(rawJSON, `"has_browser_handler":`) {
		t.Errorf("dialog(get) JSON omitted has_browser_handler: %s", rawJSON)
	}
	// Verify false has_browser_handler and empty message are never dropped by omitempty.
	falseOut, err := json.Marshal(dialogOutput{
		Open: true,
		Dialog: &collector.DialogState{
			Type:              "alert",
			Message:           "",
			HasBrowserHandler: false,
		},
	})
	if err != nil {
		t.Fatalf("marshal DialogOutput: %v", err)
	}
	if !strings.Contains(string(falseOut), `"has_browser_handler":false`) || !strings.Contains(string(falseOut), `"message":""`) {
		t.Errorf("DialogOutput JSON omitted false/empty fields: %s", falseOut)
	}
	accOut := callTool[dialogOutput](t, "dialog", map[string]any{"tab": tabID, "action": "accept"})
	if accOut.Open {
		t.Fatalf("dialog(accept) immediately reported open: %+v", accOut)
	}
	// ClearIfSeq guarantees immediate follow-up dialog(get) reports closed.
	if after := callTool[dialogOutput](t, "dialog", map[string]any{"tab": tabID, "action": "get"}); after.Open {
		t.Fatalf("dialog(get) immediately after accept still reported open: %+v", after)
	}

	// 2. Confirm dialog inspection + dismiss.
	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `setTimeout(function() { window.__confirm = confirm('proceed?'); }, 20); true`,
	})
	d, _ = waitDialogOpen("confirm")
	if d.Dialog.Message != "proceed?" {
		t.Errorf("confirm message = %q, want %q", d.Dialog.Message, "proceed?")
	}
	callTool[dialogOutput](t, "dialog", map[string]any{"tab": tabID, "action": "dismiss"})
	if after := callTool[dialogOutput](t, "dialog", map[string]any{"tab": tabID, "action": "get"}); after.Open {
		t.Fatalf("dialog(get) immediately after confirm dismiss still reported open: %+v", after)
	}
	if res := callTool[evaluateOutput](t, "evaluate", map[string]any{"tab": tabID, "expression": `window.__confirm`}); string(res.Result) != "false" {
		t.Errorf("dismissed confirm result = %s, want false", res.Result)
	}

	// 3. Prompt dialog with omitted text preserves DefaultPrompt.
	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `setTimeout(function() { window.__promptDef = prompt('Your name?', 'keep-default'); }, 20); true`,
	})
	d, _ = waitDialogOpen("prompt")
	if d.Dialog.Message != "Your name?" || d.Dialog.DefaultPrompt != "keep-default" {
		t.Errorf("prompt dialog state = %+v, want message='Your name?' default_prompt='keep-default'", d.Dialog)
	}
	callTool[dialogOutput](t, "dialog", map[string]any{"tab": tabID, "action": "accept"})
	if after := callTool[dialogOutput](t, "dialog", map[string]any{"tab": tabID, "action": "get"}); after.Open {
		t.Fatalf("dialog(get) immediately after prompt accept still reported open: %+v", after)
	}
	if res := callTool[evaluateOutput](t, "evaluate", map[string]any{"tab": tabID, "expression": `window.__promptDef`}); string(res.Result) != `"keep-default"` {
		t.Errorf("omitted text prompt result = %s, want %q", res.Result, "keep-default")
	}

	// 4. Prompt dialog with explicit empty string submits "".
	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `setTimeout(function() { window.__promptBlank = prompt('Your name?', 'keep-default'); }, 20); true`,
	})
	waitDialogOpen("prompt")
	callTool[dialogOutput](t, "dialog", map[string]any{"tab": tabID, "action": "accept", "text": ""})
	if after := callTool[dialogOutput](t, "dialog", map[string]any{"tab": tabID, "action": "get"}); after.Open {
		t.Fatalf("dialog(get) immediately after empty prompt accept still reported open: %+v", after)
	}
	if res := callTool[evaluateOutput](t, "evaluate", map[string]any{"tab": tabID, "expression": `window.__promptBlank`}); string(res.Result) != `""` {
		t.Errorf("explicit empty text prompt result = %s, want %q", res.Result, "")
	}
}

func TestHandleDialog(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	// Trigger an alert via evaluate in a background goroutine (blocks until
	// the dialog is dismissed), then handle the dialog concurrently.
	done := evalAsync(ensureHarness(t), tabID, "alert('test dialog')")

	handleDialog(t, tabID, map[string]any{"action": "accept"})

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("evaluate(alert) failed: %v", res.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for dialog handling to complete")
	}
}

func TestHandleDialogDismiss(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	done := evalAsync(ensureHarness(t), tabID, "confirm('Are you sure?')")

	handleDialog(t, tabID, map[string]any{"action": "dismiss"})

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("evaluate(confirm) failed: %v", res.err)
		}
		if res.result != "false" {
			t.Errorf("confirm() after dismiss = %s, want false", res.result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for confirm dialog")
	}
}

func TestHandleDialogConfirmAccept(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	done := evalAsync(ensureHarness(t), tabID, "confirm('Are you sure?')")

	handleDialog(t, tabID, map[string]any{"action": "accept"})

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("evaluate(confirm) failed: %v", res.err)
		}
		if res.result != "true" {
			t.Errorf("confirm() after accept = %s, want true", res.result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for confirm dialog")
	}
}

func TestHandleDialogPrompt(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	done := evalAsync(ensureHarness(t), tabID, "prompt('Enter name:')")

	handleDialog(t, tabID, map[string]any{"action": "accept", "text": "Alice"})

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("evaluate(prompt) failed: %v", res.err)
		}
		if !strings.Contains(res.result, "Alice") {
			t.Errorf("prompt() result = %s, want 'Alice'", res.result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for prompt dialog")
	}
}

func TestHandleDialogTextOnAlert(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	done := evalAsync(ensureHarness(t), tabID, "alert('test alert')")

	// Pass text to an alert dialog — should be ignored, no error.
	handleDialog(t, tabID, map[string]any{"action": "accept", "text": "ignored text"})

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("evaluate(alert) failed: %v", res.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for alert handling")
	}
}

func TestHandleDialogDismissConfirm(t *testing.T) {
	tabID := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tabID)

	done := evalAsync(ensureHarness(t), tabID, "document.getElementById('confirm-result').textContent = String(confirm('Sure?'))")

	handleDialog(t, tabID, map[string]any{"action": "dismiss"})

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("evaluate(confirm) failed: %v", res.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for confirm dialog")
	}

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('confirm-result').textContent",
	})
	if !strings.Contains(string(out.Result), "false") {
		t.Errorf("dismissed confirm result = %s, want 'false'", out.Result)
	}
}

func TestHandleDialogDismissPrompt(t *testing.T) {
	tabID := navigateToFixture(t, "interaction2.html")
	defer closeTab(t, tabID)

	done := evalAsync(ensureHarness(t), tabID, "document.getElementById('prompt-result').textContent = String(prompt('Name:'))")

	handleDialog(t, tabID, map[string]any{"action": "dismiss"})

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("evaluate(prompt) failed: %v", res.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for prompt dialog")
	}

	out := callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('prompt-result').textContent",
	})
	if !strings.Contains(string(out.Result), "null") {
		t.Errorf("dismissed prompt result = %s, want 'null'", out.Result)
	}
}

func TestDialogObserveWindowAndChainedDialog(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	// dialog(action="accept", observe_window_ms=300) observes post-dialog fetch.
	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `setTimeout(() => { if (confirm('fetch?')) fetch('/api/data'); }, 15); true`,
	})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if callTool[dialogOutput](t, "dialog", map[string]any{"tab": tabID, "action": "get"}).Open {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	dlgOut := callTool[dialogOutput](t, "dialog", map[string]any{
		"tab":               tabID,
		"action":            "accept",
		"observe_window_ms": 300,
	})
	if dlgOut.Open {
		t.Errorf("dialog(accept) still Open=true")
	}
	if dlgOut.Observed == nil || !dlgOut.Observed.HasAnyEffect || dlgOut.Observed.NetworkRequests < 1 {
		t.Errorf("dialog(accept, observe_window_ms) Observed = %+v, want HasAnyEffect=true NetworkRequests>=1", dlgOut.Observed)
	}

	// Chained dialog opening during observe_window_ms must be reflected in Open/Dialog.
	callTool[evaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `setTimeout(() => { if (confirm('first')) setTimeout(() => alert('second'), 30); }, 15); true`,
	})
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if callTool[dialogOutput](t, "dialog", map[string]any{"tab": tabID, "action": "get"}).Open {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	chained := callTool[dialogOutput](t, "dialog", map[string]any{
		"tab":               tabID,
		"action":            "accept",
		"observe_window_ms": 200,
	})
	if !chained.Open || chained.Dialog == nil || chained.Dialog.Message != "second" {
		t.Errorf("chained dialog during observe_window_ms = %+v, want Open=true Message='second'", chained)
	}
	handleDialog(t, tabID, map[string]any{"action": "accept"})
}
