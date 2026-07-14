package collector

import (
	"encoding/json"
	"maps"
	"slices"
	"sync"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
)

// ExecContext is a frame's main-world JavaScript execution context.
type ExecContext struct {
	// UniqueID is CDP's system-unique context identifier. Unlike the
	// numeric ExecutionContextID it is never reused, so holding one across
	// a navigation is safe: the old context cannot be silently mistaken
	// for the new one that replaced it, it simply becomes unevaluatable.
	UniqueID string
	// FrameID is the frame this context belongs to.
	FrameID string
	// IsMain is true for the top-level frame's context.
	IsMain bool
}

// ExecContexts tracks the main-world execution context of every frame in
// a tab.
//
// It exists so activity observation can reach the per-frame DOM mutation
// buffers. The observer script is injected into every document, so each
// frame accumulates its own buffer in its own `window` — a count read
// from the top frame alone silently ignores every iframe. Evaluating in a
// frame's own context is the only way to see its buffer.
//
// Only main-world ("default") contexts are tracked. Isolated worlds
// (extensions, and chromedp's own utility world) do not share `window`
// with page scripts, so the injected observer's buffer is invisible from
// them.
type ExecContexts struct {
	mu sync.Mutex
	// byUnique is keyed by ExecContext.UniqueID.
	byUnique map[string]ExecContext
	// frames is every frame we know exists, whether or not we hold an
	// execution context for it. Comparing this set against byUnique is what
	// lets observation say "some frames were not observable" instead of
	// reporting a confident zero for them.
	//
	// The only evidence we ever get for an out-of-process (cross-origin)
	// iframe is the frameAttached that arrives BEFORE it swaps out of our
	// process. Page.getFrameTree does not list it, and its execution
	// contexts live on a CDP session we are not attached to. So this set is
	// built from the event stream, and a frame is never dropped merely
	// because it swapped away — see HandleFrameDetached.
	frames map[string]struct{}
	// mainFrameID is the top-level frame. A frame is top-level exactly
	// when it has no parent.
	mainFrameID string

	// currentLoaderID identifies the top-level document currently committed.
	currentLoaderID string
	// retained stashes a departing document's frame set, keyed by its
	// top-frame loader ID, so a back/forward restore can get it back.
	//
	// This exists because of how the bfcache restores a page: Chrome emits
	// frameNavigated with type=BackForwardCacheRestore and re-creates
	// execution contexts for the in-process frames, but sends NO
	// frameAttached for any restored child. Since a frameAttached before
	// the process swap is our only evidence that an out-of-process iframe
	// exists at all, a frame set rebuilt from the restore's event stream
	// alone can never learn about it — and observation would report a
	// confident, complete DOM count for a page that has a frame it cannot
	// see. The loader ID is preserved across a bfcache restore, which is
	// what makes the stash retrievable.
	retained map[string]retainedFrames
	// retainedOrder is the FIFO eviction order for retained.
	retainedOrder []string
	// frameSetUnknown is set when a document is restored whose frame set we
	// never stashed (evicted, or restored from a session we did not
	// observe). We then cannot enumerate its frames, so we must not claim
	// the DOM count is complete. Cleared on the next real navigation.
	frameSetUnknown bool
}

// retainedFrames is a stashed document's frame set, kept in case the
// document comes back via the back/forward cache.
type retainedFrames struct {
	frames map[string]struct{}
	// unknown is true when the set could not be fully enumerated when it was
	// stashed. Restoring must preserve that admission rather than present
	// the set as complete.
	unknown bool
}

// maxRetainedFrameSets bounds the bfcache frame-set stash. Chrome's
// back/forward cache holds only a handful of documents; this is well above
// that, and an over-eviction degrades to frameSetUnknown (an honest
// "count may be incomplete"), never to a silently wrong count.
const maxRetainedFrameSets = 32

// NewExecContexts creates an empty execution context registry.
func NewExecContexts() *ExecContexts {
	return &ExecContexts{
		byUnique: make(map[string]ExecContext),
		frames:   make(map[string]struct{}),
		retained: make(map[string]retainedFrames),
	}
}

// HandleExecutionContextCreated records a newly created main-world context.
func (e *ExecContexts) HandleExecutionContextCreated(ev *runtime.EventExecutionContextCreated) {
	if ev == nil || ev.Context == nil || ev.Context.UniqueID == "" {
		return
	}
	var aux struct {
		IsDefault bool   `json:"isDefault"`
		FrameID   string `json:"frameId"`
	}
	if len(ev.Context.AuxData) > 0 {
		// A context whose auxData won't parse is one we cannot place in a
		// frame or classify as main-world, so it is not usable here.
		if err := json.Unmarshal([]byte(ev.Context.AuxData), &aux); err != nil {
			return
		}
	}
	if !aux.IsDefault {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.byUnique[ev.Context.UniqueID] = ExecContext{
		UniqueID: ev.Context.UniqueID,
		FrameID:  aux.FrameID,
	}
}

// HandleExecutionContextDestroyed drops a context that no longer exists.
func (e *ExecContexts) HandleExecutionContextDestroyed(ev *runtime.EventExecutionContextDestroyed) {
	if ev == nil || ev.ExecutionContextUniqueID == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.byUnique, ev.ExecutionContextUniqueID)
}

// HandleExecutionContextsCleared drops every context. Chrome emits this on
// a cross-process navigation, where all previous contexts die at once.
func (e *ExecContexts) HandleExecutionContextsCleared() {
	e.mu.Lock()
	defer e.mu.Unlock()
	clear(e.byUnique)
}

// HandleFrameNavigated records the frame. A top-level navigation (no
// parent) additionally replaces the frame set, because the document — and
// with it every child frame — is being replaced.
//
// Replacing the set is load-bearing. Chrome emits frameDetached only for
// frames removed from the *current* document's tree; when the top document
// is replaced, its children simply cease to exist, with no detach event of
// any reason. Carrying them forward would accumulate every iframe of every
// page the tab ever visited, none of which can ever have an execution
// context again — so Snapshot would report them as unobserved forever and
// every later observation would set dom_mutations_partial, on pages with no
// subframes at all. A flag that fires on ordinary pages is a flag callers
// learn to ignore, which would restore the very confident-zero failure it
// exists to prevent.
//
// The two navigation types must be handled differently:
//
//   - A real navigation (anything else) commits before the incoming
//     document's frames attach, so clearing here cannot discard a frame of
//     the new page. The outgoing document may come back via the bfcache, so
//     its frame set is stashed first.
//
//   - A BackForwardCacheRestore arrives AFTER the restored document's
//     contexts are re-created, and Chrome sends no frameAttached for any
//     restored child. Clearing on this path would permanently forget the
//     restored page's out-of-process iframes — which we can only ever learn
//     about from a frameAttached — so the set is restored from the stash
//     instead of rebuilt.
func (e *ExecContexts) HandleFrameNavigated(ev *page.EventFrameNavigated) {
	if ev == nil || ev.Frame == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	id := string(ev.Frame.ID)
	if ev.Frame.ParentID != "" {
		// A subframe navigating within the current document.
		e.frames[id] = struct{}{}
		return
	}

	loader := string(ev.Frame.LoaderID)

	// Stash the OUTGOING document on every top-frame commit, whatever the
	// reason we are leaving it. The bfcache does not only serve the back
	// button: leaving page B by going *back* to A puts B in the cache, and
	// the subsequent forward navigation restores B from it. Stashing only on
	// the plain-navigation path would lose B's frame set on the way out, and
	// the forward restore would then find nothing to restore.
	if e.currentLoaderID != "" && e.currentLoaderID != loader {
		e.stashLocked(e.currentLoaderID, e.frames, e.frameSetUnknown)
	}

	if ev.Type == page.NavigationTypeBackForwardCacheRestore {
		if entry, ok := e.retained[loader]; ok {
			e.frames = maps.Clone(entry.frames)
			// Carry the admission forward. If we could not enumerate this
			// document's frames when we stashed it, restoring the stash does
			// not make it complete.
			e.frameSetUnknown = entry.unknown
		} else {
			// We never stashed this document's frames, so we cannot
			// enumerate them — and an out-of-process iframe would be
			// invisible. Say the frame set is unknown rather than claim a
			// complete count.
			e.frames = make(map[string]struct{})
			e.frameSetUnknown = true
		}
	} else {
		// A fresh document: its frames announce themselves from here on, so
		// the set we build is complete.
		e.frames = make(map[string]struct{})
		e.frameSetUnknown = false
	}

	e.mainFrameID = id
	e.currentLoaderID = loader
	e.frames[id] = struct{}{}
}

// stashLocked retains a frame set under its document's loader ID, evicting
// the oldest entry when full. Caller must hold e.mu.
//
// `unknown` records that the set could not be fully enumerated. It must be
// stored, not dropped: a restore that silently upgraded an admitted-unknown
// set to a complete one would report a confident, complete DOM count for a
// document with a frame it cannot see — the exact silent-wrong-count failure
// the partial flag exists to prevent.
func (e *ExecContexts) stashLocked(loaderID string, frames map[string]struct{}, unknown bool) {
	if _, exists := e.retained[loaderID]; exists {
		// Refresh its FIFO position: a document we keep returning to should
		// not be evicted ahead of colder ones.
		e.retainedOrder = slices.DeleteFunc(e.retainedOrder, func(id string) bool {
			return id == loaderID
		})
	}
	e.retainedOrder = append(e.retainedOrder, loaderID)
	e.retained[loaderID] = retainedFrames{frames: maps.Clone(frames), unknown: unknown}

	for len(e.retainedOrder) > maxRetainedFrameSets {
		oldest := e.retainedOrder[0]
		e.retainedOrder = e.retainedOrder[1:]
		delete(e.retained, oldest)
	}
}

// HandleFrameAttached records a newly attached frame. This fires for
// out-of-process iframes too, which is exactly why it is tracked: it is
// the only way we learn such a frame exists at all.
func (e *ExecContexts) HandleFrameAttached(ev *page.EventFrameAttached) {
	if ev == nil || ev.FrameID == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.frames[string(ev.FrameID)] = struct{}{}
}

// HandleFrameDetached drops a frame that has been removed from the tree.
//
// Reason matters. Chrome detaches a frame from this session for two very
// different reasons:
//
//	"remove" — the frame is gone from the page.
//	"swap"   — the frame moved to another process (it became an
//	           out-of-process iframe, which is what happens to every
//	           cross-origin frame). It still exists and still mutates; we
//	           simply can no longer see into it.
//
// Deleting on "swap" would erase our only evidence that the frame exists,
// and observation would then report a confident zero for mutations it
// cannot see. Keeping it — with no execution context — is what makes it
// count as an unobserved frame.
func (e *ExecContexts) HandleFrameDetached(ev *page.EventFrameDetached) {
	if ev == nil || ev.FrameID == "" || ev.Reason == page.FrameDetachedReasonSwap {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.frames, string(ev.FrameID))
}

// SeedFrameTree primes the registry from an initial Page.getFrameTree, so
// the main frame is known without waiting for a navigation. A tab that is
// never navigated (it sits on about:blank) would otherwise have no main
// frame, and every DOM mutation count on it would report "unavailable".
//
// This is a seed, not a source of truth for the frame set: getFrameTree
// does NOT list out-of-process iframes. Those are learned only from the
// frameAttached event stream.
func (e *ExecContexts) SeedFrameTree(mainFrameID string, frameIDs []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if mainFrameID != "" {
		e.mainFrameID = mainFrameID
	}
	for _, id := range frameIDs {
		e.frames[id] = struct{}{}
	}
}

// Snapshot returns the known main-world contexts, plus the number of
// frames we know exist but hold no context for.
//
// unobservedFrames > 0 means some frame's DOM cannot be inspected — in
// practice a cross-origin (out-of-process) iframe, whose execution contexts
// live on a CDP session we are not attached to. Its mutations are invisible
// to us, so any count we report is a lower bound. Callers must surface that
// rather than presenting the count as complete.
//
// A same-process frame that has attached but whose execution context has
// not been created yet also counts as unobserved. That is honest: during
// that gap we genuinely cannot see it.
//
// IsMain is resolved at snapshot time rather than at creation time,
// because Chrome can emit executionContextCreated for a document before
// the frameNavigated that identifies its frame as top-level.
func (e *ExecContexts) Snapshot() (contexts []ExecContext, unobservedFrames int) {
	e.mu.Lock()
	defer e.mu.Unlock()

	withContext := make(map[string]struct{}, len(e.byUnique))
	contexts = make([]ExecContext, 0, len(e.byUnique))
	for _, c := range e.byUnique {
		c.IsMain = e.mainFrameID != "" && c.FrameID == e.mainFrameID
		contexts = append(contexts, c)
		withContext[c.FrameID] = struct{}{}
	}

	for id := range e.frames {
		if _, ok := withContext[id]; !ok {
			unobservedFrames++
		}
	}
	if e.frameSetUnknown {
		// A restored document whose frame set we never stashed: we cannot
		// enumerate its frames, so we cannot rule out an unobservable one.
		// Count it as unobserved rather than claim a complete picture.
		unobservedFrames++
	}
	return contexts, unobservedFrames
}
