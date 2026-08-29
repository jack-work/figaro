package cli

// THE REVIEWERS' CANARIES, kept verbatim.
//
// Five arias reviewed this branch in their own worktrees against their own
// builds; pitrev2 (the form pit) wrote these before it wrote its findings, and
// each one was watched to FAIL on b68edea2 before anything was fixed. They are
// kept as they were written rather than folded into pit_selection_test.go: a
// test whose author was trying to break the code is worth more than one whose
// author was trying to describe it, and the difference in voice is the point.
//
// F2 and F5 were still failing after my own pass -- I had put the escape gate
// at the paint boundary but not at the source, and I had not found the pendG
// arming at all.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/mattn/go-runewidth"
)

// F1: a row is clipped by columns, not bytes, and never mid-rune.
func TestCanaryF1FormRowClipsByColumns(t *testing.T) {
	v, _ := json.Marshal(strings.Repeat("日本語テキスト ", 40))
	n := &formNode{path: "uni", label: "uni", depth: 1, value: json.RawMessage(v)}
	got := renderFormRow(n, 100, false)
	if !utf8.ValidString(got) {
		t.Fatalf("clipped mid-rune: %q", got)
	}
	if w := runewidth.StringWidth(got); w < 90 {
		t.Fatalf("row used %d of 100 columns", w)
	}
}

// F2: an opened value may not carry control sequences into the pit.
func TestCanaryF2ValueLinesStripControls(t *testing.T) {
	raw, _ := json.Marshal("a\x1b[2J\x1b[10Ab\rc\x07")
	for _, l := range formValueLines(raw, 40) {
		if strings.ContainsAny(l, "\x1b\r\x07") {
			t.Fatalf("control sequence reached the pit: %q", l)
		}
	}
}

// F3: wrapping one long line is linear in its length.
func TestCanaryF3WrapPlainIsLinear(t *testing.T) {
	s := strings.Repeat("x", 120000)
	start := time.Now()
	wrapPlain(s, 96)
	if d := time.Since(start); d > 10*time.Millisecond {
		t.Fatalf("wrapPlain(120KB) took %v", d)
	}
}

// F4: when the selected row vanishes the cursor lands where it was, not at
// the top of the list.
func TestCanaryF4SetRowsKeepsThePlace(t *testing.T) {
	rows := func(ids ...string) []pitRow {
		out := make([]pitRow, 0, len(ids))
		for _, id := range ids {
			out = append(out, idRow(id, id))
		}
		return out
	}
	p := newPicker(rows("a", "b", "c", "d"))
	p.pick(3)
	if row, _ := p.selected(); row.id != "d" {
		t.Fatalf("setup: cursor on %+v", row)
	}
	p.setRows(rows("a", "b", "c"), "")
	if row, _ := p.selected(); row.id != "c" {
		t.Fatalf("the selected row vanished and the cursor fell to %q; want the row that took its place", row.id)
	}
}

// F5: gg reaches the top of a LIVE pit, exactly as it does in help.
func TestCanaryF5GGInALivePit(t *testing.T) {
	ft := ldrender.NewFakeTerminal(60, 20)
	tr := newTranscript(ft, 60, 20, ldrender.NodeText{}, aria.NewClient(), "aria1234", time.Unix(0, 0))
	tr.enter()
	rows := make([]pitRow, 0, 30)
	for i := range 30 {
		rows = append(rows, idRow(fmt.Sprintf("row %02d", i), fmt.Sprintf("%d", i)))
	}
	tr.pit.showLive(pitID("form show"), &fakeItemView{rows: rows})
	tr.dispatch(keyEvent{b: 'G', mode: modeTranscript})
	if row, _ := tr.pit.selected(); row.id != "29" {
		t.Fatalf("setup: G selected %+v", row)
	}
	tr.dispatch(keyEvent{b: 'g', mode: modeTranscript})
	tr.dispatch(keyEvent{b: 'g', mode: modeTranscript})
	if row, _ := tr.pit.selected(); row.id != "0" {
		t.Fatalf("gg in a live pit selected %q, want the first row", row.id)
	}
}
