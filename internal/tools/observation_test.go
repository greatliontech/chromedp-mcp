package tools

import (
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
				setTimeout(function() { alert('blocking'); }, 10);
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
		"observe_window_ms": 150,
	})
	elapsed := time.Since(start)
	defer handleDialog(t, tabID, map[string]any{"accept": true})

	// Shared 1s read budget + 500ms click re-probe + 150ms observe window ≈ 1.65s;
	// an O(N) 1s-per-iframe loop across 7 frames would exceed 6s.
	if elapsed > 4*time.Second {
		t.Fatalf("click with observe_window_ms across 6 iframes hung for %v when dialog opened", elapsed)
	}
	if out.Observed == nil {
		t.Fatal("expected Observed to be populated")
	}
	if !out.Observed.DOMMutationsUnavailable {
		t.Errorf("expected DOMMutationsUnavailable=true when renderer is blocked by alert, got %+v", out.Observed)
	}
}
