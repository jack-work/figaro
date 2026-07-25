package render

import (
	"fmt"
	"strings"
	"testing"
)

func proseUncached(md string, width int) []string {
	if strings.Count(md, "```")%2 == 1 {
		md += "\n```"
	}
	return SanitizeRows(renderMarkdown(md, width))
}

var proseCorpus = []string{
	"",
	"   ",
	"plain sentence",
	"# heading\n\nbody text that is long enough to wrap across the render width, several times over.",
	"- one\n- two\n- three",
	"```go\nfunc main() {}\n```",
	"```go\nfunc main() {}", // unclosed fence, synth-closed
	"| a | b |\n|---|---|\n| 1 | 2 |",
	"日本語のテキストです。これは長い行です。",
	"> quoted\n> lines",
	"text with **bold** and `code` and a [link](https://example.com)",
}

// TestProseCacheIsTransparent is the correctness claim: a cached Prose call
// returns exactly what an uncached one does, for every input and width, on
// both the cold and the warm call.
func TestProseCacheIsTransparent(t *testing.T) {
	for _, md := range proseCorpus {
		for _, w := range []int{1, 10, 40, 80, 120} {
			want := proseUncached(md, w)
			resetProseCache()
			cold := Prose(md, w)
			warm := Prose(md, w)
			for i, rows := range [][]string{cold, warm} {
				if len(rows) != len(want) {
					t.Fatalf("md=%q w=%d pass=%d rows=%d want %d", md, w, i, len(rows), len(want))
				}
				for j := range rows {
					if rows[j] != want[j] {
						t.Fatalf("md=%q w=%d pass=%d row %d = %q, want %q", md, w, i, j, rows[j], want[j])
					}
				}
			}
		}
	}
}

// TestProseCacheHandsOutPrivateSlices pins that a caller may rewrite the rows
// it gets back (renderNodeList clips in place) without poisoning the cache.
func TestProseCacheHandsOutPrivateSlices(t *testing.T) {
	resetProseCache()
	md := "some prose that renders to at least one row"
	first := Prose(md, 40)
	if len(first) == 0 {
		t.Fatal("no rows")
	}
	want := append([]string(nil), first...)
	for i := range first {
		first[i] = "CLOBBERED"
	}
	second := Prose(md, 40)
	for i := range want {
		if second[i] != want[i] {
			t.Fatalf("cache was poisoned: row %d = %q, want %q", i, second[i], want[i])
		}
	}
	third := Prose(md, 40) // and the warm path is private too
	for i := range second {
		second[i] = "CLOBBERED"
	}
	for i := range want {
		if third[i] != want[i] {
			t.Fatalf("warm hit aliased a previous result: row %d = %q, want %q", i, third[i], want[i])
		}
	}
}

// TestProseCacheRotationIsBounded pins the eviction: pushing far more than the
// budget through the cache must not grow it without limit, and the recently
// used entries must still be there.
func TestProseCacheRotationIsBounded(t *testing.T) {
	resetProseCache()
	body := strings.Repeat("filler words that render to a fat row. ", 200)
	for i := range 400 {
		Prose(fmt.Sprintf("block %d\n\n%s", i, body), 80)
		_, _, bytes := proseCacheStats()
		if bytes > 2*proseCacheBudget {
			t.Fatalf("hot generation grew to %d bytes, budget %d", bytes, proseCacheBudget)
		}
	}
	hot, cold, _ := proseCacheStats()
	if hot == 0 || cold == 0 {
		t.Fatalf("expected both generations populated after rotation, got hot=%d cold=%d", hot, cold)
	}
	resetProseCache()
}

func BenchmarkProseCached(b *testing.B) {
	resetProseCache()
	md := "# heading\n\n" + strings.Repeat("The quick brown fox jumps over the lazy dog. ", 20)
	Prose(md, 80)
	b.ReportAllocs()
	for range b.N {
		_ = Prose(md, 80)
	}
}

func BenchmarkProseUncached(b *testing.B) {
	md := "# heading\n\n" + strings.Repeat("The quick brown fox jumps over the lazy dog. ", 20)
	b.ReportAllocs()
	for range b.N {
		_ = proseUncached(md, 80)
	}
}
