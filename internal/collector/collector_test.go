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
	entries := buf.Drain(nil, 0)
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
	entries := buf.Peek(nil, 0)
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
	entries := buf.Peek(nil, 0)
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
	// Drain only even numbers. Non-matching (odd) entries must be retained.
	evens := buf.Drain(func(v int) bool { return v%2 == 0 }, 0)
	if len(evens) != 5 {
		t.Fatalf("filtered drain returned %d, want 5", len(evens))
	}
	for _, v := range evens {
		if v%2 != 0 {
			t.Errorf("filtered value %d is not even", v)
		}
	}
	// Buffer should still hold the 5 odd entries.
	if buf.Len() != 5 {
		t.Errorf("after selective drain, len = %d, want 5", buf.Len())
	}
	remaining := buf.Peek(nil, 0)
	for _, v := range remaining {
		if v%2 == 0 {
			t.Errorf("retained value %d is even, expected only odds left", v)
		}
	}
}

// TestRingBufferDrainWithLimit verifies the limit parameter caps the
// returned slice and leaves remaining matches in the buffer.
func TestRingBufferDrainWithLimit(t *testing.T) {
	buf := NewRingBuffer[int](10)
	for i := 0; i < 10; i++ {
		buf.Add(i)
	}
	first := buf.Drain(nil, 3)
	if len(first) != 3 {
		t.Fatalf("first drain returned %d, want 3", len(first))
	}
	if first[0] != 0 || first[1] != 1 || first[2] != 2 {
		t.Errorf("first drain = %v, want [0,1,2]", first)
	}
	// 7 entries should remain.
	if buf.Len() != 7 {
		t.Errorf("after partial drain, len = %d, want 7", buf.Len())
	}
	// Subsequent drain returns the next 3.
	second := buf.Drain(nil, 3)
	if len(second) != 3 || second[0] != 3 {
		t.Errorf("second drain = %v, want [3,4,5]", second)
	}
}

// TestRingBufferDrainFilterAndLimit combines filter and limit.
func TestRingBufferDrainFilterAndLimit(t *testing.T) {
	buf := NewRingBuffer[int](10)
	for i := 0; i < 10; i++ {
		buf.Add(i)
	}
	// First 2 evens.
	got := buf.Drain(func(v int) bool { return v%2 == 0 }, 2)
	if len(got) != 2 || got[0] != 0 || got[1] != 2 {
		t.Errorf("filtered+limit drain = %v, want [0,2]", got)
	}
	// 8 entries remain (3 evens + 5 odds).
	if buf.Len() != 8 {
		t.Errorf("after filtered+limit drain, len = %d, want 8", buf.Len())
	}
}

func TestRingBufferPeekWithFilter(t *testing.T) {
	buf := NewRingBuffer[int](10)
	for i := 0; i < 10; i++ {
		buf.Add(i)
	}
	odds := buf.Peek(func(v int) bool { return v%2 != 0 }, 0)
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
	result := buf.Drain(nil, 0)
	if result != nil {
		t.Errorf("drain on empty = %v, want nil", result)
	}
}

func TestRingBufferEmptyDrainReturnsNil(t *testing.T) {
	buf := NewRingBuffer[int](5)
	if result := buf.Drain(nil, 0); result != nil {
		t.Errorf("drain on new buffer = %v, want nil", result)
	}
}

func TestRingBufferEmptyPeekReturnsNil(t *testing.T) {
	buf := NewRingBuffer[int](5)
	if result := buf.Peek(nil, 0); result != nil {
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
			entries := buf.Drain(nil, 0)
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
				_ = buf.Peek(nil, 0)
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
	entries := buf.Drain(nil, 0)
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
	entries := buf.Drain(nil, 0)
	if len(entries) != 1 || entries[0] != 2 {
		t.Errorf("drain = %v, want [2]", entries)
	}
}

func TestRingBufferDrainThenAdd(t *testing.T) {
	buf := NewRingBuffer[int](5)
	buf.Add(1)
	buf.Add(2)
	buf.Drain(nil, 0)
	buf.Add(3)
	buf.Add(4)
	entries := buf.Peek(nil, 0)
	if len(entries) != 2 {
		t.Fatalf("after drain+add, len = %d, want 2", len(entries))
	}
	if entries[0] != 3 || entries[1] != 4 {
		t.Errorf("entries = %v, want [3, 4]", entries)
	}
}

func TestRingBufferPeekLimitAndClearZeroing(t *testing.T) {
	buf := NewRingBuffer[string](5)
	for _, s := range []string{"a1", "b1", "a2", "a3", "b2"} {
		buf.Add(s)
	}

	if got := buf.Peek(nil, 2); len(got) != 2 || got[0] != "a1" || got[1] != "b1" {
		t.Errorf("Peek(nil, 2) = %v, want [a1 b1]", got)
	}
	isA := func(s string) bool { return len(s) > 0 && s[0] == 'a' }
	if got := buf.Peek(isA, 2); len(got) != 2 || got[0] != "a1" || got[1] != "a2" {
		t.Errorf("Peek(isA, 2) = %v, want [a1 a2]", got)
	}
	if buf.Len() != 5 {
		t.Errorf("Peek mutated buffer len = %d, want 5", buf.Len())
	}

	// Clear must zero backing array slots so referenced values can be GC'd.
	buf.Clear()
	backing := buf.entries[:cap(buf.entries)]
	for i, s := range backing {
		if s != "" {
			t.Errorf("backing[%d] = %q after Clear, want empty zero value", i, s)
		}
	}
}

func TestTruncateUTF8AndBytes(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		maxBytes int
		want     string
		wantCut  bool
	}{
		{"fits_exact", "abc", 3, "abc", false},
		{"fits_under", "abc", 10, "abc", false},
		{"zero_max_nonempty", "abc", 0, "", true},
		{"zero_max_empty", "", 0, "", false},
		{"negative_max", "abc", -1, "", true},
		{"ascii_cut", "abcdef", 4, "abcd", true},
		// 2-byte UTF-8: 'é' = 0xC3 0xA9
		{"2byte_mid", "aé", 2, "a", true},
		{"2byte_exact", "aé", 3, "aé", false},
		// 3-byte UTF-8: '€' = 0xE2 0x82 0xAC
		{"3byte_cut_1", "€x", 1, "", true},
		{"3byte_cut_2", "€x", 2, "", true},
		{"3byte_exact", "€x", 3, "€", true},
		// 4-byte UTF-8: '😀' = 0xF0 0x9F 0x98 0x80
		{"4byte_cut_1", "😀z", 1, "", true},
		{"4byte_cut_2", "😀z", 2, "", true},
		{"4byte_cut_3", "😀z", 3, "", true},
		{"4byte_exact", "😀z", 4, "😀", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotS, cutS := TruncateUTF8(tc.in, tc.maxBytes)
			if gotS != tc.want || cutS != tc.wantCut {
				t.Errorf("TruncateUTF8(%q, %d) = (%q, %v), want (%q, %v)", tc.in, tc.maxBytes, gotS, cutS, tc.want, tc.wantCut)
			}
			gotB, cutB := TruncateUTF8Bytes([]byte(tc.in), tc.maxBytes)
			if string(gotB) != tc.want || cutB != tc.wantCut {
				t.Errorf("TruncateUTF8Bytes(%q, %d) = (%q, %v), want (%q, %v)", tc.in, tc.maxBytes, gotB, cutB, tc.want, tc.wantCut)
			}
		})
	}
}

func TestTrimUTF8Slice(t *testing.T) {
	raw := []byte("ééé") // each 'é' is 2 bytes: C3 A9
	if got := string(TrimUTF8Slice(raw[1 : len(raw)-1])); got != "é" {
		t.Errorf("TrimUTF8Slice(mid) = %q, want %q", got, "é")
	}
	if got := string(TrimUTF8Slice(raw)); got != "ééé" {
		t.Errorf("TrimUTF8Slice(full) = %q, want %q", got, "ééé")
	}
	// Continuation bytes only -> empty slice.
	emoji := []byte("😀")
	if got := TrimUTF8Slice(emoji[1:]); len(got) != 0 {
		t.Errorf("TrimUTF8Slice(continuations only) = %x, want empty", got)
	}
	if got := TrimUTF8Slice(nil); len(got) != 0 {
		t.Errorf("TrimUTF8Slice(nil) = %x, want empty", got)
	}
}
