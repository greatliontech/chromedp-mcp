package tab

import (
	"fmt"
	"maps"
	"strings"

	"github.com/chromedp/cdproto/cdp"
)

// SnapshotRef identifies a DOM element captured in a tab's latest accessibility
// snapshot. Refs ("e1", "e2", ...) stay valid until replaced by a newer
// snapshot or invalidated by a main-frame navigation or target crash (and
// iframe refs are pruned when their owning subframe navigates or detaches).
type SnapshotRef struct {
	BackendNodeID cdp.BackendNodeID
	FrameID       cdp.FrameID
	Role          string
	Name          string
}

// SnapshotGeneration returns the tab's current document generation counter for
// snapshot commits. Callers capture this before querying Chrome's accessibility
// tree and pass it to SetSnapshotRefs so a main-frame navigation or crash
// mid-snapshot cannot commit stale refs.
func (t *Tab) SnapshotGeneration() uint64 {
	t.snapshotMu.RLock()
	defer t.snapshotMu.RUnlock()
	return t.snapshotGen
}

// SetSnapshotRefs replaces the tab's active snapshot reference table if the
// tab's document generation still matches gen. Returns false without modifying
// state when a main-frame navigation or crash occurred after gen was captured.
func (t *Tab) SetSnapshotRefs(gen uint64, refs map[string]SnapshotRef) bool {
	t.snapshotMu.Lock()
	defer t.snapshotMu.Unlock()
	if t.snapshotGen != gen {
		return false
	}
	if len(refs) == 0 {
		t.snapshotRefs = nil
	} else {
		t.snapshotRefs = maps.Clone(refs)
	}
	t.snapshotHasEverSet = true
	t.snapshotStaleReason = ""
	return true
}

// ResolveRef looks up ref in the tab's latest accessibility snapshot.
func (t *Tab) ResolveRef(ref string) (SnapshotRef, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return SnapshotRef{}, fmt.Errorf("ref must not be empty")
	}
	t.snapshotMu.RLock()
	defer t.snapshotMu.RUnlock()
	if r, ok := t.snapshotRefs[ref]; ok {
		return r, nil
	}
	if t.snapshotStaleReason != "" {
		return SnapshotRef{}, fmt.Errorf("ref %q is stale (%s) — call snapshot again", ref, t.snapshotStaleReason)
	}
	if !t.snapshotHasEverSet {
		return SnapshotRef{}, fmt.Errorf("ref %q not found (no active snapshot) — call snapshot first", ref)
	}
	return SnapshotRef{}, fmt.Errorf("ref %q not found in current snapshot — call snapshot to inspect available refs", ref)
}

// InvalidateSnapshotRefs advances the document generation (blocking any
// in-flight snapshot commit), clears the active snapshot ref table, and
// records the staleness reason when a snapshot was previously taken.
func (t *Tab) InvalidateSnapshotRefs(reason string) {
	t.snapshotMu.Lock()
	defer t.snapshotMu.Unlock()
	t.snapshotGen++
	t.snapshotRefs = nil
	if t.snapshotHasEverSet {
		t.snapshotStaleReason = reason
	}
}

// InvalidateFrameSnapshotRefs prunes refs belonging to frameID without
// invalidating refs in other frames (e.g. main frame).
func (t *Tab) InvalidateFrameSnapshotRefs(frameID cdp.FrameID) {
	if frameID == "" {
		return
	}
	t.snapshotMu.Lock()
	defer t.snapshotMu.Unlock()
	for k, r := range t.snapshotRefs {
		if r.FrameID == frameID {
			delete(t.snapshotRefs, k)
		}
	}
}
