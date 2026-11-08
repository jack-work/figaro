package cli

import (
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

var ariasLikeHint = &rpc.FormView{
	Rows:    []string{"arias.*", "**.forks.*"},
	Open:    []string{"arias", "**.forks"},
	RowAria: "id",
	Through: "forks",
	Label:   "mantra",
	Mark:    &rpc.ViewMark{Key: "id", KeySubject: "aria_id"},
}

func ariasLike() *fakeFormSource {
	return things(
		"arias.top.id", `"aaa"`, "arias.top.mantra", `"top"`,
		"arias.top.forks.mid.id", `"bbb"`, "arias.top.forks.mid.mantra", `"mid"`,
		"arias.top.forks.mid.forks.me.id", `"ccc"`, "arias.top.forks.mid.forks.me.mantra", `"me"`,
		"arias.other.id", `"ddd"`, "arias.other.mantra", `"other"`,
	)
}

func attending(id string) func() form.Snapshot {
	snap := form.FromMap(formKeys("aria_id", `"`+id+`"`))
	return func() form.Snapshot { return snap }
}

func topologyPit(t *testing.T, f *fakeMounts, seeks bool, attended string) (*interactiveInput, *livelogTurn, *formView) {
	t.Helper()
	f.hint = ariasLikeHint
	f.forms["@things"] = ariasLike()
	f.forms["far/@things"] = ariasLike()
	var out countingWriter
	in, lt := navInput(t, &out, true)
	v := indexView(f)
	v.seeks = seeks
	v.subject = attending(attended)
	in.mu.Lock()
	in.installLivePit("troupe", v, false)
	in.mu.Unlock()
	return in, lt, v
}

func selectedID(in *interactiveInput, lt *livelogTurn) string {
	in.mu.Lock()
	defer in.mu.Unlock()
	row, _ := lt.tr.pit.selected()
	return row.id
}

// A mount on this node is drawn against the pit's subject, so the aria this
// shell attends is marked inside it; a peer's mount is not: no id there is
// the attended aria.
func TestAMountedFormOnThisNodeMarksTheAttendedAria(t *testing.T) {
	f := newFakeMounts()
	f.hint = ariasLikeHint
	f.forms["@things"] = ariasLike()
	f.forms["far/@things"] = ariasLike()
	v := indexView(f)
	v.subject = attending("ddd")
	v.Items(100)
	v.Activate("here")
	v.Activate("far")
	v.Items(100)
	f.waitOpened(t, 2)
	rows := rowsByID(v.Items(100))
	if got := rows["here\x1farias.other"].note; got != "●" {
		t.Errorf("the attended aria in this node's mount is not marked: note %q", got)
	}
	if got := rows["far\x1farias.other"].note; got == "●" {
		t.Errorf("a peer's row with the same id was marked as the attended aria")
	}
}

// l opens the topology with every row between the root and the attended
// aria open, this node's store mounted, and the cursor on the aria, however
// deep a fork it is.
func TestTheTopologyOpensAtTheAttendedAria(t *testing.T) {
	f := newFakeMounts()
	in, lt, _ := topologyPit(t, f, true, "ccc")
	f.waitOpened(t, 1)
	want := "here\x1farias.top.forks.mid.forks.me"
	deadline := time.Now().Add(5 * time.Second)
	for selectedID(in, lt) != want {
		if time.Now().After(deadline) {
			t.Fatalf("cursor on %q, want %q", selectedID(in, lt), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(f.opened) != 1 || f.opened[0] != "@things" {
		t.Errorf("opening at the attended aria dialled %v, want this node's store alone", f.opened)
	}
}

// L opens the topology at its root: the nodes, the cursor on this one,
// nothing mounted and nothing dialled.
func TestTheTopologyOpensAtItsRoot(t *testing.T) {
	f := newFakeMounts()
	in, lt, v := topologyPit(t, f, false, "ccc")
	if got := selectedID(in, lt); got != "here" {
		t.Errorf("cursor on %q, want this node's row", got)
	}
	time.Sleep(50 * time.Millisecond)
	in.mu.Lock()
	lt.tr.pit.lines(100, 30)
	in.mu.Unlock()
	if len(f.opened) != 0 {
		t.Errorf("the root view dialled %v", f.opened)
	}
	if v.isOpen("here") {
		t.Errorf("this node's row is open at the root")
	}
}

func TestLsSeeksTheAttendedAriaAndTroupeDoesNot(t *testing.T) {
	in := &interactiveInput{}
	for verb, seeks := range map[string]bool{"ls": true, "list": true, "troupe": false} {
		p, ok := formPitFor(verb)
		if !ok || p.form != rpc.TroupeFormID || p.ready == nil {
			t.Fatalf(":%s opens %+v, want the topology", verb, p)
		}
		v := &formView{}
		p.ready(in, v)
		if v.seeks != seeks || v.opener == nil {
			t.Errorf(":%s seeks=%v opener=%v, want seeks=%v and a mount opener", verb, v.seeks, v.opener != nil, seeks)
		}
	}
}

// l painted the node row first, then the cursor jumped to the attended aria
// once this node's store mounted, and the aria's own forks stayed closed.
// The store now loads before the pit is shown, so the first frame the pit
// paints already has the cursor on the attended aria with its forks open,
// and no later frame moves it.
func TestTheFirstFrameIsFocusedOnTheAttendedAriaWithItsForksOpen(t *testing.T) {
	f := newFakeMounts()
	f.hint = ariasLikeHint
	f.forms["@things"] = things(
		"arias.top.id", `"aaa"`, "arias.top.mantra", `"top"`,
		"arias.top.forks.mid.id", `"bbb"`, "arias.top.forks.mid.mantra", `"mid"`,
		"arias.top.forks.mid.forks.me.id", `"ccc"`, "arias.top.forks.mid.forks.me.mantra", `"me"`,
		"arias.top.forks.mid.forks.me.forks.kid.id", `"eee"`, "arias.top.forks.mid.forks.me.forks.kid.mantra", `"kid"`,
		"arias.other.id", `"ddd"`, "arias.other.mantra", `"other"`,
	)
	var out countingWriter
	in, lt := navInput(t, &out, true)
	v := indexView(f)
	v.subject = attending("ccc")
	seekAttended(v, f.open)

	lt.tr.h = 12
	type frame struct {
		cursor string
		shown  map[string]bool
	}
	var frames []frame
	prev := lt.tr.painted
	lt.tr.painted = func() {
		if p := lt.tr.pit.list(); lt.tr.pit.id != "" && p != nil {
			row, _ := p.selected()
			shown := map[string]bool{}
			for _, r := range p.rows[p.top:min(p.top+p.height, len(p.rows))] {
				shown[r.id] = true
			}
			frames = append(frames, frame{row.id, shown})
		}
		if prev != nil {
			prev()
		}
	}
	in.mu.Lock()
	in.installLivePit("troupe", v, false)
	in.mu.Unlock()
	time.Sleep(50 * time.Millisecond)

	me := "here\x1farias.top.forks.mid.forks.me"
	kid := me + ".forks.kid"
	if len(frames) == 0 {
		t.Fatal("the pit painted no frame")
	}
	for i, fr := range frames {
		if fr.cursor != me {
			t.Errorf("frame %d: cursor on %q, want %q from the first frame on", i, fr.cursor, me)
		}
		if !fr.shown[me] || !fr.shown[kid] {
			t.Errorf("frame %d: a short pit does not show the attended aria with its fork under it: %v", i, fr.shown)
		}
	}
	if len(f.opened) != 1 || f.opened[0] != "@things" {
		t.Errorf("opened %v, want this node's store alone", f.opened)
	}
}
