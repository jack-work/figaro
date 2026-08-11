package cli

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/render"
)

// TestCollapseSGRCases pins the normalizer's byte output for the shapes the
// row cache actually contains, plus the ones it must refuse to touch.
func TestCollapseSGRCases(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"no escapes", "plain text", "plain text"},
		{"empty", "", ""},
		{"single reset stays: the row asserts its own state", "\x1b[0m", "\x1b[0m"},
		{"double reset", "\x1b[0m\x1b[0m", "\x1b[0m"},
		{"reset spelled empty", "\x1b[0m\x1b[m", "\x1b[m"},
		{"dead leading pair", "\x1b[38;5;252m\x1b[0mfoo", "\x1b[0mfoo"},
		// Without a preceding reset the entry attributes are unknown, so the
		// inner reset genuinely does something (it would clear an inherited
		// bold) and has to stay. Once the row has asserted a full state, the
		// per-cell churn folds away: which is the shape glamour actually
		// emits, and where the 5x comes from.
		{"per-cell padding, entry state unknown", "\x1b[38;5;252ma\x1b[0m\x1b[38;5;252mb\x1b[0m",
			"\x1b[38;5;252ma\x1b[0m\x1b[38;5;252mb\x1b[0m"},
		{"per-cell padding after a reset", "\x1b[0m\x1b[38;5;252ma\x1b[0m\x1b[38;5;252mb\x1b[0m",
			"\x1b[0m\x1b[38;5;252mab\x1b[0m"},
		{"glamour leading churn", " \x1b[38;5;252m\x1b[0m\x1b[38;5;252m\x1b[0m  \x1b[38;5;252mx\x1b[0m",
			" \x1b[0m  \x1b[38;5;252mx\x1b[0m"},
		{"repeated identical set", "\x1b[1;31;44mx\x1b[1;31;44my", "\x1b[1;31;44mxy"},
		{"set already in effect", "\x1b[1mx\x1b[1my", "\x1b[1mxy"},
		{"256 colour repeat", "\x1b[38;5;252mx\x1b[38;5;252my", "\x1b[38;5;252mxy"},
		{"truecolour repeat", "\x1b[38;2;10;20;30mx\x1b[38;2;10;20;30my", "\x1b[38;2;10;20;30mxy"},
		{"truecolour change kept", "\x1b[38;2;10;20;30mx\x1b[38;2;10;20;31my", "\x1b[38;2;10;20;30mx\x1b[38;2;10;20;31my"},
		{"attribute cleared then reset", "\x1b[1mx\x1b[22m\x1b[0m", "\x1b[1mx\x1b[0m"},
		{"trailing reset from default is dead", "\x1b[0mx\x1b[0m", "\x1b[0mx"},
		{"trailing reset from a colour is load-bearing", "\x1b[31mx\x1b[0m", "\x1b[31mx\x1b[0m"},
		{"run collapses to its effective state", "\x1b[31m\x1b[1m\x1b[0m\x1b[32mx", "\x1b[0m\x1b[32mx"},
		{"erase is a barrier, not reorderable", "\x1b[0ma\x1b[Kb", "\x1b[0ma\x1b[Kb"},
		{"state before an erase is realized", "\x1b[41m\x1b[Kx", "\x1b[41m\x1b[Kx"},
		{"cursor motion passes through", "\x1b[0ma\x1b[3Cb\x1b[0m", "\x1b[0ma\x1b[3Cb"},
		{"non-SGR escape makes the state unknown again", "\x1b[0ma\x1b[?25hb\x1b[0m", "\x1b[0ma\x1b[?25hb\x1b[0m"},
		{"unmodeled parameter is never dropped", "\x1b[53mx\x1b[53my", "\x1b[53mx\x1b[53my"},
		{"colon sub-parameters pass through", "\x1b[4:3mx\x1b[4:3my", "\x1b[4:3mx\x1b[4:3my"},
		{"unterminated escape passes through", "x\x1b[38;5;", "x\x1b[38;5;"},
		{"bare ESC passes through", "x\x1b", "x\x1b"},
		// Dropping the reset here would splice \xc4 and \x8f into "ď".
		{"invalid UTF-8 is left alone", "\x1b[m\xc4\x1b[m\x8f", "\x1b[m\xc4\x1b[m\x8f"},
	}
	for _, c := range cases {
		got := collapseSGR(c.in)
		if got != c.want {
			t.Errorf("%s: collapseSGR(%q)\n got %q\nwant %q", c.name, c.in, got, c.want)
		}
		if d := sgrCellDiff(c.in, got); d != "" {
			t.Errorf("%s: not cell-identical: %s", c.name, d)
		}
	}
}

// TestCollapseSGRIsAllocationFreeWhenClean guards the fast path: a row with
// nothing to drop must come back as the same string, untouched.
func TestCollapseSGRIsAllocationFreeWhenClean(t *testing.T) {
	for _, s := range []string{"plain row", "\x1b[31mx\x1b[0m", "\x1b[2m  │ \x1b[0malpha"} {
		got := collapseSGR(s)
		if got != s {
			t.Fatalf("collapseSGR(%q) = %q, want it unchanged", s, got)
		}
		if allocs := testingAllocs(func() { collapseSGR(s) }); allocs != 0 {
			t.Errorf("collapseSGR(%q) allocated %v times on the clean path", s, allocs)
		}
	}
}

func testingAllocs(f func()) float64 {
	return testing.AllocsPerRun(50, f)
}

// sgrEntryStates are the rendition states a row might be entered in. collapseSGR
// treats the entry state as unknown, so equivalence has to hold under all of
// them: including a terminal left tinted by a previous row.
var sgrEntryStates = []string{"", "\x1b[0m", "\x1b[31m", "\x1b[1;4;38;5;99;48;5;238m", "\x1b[7m"}

func assertSGREquivalent(t *testing.T, in string) {
	t.Helper()
	out := collapseSGR(in)
	for _, prefix := range sgrEntryStates {
		if d := sgrCellDiff(prefix+in, prefix+out); d != "" {
			t.Fatalf("collapse changed the render (entry %q): %s\n in: %q\nout: %q", prefix, d, in, out)
		}
	}
	if len(out) > len(in) {
		t.Fatalf("collapse grew the row: %d -> %d bytes\n in: %q\nout: %q", len(in), len(out), in, out)
	}
}

// TestCollapseSGROnRealGlamourRows is the load-bearing equivalence test: real
// rendered prose, thinking blockquotes and tool output, through the same
// pipeline the row cache uses, replayed on the terminal model.
func TestCollapseSGROnRealGlamourRows(t *testing.T) {
	rows := sgrCorpusRows(t)
	if len(rows) < 20 {
		t.Fatalf("corpus is too small to prove anything: %d rows", len(rows))
	}
	var before, after int
	for _, row := range rows {
		assertSGREquivalent(t, row)
		before += len(row)
		after += len(collapseSGR(row))
	}
	t.Logf("%d rows: %d B -> %d B (%.2fx smaller)", len(rows), before, after, float64(before)/float64(after))
}

// sgrCorpusRows renders a spread of node content the way the transcript does,
// yielding rows that carry glamour's real per-cell styling.
func sgrCorpusRows(t testing.TB) []string {
	t.Helper()
	markdown := []string{
		"The quick brown fox jumps over the lazy dog, repeatedly and at length, past the right margin.\n\n日本語のテキストもここにあります。",
		"# A heading\n\nSome prose with `inline code`, **bold**, *italic* and a [link](https://example.com).\n\n- bullet one\n- bullet two\n\n```go\nfunc main() { fmt.Println(\"hi\") }\n```\n",
		"> quoted thinking\n> across two lines",
		"| a | b |\n|---|---|\n| 1 | 2 |\n",
	}
	var rows []string
	for _, md := range markdown {
		for _, w := range []int{40, 98} {
			for _, l := range render.Prose(md, w) {
				rows = append(rows, plainNodeRow(l, w+2))
			}
		}
	}
	rows = append(rows, sgrFixtureRows(t)...)
	return rows
}

// sgrFixtureRows are the golden-frame fixture's materialized rows, i.e. exactly
// what the transcript paints (headers, rules, tool gutters, selection wash).
func sgrFixtureRows(t testing.TB) []string {
	if tt, ok := t.(*testing.T); ok { //nolint:staticcheck // the fixture needs a *testing.T
		tr := frameFixture(tt)
		var rows []string
		rows = append(rows, tr.lines()...)
		tr.selectNode(1, false)
		tr.selectNode(1, true)
		rows = append(rows, tr.lines()...)
		tr.clearSelection()
		tr.matchQuery = "fox"
		rows = append(rows, tr.lines()...)
		return rows
	}
	return nil
}

// FuzzSGRCollapse is the general proof: for ANY input, and from any entry
// rendition, the collapsed row must render cell-identically.
func FuzzSGRCollapse(f *testing.F) {
	for _, row := range sgrCorpusRows(f) {
		f.Add(row)
	}
	seeds := []string{
		"", "plain", "\x1b", "\x1b[", "\x1b[0m", "\x1b[0m\x1b[0m", "\x1b[m",
		"\x1b[38;5;252ma\x1b[0m\x1b[38;5;252mb\x1b[0m",
		"\x1b[1;31;44mx\x1b[1;31;44my",
		"\x1b[38;2;10;20;30mx\x1b[38;2;10;20;30my",
		"\x1b[41m\x1b[Kx\x1b[0m",
		"\x1b[0ma\x1b[?25hb\x1b[0m",
		"\x1b[4:3mx\x1b[4:3my",
		"\x1b[53m\x1b[55mx",
		"\x1b[48;5;238m\x1b[36m▎\x1b[0m\x1b[48;5;238mbody\x1b[K\x1b[0m",
		"\x1b[7mhighlight\x1b[27m",
		"a\tb\nc\rd\bе",
		"\x1b[99999m\x1b[0m",
		"\x1b[38;5;300mx",
		"\x1b[;;m x",
		"\x1b[1m\x1b[22m\x1b[1mx",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > 4096 {
			t.Skip()
		}
		out := collapseSGR(s)
		for _, prefix := range sgrEntryStates {
			if d := sgrCellDiff(prefix+s, prefix+out); d != "" {
				t.Fatalf("entry %q: %s\n in: %q\nout: %q", prefix, d, s, out)
			}
		}
		if len(out) > len(s) {
			t.Fatalf("grew: %q -> %q", s, out)
		}
		// Collapsing is monotone: a second pass can only shrink further (it is
		// idempotent on any realistic row, but a pathological run longer than
		// the sequence buffer is decided in pieces).
		if again := collapseSGR(out); len(again) > len(out) {
			t.Fatalf("second pass grew: %q -> %q -> %q", s, out, again)
		}
	})
}

// TestCollapseSGRKeepsRowsSelfContained documents the painter's invariant that
// Axis B's scroll-region painter relies on: a row must start and end in default
// SGR, so an unreset row cannot tint the row below it through an erase. The
// normalizer never removes the bytes that uphold it: the entry state is
// modeled as unknown precisely so a leading reset survives.
func TestCollapseSGRKeepsRowsSelfContained(t *testing.T) {
	for _, row := range sgrCorpusRows(t) {
		out := collapseSGR(row)
		if strings.Contains(row, "\x1b") && strings.HasPrefix(row, "\x1b[0m") && !strings.HasPrefix(out, "\x1b[0m") {
			t.Errorf("collapse removed a leading reset: %q -> %q", row, out)
		}
		before := sgrFinalRendition("\x1b[1;31;44m" + row)
		after := sgrFinalRendition("\x1b[1;31;44m" + out)
		if before != after {
			t.Errorf("collapse changed the trailing rendition: %s vs %s\nrow: %q", before, after, row)
		}
		// And the invariant itself: a row that styles anything leaves the
		// terminal in the default rendition even when it was entered tinted,
		// so the next row's erase cannot inherit a colour. (Rows with no SGR
		// at all: blanks, plain tool output, are transparent by nature; it
		// is the styled row above them that has to have cleaned up.) True of
		// every corpus row before the collapse, and still true after it.
		if strings.Contains(out, "\x1b") && after != sgrFinalRendition("") {
			t.Errorf("row does not end in default SGR (%s): %q", after, out)
		}
	}
}

// BenchmarkCollapseSGR is the normalizer's own cost, on real glamour rows: it
// runs once per row on the way into the row cache, so this is what a transcript
// entry pays for the bytes it saves on every later frame.
func BenchmarkCollapseSGR(b *testing.B) {
	rows := sgrCorpusRows(b)
	var bytes int
	for _, r := range rows {
		bytes += len(r)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		for _, r := range rows {
			collapseSGR(r)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(bytes)/float64(len(rows)), "B/row")
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*len(rows)), "ns/row")
}

// TestTranscriptRowCacheIsCollapsed is the guard on the call site: rows enter
// the cache already normalized, so the saving is retained memory and not merely
// fewer bytes at paint time. If someone unhooks collapseSGR from renderMsgBase,
// this fails rather than quietly costing 3x the memory again.
func TestTranscriptRowCacheIsCollapsed(t *testing.T) {
	tr := frameFixture(t)
	tr.lines() // fill the cache
	var rows, textBytes, escBytes int
	for _, msg := range tr.rowCache {
		for _, r := range msg.rows {
			if !r.ref.valid() {
				continue // headers and rules are not node rows
			}
			if got := collapseSGR(r.text); got != r.text {
				t.Fatalf("cached row still carries collapsible SGR:\n cached: %q\n want:   %q", r.text, got)
			}
			rows++
			textBytes += len(r.text)
			for i := 0; i < len(r.text); {
				if r.text[i] == 0x1b {
					j := skipANSI(r.text, i)
					escBytes += j - i
					i = j
					continue
				}
				i++
			}
		}
	}
	if rows == 0 {
		t.Fatal("no node rows in the cache")
	}
	// Before the collapse, 76% of retained row text was escape bytes.
	if share := float64(escBytes) / float64(textBytes); share > 0.35 {
		t.Errorf("%.0f%% of retained row text is still ANSI (%d of %d B in %d rows)",
			100*share, escBytes, textBytes, rows)
	} else {
		t.Logf("%d rows, %d B text, %d B (%.0f%%) ANSI", rows, textBytes, escBytes, 100*share)
	}
}
