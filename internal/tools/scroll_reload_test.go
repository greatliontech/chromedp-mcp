package tools

import (
	"strings"
	"testing"
)

// ===========================================================================
// Reload: output contains URL and title
// ===========================================================================

func TestReloadReturnsOutput(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	out := callTool[ReloadOutput](t, "reload", map[string]any{"tab": tabID})
	if out.URL == "" {
		t.Error("reload output URL should not be empty")
	}
	if out.Title != "Interaction Test" {
		t.Errorf("reload output title = %q, want %q", out.Title, "Interaction Test")
	}
}

// ===========================================================================
// Reload: bypass_cache waits for page load (no sleep needed)
// ===========================================================================

func TestReloadBypassCacheWaitsForLoad(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// Mutate the DOM to verify reload actually happened.
	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.title = 'pre-reload'",
	})

	// Reload with bypass_cache. The tool should wait for the page to
	// fully load before returning — no sleep should be needed.
	out := callTool[ReloadOutput](t, "reload", map[string]any{
		"tab":          tabID,
		"bypass_cache": true,
	})

	// The title should be restored from the server, not 'pre-reload'.
	if out.Title == "pre-reload" {
		t.Error("bypass_cache reload did not wait for page load: title is still 'pre-reload'")
	}
	if out.Title != "Interaction Test" {
		t.Errorf("after bypass_cache reload, title = %q, want %q", out.Title, "Interaction Test")
	}
	if !strings.Contains(out.URL, "interaction.html") {
		t.Errorf("reload URL = %q, expected it to contain 'interaction.html'", out.URL)
	}
}

// ===========================================================================
// Reload: normal reload waits for page load
// ===========================================================================

func TestReloadNormalWaitsForLoad(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// Mutate the DOM.
	callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.title = 'before-reload'",
	})

	// Normal reload should also wait and return correct output.
	out := callTool[ReloadOutput](t, "reload", map[string]any{
		"tab": tabID,
	})

	if out.Title == "before-reload" {
		t.Error("normal reload returned before page finished loading")
	}
	if out.Title != "Interaction Test" {
		t.Errorf("after reload, title = %q, want %q", out.Title, "Interaction Test")
	}
}

// ===========================================================================
// Reload: DOM is usable immediately after reload (no stale nodes)
// ===========================================================================

func TestReloadDOMUsableImmediately(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// Reload.
	callTool[ReloadOutput](t, "reload", map[string]any{"tab": tabID})

	// Immediately query the DOM — should work without errors because
	// reload waited for the page to load.
	out := callTool[EvaluateOutput](t, "evaluate", map[string]any{
		"tab":        tabID,
		"expression": "document.getElementById('click-target').textContent",
	})
	if string(out.Result) == "" {
		t.Error("DOM query after reload returned empty result")
	}
}

// ===========================================================================
// Scroll: offset scroll returns position
// ===========================================================================

func TestScrollOffsetReturnsPosition(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	out := callTool[ScrollOutput](t, "scroll", map[string]any{
		"tab": tabID,
		"y":   500,
	})
	if out.ScrollY < 1 {
		t.Errorf("after scroll y=500, ScrollY = %f, want > 0", out.ScrollY)
	}
	if out.ScrollX != 0 {
		t.Errorf("after scroll y=500, ScrollX = %f, want 0", out.ScrollX)
	}
}

// ===========================================================================
// Scroll: negative offset returns decreased position
// ===========================================================================

func TestScrollNegativeReturnsPosition(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// Scroll down first.
	out1 := callTool[ScrollOutput](t, "scroll", map[string]any{
		"tab": tabID,
		"y":   500,
	})

	// Scroll up.
	out2 := callTool[ScrollOutput](t, "scroll", map[string]any{
		"tab": tabID,
		"y":   -200,
	})

	if out2.ScrollY >= out1.ScrollY {
		t.Errorf("after negative scroll, ScrollY = %f, expected < %f", out2.ScrollY, out1.ScrollY)
	}
	if out2.ScrollY < 0 {
		t.Errorf("ScrollY should not be negative, got %f", out2.ScrollY)
	}
}

// ===========================================================================
// Scroll: clamping at top boundary
// ===========================================================================

func TestScrollClampedAtTop(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// Scroll down a bit.
	callTool[ScrollOutput](t, "scroll", map[string]any{
		"tab": tabID,
		"y":   100,
	})

	// Scroll up by more than current position — should clamp at 0.
	out := callTool[ScrollOutput](t, "scroll", map[string]any{
		"tab": tabID,
		"y":   -9999,
	})
	if out.ScrollY != 0 {
		t.Errorf("after scrolling past top, ScrollY = %f, want 0", out.ScrollY)
	}
}

// ===========================================================================
// Scroll: horizontal scroll returns position
// ===========================================================================

func TestScrollHorizontalReturnsPosition(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	out := callTool[ScrollOutput](t, "scroll", map[string]any{
		"tab": tabID,
		"x":   300,
	})
	if out.ScrollX < 1 {
		t.Errorf("after scroll x=300, ScrollX = %f, want > 0", out.ScrollX)
	}
}

// ===========================================================================
// Scroll: scroll into view returns position
// ===========================================================================

func TestScrollIntoViewReturnsPosition(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	// The #scroll-marker is at margin-top: 2000px, so scrolling into view
	// should result in a non-zero scrollY.
	out := callTool[ScrollOutput](t, "scroll", map[string]any{
		"tab":      tabID,
		"selector": "#scroll-marker",
	})
	if out.ScrollY < 1 {
		t.Errorf("after scroll into view of #scroll-marker, ScrollY = %f, want > 0", out.ScrollY)
	}
}

// ===========================================================================
// Scroll: both axes returns correct positions
// ===========================================================================

func TestScrollBothAxesReturnsPositions(t *testing.T) {
	tabID := navigateToFixture(t, "interaction.html")
	defer closeTab(t, tabID)

	out := callTool[ScrollOutput](t, "scroll", map[string]any{
		"tab": tabID,
		"x":   200,
		"y":   400,
	})
	if out.ScrollX < 1 {
		t.Errorf("after scroll x=200,y=400, ScrollX = %f, want > 0", out.ScrollX)
	}
	if out.ScrollY < 1 {
		t.Errorf("after scroll x=200,y=400, ScrollY = %f, want > 0", out.ScrollY)
	}
}
