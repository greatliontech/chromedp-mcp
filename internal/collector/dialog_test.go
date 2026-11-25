package collector

import (
	"sync"
	"testing"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
)

func TestDialogLifecycle(t *testing.T) {
	d := NewDialog()
	if d.Open() || d.Current() != nil {
		t.Fatal("new Dialog should start closed")
	}

	// Nil events must be safe no-ops.
	d.HandleOpening(nil)
	d.HandleClosed(nil)
	if d.Open() {
		t.Fatal("nil events must not open dialog")
	}

	// Opening sets all fields and returns a defensive copy.
	d.HandleOpening(&page.EventJavascriptDialogOpening{
		URL:               "https://example.com/form",
		FrameID:           cdp.FrameID("frame-1"),
		Message:           "Enter name:",
		Type:              page.DialogTypePrompt,
		HasBrowserHandler: true,
		DefaultPrompt:     "Alice",
	})
	if !d.Open() {
		t.Fatal("expected Dialog.Open() == true")
	}
	got := d.Current()
	if got == nil {
		t.Fatal("expected non-nil DialogState")
	}
	if got.Type != "prompt" || got.Message != "Enter name:" || got.DefaultPrompt != "Alice" || got.URL != "https://example.com/form" || !got.HasBrowserHandler || got.OpenedAt.IsZero() {
		t.Fatalf("unexpected DialogState: %+v", got)
	}

	// Mutating the returned copy must not affect internal state.
	got.Message = "tampered"
	if cur := d.Current(); cur.Message != "Enter name:" {
		t.Errorf("Current() leaked mutable pointer: got %q", cur.Message)
	}

	// Stale close for a different frame must not clear the current dialog.
	d.HandleClosed(&page.EventJavascriptDialogClosed{
		FrameID: cdp.FrameID("other-frame"),
	})
	if !d.Open() {
		t.Fatal("mismatched FrameID close cleared active dialog")
	}

	// Matching close clears the dialog.
	d.HandleClosed(&page.EventJavascriptDialogClosed{
		FrameID: cdp.FrameID("frame-1"),
		Result:  true,
	})
	if d.Open() || d.Current() != nil {
		t.Fatal("matching close did not clear dialog")
	}

	// Clear() resets an open dialog directly (e.g. target crash).
	d.HandleOpening(&page.EventJavascriptDialogOpening{
		Type:    page.DialogTypeAlert,
		Message: "hi",
	})
	d.Clear()
	if d.Open() || d.Current() != nil {
		t.Fatal("Clear() did not reset dialog")
	}
}

func TestDialogClearIfSeqAndFrameLifecycle(t *testing.T) {
	d := NewDialog()

	// Nil nav/detach events are safe no-ops.
	d.HandleFrameNavigated(nil)
	d.HandleFrameDetached(nil)

	// Open dialog in a subframe.
	d.HandleOpening(&page.EventJavascriptDialogOpening{
		FrameID: cdp.FrameID("sub-1"),
		Type:    page.DialogTypeAlert,
		Message: "first",
	})
	cur, seq1 := d.Snapshot()
	if cur == nil || seq1 == 0 {
		t.Fatalf("Snapshot() = (%v, %d), want non-nil and seq > 0", cur, seq1)
	}

	// Unrelated subframe navigation and detach must NOT clear sub-1's dialog.
	d.HandleFrameNavigated(&page.EventFrameNavigated{
		Frame: &cdp.Frame{ID: cdp.FrameID("sub-2"), ParentID: cdp.FrameID("main")},
	})
	d.HandleFrameDetached(&page.EventFrameDetached{
		FrameID: cdp.FrameID("sub-2"),
	})
	if !d.Open() {
		t.Fatal("unrelated subframe nav/detach cleared active dialog")
	}

	// Same subframe navigation clears the dialog.
	d.HandleFrameNavigated(&page.EventFrameNavigated{
		Frame: &cdp.Frame{ID: cdp.FrameID("sub-1"), ParentID: cdp.FrameID("main")},
	})
	if d.Open() {
		t.Fatal("owning subframe navigation did not clear dialog")
	}

	// Top-level navigation clears any subframe dialog too.
	d.HandleOpening(&page.EventJavascriptDialogOpening{
		FrameID: cdp.FrameID("sub-1"),
		Type:    page.DialogTypeConfirm,
		Message: "stay?",
	})
	d.HandleFrameNavigated(&page.EventFrameNavigated{
		Frame: &cdp.Frame{ID: cdp.FrameID("main")},
	})
	if d.Open() {
		t.Fatal("top-level navigation did not clear dialog")
	}

	// Owning frame detach clears the dialog.
	d.HandleOpening(&page.EventJavascriptDialogOpening{
		FrameID: cdp.FrameID("sub-1"),
		Type:    page.DialogTypeAlert,
		Message: "detached",
	})
	d.HandleFrameDetached(&page.EventFrameDetached{
		FrameID: cdp.FrameID("sub-1"),
	})
	if d.Open() {
		t.Fatal("owning frame detach did not clear dialog")
	}

	// ClearIfSeq clears matching generation but preserves a chained newer dialog.
	d.HandleOpening(&page.EventJavascriptDialogOpening{
		FrameID: cdp.FrameID("main"),
		Type:    page.DialogTypeAlert,
		Message: "dialog-1",
	})
	_, gen1 := d.Snapshot()
	d.HandleOpening(&page.EventJavascriptDialogOpening{
		FrameID: cdp.FrameID("main"),
		Type:    page.DialogTypeAlert,
		Message: "dialog-2",
	})
	_, gen2 := d.Snapshot()
	if gen2 <= gen1 {
		t.Fatalf("expected strictly increasing seq: gen1=%d gen2=%d", gen1, gen2)
	}
	d.ClearIfSeq(0)
	d.ClearIfSeq(gen1)
	if cur := d.Current(); cur == nil || cur.Message != "dialog-2" {
		t.Fatalf("stale ClearIfSeq clobbered chained dialog: got %+v", cur)
	}
	d.ClearIfSeq(gen2)
	if d.Open() {
		t.Fatal("matching ClearIfSeq did not clear dialog")
	}
}

func TestDialogConcurrentAccess(t *testing.T) {
	d := NewDialog()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				d.HandleOpening(&page.EventJavascriptDialogOpening{
					FrameID: cdp.FrameID("f1"),
					Type:    page.DialogTypeAlert,
					Message: "ping",
				})
				_ = d.Open()
				_ = d.Current()
				d.HandleClosed(&page.EventJavascriptDialogClosed{
					FrameID: cdp.FrameID("f1"),
				})
			}
		}()
	}
	wg.Wait()
}
