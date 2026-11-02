package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
)

type fakeFormSource struct {
	snap  form.Snapshot
	edits []form.Patch
}

func (s *fakeFormSource) Shown() form.Snapshot { return s.snap }
func (s *fakeFormSource) Version() uint64      { return 0 }
func (s *fakeFormSource) Edit(p form.Patch) error {
	s.edits = append(s.edits, p)
	s.snap = s.snap.Apply(p)
	return nil
}

func formKeys(kv ...string) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for i := 0; i < len(kv); i += 2 {
		out[kv[i]] = json.RawMessage(kv[i+1])
	}
	return out
}

func formRowText(rows []pitRow, id string) string {
	for _, r := range rows {
		if r.id == id {
			return r.str()
		}
	}
	return ""
}

// A keystroke changes nothing the view reads, so it must cost nothing: the
// same rows, the same revision.
func TestFormViewItemsAreMemoised(t *testing.T) {
	src := &fakeFormSource{snap: form.FromMap(formKeys("a", `1`, "b.c", `2`))}
	v := &formView{src: src, open: map[string]bool{}}
	first := v.Items(80)
	rev := v.Revision()
	again := v.Items(80)
	if &first[0] != &again[0] || v.Revision() != rev {
		t.Fatal("an unchanged form rebuilt its rows")
	}
	if v.Items(81); v.Revision() == rev {
		t.Fatal("a new width did not move the revision")
	}
}

// Only the rows a delta touched are rendered again.
func TestFormViewRerendersOnlyChangedRows(t *testing.T) {
	src := &fakeFormSource{snap: form.FromMap(formKeys("a", `"one"`, "b", `"two"`, "g.x", `1`, "g.y", `2`))}
	v := &formView{src: src, open: map[string]bool{"g": true}}
	v.openRev++
	for _, r := range v.Items(80) {
		r.str()
	}
	for _, n := range v.rows {
		if n.path == "b" || n.path == "g.y" {
			n.row = "SENTINEL " + n.path
		}
	}
	src.Edit(form.Build(src.snap, formKeys("a", `"uno"`, "g.x", `9`), nil))
	rows := v.Items(80)
	if got := formRowText(rows, "b"); got != "SENTINEL b" {
		t.Fatalf("an untouched top-level row was rendered again: %q", got)
	}
	if got := formRowText(rows, "g.y"); got != "SENTINEL g.y" {
		t.Fatalf("an untouched row under a touched branch was rendered again: %q", got)
	}
	if got := formRowText(rows, "a"); !strings.Contains(got, "uno") {
		t.Fatalf("the changed row shows %q", got)
	}
	if got := formRowText(rows, "g.x"); !strings.Contains(got, "9") {
		t.Fatalf("the changed nested row shows %q", got)
	}
}

func TestFormViewFollowsShapeChanges(t *testing.T) {
	src := &fakeFormSource{snap: form.FromMap(formKeys("a.b", `1`, "c", `2`))}
	v := &formView{src: src, open: map[string]bool{"a": true}}
	v.openRev++
	v.Items(80)
	src.snap = form.FromMap(formKeys("a", `"flat"`, "c.d", `3`, "e", `4`))
	rows := v.Items(80)
	var ids []string
	for _, r := range rows {
		ids = append(ids, r.id)
	}
	if strings.Join(ids, ",") != "a,a\x000,c,e" {
		t.Fatalf("rows after a branch became a leaf and a leaf a branch: %q", ids)
	}
	if got := formRowText(rows, "c"); !strings.Contains(got, "(1)") {
		t.Fatalf("c is now a branch of one: %q", got)
	}
}

// y asks the selected node; nothing is serialised while painting.
func TestFormViewYanksOnDemand(t *testing.T) {
	src := &fakeFormSource{snap: form.FromMap(formKeys("s.b", `"x"`, "s.a.z", `[1, 2]`, "leaf", `"v"`))}
	v := &formView{src: src, open: map[string]bool{}}
	rows := v.Items(80)
	for _, r := range rows {
		if r.yank != "" {
			t.Fatalf("row %q carries a built yank string", r.id)
		}
	}
	var branch, leaf pitRow
	for _, r := range rows {
		switch r.id {
		case "s":
			branch = r
		case "leaf":
			leaf = r
		}
	}
	want := "{\n  \"a\": {\n    \"z\": [\n      1,\n      2\n    ]\n  },\n  \"b\": \"x\"\n}"
	if got := branch.yankText(); got != want {
		t.Fatalf("branch yank:\n%s\nwant\n%s", got, want)
	}
	if got := leaf.yankText(); got != `"v"` {
		t.Fatalf("leaf yank = %s", got)
	}
}

type countingItemView struct {
	fakeItemView
	rev uint64
}

func (v *countingItemView) Revision() uint64 { return v.rev }

// The pit hands rows to the picker only when the view says they moved.
func TestPitSkipsUnchangedLiveRows(t *testing.T) {
	v := &countingItemView{fakeItemView: fakeItemView{rows: []pitRow{idRow("a", "a"), idRow("b", "b")}}, rev: 1}
	var d pit
	d.showLive(pitID("form show"), v)
	d.lines(80, 10)
	d.moveSelection(1)
	before, _ := d.selected()
	v.rows = []pitRow{idRow("zzz", "zzz")}
	d.lines(80, 10)
	if got, _ := d.selected(); got.id != before.id {
		t.Fatalf("rows were reloaded although the revision held: selected %q", got.id)
	}
	v.rev++
	d.lines(80, 10)
	if got, _ := d.selected(); got.id != "zzz" {
		t.Fatalf("a moved revision did not reload the rows: selected %q", got.id)
	}
}
