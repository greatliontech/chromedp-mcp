package collector

import (
	"fmt"
	"strings"
	"time"

	"github.com/chromedp/cdproto/runtime"
)

// ConsoleEntry represents a captured console message.
type ConsoleEntry struct {
	Level     string    `json:"level"`
	Text      string    `json:"text"`
	Timestamp time.Time `json:"timestamp"`
	// ReceivedAt is the wall-clock time the event was processed by the
	// Go-side collector. Used by observe_activity for time-window
	// filtering — Timestamp is converted from CDP's MonotonicTime which
	// drifts vs. time.Now(). JSON-hidden.
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

// Handle processes a runtime.EventConsoleAPICalled event.
func (c *Console) Handle(ev *runtime.EventConsoleAPICalled) {
	var parts []string
	for _, arg := range ev.Args {
		parts = append(parts, remoteObjectToString(arg))
	}
	c.buf.Add(ConsoleEntry{
		Level:      string(ev.Type),
		Text:       strings.Join(parts, " "),
		Timestamp:  ev.Timestamp.Time(),
		ReceivedAt: time.Now(),
	})
}

// Drain returns up to limit entries matching level, removing only the
// returned entries from the buffer.
func (c *Console) Drain(level string, limit int) []ConsoleEntry {
	return c.buf.Drain(levelFilter(level), limit)
}

// Peek returns entries without clearing the buffer.
func (c *Console) Peek(level string, limit int) []ConsoleEntry {
	entries := c.buf.Peek(levelFilter(level))
	return applyLimit(entries, limit)
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

func applyLimit[T any](entries []T, limit int) []T {
	if limit > 0 && len(entries) > limit {
		return entries[:limit]
	}
	return entries
}

// remoteObjectToString extracts a string representation from a runtime.RemoteObject.
func remoteObjectToString(obj *runtime.RemoteObject) string {
	if obj.Value != nil {
		// Value is a json.RawMessage; for simple types it's already a usable string.
		s := string(obj.Value)
		// Strip surrounding quotes if it's a JSON string.
		if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
			s = s[1 : len(s)-1]
		}
		return s
	}
	if obj.Description != "" {
		return obj.Description
	}
	if obj.UnserializableValue != "" {
		return string(obj.UnserializableValue)
	}
	return fmt.Sprintf("[%s]", obj.Type)
}
