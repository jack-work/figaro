package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/render"
)

// How much content does the clip actually destroy, and at which widths?
func TestFarmerClipEatsContent(t *testing.T) {
	for name, md := range farmerCorpus() {
		worst, worstW, hits := 0, 0, 0
		for w := 20; w <= 200; w++ {
			n := livedoc.Node{Type: livedoc.NodeThinking, Markdown: md}
			pw := proseWidth(n, w)
			raw := clampTables(render.Prose(nodeMarkdown(n), pw), proseTableCapDefault, pw)
			lost := 0
			for _, r := range raw {
				if d := displayWidth(r); d > pw {
					lost += d - pw
				}
			}
			if lost > 0 {
				hits++
				if lost > worst {
					worst, worstW = lost, w
				}
			}
		}
		if hits > 0 {
			t.Logf("%-12s overflows at %d/181 widths; worst %d cells clipped away at w=%d", name, hits, worst, worstW)
		}
	}
}

// Which widths overflow for a plain (unquoted) node? The commit claims plain
// prose never does it.
func TestFarmerPlainOverflows(t *testing.T) {
	for name, md := range farmerCorpus() {
		var bad []string
		for w := 20; w <= 200; w++ {
			rows := nodeProseRows(livedoc.Node{Type: livedoc.NodeProse, Markdown: md}, w, false)
			for i, r := range rows {
				if d := displayWidth(r); d > w {
					bad = append(bad, fmt.Sprintf("w=%d row %d = %d cells", w, i, d))
					break
				}
			}
		}
		if len(bad) > 0 {
			t.Logf("PLAIN %-12s overflows at %d widths: %s", name, len(bad), strings.Join(bad[:min(len(bad), 4)], "; "))
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// THE REPAIR MUST NOT EAT TEXT. repairOneQuoteRow keeps the row's own leading
// spaces AND adds a two-cell rule, so a repaired row is two cells wider than
// glamour made it; the clip then takes those two cells back off the RIGHT end.
// When the row was space-padded that is harmless. When it was not, two
// characters of the agent's own words are destroyed.
func TestFarmerRepairEatsText(t *testing.T) {
	seen := map[string]bool{}
	for name, md := range farmerCorpus() {
		for w := 20; w <= 200; w++ {
			n := livedoc.Node{Type: livedoc.NodeThinking, Markdown: md}
			pw := proseWidth(n, w)
			raw := clampTables(render.Prose(nodeMarkdown(n), pw), proseTableCapDefault, pw)
			got := nodeProseRows(n, w, false)
			if len(raw) != len(got) {
				t.Fatalf("%s w=%d: row count changed", name, w)
			}
			for i := range raw {
				was := strings.TrimRight(farmerStrip(raw[i]), " ")
				now := strings.TrimRight(farmerStrip(got[i]), " ")
				// strip the rule the repair adds, then compare the words
				wasT := strings.TrimSpace(strings.ReplaceAll(was, "│", ""))
				nowT := strings.TrimSpace(strings.ReplaceAll(now, "│", ""))
				if wasT != nowT && !seen[name] {
					seen[name] = true
					t.Errorf("%s w=%d row %d: TEXT LOST\n  was %q\n  now %q", name, w, i, was, now)
				}
			}
		}
	}
}
