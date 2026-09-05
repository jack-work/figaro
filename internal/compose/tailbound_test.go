package compose

import (
	"fmt"
	"strings"
	"testing"
)

// tailBoundSplit is the implementation tailBound replaced: split the whole
// text, keep the last composeBashCap lines, join them back. It is kept HERE,
// permanently, as the oracle. The replacement is a byte-for-byte equivalence,
// not an improvement in behaviour, and an equivalence claim needs both sides
// present to stay checkable. A composed node is cached per LT and goes on the
// wire; a rendering, once cached, is permanent, so "correct-looking" is not
// the standard.
func tailBoundSplit(text string) string {
	if text == "" {
		return ""
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > composeBashCap {
		lines = lines[len(lines)-composeBashCap:]
	}
	return strings.Join(lines, "\n")
}

// tailBoundCorpus is built around the ways backward line-scanning goes wrong.
func tailBoundCorpus() map[string]string {
	n := func(count int, suffix string) string {
		var b strings.Builder
		for i := 0; i < count; i++ {
			fmt.Fprintf(&b, "line %d\n", i)
		}
		return strings.TrimSuffix(b.String(), "\n") + suffix
	}
	return map[string]string{
		"empty":                   "",
		"single line no newline":  "just one line",
		"single line trailing nl": "just one line\n",
		"only a newline":          "\n",
		"only newlines":           "\n\n\n\n",
		"199 lines":               n(199, ""),
		"200 lines":               n(200, ""),
		"201 lines":               n(201, ""),
		"200 lines trailing nl":   n(200, "\n"),
		"201 lines trailing nl":   n(201, "\n"),
		"201 lines trailing nls":  n(201, "\n\n\n"),
		"400 lines":               n(400, ""),
		"blank interior lines":    strings.Repeat("a\n\n", 300),
		"trailing blank run":      n(250, "") + "\n\n\n\n\n",
		"crlf":                    strings.Repeat("line\r\n", 300),
		"crlf no final":           strings.TrimSuffix(strings.Repeat("line\r\n", 300), "\r\n"),
		// The 200th-from-last newline is the first byte: the kept region
		// starts at index 1 and there is nothing above it.
		"cut at first byte": "\n" + strings.TrimSuffix(strings.Repeat("x\n", 200), "\n"),
		// One line above the cut, so the kept region must not include it.
		"one line above the cut": "keepmeout\n" + strings.TrimSuffix(strings.Repeat("x\n", 200), "\n"),
		"no newlines at all":     strings.Repeat("x", 10_000),
		"unicode":                strings.Repeat("héllo wörld ✂\n", 300),
	}
}

func TestTailBoundIsByteIdenticalToTheSplitImplementation(t *testing.T) {
	for name, in := range tailBoundCorpus() {
		t.Run(name, func(t *testing.T) {
			want, got := tailBoundSplit(in), tailBound(in)
			if want != got {
				t.Fatalf("clamp diverged from the oracle\n input %d bytes\n  want %d bytes: %q\n  got  %d bytes: %q",
					len(in), len(want), truncForMsg(want), len(got), truncForMsg(got))
			}
		})
	}
}

// TestTailBoundOracleCanFail proves the equivalence test above can fail:
// a clamp that is off by one line must be caught by this corpus. If this stops
// failing the oracle, the corpus has stopped covering the boundary.
func TestTailBoundOracleCanFail(t *testing.T) {
	offByOne := func(text string) string {
		if text == "" {
			return ""
		}
		lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
		if len(lines) > composeBashCap {
			lines = lines[len(lines)-composeBashCap+1:]
		}
		return strings.Join(lines, "\n")
	}
	var caught int
	for _, in := range tailBoundCorpus() {
		if tailBoundSplit(in) != offByOne(in) {
			caught++
		}
	}
	if caught == 0 {
		t.Fatal("no corpus input distinguishes a one-line-short clamp: the equivalence test cannot fail")
	}
}

// TestTailBoundDoesNotReadWhatItDiscards states the property the fix exists
// for: the clamp's cost is the size of what it KEEPS, not the size of what the
// tool wrote. Asserted as allocations, which is the fact; the old
// implementation allocated one []string of every line in the input.
func TestTailBoundDoesNotReadWhatItDiscards(t *testing.T) {
	big := strings.Repeat("a line of tool output\n", 100_000)
	if got := testing.AllocsPerRun(50, func() { _ = tailBound(big) }); got != 0 {
		t.Fatalf("tailBound allocated %.1f times per call on a 100k-line input, want 0: the clamp is still materialising what it discards", got)
	}
}

func truncForMsg(s string) string {
	if len(s) <= 120 {
		return s
	}
	return s[:60] + "..." + s[len(s)-60:]
}

func BenchmarkTailBound(b *testing.B) {
	for _, lines := range []int{200, 2_000, 20_000} {
		in := strings.Repeat(strings.Repeat("x", 79)+"\n", lines)
		b.Run(fmt.Sprintf("lines=%d/old", lines), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = tailBoundSplit(in)
			}
		})
		b.Run(fmt.Sprintf("lines=%d/new", lines), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = tailBound(in)
			}
		})
	}
}
