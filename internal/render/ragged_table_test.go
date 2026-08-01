package render

import (
	"strings"
	"sync"
	"testing"
)

// Two goroutines rendering markdown at the SAME width share one glamour
// TermRenderer, because rendererFor caches by width and releases its mutex
// before the caller renders. glamour accumulates table state on that renderer
// (ctx.table.row / ctx.table.header, reset in Finish), so concurrent renders
// interleave cells into one row. A row then has more cells than the table's
// Alignments, and glamour's StyleFunc indexes Alignments by column:
//
//	panic: index out of range [3] with length 3
//	  glamour/ansi.(*TableElement).setStyles.func2
//	  lipgloss/table.(*Table).resize
//
// The panic lands in whichever goroutine is rendering, which for figaro is a
// detached one (refreshQueued), so it takes the whole CLI down rather than
// spoiling one frame.
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
