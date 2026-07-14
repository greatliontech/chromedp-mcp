package collector

import (
	"testing"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
)

func created(uniqueID, frameID string, isDefault bool) *runtime.EventExecutionContextCreated {
	aux := `{"isDefault":false,"type":"isolated","frameId":"` + frameID + `"}`
	if isDefault {
		aux = `{"isDefault":true,"type":"default","frameId":"` + frameID + `"}`
	}
	return &runtime.EventExecutionContextCreated{
		Context: &runtime.ExecutionContextDescription{
			UniqueID: uniqueID,
			AuxData:  []byte(aux),
		},
	}
}

func navigated(frameID, parentID string) *page.EventFrameNavigated {
	return &page.EventFrameNavigated{
		Frame: &cdp.Frame{
			ID:       cdp.FrameID(frameID),
			ParentID: cdp.FrameID(parentID),
		},
	}
}

func contextsOf(e *ExecContexts) []ExecContext {
	cs, _ := e.Snapshot()
	return cs
}

func ids(cs []ExecContext) map[string]ExecContext {
	m := make(map[string]ExecContext, len(cs))
	for _, c := range cs {
		m[c.UniqueID] = c
	}
	return m
}

// TestExecContextsTracksMainWorldPerFrame verifies each frame's main-world
// context is tracked, and that the top frame is identified as main.
func TestExecContextsTracksMainWorldPerFrame(t *testing.T) {
	e := NewExecContexts()
	e.HandleFrameNavigated(navigated("top", ""))
	e.HandleExecutionContextCreated(created("u-top", "top", true))
	e.HandleExecutionContextCreated(created("u-frame", "child", true))

	got := ids(contextsOf(e))
	if len(got) != 2 {
		t.Fatalf("Snapshot() has %d contexts, want 2: %+v", len(got), got)
	}
	if !got["u-top"].IsMain {
		t.Error("top frame's context should be IsMain")
	}
	if got["u-frame"].IsMain {
		t.Error("child frame's context should not be IsMain")
	}
}

// TestExecContextsIgnoresIsolatedWorlds verifies non-default contexts are
// skipped. An isolated world does not share `window` with page scripts, so
// the injected mutation observer's buffer is invisible from it — counting
// one as a frame would produce a spurious zero.
func TestExecContextsIgnoresIsolatedWorlds(t *testing.T) {
	e := NewExecContexts()
	e.HandleExecutionContextCreated(created("u-main", "top", true))
	e.HandleExecutionContextCreated(created("u-isolated", "top", false))

	got := ids(contextsOf(e))
	if _, ok := got["u-isolated"]; ok {
		t.Errorf("isolated world should not be tracked; got %+v", got)
	}
	if _, ok := got["u-main"]; !ok {
		t.Errorf("main world should be tracked; got %+v", got)
	}
}

// TestExecContextsDestroyRemoves verifies a destroyed context is dropped.
// This is what makes a stale anchor unusable rather than silently wrong:
// after a navigation the old context is gone, so the count against it
// fails instead of being applied to the new document.
func TestExecContextsDestroyRemoves(t *testing.T) {
	e := NewExecContexts()
	e.HandleExecutionContextCreated(created("u-old", "top", true))
	e.HandleExecutionContextDestroyed(&runtime.EventExecutionContextDestroyed{
		ExecutionContextUniqueID: "u-old",
	})
	if len(contextsOf(e)) != 0 {
		t.Errorf("destroyed context should be dropped; got %+v", contextsOf(e))
	}
}

// TestExecContextsClearedRemovesAll verifies executionContextsCleared (a
// cross-process navigation) drops everything.
func TestExecContextsClearedRemovesAll(t *testing.T) {
	e := NewExecContexts()
	e.HandleExecutionContextCreated(created("u-a", "top", true))
	e.HandleExecutionContextCreated(created("u-b", "child", true))
	e.HandleExecutionContextsCleared()
	if len(contextsOf(e)) != 0 {
		t.Errorf("cleared should drop all contexts; got %+v", contextsOf(e))
	}
}

// TestExecContextsMainResolvedAtSnapshot verifies IsMain is resolved when
// Snapshot() is called, not when the context is created. Chrome can emit
// executionContextCreated for a document before the frameNavigated that
// identifies its frame as top-level; resolving eagerly would leave the
// main frame permanently unmarked, which makes every DOM count report
// "unavailable".
func TestExecContextsMainResolvedAtSnapshot(t *testing.T) {
	e := NewExecContexts()
	// Context first, navigation second — the awkward order.
	e.HandleExecutionContextCreated(created("u-top", "top", true))
	if contextsOf(e)[0].IsMain {
		t.Fatal("IsMain should be false before the top frame is known")
	}
	e.HandleFrameNavigated(navigated("top", ""))
	if !contextsOf(e)[0].IsMain {
		t.Error("IsMain should become true once the top frame is known")
	}
}

// TestExecContextsSubframeNavigationDoesNotClaimMain verifies a subframe
// navigation never overwrites the main frame ID — a frame is top-level
// exactly when it has no parent.
func TestExecContextsSubframeNavigationDoesNotClaimMain(t *testing.T) {
	e := NewExecContexts()
	e.HandleFrameNavigated(navigated("top", ""))
	e.HandleFrameNavigated(navigated("child", "top"))
	e.HandleExecutionContextCreated(created("u-top", "top", true))
	e.HandleExecutionContextCreated(created("u-child", "child", true))

	got := ids(contextsOf(e))
	if !got["u-top"].IsMain {
		t.Error("top frame should still be main after a subframe navigation")
	}
	if got["u-child"].IsMain {
		t.Error("subframe must not be marked main")
	}
}

// TestExecContextsMalformedAuxDataIgnored verifies a context whose auxData
// cannot be parsed is skipped rather than tracked with a zero frame ID
// (which would collide with any other unparseable context).
func TestExecContextsMalformedAuxDataIgnored(t *testing.T) {
	e := NewExecContexts()
	e.HandleExecutionContextCreated(&runtime.EventExecutionContextCreated{
		Context: &runtime.ExecutionContextDescription{
			UniqueID: "u-bad",
			AuxData:  []byte(`{not json`),
		},
	})
	if len(contextsOf(e)) != 0 {
		t.Errorf("context with malformed auxData should be ignored; got %+v", contextsOf(e))
	}
}

// TestExecContextsSwapKeepsFrame pins the swap/remove distinction, which
// is the difference between seeing an out-of-process iframe and pretending
// it doesn't exist.
//
// Chrome detaches a frame from our session with reason "swap" when it
// moves to another process — every cross-origin iframe does this. The
// frame still exists and still mutates; we just can't see into it.
// Dropping it on swap would erase the only evidence it exists, and the
// mutation count would then silently present itself as complete.
func TestExecContextsSwapKeepsFrame(t *testing.T) {
	e := NewExecContexts()
	e.HandleFrameNavigated(navigated("top", ""))
	e.HandleExecutionContextCreated(created("u-top", "top", true))
	e.HandleFrameAttached(&page.EventFrameAttached{FrameID: cdp.FrameID("oopif")})

	// The frame goes out-of-process: detached from our session with "swap",
	// and its execution context (if we ever had one) is destroyed.
	e.HandleFrameDetached(&page.EventFrameDetached{
		FrameID: cdp.FrameID("oopif"),
		Reason:  page.FrameDetachedReasonSwap,
	})

	_, unobserved := e.Snapshot()
	if unobserved != 1 {
		t.Errorf("unobservedFrames = %d, want 1 — a swapped-out frame still exists and is unobservable, so it must be reported, not forgotten", unobserved)
	}
}

// TestExecContextsRemoveDropsFrame verifies the other reason: a frame
// genuinely removed from the page is gone and must NOT be reported as an
// unobserved frame, which would flag every count as partial forever.
func TestExecContextsRemoveDropsFrame(t *testing.T) {
	e := NewExecContexts()
	e.HandleFrameNavigated(navigated("top", ""))
	e.HandleExecutionContextCreated(created("u-top", "top", true))
	e.HandleFrameAttached(&page.EventFrameAttached{FrameID: cdp.FrameID("gone")})
	e.HandleFrameDetached(&page.EventFrameDetached{
		FrameID: cdp.FrameID("gone"),
		Reason:  page.FrameDetachedReasonRemove,
	})

	_, unobserved := e.Snapshot()
	if unobserved != 0 {
		t.Errorf("unobservedFrames = %d, want 0 — a removed frame is gone, not unobservable", unobserved)
	}
}

// TestExecContextsObservedFrameIsNotUnobserved verifies a frame we DO hold
// a context for is not counted as unobserved — otherwise every page with a
// same-origin iframe would permanently report a partial count.
func TestExecContextsObservedFrameIsNotUnobserved(t *testing.T) {
	e := NewExecContexts()
	e.HandleFrameNavigated(navigated("top", ""))
	e.HandleFrameNavigated(navigated("child", "top"))
	e.HandleExecutionContextCreated(created("u-top", "top", true))
	e.HandleExecutionContextCreated(created("u-child", "child", true))

	contexts, unobserved := e.Snapshot()
	if len(contexts) != 2 {
		t.Fatalf("contexts = %d, want 2", len(contexts))
	}
	if unobserved != 0 {
		t.Errorf("unobservedFrames = %d, want 0 — both frames have contexts", unobserved)
	}
}

// TestExecContextsSeedFrameTree verifies the main frame can be established
// without waiting for a navigation. Without the seed, attaching to a tab
// that is already loaded leaves mainFrameID empty, and every DOM mutation
// count on that tab reports "unavailable" forever.
func TestExecContextsSeedFrameTree(t *testing.T) {
	e := NewExecContexts()
	e.SeedFrameTree("top", []string{"top", "child"})
	e.HandleExecutionContextCreated(created("u-top", "top", true))

	contexts, unobserved := e.Snapshot()
	if len(contexts) != 1 || !contexts[0].IsMain {
		t.Fatalf("seeded main frame should make its context IsMain; got %+v", contexts)
	}
	if unobserved != 1 {
		t.Errorf("unobservedFrames = %d, want 1 (the seeded child has no context yet)", unobserved)
	}
}

// TestExecContextsAbsentAuxDataIgnored verifies a context with NO auxData
// (worker contexts have none) is dropped rather than tracked with an empty
// frame ID, where it would collide with every other such context.
func TestExecContextsAbsentAuxDataIgnored(t *testing.T) {
	e := NewExecContexts()
	e.HandleExecutionContextCreated(&runtime.EventExecutionContextCreated{
		Context: &runtime.ExecutionContextDescription{UniqueID: "u-worker"},
	})
	if len(contextsOf(e)) != 0 {
		t.Errorf("context with no auxData should be ignored; got %+v", contextsOf(e))
	}
}

// TestExecContextsTopFrameNavigationPrunesStaleFrames pins the frame set's
// lifecycle.
//
// Chrome sends frameDetached only for frames removed from the CURRENT
// document's tree. When the top frame navigates, the old document's
// children simply cease to exist — no detach event of any reason arrives
// for them. Without pruning on top-frame navigation they accumulate
// forever, are never observable again, and so make every subsequent
// mutation count report itself as partial.
func TestExecContextsTopFrameNavigationPrunesStaleFrames(t *testing.T) {
	e := NewExecContexts()
	e.HandleFrameNavigated(navigated("top", ""))
	e.HandleExecutionContextCreated(created("u-top", "top", true))
	// The first page has an iframe.
	e.HandleFrameAttached(&page.EventFrameAttached{FrameID: cdp.FrameID("child")})
	e.HandleFrameNavigated(navigated("child", "top"))
	e.HandleExecutionContextCreated(created("u-child", "child", true))

	if _, unobserved := e.Snapshot(); unobserved != 0 {
		t.Fatalf("unobservedFrames = %d, want 0 before navigating away", unobserved)
	}

	// Navigate the top frame. Chrome clears the contexts and re-navigates
	// the main frame; NO frameDetached arrives for "child".
	e.HandleExecutionContextsCleared()
	e.HandleFrameNavigated(navigated("top", ""))
	e.HandleExecutionContextCreated(created("u-top2", "top", true))

	contexts, unobserved := e.Snapshot()
	if unobserved != 0 {
		t.Errorf("unobservedFrames = %d, want 0 — the previous document's iframe no longer exists and must not linger as an 'unobservable frame' forever", unobserved)
	}
	if len(contexts) != 1 || !contexts[0].IsMain {
		t.Errorf("want just the new main-frame context; got %+v", contexts)
	}
}

func navigatedTyped(frameID, parentID, loaderID string, typ page.NavigationType) *page.EventFrameNavigated {
	return &page.EventFrameNavigated{
		Frame: &cdp.Frame{
			ID:       cdp.FrameID(frameID),
			ParentID: cdp.FrameID(parentID),
			LoaderID: cdp.LoaderID(loaderID),
		},
		Type: typ,
	}
}

// TestExecContextsBFCacheRestoreKeepsUnobservableFrames pins the
// back/forward path.
//
// A bfcache restore re-commits a document WITHOUT re-attaching its frames:
// Chrome sends frameNavigated(type=BackForwardCacheRestore) and re-creates
// execution contexts, but no frameAttached for any restored child. Since a
// frameAttached (before the process swap) is the ONLY evidence we ever get
// that an out-of-process iframe exists, rebuilding the frame set from the
// restore's events alone can never learn about it — and the DOM count would
// present itself as complete for a page with a frame it cannot see.
//
// The departing document's frame set is therefore stashed by loader ID
// (which the bfcache preserves) and restored.
func TestExecContextsBFCacheRestoreKeepsUnobservableFrames(t *testing.T) {
	e := NewExecContexts()

	// Page A: top frame + a cross-origin iframe that swaps out of process.
	e.HandleFrameNavigated(navigatedTyped("top", "", "loaderA", page.NavigationTypeNavigation))
	e.HandleExecutionContextCreated(created("u-topA", "top", true))
	e.HandleFrameAttached(&page.EventFrameAttached{FrameID: cdp.FrameID("oopif")})
	e.HandleFrameDetached(&page.EventFrameDetached{
		FrameID: cdp.FrameID("oopif"),
		Reason:  page.FrameDetachedReasonSwap,
	})
	if _, unobserved := e.Snapshot(); unobserved != 1 {
		t.Fatalf("unobservedFrames = %d on page A, want 1 (the OOPIF)", unobserved)
	}

	// Navigate away to a frameless page B.
	e.HandleExecutionContextsCleared()
	e.HandleFrameNavigated(navigatedTyped("top", "", "loaderB", page.NavigationTypeNavigation))
	e.HandleExecutionContextCreated(created("u-topB", "top", true))
	if _, unobserved := e.Snapshot(); unobserved != 0 {
		t.Fatalf("unobservedFrames = %d on frameless page B, want 0", unobserved)
	}

	// Go back. The bfcache restores page A under its ORIGINAL loader ID.
	// Contexts are re-created; NO frameAttached arrives for the OOPIF.
	e.HandleExecutionContextsCleared()
	e.HandleExecutionContextCreated(created("u-topA2", "top", true))
	e.HandleFrameNavigated(navigatedTyped("top", "", "loaderA", page.NavigationTypeBackForwardCacheRestore))

	if _, unobserved := e.Snapshot(); unobserved != 1 {
		t.Errorf("unobservedFrames = %d after bfcache restore, want 1 — the restored page's out-of-process iframe is alive and unobservable, but no frameAttached announces it, so forgetting it means reporting a confident complete DOM count for a page we cannot fully see", unobserved)
	}
}

// TestExecContextsBFCacheRestoreUnknownLoaderIsUnknown verifies the
// fallback: restoring a document we never stashed means we cannot
// enumerate its frames, so we must not claim a complete count.
func TestExecContextsBFCacheRestoreUnknownLoaderIsUnknown(t *testing.T) {
	e := NewExecContexts()
	e.HandleFrameNavigated(navigatedTyped("top", "", "loaderX", page.NavigationTypeBackForwardCacheRestore))
	e.HandleExecutionContextCreated(created("u-top", "top", true))

	if _, unobserved := e.Snapshot(); unobserved == 0 {
		t.Error("unobservedFrames = 0 after restoring a document whose frame set we never saw; we cannot rule out an unobservable frame, so the count must not present itself as complete")
	}

	// A real navigation re-establishes a known frame set.
	e.HandleFrameNavigated(navigatedTyped("top", "", "loaderY", page.NavigationTypeNavigation))
	e.HandleExecutionContextCreated(created("u-top2", "top", true))
	if _, unobserved := e.Snapshot(); unobserved != 0 {
		t.Errorf("unobservedFrames = %d after a real navigation; the unknown-frame-set state must clear", unobserved)
	}
}

// TestExecContextsBFCacheForwardRestoreKeepsFrames pins the FORWARD leg of
// the back/forward cache.
//
// The bfcache is not only the back button. Leaving page B by going *back*
// to A puts B into the cache, and the subsequent go_forward restores B from
// it — again with no frameAttached for B's children. A stash that only runs
// on the plain-navigation path never saves B on the way out, so the forward
// restore finds nothing:
//
//   - if B has an out-of-process iframe, it is forgotten (a confident,
//     complete-looking count for a page we cannot fully see), and
//   - if B has no frames at all, the miss forces "frame set unknown", which
//     fires dom_mutations_partial on a page with zero iframes — the
//     fires-on-ordinary-pages failure the flag must never have.
func TestExecContextsBFCacheForwardRestoreKeepsFrames(t *testing.T) {
	e := NewExecContexts()

	// Page A: top frame + a cross-origin iframe that swaps out of process.
	e.HandleFrameNavigated(navigatedTyped("top", "", "loaderA", page.NavigationTypeNavigation))
	e.HandleExecutionContextCreated(created("u-topA", "top", true))
	e.HandleFrameAttached(&page.EventFrameAttached{FrameID: cdp.FrameID("oopif")})
	e.HandleFrameDetached(&page.EventFrameDetached{
		FrameID: cdp.FrameID("oopif"),
		Reason:  page.FrameDetachedReasonSwap,
	})

	// Navigate to page B, which has NO frames at all.
	e.HandleExecutionContextsCleared()
	e.HandleFrameNavigated(navigatedTyped("top", "", "loaderB", page.NavigationTypeNavigation))
	e.HandleExecutionContextCreated(created("u-topB", "top", true))
	if _, unobserved := e.Snapshot(); unobserved != 0 {
		t.Fatalf("unobservedFrames = %d on frameless page B, want 0", unobserved)
	}

	// go_back: A restored from the bfcache. Its OOPIF must come back.
	e.HandleExecutionContextsCleared()
	e.HandleExecutionContextCreated(created("u-topA2", "top", true))
	e.HandleFrameNavigated(navigatedTyped("top", "", "loaderA", page.NavigationTypeBackForwardCacheRestore))
	if _, unobserved := e.Snapshot(); unobserved != 1 {
		t.Fatalf("unobservedFrames = %d after going back to A, want 1 (its OOPIF)", unobserved)
	}

	// go_forward: B restored from the bfcache. B has no frames, so the count
	// is complete and must NOT claim to be partial.
	e.HandleExecutionContextsCleared()
	e.HandleExecutionContextCreated(created("u-topB2", "top", true))
	e.HandleFrameNavigated(navigatedTyped("top", "", "loaderB", page.NavigationTypeBackForwardCacheRestore))

	if _, unobserved := e.Snapshot(); unobserved != 0 {
		t.Errorf("unobservedFrames = %d after going FORWARD to frameless page B, want 0 — leaving B via the back button put it in the bfcache, so its frame set must have been stashed on the way out; missing it makes dom_mutations_partial fire on a page with no iframes at all", unobserved)
	}
}

// TestExecContextsUnknownFrameSetNotLaunderedByStash pins the durability of
// the "we could not enumerate this document's frames" admission.
//
// If that bit is dropped when the set is stashed, a later restore of the
// same document reads the stash, finds an entry, and declares the set
// complete — upgrading an admitted-unknown frame set into a confident one.
// The result is a complete-looking DOM count for a page that may have an
// out-of-process iframe we never knew about: a silently wrong count, which
// is strictly worse than a false alarm.
func TestExecContextsUnknownFrameSetNotLaunderedByStash(t *testing.T) {
	e := NewExecContexts()

	// Restore a document we never stashed: its frame set is unknowable.
	e.HandleFrameNavigated(navigatedTyped("top", "", "loaderX", page.NavigationTypeBackForwardCacheRestore))
	e.HandleExecutionContextCreated(created("u-topX", "top", true))
	if _, unobserved := e.Snapshot(); unobserved == 0 {
		t.Fatal("precondition: restoring an unstashed document must not claim a complete frame set")
	}

	// Navigate away — the unknown set gets stashed — then come back to it.
	e.HandleExecutionContextsCleared()
	e.HandleFrameNavigated(navigatedTyped("top", "", "loaderY", page.NavigationTypeNavigation))
	e.HandleExecutionContextCreated(created("u-topY", "top", true))

	e.HandleExecutionContextsCleared()
	e.HandleExecutionContextCreated(created("u-topX2", "top", true))
	e.HandleFrameNavigated(navigatedTyped("top", "", "loaderX", page.NavigationTypeBackForwardCacheRestore))

	if _, unobserved := e.Snapshot(); unobserved == 0 {
		t.Error("unobservedFrames = 0 after restoring a document whose stashed frame set was itself never fully enumerated — the stash laundered an admitted-unknown set into a confident, complete one, so the DOM count now presents itself as covering frames it may never have seen")
	}
}
