package form

import (
	"encoding/json"
	"reflect"
	"testing"
	"text/template"
)

// oldFold and oldRenderLoop are the loops Fold and FoldRender replaced, kept
// as the equivalence oracle.
func oldFold(s Snapshot, patches []Patch) Snapshot {
	for _, p := range patches {
		s = s.Apply(p)
	}
	return s
}

func oldRenderLoop(s Snapshot, patches []Patch, tmpls *template.Template) ([]string, Snapshot, []error) {
	var out []string
	var errs []error
	for _, p := range patches {
		rendered, err := Render(p, s, tmpls)
		if err != nil {
			errs = append(errs, err)
		} else {
			for _, r := range rendered {
				out = append(out, r.Key+"="+r.Body)
			}
		}
		s = s.Apply(p)
	}
	return out, s, errs
}

func foldCorpus(t *testing.T) [][]Patch {
	t.Helper()
	set := func(kv ...string) Patch {
		m := map[string]json.RawMessage{}
		for i := 0; i+1 < len(kv); i += 2 {
			b, err := json.Marshal(kv[i+1])
			if err != nil {
				t.Fatal(err)
			}
			m[kv[i]] = b
		}
		return Build(Snapshot{}, m, nil)
	}
	rm := func(keys ...string) Patch { return Build(Snapshot{}, nil, keys) }
	return [][]Patch{
		nil,
		{},
		{Patch{}},
		{set("mantra", "one")},
		{set("mantra", "one"), set("mantra", "two")},
		{set("a", "1"), set("b", "2"), set("c", "3")},
		{set("a", "1"), rm("a")},
		{rm("never-existed")},
		{set("a", "1"), rm("a"), set("a", "2")},
		{set("system.model", "m"), set("mantra", "visible")},
		{set("a", "1"), Patch{}, set("b", "2")},
		{set("k", "<&\">"), set("k", "plain")},
		{set("a", "1"), set("b", "2"), rm("a"), set("c", "3"), rm("b")},
	}
}

func asMap(s Snapshot) map[string]string {
	out := map[string]string{}
	for k, v := range s.All() {
		out[k] = string(v)
	}
	return out
}

func startingBoundForms() []Snapshot {
	empty := Snapshot{}
	seeded := FromMap(map[string]json.RawMessage{
		"mantra":       json.RawMessage(`"before"`),
		"system.model": json.RawMessage(`"m"`),
		"a":            json.RawMessage(`"seeded"`),
	})
	return []Snapshot{empty, seeded}
}

func TestFoldEqualsTheLoopItReplaced(t *testing.T) {
	for bi, bound := range startingBoundForms() {
		for ci, patches := range foldCorpus(t) {
			want := oldFold(bound, patches)
			got := Fold(bound, patches)
			if !reflect.DeepEqual(asMap(want), asMap(got)) {
				t.Fatalf("bound form %d corpus %d: Fold diverged from the loop it replaced\n want %v\n got  %v",
					bi, ci, asMap(want), asMap(got))
			}
			if len(patches) == 0 && !got.Root().Equal(bound.Root()) {
				t.Fatalf("bound form %d: an empty fold minted a new root", bi)
			}
		}
	}
}

// orderSensitiveTemplates render Entry.Old: the default templates print only
// the new value and cannot tell "render then apply" from the reverse.
func orderSensitiveTemplates(t *testing.T) *template.Template {
	t.Helper()
	base, err := LoadDefaultTemplates()
	if err != nil {
		t.Fatalf("templates: %v", err)
	}
	root, err := base.Clone()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := root.New("mantra").Parse(`{{.OldString}} -> {{.NewString}}`); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestFoldRenderEqualsTheLoopItReplaced(t *testing.T) {
	tmpls := orderSensitiveTemplates(t)
	for bi, bound := range startingBoundForms() {
		for ci, patches := range foldCorpus(t) {
			wantOut, wantSnap, wantErrs := oldRenderLoop(bound, patches, tmpls)

			var gotOut []string
			var gotErrs []error
			gotSnap := FoldRender(bound, patches, tmpls,
				func(r RenderedEntry) { gotOut = append(gotOut, r.Key+"="+r.Body) },
				func(err error) { gotErrs = append(gotErrs, err) })

			if !reflect.DeepEqual(wantOut, gotOut) {
				t.Fatalf("bound form %d corpus %d: rendered entries diverged\n want %q\n got  %q",
					bi, ci, wantOut, gotOut)
			}
			if len(wantErrs) != len(gotErrs) {
				t.Fatalf("bound form %d corpus %d: %d errors vs %d", bi, ci, len(wantErrs), len(gotErrs))
			}
			if !reflect.DeepEqual(asMap(wantSnap), asMap(gotSnap)) {
				t.Fatalf("bound form %d corpus %d: final bound form diverged\n want %v\n got  %v",
					bi, ci, asMap(wantSnap), asMap(gotSnap))
			}
		}
	}
}

func TestFoldRenderSeesTheBoundFormBeforeEachPatch(t *testing.T) {
	tmpls := orderSensitiveTemplates(t)
	bound := FromMap(map[string]json.RawMessage{"mantra": json.RawMessage(`"first"`)})
	patches := []Patch{
		Build(Snapshot{}, map[string]json.RawMessage{"mantra": json.RawMessage(`"second"`)}, nil),
		Build(Snapshot{}, map[string]json.RawMessage{"mantra": json.RawMessage(`"third"`)}, nil),
	}
	var bodies []string
	final := FoldRender(bound, patches, tmpls,
		func(r RenderedEntry) { bodies = append(bodies, r.Body) },
		func(err error) { t.Fatalf("render: %v", err) })

	want := []string{"first -> second", "second -> third"}
	if !reflect.DeepEqual(bodies, want) {
		t.Fatalf("rendered %q, want %q: each patch must render against the bound form BEFORE it",
			bodies, want)
	}
	if v, _ := final.Get("mantra"); string(v) != `"third"` {
		t.Fatalf("final bound form is %s, want \"third\"", v)
	}
}
