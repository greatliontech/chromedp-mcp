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

// SnapshotGenToken captures the document generation state before a snapshot
// fetch so SetSnapshotRefs can reject commits raced by a main-frame or target
// subframe navigation/detach.
type SnapshotGenToken struct {
	mainGen  uint64
	frameID  cdp.FrameID
	frameGen uint64
}

// SnapshotGeneration captures the current generation token for main-frame
// (frameID == "") or subframe (frameID != "") snapshot commits.
func (t *Tab) SnapshotGeneration(frameID cdp.FrameID) SnapshotGenToken {
	t.snapshotMu.RLock()
	defer t.snapshotMu.RUnlock()
	return SnapshotGenToken{
		mainGen:  t.snapshotGen,
		frameID:  frameID,
		frameGen: t.snapshotFrameGen[frameID],
	}
}

// SetSnapshotRefs replaces the tab's active snapshot reference table if no
// invalidating main-frame or target-subframe navigation/crash occurred since
// tok was captured.
func (t *Tab) SetSnapshotRefs(tok SnapshotGenToken, refs map[string]SnapshotRef) bool {
	t.snapshotMu.Lock()
	defer t.snapshotMu.Unlock()
	if t.snapshotGen != tok.mainGen {
		return false
	}
	if tok.frameID != "" && t.snapshotFrameGen[tok.frameID] != tok.frameGen {
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

// InvalidateSnapshotRefs advances the main document generation (blocking any
// in-flight snapshot commit), clears the active snapshot ref table, and
// records the staleness reason when a snapshot was previously taken.
func (t *Tab) InvalidateSnapshotRefs(reason string) {
	t.snapshotMu.Lock()
	defer t.snapshotMu.Unlock()
	t.snapshotGen++
	clear(t.snapshotFrameGen)
	t.snapshotRefs = nil
	if t.snapshotHasEverSet {
		t.snapshotStaleReason = reason
	}
}

// InvalidateFrameSnapshotRefs advances frameID's generation (blocking in-flight
// subframe snapshot commits) and prunes refs belonging to frameID without
// invalidating refs in other frames. If all active refs belonged to frameID,
// marks the snapshot stale with a clear frame-specific reason.
func (t *Tab) InvalidateFrameSnapshotRefs(frameID cdp.FrameID) {
	if frameID == "" {
		return
	}
	t.snapshotMu.Lock()
	defer t.snapshotMu.Unlock()
	if t.snapshotFrameGen == nil {
		t.snapshotFrameGen = make(map[cdp.FrameID]uint64)
	}
	t.snapshotFrameGen[frameID]++
	removed := 0
	for k, r := range t.snapshotRefs {
		if r.FrameID == frameID {
			delete(t.snapshotRefs, k)
			removed++
		}
	}
	if removed > 0 && len(t.snapshotRefs) == 0 {
		t.snapshotStaleReason = "frame navigated or detached"
	}
}
