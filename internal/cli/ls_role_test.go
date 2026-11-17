package cli

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/term"
)

// Gluck, 11-16: a figaro that holds a role shows the role's name on its :ls
// row beside the mantra; a figaro without one draws exactly as before.
func TestLsRowShowsTheRoleItHolds(t *testing.T) {
	keys := formKeys(
		"arias.alpha.id", `"`+ovA+`"`, "arias.alpha.mantra", `"alpha one"`, "arias.alpha.role", `"@reviewer"`,
		"arias.bravo.id", `"`+ovB+`"`, "arias.bravo.mantra", `"bravo two"`,
		"arias.charlie.id", `"`+ovC+`"`, "arias.charlie.role", `"@boss"`,
	)
	rows := func(badge string) map[string]string {
		view := *overlayView
		view.Overlay, view.Badge = nil, badge
		v := &formView{src: &fakeFormSource{snap: form.FromMap(keys)}, open: map[string]bool{}, aria: rpc.AriasFormID}
		v.setView(&view)
		v.setAccess(&rpc.FormAccess{Mode: rpc.FormReadOnly})
		out := map[string]string{}
		for _, r := range openAll(v) {
			out[r.id] = stripANSI(r.str())
		}
		return out
	}
	with, without := rows("role"), rows("")
	if got := with["arias.alpha"]; !strings.Contains(got, `@reviewer  "alpha one"`) {
		t.Errorf("alpha's row %q does not show its role beside the mantra", got)
	}
	if got := with["arias.charlie"]; !strings.Contains(got, "@boss") {
		t.Errorf("charlie has no mantra; its row %q should still show the role", got)
	}
	if with["arias.bravo"] != without["arias.bravo"] {
		t.Errorf("a figaro with no role changed:\nbadge   %q\nnobadge %q", with["arias.bravo"], without["arias.bravo"])
	}
	if strings.Contains(without["arias.alpha"], "@reviewer") {
		t.Errorf("a view that names no badge drew it anyway: %q", without["arias.alpha"])
	}
}

// Gluck, 11-17: the role is drawn in its own colour, the one that says this
// figaro holds a role, and the mantra after it keeps the row's quiet colour.
// On the selected row the role takes the lighter shade the wash needs.
func TestLsRoleBadgeIsDrawnInTheHolderColour(t *testing.T) {
	defer term.SetColorMode(term.ColorAlways)()
	keys := formKeys(
		"arias.alpha.id", `"`+ovA+`"`, "arias.alpha.mantra", `"alpha one"`, "arias.alpha.role", `"@reviewer"`,
		"arias.bravo.id", `"`+ovB+`"`, "arias.bravo.mantra", `"bravo two"`,
	)
	view := *overlayView
	view.Overlay, view.Badge = nil, "role"
	v := &formView{src: &fakeFormSource{snap: form.FromMap(keys)}, open: map[string]bool{}, aria: rpc.AriasFormID}
	v.setView(&view)
	v.setAccess(&rpc.FormAccess{Mode: rpc.FormReadOnly})
	rows := openAll(v)
	p := newPicker(rows)
	for _, focus := range []string{"arias.bravo", "arias.alpha"} {
		p.focus(focus)
		var alpha, bravo string
		for _, l := range p.lines(pitForm, 80, 10) {
			switch plain := stripANSI(l); {
			case strings.Contains(plain, "alpha one"):
				alpha = l
			case strings.Contains(plain, "bravo two"):
				bravo = l
			}
		}
		holder := term.HolderBody()
		if focus == "arias.alpha" {
			holder = term.HolderOnWash()
		}
		if !strings.Contains(alpha, holder+"@reviewer") {
			t.Errorf("focus %s: alpha's role is not in the holder colour %q: %q", focus, holder, alpha)
		}
		if strings.Contains(alpha, holder+"@reviewer  \"alpha") {
			t.Errorf("focus %s: the mantra took the role's colour: %q", focus, alpha)
		}
		for _, c := range []string{term.HolderBody(), term.HolderOnWash()} {
			if strings.Contains(bravo, c) {
				t.Errorf("focus %s: bravo holds no role but wears the holder colour: %q", focus, bravo)
			}
		}
	}
}
