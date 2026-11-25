package collector

import (
	"testing"
	"time"

	"github.com/chromedp/cdproto/runtime"
)

func TestConsoleLevelNormalization(t *testing.T) {
	c := NewConsole(10)
	ts := runtime.Timestamp(float64(time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC).UnixMilli()))

	c.Handle(&runtime.EventConsoleAPICalled{
		Type:      runtime.ConsoleAPICalledTypeWarning,
		Timestamp: ts,
		Args:      []*runtime.RemoteObject{{Type: runtime.RemoteObjectTypeString, Value: []byte(`"watch out"`)}},
	})
	c.Handle(&runtime.EventConsoleAPICalled{
		Type:      runtime.ConsoleAPICalledTypeLog,
		Timestamp: ts,
		Args:      []*runtime.RemoteObject{{Type: runtime.RemoteObjectTypeString, Value: []byte(`"info line"`)}},
	})
	c.Handle(&runtime.EventConsoleAPICalled{
		Type:      runtime.ConsoleAPICalledTypeError,
		Timestamp: ts,
		Args:      []*runtime.RemoteObject{{Type: runtime.RemoteObjectTypeString, Value: []byte(`"boom"`)}},
	})

	all := c.Peek("", 0)
	if len(all) != 3 {
		t.Fatalf("Peek all len = %d, want 3", len(all))
	}
	if all[0].Level != "warn" {
		t.Errorf("warning event Level = %q, want %q", all[0].Level, "warn")
	}
	if all[0].Text != "watch out" {
		t.Errorf("warning event Text = %q, want %q", all[0].Text, "watch out")
	}
	if all[0].ReceivedAt.IsZero() {
		t.Error("ReceivedAt should be non-zero")
	}
	if all[0].Timestamp.UnixMilli() != int64(ts) {
		t.Errorf("Timestamp = %v, want unix ms %d", all[0].Timestamp, int64(ts))
	}

	warns := c.Peek("warn", 0)
	if len(warns) != 1 || warns[0].Text != "watch out" {
		t.Errorf("Peek(\"warn\") = %+v, want 1 entry \"watch out\"", warns)
	}
	if raw := c.Peek("warning", 0); len(raw) != 0 {
		t.Errorf("Peek(\"warning\") = %+v, want empty (normalized to \"warn\")", raw)
	}
}

func TestConsoleRemoteObjectFormatting(t *testing.T) {
	c := NewConsole(10)
	c.Handle(&runtime.EventConsoleAPICalled{
		Type: runtime.ConsoleAPICalledTypeLog,
		Args: []*runtime.RemoteObject{
			{Type: runtime.RemoteObjectTypeString, Value: []byte(`"line1\nline2\t\"quoted\"\\slash\u2603"`)},
			{Type: runtime.RemoteObjectTypeNumber, Value: []byte(`42`)},
			{Type: runtime.RemoteObjectTypeBoolean, Value: []byte(`true`)},
			{Type: runtime.RemoteObjectTypeObject, Subtype: runtime.RemoteObjectSubtypeNull, Value: []byte(`null`)},
			{Type: runtime.RemoteObjectTypeObject, Description: "Object"},
			{Type: runtime.RemoteObjectTypeNumber, UnserializableValue: "NaN"},
			{Type: runtime.RemoteObjectTypeSymbol},
			nil,
		},
	})

	entries := c.Peek("", 0)
	if len(entries) != 1 {
		t.Fatalf("len = %d, want 1", len(entries))
	}
	want := "line1\nline2\t\"quoted\"\\slash\u2603 42 true null Object NaN [symbol]"
	if entries[0].Text != want {
		t.Errorf("Text = %q, want %q", entries[0].Text, want)
	}
}

func TestConsoleSourceExtraction(t *testing.T) {
	c := NewConsole(10)
	c.Handle(&runtime.EventConsoleAPICalled{
		Type: runtime.ConsoleAPICalledTypeLog,
		Args: []*runtime.RemoteObject{{Type: runtime.RemoteObjectTypeString, Value: []byte(`"with source"`)}},
		StackTrace: &runtime.StackTrace{
			CallFrames: []*runtime.CallFrame{
				{URL: "https://example.com/app.js", LineNumber: 12, ColumnNumber: 4},
			},
		},
	})
	c.Handle(&runtime.EventConsoleAPICalled{
		Type: runtime.ConsoleAPICalledTypeLog,
		Args: []*runtime.RemoteObject{{Type: runtime.RemoteObjectTypeString, Value: []byte(`"empty url"`)}},
		StackTrace: &runtime.StackTrace{
			CallFrames: []*runtime.CallFrame{
				{URL: "", LineNumber: 0, ColumnNumber: 0},
			},
		},
	})
	c.Handle(&runtime.EventConsoleAPICalled{
		Type: runtime.ConsoleAPICalledTypeLog,
		Args: []*runtime.RemoteObject{{Type: runtime.RemoteObjectTypeString, Value: []byte(`"nil frame"`)}},
		StackTrace: &runtime.StackTrace{
			CallFrames: []*runtime.CallFrame{nil},
		},
	})

	entries := c.Peek("", 0)
	if len(entries) != 3 {
		t.Fatalf("len = %d, want 3", len(entries))
	}
	if entries[0].Source != "https://example.com/app.js:12:4" {
		t.Errorf("entry[0].Source = %q, want %q", entries[0].Source, "https://example.com/app.js:12:4")
	}
	if entries[1].Source != "" {
		t.Errorf("entry[1].Source = %q, want empty", entries[1].Source)
	}
	if entries[2].Source != "" {
		t.Errorf("entry[2].Source = %q, want empty", entries[2].Source)
	}
}

func TestConsolePeekDrainLimitClearAndNilGuards(t *testing.T) {
	c := NewConsole(10)
	c.Handle(nil)
	if got := c.Peek("", 0); len(got) != 0 {
		t.Fatalf("nil event added entry: %+v", got)
	}

	for _, msg := range []string{`"a"`, `"b"`, `"c"`} {
		c.Handle(&runtime.EventConsoleAPICalled{
			Type: runtime.ConsoleAPICalledTypeLog,
			Args: []*runtime.RemoteObject{{Type: runtime.RemoteObjectTypeString, Value: []byte(msg)}},
		})
	}
	c.Handle(&runtime.EventConsoleAPICalled{
		Type: runtime.ConsoleAPICalledTypeError,
		Args: []*runtime.RemoteObject{{Type: runtime.RemoteObjectTypeString, Value: []byte(`"e1"`)}},
	})

	if got := c.Peek("log", 2); len(got) != 2 || got[0].Text != "a" || got[1].Text != "b" {
		t.Fatalf("Peek(log, 2) = %+v, want [a, b]", got)
	}
	if got := c.Drain("log", 2); len(got) != 2 || got[0].Text != "a" || got[1].Text != "b" {
		t.Fatalf("Drain(log, 2) = %+v, want [a, b]", got)
	}
	// Remaining should be "c" and "e1".
	rem := c.Peek("", 0)
	if len(rem) != 2 || rem[0].Text != "c" || rem[1].Text != "e1" {
		t.Fatalf("remaining = %+v, want [c, e1]", rem)
	}

	c.Clear()
	if got := c.Peek("", 0); len(got) != 0 {
		t.Errorf("after Clear, Peek = %+v, want empty", got)
	}
}
