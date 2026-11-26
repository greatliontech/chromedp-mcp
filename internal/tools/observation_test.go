package tools

import (
	"context"
	"testing"
	"time"
)

func TestObserveWindowDegradesCleanlyWhenDialogOpens(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab": tabID,
		"expression": `(function() {
			// Embed multiple same-origin iframes so O(N) per-frame timeouts
			// would exceed the bounded assertion below.
			for (var i = 0; i < 6; i++) {
				var f = document.createElement('iframe');
				f.src = '/page2.html';
				document.body.appendChild(f);
			}
			var btn = document.createElement('button');
			btn.id = 'obs-alert';
			btn.textContent = 'Alert';
			btn.addEventListener('click', function() {
				// Delay 50ms so finalizeClickState finishes while the renderer
				// is still responsive, then the alert opens well before the
				// 200ms observe window closes so countMutations hits the
				// Dialog.Open() fast-path instead of waiting 1s on evalInContextTimeout.
				setTimeout(function() { alert('blocking'); }, 50);
			});
			document.body.appendChild(btn);
		})()`,
	})
	// Wait until all 6 iframes have loaded and registered execution contexts.
	callTool[struct{}](t, "wait_for", map[string]any{
		"tab":        tabID,
		"expression": `Array.from(document.querySelectorAll('iframe')).every(function(f){ return f.contentDocument && f.contentDocument.readyState === 'complete'; })`,
		"timeout":    5000,
	})

	start := time.Now()
	out := callTool[ClickOutput](t, "click", map[string]any{
		"tab":               tabID,
		"selector":          "#obs-alert",
		"observe_window_ms": 200,
	})
	elapsed := time.Since(start)
	defer handleDialog(t, tabID, map[string]any{"action": "accept"})

	// countMutations short-circuits via Dialog.Open() instead of stalling for
	// evalInContextTimeout (1s), so total elapsed time stays close to 200ms.
	if elapsed > 900*time.Millisecond {
		t.Fatalf("click with observe_window_ms across 6 iframes took %v when dialog opened, want fast short-circuit", elapsed)
	}
	if out.Observed == nil {
		t.Fatal("expected Observed to be populated")
	}
	if !out.Observed.DOMMutationsUnavailable {
		t.Errorf("expected DOMMutationsUnavailable=true when renderer is blocked by alert, got %+v", out.Observed)
	}
}

func TestOpenObservationFastPathWhenDialogAlreadyOpen(t *testing.T) {
	tabID := navigateToFixture(t, "page2.html")
	defer closeTab(t, tabID)

	// Open an alert BEFORE openObservation runs.
	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": `setTimeout(function() { alert('pre-existing'); }, 10); true`,
	})
	defer handleDialog(t, tabID, map[string]any{"action": "accept"})

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if callTool[DialogOutput](t, "dialog", map[string]any{"tab": tabID, "action": "get"}).Open {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if !callTool[DialogOutput](t, "dialog", map[string]any{"tab": tabID, "action": "get"}).Open {
		t.Fatal("timed out waiting for pre-existing alert to open")
	}

	tb, err := harness.mgr.ResolveTab("", tabID)
	if err != nil {
		t.Fatalf("ResolveTab: %v", err)
	}
	tctx, cancel := tabContext(context.Background(), tb.Context())
	defer cancel()

	// openObservation must short-circuit immediately without waiting 1s on
	// Runtime.evaluate(performance.now()).
	start := time.Now()
	obs := openObservation(tctx, tb, 50)
	openElapsed := time.Since(start)
	if openElapsed > 400*time.Millisecond {
		t.Fatalf("openObservation took %v with open dialog, want immediate fast-path", openElapsed)
	}
	if obs == nil || !obs.anchorFailed || len(obs.anchors) != 0 {
		t.Fatalf("expected fast-path observation with anchorFailed=true and no anchors, got %+v", obs)
	}
	res := obs.close(tctx)
	if res == nil || !res.DOMMutationsUnavailable {
		t.Errorf("expected DOMMutationsUnavailable=true, got %+v", res)
	}
}
