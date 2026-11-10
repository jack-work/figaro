package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/jack-work/figaro/api/form"
)

// testdata/ls_search holds a copy of 59 live figaros' @arias rows (personal
// mantras reworded, ids rehashed) and one attended figaro's bound form, the
// one :ls mounts under its row.

func lsSearchFixture(tb testing.TB) (arias map[string]any, attended map[string]any) {
	tb.Helper()
	read := func(name string) map[string]any {
		b, err := os.ReadFile(filepath.Join("testdata", "ls_search", name))
		if err != nil {
			tb.Fatal(err)
		}
		var out map[string]any
		if err := json.Unmarshal(b, &out); err != nil {
			tb.Fatal(err)
		}
		return out
	}
	return read("arias.json"), read("attended.json")
}

func flatKeys(prefix string, v any, out map[string]json.RawMessage) {
	if m, ok := v.(map[string]any); ok {
		for k, c := range m {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			flatKeys(p, c, out)
		}
		return
	}
	raw, _ := json.Marshal(v)
	out[prefix] = raw
}

func snapOfJSON(v any) form.Snapshot {
	m := map[string]json.RawMessage{}
	flatKeys("", v, m)
	return form.FromMap(m)
}

type ariaRow struct {
	path, id, mantra string
}

func ariaRows(level map[string]any, prefix string) []ariaRow {
	var out []ariaRow
	keys := make([]string, 0, len(level))
	for k := range level {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		r, ok := level[k].(map[string]any)
		if !ok {
			continue
		}
		p := prefix + "." + k
		id, _ := r["id"].(string)
		mantra, _ := r["mantra"].(string)
		out = append(out, ariaRow{path: p, id: id, mantra: mantra})
		if forks, ok := r["forks"].(map[string]any); ok {
			out = append(out, ariaRows(forks, p+".forks")...)
		}
	}
	return out
}

// lsRig is :ls as the pager opens it: the troupe's index, its @arias mounted
// under the local node, the attended figaro opened and its form mounted.
func lsRig(arias form.Snapshot, rows []ariaRow, attendedID string, attended form.Snapshot) (*fakeMounts, *formView) {
	hint := *ariasLikeHint
	hint.Mount, hint.Sort = "id", "-active_at"
	f := newFakeMounts()
	f.hint, f.plain = &hint, map[string]bool{}
	f.forms["@things"] = &fakeFormSource{snap: arias}
	small := figaroForm("a figaro")
	for _, r := range rows {
		f.forms[r.id], f.plain[r.id] = small, true
	}
	f.forms[attendedID] = &fakeFormSource{snap: attended}
	src := &fakeFormSource{snap: form.FromMap(formKeys("here.self", `true`, "here.form", `"@things"`))}
	v := &formView{src: src, open: map[string]bool{}, opener: f.open}
	v.setView(indexHint)
	v.subject = attending(attendedID)
	seekAttended(v, f.open)
	v.seekMarked()
	return f, v
}
