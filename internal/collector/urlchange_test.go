package collector

import (
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
)

func TestURLChangesSeedAllowsImmediateSameDocumentNav(t *testing.T) {
	u := NewURLChanges(10)

	// Unseeded same-document navigation must be dropped to avoid subframe poisoning.
	u.HandleNavigatedWithinDocument(&page.EventNavigatedWithinDocument{
		FrameID: "sub-frame",
		URL:     "https://sub.test/a",
	})
	if len(u.buf.Peek(nil, 0)) != 0 {
		t.Fatal("expected unseeded same-document event to be dropped")
	}

	// Empty mainID seed is a no-op.
	u.Seed("", "https://ignored.test/")
	u.Seed("main-1", "https://app.test/home")
	// Second Seed is a no-op once initialized.
	u.Seed("other", "https://overwrite.test/")

	// Subframe same-document navigation is ignored.
	u.HandleNavigatedWithinDocument(&page.EventNavigatedWithinDocument{
		FrameID: "sub-frame",
		URL:     "https://sub.test/b",
	})
	if len(u.buf.Peek(nil, 0)) != 0 {
		t.Fatal("expected subframe same-document nav to be ignored")
	}

	// Top-frame pushState immediately records transition from seeded URL.
	u.HandleNavigatedWithinDocument(&page.EventNavigatedWithinDocument{
		FrameID: "main-1",
		URL:     "https://app.test/settings",
	})
	// Duplicate URL produces no entry.
	u.HandleNavigatedWithinDocument(&page.EventNavigatedWithinDocument{
		FrameID: "main-1",
		URL:     "https://app.test/settings",
	})

	entries := u.buf.Peek(nil, 0)
	if len(entries) != 1 {
		t.Fatalf("len = %d, want 1", len(entries))
	}
	if entries[0].From != "https://app.test/home" || entries[0].To != "https://app.test/settings" || entries[0].Kind != URLChangeSameDocument {
		t.Errorf("entry = %+v", entries[0])
	}
}

func TestURLChangesCrossDocumentFragmentsAndSubframes(t *testing.T) {
	u := NewURLChanges(10)
	u.HandleFrameNavigated(nil)
	u.HandleFrameNavigated(&page.EventFrameNavigated{})
	u.HandleFrameNavigated(&page.EventFrameNavigated{Frame: &cdp.Frame{ID: "", URL: "https://ignored.test"}})
	u.HandleNavigatedWithinDocument(nil)
	if u.mainID != "" || u.lastURL != "" {
		t.Fatalf("empty Frame.ID mutated state: mainID=%q lastURL=%q", u.mainID, u.lastURL)
	}

	// First cross-document nav sets baseline without emitting an entry.
	u.HandleFrameNavigated(&page.EventFrameNavigated{
		Frame: &cdp.Frame{ID: "top-1", URL: "https://example.com/doc", URLFragment: "#intro"},
	})
	if len(u.buf.Peek(nil, 0)) != 0 {
		t.Fatal("first nav should set baseline without emitting entry")
	}

	// Subframe nav ignored.
	u.HandleFrameNavigated(&page.EventFrameNavigated{
		Frame: &cdp.Frame{ID: "child-1", ParentID: "top-1", URL: "https://child.test/"},
	})
	if len(u.buf.Peek(nil, 0)) != 0 {
		t.Fatal("subframe nav should be ignored")
	}

	// Same URL + same fragment ignored.
	u.HandleFrameNavigated(&page.EventFrameNavigated{
		Frame: &cdp.Frame{ID: "top-1", URL: "https://example.com/doc", URLFragment: "#intro"},
	})
	if len(u.buf.Peek(nil, 0)) != 0 {
		t.Fatal("identical URL+fragment should not emit entry")
	}

	before := time.Now()
	// Fragment change recorded with full URL+fragment.
	u.HandleFrameNavigated(&page.EventFrameNavigated{
		Frame: &cdp.Frame{ID: "top-1", URL: "https://example.com/doc", URLFragment: "#details"},
	})
	// Top-level frame ID swap continues tracking seamlessly.
	u.HandleFrameNavigated(&page.EventFrameNavigated{
		Frame: &cdp.Frame{ID: "top-2", URL: "https://example.com/next"},
	})

	entries := u.buf.Peek(nil, 0)
	if len(entries) != 2 {
		t.Fatalf("len = %d, want 2", len(entries))
	}
	if entries[0].From != "https://example.com/doc#intro" || entries[0].To != "https://example.com/doc#details" || entries[0].Kind != URLChangeCrossDocument {
		t.Errorf("entries[0] = %+v", entries[0])
	}
	if entries[1].From != "https://example.com/doc#details" || entries[1].To != "https://example.com/next" {
		t.Errorf("entries[1] = %+v", entries[1])
	}

	if got := u.CountSince(before); got != 2 {
		t.Errorf("CountSince(before) = %d, want 2", got)
	}
	if got := u.CountSince(time.Now().Add(time.Hour)); got != 0 {
		t.Errorf("CountSince(future) = %d, want 0", got)
	}

	// Clear drops buffered entries but preserves baseline URL & mainID.
	u.Clear()
	if len(u.buf.Peek(nil, 0)) != 0 {
		t.Fatal("expected empty after Clear")
	}
	u.HandleNavigatedWithinDocument(&page.EventNavigatedWithinDocument{
		FrameID: "top-2",
		URL:     "https://example.com/next#after-clear",
	})
	afterClear := u.buf.Peek(nil, 0)
	if len(afterClear) != 1 || afterClear[0].From != "https://example.com/next" || afterClear[0].To != "https://example.com/next#after-clear" {
		t.Errorf("after Clear transition = %+v", afterClear)
	}
}
