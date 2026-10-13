package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
	"github.com/mattn/go-runewidth"
)

// A row is clipped by columns, never mid-rune.
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

func TestCanaryF2ValueLinesStripControls(t *testing.T) {
	raw, _ := json.Marshal("a\x1b[2J\x1b[10Ab\rc\x07")
	for _, l := range formValueLines(raw, 40) {
		if strings.ContainsAny(l, "\x1b\r\x07") {
			t.Fatalf("control sequence reached the pit: %q", l)
		}
	}
}

func TestCanaryF3WrapPlainIsLinear(t *testing.T) {
	s := strings.Repeat("x", 120000)
	start := time.Now()
	wrapPlain(s, 96)
	if d := time.Since(start); d > 10*time.Millisecond {
		t.Fatalf("wrapPlain(120KB) took %v", d)
	}
}

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
