package cli

import (
	"io"
	"time"

	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

func reapedShaped() *fakeFormSource {
	return &fakeFormSource{snap: form.FromMap(formKeys(
		"items.n2.mantra", `"second"`, "items.n2.nodes", `["n2","n3"]`, "items.n2.purge_at", `1000`,
		"items.n1.mantra", `"first"`, "items.n1.nodes", `["n1"]`, "items.n1.purge_at", `2000`,
		"note", `"not in the list"`,
	))}
}

var reapedView = &rpc.FormView{
	List: "items",
	Columns: []rpc.ViewColumn{
		{Key: "", Title: "root"},
		{Key: "mantra", Shrink: 1},
		{Key: "nodes", Title: "n", Format: rpc.FormatCount, Right: true},
	},
	Sort: "purge_at",
	Mark: &rpc.ViewMark{Key: "mantra", Equals: "second"},
}

// A form with a view hint draws its list as a table: a header, one row per
// child in the hint's order, columns read under each child, and the mark.
func TestAViewHintDrawsTheListAsATable(t *testing.T) {
	v := &formView{src: reapedShaped(), open: map[string]bool{}}
	v.setView(reapedView)
	rows := v.Items(80)
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want a header and two: %#v", len(rows), rows)
	}
	if !strings.Contains(rows[0].str(), "root") || !strings.Contains(rows[0].str(), "mantra") || rows[0].selectable() {
		t.Errorf("header = %q (selectable %v)", rows[0].str(), rows[0].selectable())
	}
	if rows[1].id != "items.n2" || rows[2].id != "items.n1" {
		t.Errorf("order = %s, %s; want items.n2 then items.n1 (purge_at, not key order)", rows[1].id, rows[2].id)
	}
	if got := rows[1].str(); !strings.Contains(got, "●") || !strings.Contains(got, "second") || !strings.Contains(got, "2") {
		t.Errorf("marked row = %q", got)
	}
	if strings.Contains(rows[2].str(), "●") {
		t.Errorf("unmarked row carries the mark: %q", rows[2].str())
	}
}

// Opening a row shows its subtree nested, and its leaves stay editable.
func TestAnOpenedTableRowNestsItsKeys(t *testing.T) {
	src := reapedShaped()
	v := &formView{src: src, open: map[string]bool{}}
	v.setView(reapedView)
	v.Items(80)
	v.Activate("items.n1")
	rows := v.Items(80)
	if formRowText(rows, "items.n1.mantra") == "" {
		t.Fatalf("items.n1.mantra is not shown under the opened row: %#v", rows)
	}
	if line, note := v.changeLine("items.n1.mantra", "host"); line == "" {
		t.Errorf("a nested leaf cannot be changed: %s", note)
	}
}

// Without a hint, or with one naming a path the form lacks, the pit nests.
func TestAViewOnAMissingListFallsBackToNesting(t *testing.T) {
	v := &formView{src: reapedShaped(), open: map[string]bool{}}
	v.setView(&rpc.FormView{List: "nowhere"})
	if formRowText(v.Items(80), "items") == "" {
		t.Error("a view naming no list hid the form")
	}
}

func tableTranscript(t *testing.T, v *formView) *transcript {
	t.Helper()
	tr := newTranscript(io.Discard, 100, 30, ldrender.NodeText{}, aria.NewClient(), "aria1234", time.Unix(0, 0))
	tr.enter()
	tr.showLivePit("reaped", v, false)
	tr.pit.lines(100, 20)
	return tr
}

// The pit binds X to "delete now" for every list; a view that names X as an
// action takes it first, so the press reaches the view, not the pit's drop.

// A form pit's row is a path, not an aria: x and X send nothing, so they
// must not take the row off the screen either.
func TestDropKeysLeaveAFormPitsRowsAlone(t *testing.T) {
	v := &formView{src: reapedShaped(), open: map[string]bool{}}
	v.setView(reapedView)
	tr := tableTranscript(t, v)
	tr.dropRow = func(pit, id, next string) { t.Errorf("a drop was sent for %s", id) }
	before := len(tr.pit.list().rows)
	tr.key('x')
	tr.key('X')
	if after := len(tr.pit.list().rows); after != before {
		t.Errorf("x/X removed a form row (%d -> %d)", before, after)
	}
}
