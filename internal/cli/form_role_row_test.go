package cli

import (
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

var recencyView = &rpc.FormView{
	Rows:    []string{"arias.*", "**.forks.*"},
	Open:    []string{"arias"},
	RowAria: "id",
	Through: "forks",
	Label:   "mantra",
	Badge:   "role",
	Sort:    "-recency",
}

func recencyRig() *formView {
	src := &fakeFormSource{snap: form.FromMap(formKeys(
		"arias.lone.id", `"a"`, "arias.lone.active_at", "9000", "arias.lone.recency", "9000",
		"arias.busy.id", `"b"`, "arias.busy.active_at", "100", "arias.busy.recency", "20000",
		"arias.busy.forks.fork.id", `"c"`, "arias.busy.forks.fork.active_at", "20000", "arias.busy.forks.fork.recency", "20000",
		"arias.@r1.id", `"@r1"`, "arias.@r1.kind", `"form"`, "arias.@r1.name", `"Figaro Engineer"`, "arias.@r1.target", `"d"`,
		"arias.@r1.active_at", "15000", "arias.@r1.recency", "15000",
		"arias.holder.id", `"d"`, "arias.holder.recency", "14000",
		"arias.@r2.id", `"@r2"`, "arias.@r2.kind", `"form"`, "arias.@r2.name", `"Frontend Strategist"`, "arias.@r2.target", `"unlisted"`,
		"arias.@r2.recency", "12000",
		"arias.stale.id", `"e"`, "arias.stale.recency", "50",
	))}
	v := &formView{src: src, open: map[string]bool{}}
	v.setView(recencyView)
	return v
}

func TestARoleRowIsNamedForTheRoleAndPointsAtItsHolder(t *testing.T) {
	var got []string
	for _, r := range recencyRig().Items(80) {
		got = append(got, r.str())
	}
	want := []string{
		"arias (6)",
		"├─ busy (4)  active_at · forks · id · recency",
		"├─ @Figaro Engineer (6)  → holder",
		"├─ holder: {id: \"d\", recency: 14000}",
		"├─ @Frontend Strategist (5)  → " + ariaName("unlisted"),
		"├─ lone: {active_at: 9000, id: \"a\", recency: 9000}",
		"└─ stale: {id: \"e\", recency: 50}",
	}
	if len(got) != len(want) {
		t.Fatalf("rows %q", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %q, want %q", i, got[i], want[i])
		}
	}
}
