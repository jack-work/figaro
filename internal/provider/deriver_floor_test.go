package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/arialog"
)

// flooredForm holds one patch per version but answers ranges only above
// floor, the way a form does once its oldest records are dropped; stated says
// whether its projected state is still readable.
type flooredForm struct {
	floor  uint64
	stated bool
	n      uint64
}

var errFloor = errors.New("below the floor")

func (f flooredForm) patch(v uint64) message.Patch {
	return form.Build(form.Snapshot{}, map[string]json.RawMessage{
		fmt.Sprintf("k%d", v%3): json.RawMessage(fmt.Sprint(v)),
	}, nil)
}

func (f flooredForm) PatchesBetween(after, upTo uint64) ([]message.Patch, error) {
	if after < f.floor {
		return nil, errFloor
	}
	var out []message.Patch
	for v := after + 1; v <= upTo && v <= f.n; v++ {
		out = append(out, f.patch(v))
	}
	return out, nil
}

func (f flooredForm) StateAt(v uint64) (form.Snapshot, bool) {
	if !f.stated {
		return form.Snapshot{}, false
	}
	s := form.Snapshot{}
	for i := uint64(1); i <= v; i++ {
		s = s.Apply(f.patch(i))
	}
	return s, true
}

func inputAt(lt, version uint64) arialog.Entry[message.Message] {
	return arialog.Entry[message.Message]{LT: lt, FormChannelVersion: version,
		Payload: message.Message{Role: message.RoleInput, Content: []message.Content{message.TextContent("hi")}}}
}

func derive(d *Deriver, e arialog.Entry[message.Message]) form.Snapshot {
	msg, _, _ := d.Next(e)
	d.Commit(e, msg)
	return d.snap
}

// Below the floor an unanswerable range is a resync, never "no change": the
// state the deriver renders against after the window must be the form's.
func TestADerivationBelowTheFloorResyncsFromProjectedState(t *testing.T) {
	f := flooredForm{floor: 10, stated: true, n: 20}
	d := NewDeriver(f, nil)
	derive(d, inputAt(1, 2))
	got := derive(d, inputAt(2, 20))
	want, _ := f.StateAt(20)
	if g, w := snapJSON(t, got), snapJSON(t, want); g != w {
		t.Fatalf("after a window below the floor the derivation holds %s, the form %s", g, w)
	}
}

// With neither the patches nor the state, the window stays open: the next
// entry whose range is answerable carries the change instead of losing it.
func TestAnUnanswerableWindowIsHeldForTheNextEntry(t *testing.T) {
	f := flooredForm{floor: 0, n: 20}
	d := NewDeriver(f, nil)
	derive(d, inputAt(1, 2))
	d.form = flooredForm{floor: 10, n: 20}
	derive(d, inputAt(2, 12))
	if d.lastForm != 2 {
		t.Fatalf("an unanswered window advanced the cursor to %d", d.lastForm)
	}
	d.form = flooredForm{floor: 0, n: 20}
	got := derive(d, inputAt(3, 20))
	want, _ := flooredForm{stated: true, n: 20}.StateAt(20)
	if g, w := snapJSON(t, got), snapJSON(t, want); g != w {
		t.Fatalf("the held window was lost: derivation %s, form %s", g, w)
	}
}

func snapJSON(t *testing.T, s form.Snapshot) string {
	t.Helper()
	b, err := s.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// #106: a long study window over a few hot keys folds to far more than the
// state it lands on; the window goes as one diff, and lands on the same state.
func TestALongStudyWindowGoesAsTheStateDiff(t *testing.T) {
	study := flooredForm{stated: true, n: 410}
	d := NewDeriver(nil, map[string]Form{"@s": study})
	e := inputAt(1, 0)
	e.StudyVersions = map[string]uint64{"@s": 400}
	msg, _, _ := d.Next(e)
	ps := msg.StudyPatches["@s"]
	if len(ps) != 1 {
		t.Fatalf("a 400-patch window over 3 keys went as %d patches", len(ps))
	}
	want, _ := study.StateAt(400)
	if g, w := snapJSON(t, form.Fold(form.Snapshot{}, ps)), snapJSON(t, want); g != w {
		t.Fatalf("the diff lands on %s, the form is %s", g, w)
	}
	d.Commit(e, msg)
	e = inputAt(2, 0)
	e.StudyVersions = map[string]uint64{"@s": 410}
	msg, _, _ = d.Next(e)
	if len(msg.StudyPatches["@s"]) != 10 {
		t.Fatalf("a short window was collapsed: %d patches", len(msg.StudyPatches["@s"]))
	}
}

// countedForm counts its state reads: each one is a replay of the libretto.
type countedForm struct {
	flooredForm
	reads *int
}

func (f countedForm) StateAt(v uint64) (form.Snapshot, bool) {
	*f.reads++
	return f.flooredForm.StateAt(v)
}

// Consecutive long windows read the studied form's state once: each window
// starts where the one before ended, and that state is folded, not replayed.
func TestLongStudyWindowsReadTheStateOnce(t *testing.T) {
	reads := 0
	study := countedForm{flooredForm{stated: true, n: 2000}, &reads}
	d := NewDeriver(nil, map[string]Form{"@s": study})
	for i, at := range []uint64{400, 800, 810, 1200, 2000} {
		e := inputAt(uint64(i+1), 0)
		e.StudyVersions = map[string]uint64{"@s": at}
		msg, _, _ := d.Next(e)
		want, _ := study.flooredForm.StateAt(at)
		d.Commit(e, msg)
		if got := snapJSON(t, d.studyAt["@s"].snap); got != snapJSON(t, want) {
			t.Fatalf("the kept state at %d is %s, the form is %s", at, got, snapJSON(t, want))
		}
	}
	if reads != 0 {
		t.Fatalf("five windows from the start read the state %d times", reads)
	}
	d2 := NewDeriver(nil, map[string]Form{"@s": study})
	d2.lastStudy["@s"] = 400
	for i, at := range []uint64{800, 1200, 2000} {
		e := inputAt(uint64(i+1), 0)
		e.StudyVersions = map[string]uint64{"@s": at}
		msg, _, _ := d2.Next(e)
		d2.Commit(e, msg)
	}
	if reads != 1 {
		t.Fatalf("three long windows from a seeded cursor read the state %d times, want 1", reads)
	}
}
