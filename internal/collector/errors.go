package collector

import (
	"strconv"
	"time"

	"github.com/chromedp/cdproto/runtime"
)

// JSErrorEntry represents a captured JavaScript exception.
type JSErrorEntry struct {
	Message    string    `json:"message"`
	Source     string    `json:"source,omitempty"`
	Line       int64     `json:"line,omitempty"`
	Column     int64     `json:"column,omitempty"`
	StackTrace string    `json:"stack_trace,omitempty"`
	Timestamp  time.Time `json:"timestamp"`
}

// JSErrors collects uncaught JavaScript exceptions and promise rejections.
type JSErrors struct {
	buf *RingBuffer[JSErrorEntry]
}

// NewJSErrors creates a JS error collector with the given buffer size.
func NewJSErrors(maxSize int) *JSErrors {
	return &JSErrors{buf: NewRingBuffer[JSErrorEntry](maxSize)}
}

// Handle processes a runtime.EventExceptionThrown event.
func (je *JSErrors) Handle(ev *runtime.EventExceptionThrown) {
	details := ev.ExceptionDetails
	entry := JSErrorEntry{
		Message:   details.Text,
		Timestamp: ev.Timestamp.Time(),
	}
	if details.Exception != nil && details.Exception.Description != "" {
		entry.Message = details.Exception.Description
	}
	if details.URL != "" {
		entry.Source = details.URL
	}
	entry.Line = details.LineNumber
	entry.Column = details.ColumnNumber
	if details.StackTrace != nil {
		entry.StackTrace = formatStackTrace(details.StackTrace)
	}
	je.buf.Add(entry)
}

// Drain returns all entries and clears the buffer.
func (je *JSErrors) Drain(limit int) []JSErrorEntry {
	entries := je.buf.Drain(nil)
	return applyLimit(entries, limit)
}

// Peek returns entries without clearing the buffer.
func (je *JSErrors) Peek(limit int) []JSErrorEntry {
	entries := je.buf.Peek(nil)
	return applyLimit(entries, limit)
}

// Clear removes all entries.
func (je *JSErrors) Clear() {
	je.buf.Clear()
}

// formatStackTrace formats a runtime.StackTrace into a readable string.
func formatStackTrace(st *runtime.StackTrace) string {
	if st == nil || len(st.CallFrames) == 0 {
		return ""
	}
	var result string
	for _, frame := range st.CallFrames {
		name := frame.FunctionName
		if name == "" {
			name = "(anonymous)"
		}
		result += name + " (" + frame.URL + ":" +
			strconv.FormatInt(frame.LineNumber, 10) + ":" +
			strconv.FormatInt(frame.ColumnNumber, 10) + ")\n"
	}
	return result
}
