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
// If maxSize is less than 1, it is clamped to 1.
func NewRingBuffer[T any](maxSize int) *RingBuffer[T] {
	if maxSize < 1 {
		maxSize = 1
	}
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

// Drain returns up to limit entries matching the filter and removes only
// the returned entries from the buffer. Entries that don't match the
// filter are retained. Entries that match the filter beyond the limit
// are also retained, so successive Drain calls paginate. limit <= 0
// means no limit.
//
// When nothing matches (filter rejects all entries), the underlying
// buffer is left intact and no allocation occurs, so a "drain matching X"
// call against a buffer of all-non-X is a single O(N) scan.
func (rb *RingBuffer[T]) Drain(filter func(T) bool, limit int) []T {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	if len(rb.entries) == 0 {
		return nil
	}
	// First pass: count matches subject to limit. Lets us allocate the
	// taken/keep slices with exactly the right capacity and fast-path
	// when there are zero matches.
	var matches int
	for _, e := range rb.entries {
		if (filter == nil || filter(e)) && (limit <= 0 || matches < limit) {
			matches++
		}
	}
	if matches == 0 {
		return nil
	}
	taken := make([]T, 0, matches)
	keep := make([]T, 0, len(rb.entries)-matches)
	for _, e := range rb.entries {
		if (filter == nil || filter(e)) && len(taken) < matches {
			taken = append(taken, e)
		} else {
			keep = append(keep, e)
		}
	}
	rb.entries = keep
	return taken
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
