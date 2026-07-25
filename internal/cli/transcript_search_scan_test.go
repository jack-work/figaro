package cli

import (
	"strings"
	"testing"
)

// searchContainsReference is the pre-optimization implementation: it built the
// ANSI-stripped row and ran strings.Contains over it.
func searchContainsReference(row, q string) bool {
	if !strings.ContainsRune(row, '\x1b') {
		return strings.Contains(row, q)
	}
	var visible strings.Builder
	visible.Grow(len(row))
	for i := 0; i < len(row); {
		if row[i] != '\x1b' {
			visible.WriteByte(row[i])
			i++
			continue
		}
		if i+1 >= len(row) {
			break
		}
		if row[i+1] == '[' {
			i += 2
			for i < len(row) {
				final := row[i]
				i++
				if final >= 0x40 && final <= 0x7e {
					break
				}
			}
			continue
		}
		i += 2
	}
	return strings.Contains(visible.String(), q)
}

var searchRows = append([]string{
	"",
	"plain row",
	"\x1b[2mdim row\x1b[0m",
	"\x1b[2m  │ \x1b[0m   12  internal/cli/transcript.go:7: captured output",
	"tr\x1b[0manscript",            // match split by an escape
	"\x1b[7m",                      // escape only
	"\x1b[7",                       // truncated CSI
	"\x1b",                         // lone ESC
	"a\x1bZb",                      // two-byte escape
	"7m and \x1b[7m",               // the query may look like escape bytes
	"\x1b[38;5;238m日本語\x1b[0mtail", // wide runes around escapes
	"aaa\x1b[0maaab",
}, clipCorpus...)

var searchQueries = []string{
	"", "a", "b", "aaab", "transcript", "tr", "7m", "[7m", "\x1b", "0m",
	"日本語", "captured output", "internal/cli", "xyz", " ", "  │ ",
}

func TestSearchContainsMatchesReference(t *testing.T) {
	for _, row := range searchRows {
		for _, q := range searchQueries {
			if got, want := searchContains(row, q), searchContainsReference(row, q); got != want {
				t.Errorf("searchContains(%q, %q) = %v, want %v", row, q, got, want)
			}
		}
	}
}

// TestVisibleIndexAgreesWithHighlight pins the prefilter against the
// highlighter it guards: if the scan says "no match", the slow path must
// agree that the row is unchanged, and vice versa.
func TestVisibleIndexAgreesWithHighlight(t *testing.T) {
	for _, row := range searchRows {
		for _, q := range searchQueries {
			if q == "" || row == "" || !strings.ContainsRune(row, '\x1b') {
				continue
			}
			changed := highlightMatchesReference(row, q) != row
			if found := visibleIndex(row, q) >= 0; found != changed {
				t.Errorf("visibleIndex(%q, %q) >= 0 = %v, but highlight changed = %v",
					row, q, found, changed)
			}
		}
	}
}

func TestHighlightMatchesMatchesReference(t *testing.T) {
	for _, row := range searchRows {
		for _, q := range searchQueries {
			if got, want := highlightMatches(row, q), highlightMatchesReference(row, q); got != want {
				t.Errorf("highlightMatches(%q, %q) = %q, want %q", row, q, got, want)
			}
		}
	}
}

// highlightMatchesReference is the pre-optimization implementation (no
// allocation-free prefilter).
func highlightMatchesReference(row, q string) string {
	if q == "" || row == "" {
		return row
	}
	const hlOn, hlOff = "\x1b[7m", "\x1b[27m"
	if !strings.ContainsRune(row, '\x1b') {
		if !strings.Contains(row, q) {
			return row
		}
		return strings.ReplaceAll(row, q, hlOn+q+hlOff)
	}
	var visBuf strings.Builder
	visBuf.Grow(len(row))
	for i := 0; i < len(row); {
		if row[i] == '\x1b' {
			i = skipANSI(row, i)
			continue
		}
		visBuf.WriteByte(row[i])
		i++
	}
	visible := visBuf.String()
	if !strings.Contains(visible, q) {
		return row
	}
	next := strings.Index(visible, q)
	matchEnd := -1
	vi := 0
	var b strings.Builder
	b.Grow(len(row) + 16)
	for i := 0; i < len(row); {
		if row[i] == '\x1b' {
			j := skipANSI(row, i)
			b.WriteString(row[i:j])
			i = j
			continue
		}
		if next >= 0 && vi == next {
			b.WriteString(hlOn)
			matchEnd = vi + len(q)
			after := matchEnd
			if rel := strings.Index(visible[after:], q); rel >= 0 {
				next = after + rel
			} else {
				next = -1
			}
		}
		b.WriteByte(row[i])
		i++
		vi++
		if vi == matchEnd {
			b.WriteString(hlOff)
			matchEnd = -1
		}
	}
	return b.String()
}

// TestSearchContainsNoAllocOnMiss is the point: with a search active every
// retained row is scanned every frame, and misses must be free.
func TestSearchContainsNoAllocOnMiss(t *testing.T) {
	row := "\x1b[2m  │ \x1b[0m   12  internal/cli/transcript.go:7: captured output"
	if got := testing.AllocsPerRun(100, func() { _ = searchContains(row, "nomatchhere") }); got != 0 {
		t.Errorf("searchContains miss allocated %v times, want 0", got)
	}
	if got := testing.AllocsPerRun(100, func() { _ = highlightMatches(row, "nomatchhere") }); got != 0 {
		t.Errorf("highlightMatches miss allocated %v times, want 0", got)
	}
}

func FuzzVisibleSearch(f *testing.F) {
	for _, row := range searchRows {
		for _, q := range searchQueries {
			f.Add(row, q)
		}
	}
	f.Fuzz(func(t *testing.T, row, q string) {
		if got, want := searchContains(row, q), searchContainsReference(row, q); got != want {
			t.Fatalf("searchContains(%q, %q) = %v, want %v", row, q, got, want)
		}
		if got, want := highlightMatches(row, q), highlightMatchesReference(row, q); got != want {
			t.Fatalf("highlightMatches(%q, %q) = %q, want %q", row, q, got, want)
		}
	})
}
