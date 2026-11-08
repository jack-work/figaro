package cli

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/mattn/go-runewidth"
)

var asideView = &rpc.FormView{
	Rows:  []string{"*"},
	Label: "title",
	Aside: "active_at",
	Sort:  "-active_at",
}

func asideRig(now time.Time) *formView {
	ms := func(ago time.Duration) string { return strconv.FormatInt(now.Add(-ago).UnixMilli(), 10) }
	src := &fakeFormSource{snap: form.FromMap(formKeys(
		"pushup.title", `"every pager hotkey gets a command"`, "pushup.active_at", ms(4*time.Minute),
		"egomaniac.title", `"recent: flat recency pit"`, "egomaniac.active_at", ms(20*time.Second),
		"ox.title", `"a long-idle figaro"`, "ox.active_at", ms(3*24*time.Hour),
		"quiet.title", `"never prompted"`,
	))}
	v := &formView{src: src, open: map[string]bool{}}
	v.setView(asideView)
	return v
}

func asidePit(t *testing.T) (*inputProbe, *formView) {
	t.Helper()
	p := newInputProbe(t, true)
	v := asideRig(time.Now())
	p.lt.tr.pit.showLive(pitID("recent"), v)
	return p, v
}

func drawnPit(p *inputProbe, w int) []string {
	var out []string
	for _, line := range p.lt.tr.pit.lines(w, 10) {
		out = append(out, strings.TrimRight(pitText(line), " "))
	}
	return out
}

// A view's aside takes the place of the row's size: the row reads as its
// name, padded to one column with its siblings, then its label's value.
func TestAnAsideRowIsItsNameAndLabel(t *testing.T) {
	rows := asideRig(time.Now()).Items(60)
	want := []string{
		`egomaniac  "recent: flat recency pit"`,
		`pushup     "every pager hotkey gets a command"`,
		`ox         "a long-idle figaro"`,
		`quiet      "never prompted"`,
	}
	ages := []string{"20s", "4m", "3d", ""}
	if len(rows) != len(want) {
		t.Fatalf("%d rows, want %d", len(rows), len(want))
	}
	for i, r := range rows {
		if r.str() != want[i] {
			t.Errorf("row %d = %q, want %q", i, r.str(), want[i])
		}
		if got := r.asideText(); got != ages[i] {
			t.Errorf("row %d aside = %q, want %q", i, got, ages[i])
		}
	}
}

// The pit draws the aside at its right edge, as an age, past the cursor's
// gutter and the row's own notes.
func TestThePitEndsAnAsideRowWithItsAge(t *testing.T) {
	p, _ := asidePit(t)
	lines := drawnPit(p, 60)
	ages := []string{"20s", "4m", "3d"}
	for i, age := range ages {
		if i >= len(lines) {
			t.Fatalf("pit drew %d lines:\n%s", len(lines), strings.Join(lines, "\n"))
		}
		if !strings.HasSuffix(lines[i], "  "+age) || runewidth.StringWidth(lines[i]) != 60 {
			t.Errorf("line %d = %q, want its age %s at column 60", i, lines[i], age)
		}
	}
	if len(lines) > 3 && strings.HasSuffix(lines[3], "s") {
		t.Errorf("a row with no activity drew an age: %q", lines[3])
	}
}

// A narrow pit clips the label, never the age.
func TestANarrowPitClipsTheLabelNotTheAge(t *testing.T) {
	p, _ := asidePit(t)
	for _, line := range drawnPit(p, 30) {
		if runewidth.StringWidth(line) > 30 {
			t.Errorf("%q overflows 30 columns", line)
		}
	}
	if line := drawnPit(p, 30)[1]; !strings.HasSuffix(line, "  4m") || !strings.Contains(line, "…") {
		t.Errorf("line = %q, want its title clipped and its age kept", line)
	}
}

// A search draws the hit's row with its age still at the right edge, and the
// marks land on the letters of the title that matched.
func TestASearchedAsideRowKeepsItsAgeAndItsMarks(t *testing.T) {
	p, _ := asidePit(t)
	tr := p.lt.tr
	feed(t, p.in, "/hotkey")
	for _, r := range tr.pit.list().rows {
		if r.id != "pushup" {
			continue
		}
		text := r.str()
		var marked []byte
		for _, m := range r.marks {
			marked = append(marked, text[m])
		}
		if string(marked) != "hotkey" {
			t.Errorf("row %q marks %q, want the letters of hotkey", text, marked)
		}
		if r.asideText() != "4m" {
			t.Errorf("searched row's aside = %q, want 4m", r.asideText())
		}
		return
	}
	t.Fatalf("no row for the hit: %v", pitIDs(tr))
}
