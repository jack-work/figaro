package cli

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
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
