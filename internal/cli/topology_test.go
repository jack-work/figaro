package cli

import (
	"testing"

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
