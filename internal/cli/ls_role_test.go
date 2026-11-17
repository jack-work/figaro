package cli

import (
	"encoding/json"
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

// figaro ls draws the role where :ls does, before the mantra and in the
// holder colour, and -j carries it as the addresses the list returned.
func TestFigaroLsShowsTheRoleBeforeTheMantra(t *testing.T) {
	figs := []rpc.FigaroInfoResponse{
		{ID: ovA, Vector: []int{1}, Mantra: "alpha one", Roles: []string{"@reviewer"}},
		{ID: ovB, Vector: []int{2}, Mantra: "bravo two"},
	}
	draw := func() map[string]string {
		tree, _ := listTree(figs, "", -1)
		out := map[string]string{}
		for _, line := range strings.Split(renderListRows(tree.Rows(), 120, false), "\n") {
			for _, m := range []string{"alpha one", "bravo two"} {
				if strings.Contains(stripANSI(line), m) {
					out[m] = line
				}
			}
		}
		return out
	}
	plain := draw()
	if got := plain["alpha one"]; !strings.Contains(got, "@reviewer  alpha one") {
		t.Errorf("alpha's row %q does not lead its mantra with the role", got)
	}
	if strings.Contains(plain["bravo two"], "@") {
		t.Errorf("bravo holds no role: %q", plain["bravo two"])
	}
	restore := term.SetColorMode(term.ColorAlways)
	colour := draw()
	restore()
	if got := colour["alpha one"]; !strings.Contains(got, term.HolderBody()) || !strings.Contains(got, "@reviewer") {
		t.Errorf("alpha's role is not in the holder colour: %q", got)
	}
	raw, err := json.Marshal(displayed(&figs[0]))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"roles":["@reviewer"]`) {
		t.Errorf("ls -j does not carry the role: %s", raw)
	}
}

// A figaro holding three long-named roles pushed its label past the tree
// column and wrapped every row of the listing; the role and the mantra share
// the label's room.
func TestFigaroLsRolesShareTheLabelsRoom(t *testing.T) {
	long := "a mantra long enough to fill the whole label column by itself"
	plain := rpc.FigaroInfoResponse{ID: ovA, Vector: []int{1}, Mantra: long}
	roled := plain
	roled.Roles = []string{"@Coordinador de Spain", "@King of Spain", "@paperd-figaro-identity-plan"}
	cell := func(f rpc.FigaroInfoResponse) string {
		tree, _ := listTree([]rpc.FigaroInfoResponse{f}, "", -1)
		return tree.Rows()[0].Cell()
	}
	if a, b := displayWidth(cell(plain)), displayWidth(cell(roled)); b > a {
		t.Fatalf("roles widened the cell from %d to %d: %q", a, b, cell(roled))
	}
	if got := cell(roled); !strings.HasPrefix(stripANSI(got), `○ "@Coordinador`) {
		t.Errorf("the role does not lead the cell: %q", got)
	}
}

// Role names hold spaces ("@El Zapador"); the badge quotes such an address
// so the roles on a row still read apart, and a cut name keeps its quotes.
func TestFigaroLsRoleBadgeKeepsAddressesApart(t *testing.T) {
	for _, tc := range []struct {
		roles []string
		want  string
	}{
		{[]string{"@El Zapador", "@edge"}, `"@El Zapador" @edge`},
		{[]string{"@reviewer"}, "@reviewer"},
		{[]string{"@Coordinador de Spain", "@King of Spain"}, `"@Coordinador de .." +1`},
		{[]string{"@a", "@b c", "@paperd-figaro-identity-plan"}, `@a "@b c" +1`},
	} {
		if got := roleBadge(rpc.FigaroInfoResponse{Roles: tc.roles}); got != tc.want {
			t.Errorf("%q badge %s, want %s", tc.roles, got, tc.want)
		}
	}
}
