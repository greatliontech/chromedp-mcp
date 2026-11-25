package collector

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/runtime"
)

func TestJSErrorsStandardAndPrimitiveExceptions(t *testing.T) {
	je := NewJSErrors(10)
	ts := runtime.Timestamp(float64(time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC).UnixMilli()))

	// Standard Error object (Description present).
	je.Handle(&runtime.EventExceptionThrown{
		Timestamp: ts,
		ExceptionDetails: &runtime.ExceptionDetails{
			Text:         "Uncaught",
			URL:          "https://example.com/main.js",
			LineNumber:   0,
			ColumnNumber: 0,
			Exception: &runtime.RemoteObject{
				Type:        runtime.RemoteObjectTypeObject,
				Description: "TypeError: x is not a function",
			},
			StackTrace: &runtime.StackTrace{
				CallFrames: []*runtime.CallFrame{
					{FunctionName: "run", URL: "https://example.com/main.js", LineNumber: 0, ColumnNumber: 5},
					nil,
					{FunctionName: "", URL: "https://example.com/boot.js", LineNumber: 10, ColumnNumber: 2},
				},
			},
		},
	})

	// Thrown primitive string (no Description, Value has JSON string).
	je.Handle(&runtime.EventExceptionThrown{
		Timestamp: ts,
		ExceptionDetails: &runtime.ExceptionDetails{
			Text: "Uncaught",
			Exception: &runtime.RemoteObject{
				Type:  runtime.RemoteObjectTypeString,
				Value: []byte(`"boom\nnow"`),
			},
		},
	})

	// Rejected primitive number with empty Text.
	je.Handle(&runtime.EventExceptionThrown{
		Timestamp: ts,
		ExceptionDetails: &runtime.ExceptionDetails{
			Exception: &runtime.RemoteObject{
				Type:  runtime.RemoteObjectTypeNumber,
				Value: []byte(`42`),
			},
		},
	})

	// Thrown null primitive (Value is JSON null — must not collapse to empty string).
	je.Handle(&runtime.EventExceptionThrown{
		Timestamp: ts,
		ExceptionDetails: &runtime.ExceptionDetails{
			Text: "Uncaught",
			Exception: &runtime.RemoteObject{
				Type:    runtime.RemoteObjectTypeObject,
				Subtype: runtime.RemoteObjectSubtypeNull,
				Value:   []byte(`null`),
			},
		},
	})

	// No Exception object at all: falls back to Text.
	je.Handle(&runtime.EventExceptionThrown{
		Timestamp: ts,
		ExceptionDetails: &runtime.ExceptionDetails{
			Text: "Script error.",
		},
	})

	entries := je.Peek(0)
	if len(entries) != 5 {
		t.Fatalf("len = %d, want 5", len(entries))
	}

	if entries[0].Message != "TypeError: x is not a function" {
		t.Errorf("entry[0].Message = %q", entries[0].Message)
	}
	if entries[0].Source != "https://example.com/main.js" {
		t.Errorf("entry[0].Source = %q", entries[0].Source)
	}
	if entries[0].ReceivedAt.IsZero() {
		t.Error("entry[0].ReceivedAt should be non-zero")
	}
	wantStack := "run (https://example.com/main.js:0:5)\n(anonymous) (https://example.com/boot.js:10:2)\n"
	if entries[0].StackTrace != wantStack {
		t.Errorf("entry[0].StackTrace = %q, want %q", entries[0].StackTrace, wantStack)
	}

	// Verify 0-based Line and Column are serialized in JSON (not omitted).
	raw, err := json.Marshal(entries[0])
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	js := string(raw)
	if !strings.Contains(js, `"line":0`) || !strings.Contains(js, `"column":0`) {
		t.Errorf("JSON omitted 0-based line/column: %s", js)
	}

	if entries[1].Message != "Uncaught: boom\nnow" {
		t.Errorf("entry[1].Message = %q, want %q", entries[1].Message, "Uncaught: boom\nnow")
	}
	if entries[2].Message != "42" {
		t.Errorf("entry[2].Message = %q, want %q", entries[2].Message, "42")
	}
	if entries[3].Message != "Uncaught: null" {
		t.Errorf("entry[3].Message = %q, want %q", entries[3].Message, "Uncaught: null")
	}
	if entries[4].Message != "Script error." {
		t.Errorf("entry[4].Message = %q, want %q", entries[4].Message, "Script error.")
	}
}

func TestJSErrorsNilGuardsAndBufferOps(t *testing.T) {
	je := NewJSErrors(5)
	je.Handle(nil)
	je.Handle(&runtime.EventExceptionThrown{})
	if got := je.Peek(0); len(got) != 0 {
		t.Fatalf("nil guards failed: %+v", got)
	}

	for _, msg := range []string{"e1", "e2", "e3"} {
		je.Handle(&runtime.EventExceptionThrown{
			ExceptionDetails: &runtime.ExceptionDetails{Text: msg},
		})
	}

	if got := je.Peek(2); len(got) != 2 || got[0].Message != "e1" || got[1].Message != "e2" {
		t.Fatalf("Peek(2) = %+v, want [e1, e2]", got)
	}
	if got := je.Drain(2); len(got) != 2 || got[0].Message != "e1" || got[1].Message != "e2" {
		t.Fatalf("Drain(2) = %+v, want [e1, e2]", got)
	}
	if got := je.Peek(0); len(got) != 1 || got[0].Message != "e3" {
		t.Fatalf("remaining = %+v, want [e3]", got)
	}

	je.Clear()
	if got := je.Peek(0); len(got) != 0 {
		t.Errorf("after Clear, Peek = %+v, want empty", got)
	}
}
