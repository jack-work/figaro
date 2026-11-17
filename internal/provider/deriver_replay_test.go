package provider

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/arialog"
)

type foldedForm struct{ versionedForm }

func (f foldedForm) StateAt(v uint64) (form.Snapshot, bool) {
	return form.Fold(form.Snapshot{}, f.PatchesBetween(0, v)), true
}

func replayLog(t *testing.T) (arialog.Log[message.Message], foldedForm) {
	t.Helper()
	birth := form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"aria_id":          json.RawMessage(`"branch"`),
		form.ForkedFromKey: json.RawMessage(`"parent"`),
	}, nil)
	bound := foldedForm{versionedForm{patches: []message.Patch{
		setKey("aria_id", "parent"),
		birth,
		setKey("datetime", "Thursday 2PM"),
		setKey("datetime", "Monday 9PM"),
		setKey("datetime", "Thursday 2PM"),
	}}}
	mark := &message.Replay{Of: "parent", Turn: 2, LT: 3, Notice: true, Clock: true, Identity: true, Tools: true, AriaID: "parent"}
	ask := func(text string, r *message.Replay, v uint64) arialog.Entry[message.Message] {
		return arialog.Entry[message.Message]{FormChannelVersion: v, Payload: message.Message{Role: message.RoleInput, Replay: r, Content: []message.Content{message.TextContent(text)}}}
	}
	answer := func(v uint64) arialog.Entry[message.Message] {
		return arialog.Entry[message.Message]{FormChannelVersion: v, Payload: message.Message{Role: message.RoleOutput, Content: []message.Content{message.TextContent("ok")}}}
	}
	log := arialog.NewMemLog[message.Message]()
	for _, e := range []arialog.Entry[message.Message]{
		ask("first", nil, 1), answer(1),
		ask("second, replayed", mark, 3), answer(3),
		ask("after the unset", nil, 4), answer(4),
		ask("second, again", mark, 5), answer(5),
	} {
		if _, err := log.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	return log, bound
}

type seen struct{ id, forkedFrom, datetime string }

func believed(s form.Snapshot) seen {
	str := func(k string) string {
		if v := s.Lookup(k); v != nil {
			return *v
		}
		return ""
	}
	return seen{str("aria_id"), str(form.ForkedFromKey), str("datetime")}
}

func walk(t *testing.T, d *Deriver, log arialog.Log[message.Message], from uint64) map[uint64]seen {
	t.Helper()
	out := map[uint64]seen{}
	entries, _ := arialog.TailAfter(log, from)
	for _, e := range entries {
		msg, _, ok := d.Next(e)
		if !ok {
			continue
		}
		d.Commit(e, msg)
		if msg.Role == message.RoleInput {
			out[e.LT] = believed(d.snap)
		}
	}
	return out
}

// Defends #114: a turn run under replay shows the model the original's id
// and no fork mark, and the first turn after the unset shows it the truth.
func TestTheModelReadsEachTurnUnderItsOwnReplayMark(t *testing.T) {
	log, bound := replayLog(t)
	got := walk(t, NewDeriver(bound, nil), log, 0)
	want := map[uint64]seen{
		1: {"parent", "", ""},
		3: {"parent", "", "Thursday 2PM"},
		5: {"branch", "parent", "Monday 9PM"},
		7: {"parent", "parent", "Thursday 2PM"},
	}
	for lt, w := range want {
		if got[lt] != w {
			t.Errorf("LT %d: the model reads %+v, want %+v", lt, got[lt], w)
		}
	}
}

func TestASeedInsideAReplayTurnKeepsTheMask(t *testing.T) {
	log, bound := replayLog(t)
	d := NewDeriver(bound, nil)
	d.SeedAt(log, rowsThrough(t, 4))
	if b := believed(d.snap); b.id != "parent" || b.forkedFrom != "" {
		t.Fatalf("seeded inside the replay, the model reads %+v", b)
	}
	if got := walk(t, d, log, 4)[5]; got != (seen{"branch", "parent", "Monday 9PM"}) {
		t.Fatalf("after the unset the model reads %+v", got)
	}
}

func TestAFormWithNoReplayIsPassedThrough(t *testing.T) {
	log := arialog.NewMemLog[message.Message]()
	e, _ := log.Append(arialog.Entry[message.Message]{FormChannelVersion: 1, Payload: message.Message{Role: message.RoleInput, Content: []message.Content{message.TextContent("hi")}}})
	bound := versionedForm{patches: []message.Patch{setKey("aria_id", "x")}}
	msg, _, _ := NewDeriver(bound, nil).Next(e)
	if !reflect.DeepEqual(msg.Patches, bound.patches) {
		t.Fatalf("patches rewritten: %+v", msg.Patches)
	}
}
