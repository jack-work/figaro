package cli

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

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
	if got := formRowText(rows, "c"); !strings.Contains(got, "{d: 3}") {
		t.Fatalf("c is now a branch of one, drawn inline: %q", got)
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

func formRows(t *testing.T, kv map[string]json.RawMessage, open map[string]bool) []string {
	t.Helper()
	v := &formView{src: &fakeFormSource{snap: form.FromMap(kv)}, open: open}
	v.openRev++
	var out []string
	for _, r := range v.Items(100) {
		out = append(out, r.str())
	}
	return out
}

// Nesting is drawn with guide lines, the last child closing its branch.
func TestFormRowsDrawGuideLines(t *testing.T) {
	rows := formRows(t, formKeys("s.a.x", `1`, "s.a.y", `2`, "s.a.z", `3`, "s.a.w", `4`, "s.b", `5`), map[string]bool{"s": true, "s.a": true})
	want := []string{
		"s (2)",
		"├─ a (4)",
		"│  ├─ w: 4",
		"│  ├─ x: 1",
		"│  ├─ y: 2",
		"│  └─ z: 3",
		"└─ b: 5",
	}
	if strings.Join(rows, "\n") != strings.Join(want, "\n") {
		t.Fatalf("rows:\n%s\nwant:\n%s", strings.Join(rows, "\n"), strings.Join(want, "\n"))
	}
}

// Sibling values line up in one column.
func TestFormLeafValuesAlign(t *testing.T) {
	rows := formRows(t, formKeys("a", `1`, "longer", `2`), map[string]bool{})
	if rows[0] != "a:      1" || rows[1] != "longer: 2" {
		t.Fatalf("values not aligned: %q", rows)
	}
}

// A collapsed branch shows its first keys; a small one of scalars shows inline.
func TestACollapsedBranchPeeksAtItsKeys(t *testing.T) {
	kv := formKeys("skills.brave", `{"filePath":"/a"}`, "skills.cheroot", `1`, "skills.docker", `2`, "skills.figla", `3`, "skills.golang", `4`, "pair.a", `1`, "pair.b", `"x"`)
	rows := formRows(t, kv, map[string]bool{})
	if rows[0] != "pair: {a: 1, b: \"x\"}" {
		t.Fatalf("small branch not inline: %q", rows[0])
	}
	if rows[1] != "skills (5)  brave · cheroot · docker · figla …" {
		t.Fatalf("collapsed branch peek: %q", rows[1])
	}
}

// A millisecond at or *_at value reads as local time in every form, but only
// on screen: c still edits the number the form holds.
func TestAMillisecondAtValueReadsAsLocalTime(t *testing.T) {
	const ms = 1793880298071
	src := &fakeFormSource{snap: form.FromMap(formKeys(
		"refreshed_at", strconv.Itoa(ms),
		"at", strconv.Itoa(ms),
		"count", strconv.Itoa(ms),
		"named_at", `"tomorrow"`,
	))}
	v := &formView{src: src, open: map[string]bool{}, aria: "host"}
	rows := v.Items(120)
	want := time.UnixMilli(ms).Local().Format("2006-01-02 15:04:05")
	if got := formRowText(rows, "refreshed_at"); !strings.HasSuffix(got, want) {
		t.Errorf("refreshed_at = %q, want it ending %q", got, want)
	}
	if got := formRowText(rows, "at"); !strings.HasSuffix(got, want) {
		t.Errorf("at = %q, want it ending %q", got, want)
	}
	if got := formRowText(rows, "count"); !strings.HasSuffix(got, strconv.Itoa(ms)) {
		t.Errorf("count = %q: only an *_at key is a time", got)
	}
	if got := formRowText(rows, "named_at"); !strings.HasSuffix(got, `"tomorrow"`) {
		t.Errorf("named_at = %q: only a number is a time", got)
	}
	if line, _ := v.changeLine("refreshed_at", "host"); !strings.HasSuffix(line, strconv.Itoa(ms)) {
		t.Errorf("c offers %q, want the raw number", line)
	}
}
