package render

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// The premature-break oracle, pointed at a REAL corpus.
//
// prose_wrap_test.go pins the defect with four paragraphs. This runs the same
// oracle over as much genuine aria markdown as you care to hand it: bold,
// links, em dashes, curly quotes, accents, the hyphens that were the trigger -
// at every width a pane plausibly has. It is opt-in because the corpus is
// private: a JSON array of markdown strings, path in FIGARO_ORPHAN_CORPUS.
//
//	cd ~/.local/state/figaro/arias   # or a copy
//	python3 -c '…dump prose/thinking text blocks to a JSON array…' > /var/tmp/corpus.json
//	FIGARO_ORPHAN_CORPUS=/var/tmp/corpus.json go test ./internal/render -run CorpusSweep
//
// Measured, 600 samples x widths 30..120, on the store as of 2026-08-02:
// before glamour v2 (30aae84^) 4106 premature breaks, after it 6, and all six
// are the oracle's own blind spots, not orphans: it collapses a double space
// after a period, and it re-joins a word the renderer legitimately broke at a
// hyphen breakpoint ("deep-" / "dive"). Treat a handful of hits as noise and a
// thousand as the bug; the ratio is the signal.
func TestProse_CorpusSweep(t *testing.T) {
	path := os.Getenv("FIGARO_ORPHAN_CORPUS")
	if path == "" {
		t.Skip("set FIGARO_ORPHAN_CORPUS to a JSON array of markdown strings")
	}
	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var corpus []string
	if err := json.Unmarshal(blob, &corpus); err != nil {
		t.Fatal(err)
	}
	premature, over, shown := 0, 0, 0
	report := func(format string, args ...any) {
		if shown < 12 {
			shown++
			t.Errorf(format, args...)
		}
	}
	for si, md := range corpus {
		// Paragraphs only. The oracle re-flows a block's ink greedily, so a
		// list, a table or a code block is not its business: those have their
		// own guards (table_wrap_test.go, hardwrap_test.go).
		if strings.ContainsAny(md, "`|") || strings.Contains(md, "\n-") ||
			strings.Contains(md, "\n#") || strings.Contains(md, "\n ") || strings.Contains(md, "\n1.") {
			continue
		}
		for w := 30; w <= 120; w++ {
			var block []string
			flush := func() {
				defer func() { block = nil }()
				if len(block) < 2 {
					return
				}
				if n, ink := prematureBreak(block); n > 0 {
					premature++
					report("PREMATURE sample=%d w=%d surplus=%d\n  %s", si, w, n, ink)
				}
			}
			for _, r := range Prose(md, w) {
				if c := cells(r); c > w {
					over++
					report("OVER sample=%d w=%d cells=%d %q", si, w, c, StripEscapes(r))
				}
				if strings.TrimSpace(stripANSI(r)) == "" {
					flush()
					continue
				}
				block = append(block, r)
			}
			flush()
		}
	}
	t.Logf("corpus sweep: %d samples, widths 30..120: premature=%d over=%d", len(corpus), premature, over)
}
