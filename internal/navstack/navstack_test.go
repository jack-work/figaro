package navstack

import (
	"fmt"
	"strings"
	"testing"
)

func hist(pos int, ids ...string) History { return History{IDs: ids, Pos: pos} }

func alive(string) string { return "" }

func deadAmong(dead map[string]string) func(string) string {
	return func(id string) string { return dead[id] }
}

func TestPush(t *testing.T) {
	for _, c := range []struct {
		name string
		h    History
		push string
		want History
	}{
		{"first attend", History{}, "A", hist(0, "A")},
		{"a new figaro goes on top", hist(1, "A", "B"), "C", hist(2, "A", "B", "C")},
		{"the current figaro again is no move", hist(1, "A", "B"), "B", hist(1, "A", "B")},
		{"after a back, a new attend drops what was ahead", hist(0, "A", "B", "C"), "D", hist(1, "A", "D")},
		{"a revisit is its own entry", hist(2, "A", "B", "C"), "A", hist(3, "A", "B", "C", "A")},
		{"an empty id is nothing", hist(0, "A"), "", hist(0, "A")},
		{"a remote id is an entry", hist(0, "A"), "cccb44ac/x", hist(1, "A", "cccb44ac/x")},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := c.h
			h.IDs = append([]string(nil), h.IDs...)
			h.Push(c.push)
			if fmt.Sprint(h) != fmt.Sprint(c.want) {
				t.Fatalf("got %v, want %v", h, c.want)
			}
		})
	}
}

func TestPushKeepsTheNewestBound(t *testing.T) {
	var h History
	for i := range Bound + 7 {
		h.Push(fmt.Sprint(i))
	}
	if len(h.IDs) != Bound || h.IDs[0] != "7" || h.Current() != fmt.Sprint(Bound+6) || h.Pos != Bound-1 {
		t.Fatalf("got %d entries from %s, at %d (%s)", len(h.IDs), h.IDs[0], h.Pos, h.Current())
	}
}

func TestHop(t *testing.T) {
	for _, c := range []struct {
		name    string
		h       History
		steps   int
		home    bool
		dead    map[string]string
		land    string
		ok      bool
		want    History
		dropped string
	}{
		{"back one", hist(2, "A", "B", "C"), -1, false, nil, "B", true, hist(1, "A", "B", "C"), ""},
		{"forward one", hist(0, "A", "B", "C"), 1, false, nil, "B", true, hist(1, "A", "B", "C"), ""},
		{"back three at once", hist(3, "A", "B", "C", "D"), -3, false, nil, "A", true, hist(0, "A", "B", "C", "D"), ""},
		{"nothing older", hist(0, "A", "B"), -1, false, nil, "", false, hist(0, "A", "B"), ""},
		{"nothing newer", hist(1, "A", "B"), 1, false, nil, "", false, hist(1, "A", "B"), ""},
		{"as far as there is", hist(1, "A", "B", "C"), -5, false, nil, "A", true, hist(0, "A", "B", "C"), ""},
		{"empty", History{}, -1, false, nil, "", false, History{}, ""},
		// The bug in the pager's jumplist: forward through a revisited parent.
		{"forward through a revisit keeps its place", hist(1, "A", "B", "A", "C"), 1, false, nil, "A", true, hist(2, "A", "B", "A", "C"), ""},
		{"and reaches the end", hist(2, "A", "B", "A", "C"), 1, false, nil, "C", true, hist(3, "A", "B", "A", "C"), ""},
		{"back over a killed figaro", hist(2, "A", "B", "C"), -1, false, map[string]string{"B": "gone"}, "A", true, hist(0, "A", "C"), "B gone"},
		{"forward over a reaped figaro", hist(0, "A", "B", "C"), 1, false, map[string]string{"B": "reaped"}, "C", true, hist(1, "A", "C"), "B reaped"},
		{"every way back is dead", hist(2, "A", "B", "C"), -1, false, map[string]string{"A": "gone", "B": "gone"}, "", false, hist(0, "C"), "B gone,A gone"},
		{"a neighbour equal to here is not a move", hist(2, "A", "B", "B"), -1, false, nil, "A", true, hist(0, "A", "B", "B"), ""},
		{"back from home lands where you left", hist(1, "A", "B", "C"), -1, true, nil, "B", true, hist(1, "A", "B", "C"), ""},
		{"back two from home", hist(1, "A", "B", "C"), -2, true, nil, "A", true, hist(0, "A", "B", "C"), ""},
		{"forward from home", hist(1, "A", "B", "C"), 1, true, nil, "C", true, hist(2, "A", "B", "C"), ""},
		{"back from home, where you left is gone", hist(1, "A", "B", "C"), -1, true, map[string]string{"B": "gone"}, "A", true, hist(0, "A", "C"), "B gone"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := c.h
			h.IDs = append([]string(nil), h.IDs...)
			fate := alive
			if c.dead != nil {
				fate = deadAmong(c.dead)
			}
			land, dropped, ok := h.Hop(c.steps, c.home, fate)
			if land != c.land || ok != c.ok {
				t.Fatalf("landed %q ok=%v, want %q ok=%v", land, ok, c.land, c.ok)
			}
			if fmt.Sprint(h) != fmt.Sprint(c.want) {
				t.Fatalf("history %v, want %v", h, c.want)
			}
			var got []string
			for _, d := range dropped {
				got = append(got, d.ID+" "+d.Why)
			}
			if strings.Join(got, ",") != c.dropped {
				t.Fatalf("dropped %q, want %q", strings.Join(got, ","), c.dropped)
			}
		})
	}
}

func TestWhere(t *testing.T) {
	if p, n := (History{}).Where(); p != 0 || n != 0 {
		t.Fatalf("empty: %d/%d", p, n)
	}
	if p, n := hist(1, "A", "B", "C").Where(); p != 2 || n != 3 {
		t.Fatalf("got %d/%d", p, n)
	}
}
