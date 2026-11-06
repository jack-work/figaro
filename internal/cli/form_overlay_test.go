package cli

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

const (
	ovA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	ovB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	ovC = "cccccccccccccccccccccccccccccccc"
)

var overlayView = &rpc.FormView{
	Rows:    []string{"arias.*", "**.forks.*"},
	Open:    []string{"arias", "**.forks"},
	RowAria: "id",
	Through: "forks",
	Label:   "mantra",
	Overlay: &rpc.ViewOverlay{Form: rpc.TopologyFormID, Key: "parent"},
	Actions: []rpc.ViewAction{{Key: "x", Label: "kill", Method: rpc.MethodKill, Confirm: true}},
}

// b forks from a; c forks from b but is drawn under a, which only the
// override form says.
func overlayRig(over ...string) (*formView, *fakeFormSource, *fakeFormSource) {
	base := &fakeFormSource{snap: form.FromMap(formKeys(
		"arias.a.id", `"`+ovA+`"`, "arias.a.mantra", `"first"`,
		"arias.a.forks.b.id", `"`+ovB+`"`, "arias.a.forks.b.parent", `"`+ovA+`"`,
		"arias.a.forks.c.id", `"`+ovC+`"`, "arias.a.forks.c.parent", `"`+ovB+`"`,
	))}
	ov := &fakeFormSource{snap: form.FromMap(formKeys(over...))}
	v := &formView{src: base, open: map[string]bool{}, aria: rpc.AriasFormID}
	v.setView(overlayView)
	v.setAccess(&rpc.FormAccess{Mode: rpc.FormReadOnly})
	v.overlay = &formView{src: ov, open: map[string]bool{}}
	return v, base, ov
}

func rowNamed(rows []pitRow, id string) (pitRow, bool) {
	for _, r := range rows {
		if r.id == id {
			return r, true
		}
	}
	return pitRow{}, false
}

func openAll(v *formView, paths ...string) []pitRow {
	v.Items(100)
	for _, p := range paths {
		v.Activate(p)
		v.Items(100)
	}
	return v.Items(100)
}

// The overlay draws the override's value in place of the base's, marks the
// row it moved, and shows the base value on the opened key.
func TestAnOverlayDrawsTheEffectiveValueAndMarksTheRow(t *testing.T) {
	v, _, _ := overlayRig(ovC, `"`+ovA+`"`)
	rows := openAll(v, "arias.a", "arias.a.forks.c", "arias.a.forks.b")

	c, _ := rowNamed(rows, "arias.a.forks.c")
	if !strings.Contains(c.note, "override") {
		t.Errorf("the overridden row's note = %q, want it marked", c.note)
	}
	b, _ := rowNamed(rows, "arias.a.forks.b")
	if strings.Contains(b.note, "override") {
		t.Errorf("a row drawn where its base puts it is marked: %q", b.note)
	}
	parent, ok := rowNamed(rows, "arias.a.forks.c.parent")
	if !ok {
		t.Fatalf("the opened row does not show its parent key: %#v", rows)
	}
	if !strings.Contains(parent.str(), ovA) || strings.Contains(parent.str(), ovB) {
		t.Errorf("c.parent draws %q, want the override %s", parent.str(), ovA)
	}
	if parent.note != "base b" {
		t.Errorf("c.parent's note = %q, want the base value by its row's name, base b", parent.note)
	}
	if bp, _ := rowNamed(rows, "arias.a.forks.b.parent"); bp.note != "" {
		t.Errorf("a key with no override carries a note: %q", bp.note)
	}
}

// c and x on the overridden key write the override form, never the base,
// and name the form on the base's node.
func TestOverlayEditsGoToTheOverrideForm(t *testing.T) {
	v, _, _ := overlayRig(ovC, `"`+ovA+`"`)
	openAll(v, "arias.a", "arias.a.forks.c", "arias.a.forks.b")

	line, note := v.changeLine("arias.a.forks.c.parent", "")
	if want := "form set --id @topology " + ovC + ` '"` + ovA + `"'`; line != want {
		t.Errorf("c on the override = %q (%q), want %q", line, note, want)
	}
	if line, ok := v.overlayLine("arias.a.forks.c.parent", true); !ok || line != "form unset --id @topology "+ovC {
		t.Errorf("x on the override = %q, %v", line, ok)
	}
	if _, ok := v.overlayLine("arias.a.forks.b.parent", true); ok {
		t.Error("x on a key with no override offered to clear one")
	}
	if line, _ := v.changeLine("arias.a.forks.b.parent", ""); !strings.HasPrefix(line, "form set --id @topology "+ovB+" ") {
		t.Errorf("c on a key with no override = %q, want a new override for b", line)
	}
	if _, note := v.changeLine("arias.a.mantra", ""); note == "" {
		t.Error("c on a base key of a read-only form was offered")
	}

	v.aria = "spain" + rpc.NodeSeparator + rpc.AriasFormID
	if line, _ := v.overlayLine("arias.a.forks.c.parent", true); !strings.Contains(line, "--id spain"+rpc.NodeSeparator+rpc.TopologyFormID+" ") {
		t.Errorf("an overlay loaded from spain clears through %q", line)
	}
}

// x belongs to the overlay on an overridden key and to the view's own action
// everywhere else.
func TestXClearsOnTheOverrideAndKillsElsewhere(t *testing.T) {
	v, _, _ := overlayRig(ovC, `"`+ovA+`"`)
	openAll(v, "arias.a", "arias.a.forks.c")
	onOverride := true
	cleared, acted := 0, 0
	v.clear = func() bool {
		if onOverride {
			cleared++
		}
		return onOverride
	}
	v.act = func(rpc.ViewAction) { acted++ }
	if !v.OwnsKey('x') || !v.Key('x') || cleared != 1 || acted != 0 {
		t.Errorf("x on the override: cleared %d, acted %d", cleared, acted)
	}
	onOverride = false
	if !v.Key('x') || acted != 1 {
		t.Errorf("x off the override did not reach the view's action: acted %d", acted)
	}
}

// Either form may move alone: the view redraws from whichever changed.
func TestBaseAndOverrideUpdateIndependently(t *testing.T) {
	v, base, ov := overlayRig()
	rows := openAll(v, "arias.a", "arias.a.forks.c")
	if c, _ := rowNamed(rows, "arias.a.forks.c"); strings.Contains(c.note, "override") {
		t.Fatalf("with no override the row is marked: %q", c.note)
	}

	ov.snap = form.FromMap(formKeys(ovC, `"`+ovA+`"`))
	rows = v.Items(100)
	if c, _ := rowNamed(rows, "arias.a.forks.c"); !strings.Contains(c.note, "override") {
		t.Errorf("an override that arrived alone is not drawn: %q", c.note)
	}
	if p, _ := rowNamed(rows, "arias.a.forks.c.parent"); !strings.Contains(p.str(), ovA) {
		t.Errorf("c.parent = %q after the override arrived", p.str())
	}

	base.snap = base.snap.SetPath("arias.a.forks.c.mantra", []byte(`"renamed"`))
	rows = v.Items(100)
	c, _ := rowNamed(rows, "arias.a.forks.c")
	if m, _ := rowNamed(rows, "arias.a.forks.c.mantra"); !strings.Contains(c.note, "override") || !strings.Contains(m.str(), "renamed") {
		t.Errorf("a base change lost the override or the change: mantra %q, note %q", m.str(), c.note)
	}

	ov.snap = form.Snapshot{}
	rows = v.Items(100)
	if c, _ := rowNamed(rows, "arias.a.forks.c"); strings.Contains(c.note, "override") {
		t.Errorf("a cleared override still marks the row: %q", c.note)
	}
	if p, _ := rowNamed(rows, "arias.a.forks.c.parent"); !strings.Contains(p.str(), ovB) || p.note != "" {
		t.Errorf("after the clear c.parent = %q (note %q), want the base %s", p.str(), p.note, ovB)
	}
}
