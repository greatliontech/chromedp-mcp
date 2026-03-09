// Package collector provides a generic ring buffer for capturing CDP events.
package collector

import "sync"

// RingBuffer is a bounded, thread-safe buffer that overwrites the oldest
// entries when full. It supports drain (read+clear) and peek (read-only)
// operations, with optional filtering.
type RingBuffer[T any] struct {
	mu      sync.Mutex
	entries []T
	maxSize int
}

// NewRingBuffer creates a ring buffer with the given maximum capacity.
func NewRingBuffer[T any](maxSize int) *RingBuffer[T] {
	return &RingBuffer[T]{
		entries: make([]T, 0, min(maxSize, 64)),
		maxSize: maxSize,
	}
}

// Add appends an entry to the buffer. If the buffer is full, the oldest
// entry is dropped.
func (rb *RingBuffer[T]) Add(entry T) {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	if len(rb.entries) >= rb.maxSize {
		// Drop oldest by shifting left. We use a slice-based approach rather
		// than a circular buffer for simplicity since the typical access
		// pattern is drain (which resets to empty).
		copy(rb.entries, rb.entries[1:])
		rb.entries[len(rb.entries)-1] = entry
	} else {
		rb.entries = append(rb.entries, entry)
	}
}

// Drain returns all entries and clears the buffer. The optional filter
// function, if non-nil, selects which entries to return (but all entries
// are removed regardless).
func (rb *RingBuffer[T]) Drain(filter func(T) bool) []T {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	if len(rb.entries) == 0 {
		return nil
	}
	var result []T
	if filter != nil {
		result = make([]T, 0, len(rb.entries))
		for _, e := range rb.entries {
			if filter(e) {
				result = append(result, e)
			}
		}
	} else {
		result = make([]T, len(rb.entries))
		copy(result, rb.entries)
	}
	rb.entries = rb.entries[:0]
	return result
}

// Peek returns entries without clearing the buffer. The optional filter
// function selects which entries to return.
func (rb *RingBuffer[T]) Peek(filter func(T) bool) []T {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	if len(rb.entries) == 0 {
		return nil
	}
	if filter != nil {
		var result []T
		for _, e := range rb.entries {
			if filter(e) {
				result = append(result, e)
			}
		}
		return result
	}
	result := make([]T, len(rb.entries))
	copy(result, rb.entries)
	return result
}

// Clear removes all entries from the buffer.
func (rb *RingBuffer[T]) Clear() {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.entries = rb.entries[:0]
}

// Len returns the current number of entries in the buffer.
func (rb *RingBuffer[T]) Len() int {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return len(rb.entries)
}
