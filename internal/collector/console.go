package collector

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/chromedp/cdproto/runtime"
)

// ConsoleEntry represents a captured console message.
type ConsoleEntry struct {
	Level     string    `json:"level"`
	Text      string    `json:"text"`
	Source    string    `json:"source,omitempty"`
	Timestamp time.Time `json:"timestamp"`
	// ReceivedAt is the wall-clock time the event was processed by the
	// Go-side collector. Used by activity observation for time-window
	// filtering on the same Go clock as window start. JSON-hidden.
	ReceivedAt time.Time `json:"-"`
}

// Console collects console.log/warn/error/info/debug messages.
type Console struct {
	buf *RingBuffer[ConsoleEntry]
}

// NewConsole creates a console collector with the given buffer size.
func NewConsole(maxSize int) *Console {
	return &Console{buf: NewRingBuffer[ConsoleEntry](maxSize)}
}

// normalizeConsoleLevel maps CDP's ConsoleAPICalledType to the canonical level
// names matching the JS Console API ("warning" -> "warn").
func normalizeConsoleLevel(t runtime.ConsoleAPICalledType) string {
	if t == runtime.ConsoleAPICalledTypeWarning {
		return "warn"
	}
	return string(t)
}

// Handle processes a runtime.EventConsoleAPICalled event.
func (c *Console) Handle(ev *runtime.EventConsoleAPICalled) {
	if ev == nil {
		return
	}
	parts := make([]string, 0, len(ev.Args))
	for _, arg := range ev.Args {
		if arg == nil {
			continue
		}
		parts = append(parts, remoteObjectToString(arg))
	}
	var source string
	if ev.StackTrace != nil && len(ev.StackTrace.CallFrames) > 0 {
		if f := ev.StackTrace.CallFrames[0]; f != nil && f.URL != "" {
			source = f.URL + ":" + strconv.FormatInt(f.LineNumber, 10) + ":" + strconv.FormatInt(f.ColumnNumber, 10)
		}
	}
	c.buf.Add(ConsoleEntry{
		Level:      normalizeConsoleLevel(ev.Type),
		Text:       strings.Join(parts, " "),
		Source:     source,
		Timestamp:  timeFromRuntimeTimestamp(ev.Timestamp),
		ReceivedAt: time.Now(),
	})
}

// Drain returns up to limit entries matching level, removing only the
// returned entries from the buffer.
func (c *Console) Drain(level string, limit int) []ConsoleEntry {
	return c.buf.Drain(levelFilter(level), limit)
}

// Peek returns up to limit entries without clearing the buffer.
func (c *Console) Peek(level string, limit int) []ConsoleEntry {
	return c.buf.Peek(levelFilter(level), limit)
}

// Clear removes all entries.
func (c *Console) Clear() {
	c.buf.Clear()
}

func levelFilter(level string) func(ConsoleEntry) bool {
	if level == "" {
		return nil
	}
	return func(e ConsoleEntry) bool {
		return e.Level == level
	}
}

// remoteObjectToString extracts a readable string from a runtime.RemoteObject.
func remoteObjectToString(obj *runtime.RemoteObject) string {
	if obj == nil {
		return ""
	}
	if len(obj.Value) > 0 {
		// Unescape JSON strings cleanly (handling \n, \t, \", \\, \uXXXX).
		// Guard on leading quote so JSON null does not unmarshal into an empty string.
		if obj.Value[0] == '"' {
			var s string
			if err := json.Unmarshal([]byte(obj.Value), &s); err == nil {
				return s
			}
		}
		return string(obj.Value)
	}
	if obj.Description != "" {
		return obj.Description
	}
	if obj.UnserializableValue != "" {
		return string(obj.UnserializableValue)
	}
	return fmt.Sprintf("[%s]", obj.Type)
}
