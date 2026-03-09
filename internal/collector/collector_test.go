package collector

import (
	"sync"
	"testing"
)

func TestRingBufferOverflow(t *testing.T) {
	// Buffer of size 10, add 15 entries, verify oldest 5 are evicted.
	buf := NewRingBuffer[int](10)
	for i := 0; i < 15; i++ {
		buf.Add(i)
	}
	if buf.Len() != 10 {
		t.Fatalf("len = %d, want 10", buf.Len())
	}
	entries := buf.Drain(nil)
	if len(entries) != 10 {
		t.Fatalf("drain returned %d entries, want 10", len(entries))
	}
	// Oldest entries (0-4) should be evicted; entries should be [5..14].
	for i, v := range entries {
		expected := i + 5
		if v != expected {
			t.Errorf("entries[%d] = %d, want %d", i, v, expected)
		}
	}
}

func TestRingBufferOverflowExact(t *testing.T) {
	// Add exactly maxSize entries, all should be present.
	buf := NewRingBuffer[int](5)
	for i := 0; i < 5; i++ {
		buf.Add(i)
	}
	entries := buf.Peek(nil)
	if len(entries) != 5 {
		t.Fatalf("peek returned %d, want 5", len(entries))
	}
	for i, v := range entries {
		if v != i {
			t.Errorf("entries[%d] = %d, want %d", i, v, i)
		}
	}
}

func TestRingBufferOverflowOnePastCapacity(t *testing.T) {
	buf := NewRingBuffer[int](5)
	for i := 0; i < 6; i++ {
		buf.Add(i)
	}
	entries := buf.Peek(nil)
	if len(entries) != 5 {
		t.Fatalf("len = %d, want 5", len(entries))
	}
	// 0 evicted, should be [1,2,3,4,5].
	for i, v := range entries {
		if v != i+1 {
			t.Errorf("entries[%d] = %d, want %d", i, v, i+1)
		}
	}
}

func TestRingBufferDrainWithFilter(t *testing.T) {
	buf := NewRingBuffer[int](10)
	for i := 0; i < 10; i++ {
		buf.Add(i)
	}
	// Drain only even numbers. ALL entries should be cleared regardless.
	evens := buf.Drain(func(v int) bool { return v%2 == 0 })
	if len(evens) != 5 {
		t.Fatalf("filtered drain returned %d, want 5", len(evens))
	}
	for _, v := range evens {
		if v%2 != 0 {
			t.Errorf("filtered value %d is not even", v)
		}
	}
	// Buffer should be empty after drain (all cleared, not just matched).
	if buf.Len() != 0 {
		t.Errorf("after drain, len = %d, want 0", buf.Len())
	}
}

func TestRingBufferPeekWithFilter(t *testing.T) {
	buf := NewRingBuffer[int](10)
	for i := 0; i < 10; i++ {
		buf.Add(i)
	}
	odds := buf.Peek(func(v int) bool { return v%2 != 0 })
	if len(odds) != 5 {
		t.Fatalf("filtered peek returned %d, want 5", len(odds))
	}
	// Buffer should still have all 10 entries.
	if buf.Len() != 10 {
		t.Errorf("after peek, len = %d, want 10", buf.Len())
	}
}

func TestRingBufferClearIdempotent(t *testing.T) {
	buf := NewRingBuffer[int](5)
	buf.Add(1)
	buf.Clear()
	buf.Clear() // double clear
	if buf.Len() != 0 {
		t.Errorf("after double clear, len = %d, want 0", buf.Len())
	}
	result := buf.Drain(nil)
	if result != nil {
		t.Errorf("drain on empty = %v, want nil", result)
	}
}

func TestRingBufferEmptyDrainReturnsNil(t *testing.T) {
	buf := NewRingBuffer[int](5)
	if result := buf.Drain(nil); result != nil {
		t.Errorf("drain on new buffer = %v, want nil", result)
	}
}

func TestRingBufferEmptyPeekReturnsNil(t *testing.T) {
	buf := NewRingBuffer[int](5)
	if result := buf.Peek(nil); result != nil {
		t.Errorf("peek on new buffer = %v, want nil", result)
	}
}

func TestRingBufferLargeOverflow(t *testing.T) {
	// Simulate the real buffer sizes used in the application.
	// Console: 1000, JS errors: 500, Network: 1000.
	for _, tc := range []struct {
		name    string
		maxSize int
		total   int
	}{
		{"console_1000", 1000, 1200},
		{"errors_500", 500, 700},
		{"network_1000", 1000, 1500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf := NewRingBuffer[int](tc.maxSize)
			for i := 0; i < tc.total; i++ {
				buf.Add(i)
			}
			if buf.Len() != tc.maxSize {
				t.Fatalf("len = %d, want %d", buf.Len(), tc.maxSize)
			}
			entries := buf.Drain(nil)
			if len(entries) != tc.maxSize {
				t.Fatalf("drain returned %d, want %d", len(entries), tc.maxSize)
			}
			// First entry should be (total - maxSize).
			expectedFirst := tc.total - tc.maxSize
			if entries[0] != expectedFirst {
				t.Errorf("first entry = %d, want %d", entries[0], expectedFirst)
			}
			// Last entry should be (total - 1).
			if entries[len(entries)-1] != tc.total-1 {
				t.Errorf("last entry = %d, want %d", entries[len(entries)-1], tc.total-1)
			}
		})
	}
}

func TestRingBufferConcurrentAccess(t *testing.T) {
	buf := NewRingBuffer[int](100)
	var wg sync.WaitGroup

	// Writer goroutines.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(start int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				buf.Add(start*50 + j)
			}
		}(i)
	}

	// Reader goroutines.
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_ = buf.Peek(nil)
				_ = buf.Len()
			}
		}()
	}

	wg.Wait()

	// Buffer should still be valid and have at most 100 entries.
	if buf.Len() > 100 {
		t.Errorf("after concurrent access, len = %d, want <= 100", buf.Len())
	}
}

// ===========================================================================
// Fix: RingBuffer panic guard (collector.go)
//
// NewRingBuffer now clamps maxSize to 1 if <= 0, preventing panics from
// make([]T, 0, negative) and rb.entries[-1] on Add.
// ===========================================================================

func TestRingBufferZeroMaxSize(t *testing.T) {
	// Previously this would panic with "makeslice: cap out of range" or
	// "index out of range [-1]" on the first Add.
	buf := NewRingBuffer[int](0)
	buf.Add(42) // should not panic
	if buf.Len() != 1 {
		t.Errorf("len = %d, want 1 (maxSize clamped to 1)", buf.Len())
	}
	entries := buf.Drain(nil)
	if len(entries) != 1 || entries[0] != 42 {
		t.Errorf("drain = %v, want [42]", entries)
	}
}

func TestRingBufferNegativeMaxSize(t *testing.T) {
	// Negative maxSize should also be clamped to 1.
	buf := NewRingBuffer[int](-5)
	buf.Add(1)
	buf.Add(2) // should evict 1 since maxSize=1
	if buf.Len() != 1 {
		t.Errorf("len = %d, want 1", buf.Len())
	}
	entries := buf.Drain(nil)
	if len(entries) != 1 || entries[0] != 2 {
		t.Errorf("drain = %v, want [2]", entries)
	}
}

func TestRingBufferDrainThenAdd(t *testing.T) {
	buf := NewRingBuffer[int](5)
	buf.Add(1)
	buf.Add(2)
	buf.Drain(nil)
	buf.Add(3)
	buf.Add(4)
	entries := buf.Peek(nil)
	if len(entries) != 2 {
		t.Fatalf("after drain+add, len = %d, want 2", len(entries))
	}
	if entries[0] != 3 || entries[1] != 4 {
		t.Errorf("entries = %v, want [3, 4]", entries)
	}
}
