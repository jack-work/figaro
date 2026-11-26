package cli

import (
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
)

func nestedFormPit(t *testing.T) (*transcript, *formView) {
	t.Helper()
	tr := newTranscript(io.Discard, 100, 30, ldrender.NodeText{}, aria.NewClient(), "aria1234", time.Unix(0, 0))
	tr.enter()
	src := &fakeFormSource{snap: form.FromMap(formKeys(
		"a.b.c", `1`, "a.b.d.e", `2`, "a.f", `3`, "g.h.i", `4`, "j", `5`,
	))}
	v := &formView{src: src, open: map[string]bool{}, aria: "aria1234"}
	tr.showLivePit("form show", v, false)
	tr.pit.lines(100, 20)
	return tr, v
}

func TestZRInAFormPitOpensEveryBranchAndZMClosesThemAll(t *testing.T) {
	tr, v := nestedFormPit(t)
	ids := func() string { return strings.Join(formRowIDs(v.Items(100)), ",") }
	if got := ids(); got != "a,g,j" {
		t.Fatalf("closed form shows %s", got)
	}
	tr.key('z')
	tr.key('R')
	got := formRowIDs(v.Items(100))
	for _, want := range []string{"a", "a.b", "a.b.c", "a.b.d", "a.b.d.e", "a.f", "g", "g.h", "g.h.i", "j"} {
		if !slices.Contains(got, want) {
			t.Errorf("zR left %s unopened: rows %v", want, got)
		}
	}
	tr.key('z')
	tr.key('M')
	if got := ids(); got != "a,g,j" {
		t.Fatalf("zM left rows %s", got)
	}
}

func TestZMFocusesTheTopLevelAncestorOfTheCursorRow(t *testing.T) {
	tr, v := nestedFormPit(t)
	tr.key('z')
	tr.key('R')
	v.Items(100)
	tr.pit.lines(100, 20)
	if !tr.pit.focus("a.b.d.e") {
		t.Fatal("no row a.b.d.e after zR")
	}
	tr.key('z')
	tr.key('M')
	tr.pit.lines(100, 20)
	if row, ok := tr.pit.selected(); !ok || row.id != "a" {
		t.Fatalf("cursor on %q after zM, want a", row.id)
	}
}

func TestZIsOnlyTheFoldPrefixInThePitLayers(t *testing.T) {
	for i := range keymap {
		bd := &keymap[i]
		if bd.prefix != "" || bd.chord.kind != chordByte || bd.chord.b != 'z' {
			continue
		}
		if bd.action != "prefix:z" || bd.modes != inList {
			t.Errorf("z is bound to %s in modes %v, want only the fold prefix in the list", bd.action, bd.modes)
		}
	}
	for _, act := range []string{"branch-open-all", "branch-close-all"} {
		for i := range keymap {
			bd := &keymap[i]
			if bd.action == act && (bd.prefix != "z" || bd.modes != inList) {
				t.Errorf("%s is reachable by %s outside z in modes %v", act, bd.chord, bd.modes)
			}
		}
	}
}
