package render

import (
	"strings"
	"sync"
	"testing"
)

// Renders at one width share a glamour renderer that accumulates table
// state; rendering outside the lock interleaves cells and panics in setStyles.
func TestConcurrentProseAtSameWidthIsSafe(t *testing.T) {
	table := "| a | b | c |\n|---|---|---|\n| 1 | 2 | 3 |\n| 4 | 5 | 6 |\n"
	var wg sync.WaitGroup
	panics := make(chan any, 64)
	for i := range 32 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					panics <- r
				}
			}()
			// Distinct text so the prose cache cannot serve it.
			Prose(strings.Repeat(" ", i%3)+table, 100)
		}(i)
	}
	wg.Wait()
	close(panics)
	for p := range panics {
		t.Fatalf("concurrent Prose panicked: %v", p)
	}
}
