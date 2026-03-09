package id

import (
	"regexp"
	"sync"
	"testing"
)

func TestGenerateFormat(t *testing.T) {
	id := Generate()
	if len(id) != 8 {
		t.Errorf("Generate() = %q, want 8 hex characters (len=%d)", id, len(id))
	}
	matched, err := regexp.MatchString(`^[0-9a-f]{8}$`, id)
	if err != nil {
		t.Fatalf("regexp error: %v", err)
	}
	if !matched {
		t.Errorf("Generate() = %q, want lowercase hex characters only", id)
	}
}

func TestGenerateUniqueness(t *testing.T) {
	const n = 1000
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		id := Generate()
		if _, dup := seen[id]; dup {
			t.Fatalf("Generate() produced duplicate ID %q after %d calls", id, i)
		}
		seen[id] = struct{}{}
	}
}

func TestGenerateConcurrent(t *testing.T) {
	const goroutines = 10
	const perGoroutine = 100

	var mu sync.Mutex
	seen := make(map[string]struct{}, goroutines*perGoroutine)
	var wg sync.WaitGroup

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := make([]string, 0, perGoroutine)
			for i := 0; i < perGoroutine; i++ {
				local = append(local, Generate())
			}
			mu.Lock()
			defer mu.Unlock()
			for _, id := range local {
				if _, dup := seen[id]; dup {
					t.Errorf("duplicate ID %q from concurrent Generate()", id)
				}
				seen[id] = struct{}{}
			}
		}()
	}
	wg.Wait()
}
