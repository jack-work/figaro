package form

import (
	"encoding/json"
	"reflect"
	"testing"
	"text/template"
)

// THE EQUIVALENCE ORACLE FOR THE FOLD.
//
// Seven sites folded form patches forward, four of them rendering each patch
// against the board as it stood BEFORE that patch. Fold and FoldRender replace
// them. The old loops are kept HERE, permanently, because an equivalence claim
// is a fact about every day after the change, not the day of it: delete the
// oracle and the corpus degrades into hard-coded strings some later, subtly
// different implementation will also satisfy.

// oldFold is the loop as it appeared at projection.go:135, :156, :239.
func oldFold(s Snapshot, patches []Patch) Snapshot {
	for _, p := range patches {
		s = s.Apply(p)
	}
	return s
}

// oldRenderLoop is the shape shared by anthropic.renderPatchBlocks,
// anthropicsdk.renderPatchBlocks, openaichat.renderPatches and copilot's
// inline loop: render against the pre-patch board, then advance.
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
		p := Patch{Set: map[string]json.RawMessage{}}
		for i := 0; i+1 < len(kv); i += 2 {
			b, err := json.Marshal(kv[i+1])
			if err != nil {
				t.Fatal(err)
			}
			p.Set[kv[i]] = b
		}
		return p
	}
	rm := func(keys ...string) Patch { return Patch{Remove: keys} }
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

// asMap materializes a snapshot for comparison. Test-only: the product never
// needs it, which is why Snapshot does not carry one.
func asMap(s Snapshot) map[string]string {
	out := map[string]string{}
	for k, v := range s.All() {
		out[k] = string(v)
	}
	return out
}

func startingBoards() []Snapshot {
	empty := Snapshot{}
	seeded := FromMap(map[string]json.RawMessage{
		"mantra":       json.RawMessage(`"before"`),
		"system.model": json.RawMessage(`"m"`),
		"a":            json.RawMessage(`"seeded"`),
	})
	return []Snapshot{empty, seeded}
}

func TestFoldEqualsTheLoopItReplaced(t *testing.T) {
	for bi, board := range startingBoards() {
		for ci, patches := range foldCorpus(t) {
			want := oldFold(board, patches)
			got := Fold(board, patches)
			if !reflect.DeepEqual(asMap(want), asMap(got)) {
				t.Fatalf("board %d corpus %d: Fold diverged from the loop it replaced\n want %v\n got  %v",
					bi, ci, asMap(want), asMap(got))
			}
			// Identity is part of the contract: a no-op fold must not mint a
			// new root, which is what makes an empty patch free.
			if len(patches) == 0 && got.root != board.root {
				t.Fatalf("board %d: an empty fold minted a new root", bi)
			}
		}
	}
}

// orderSensitiveTemplates render Entry.Old, which is computed against the
// board handed to Render. The DEFAULT templates print only the new value, so
// an oracle built on them cannot tell "render then apply" from "apply then
// render" -- proven: a canary that advanced the board first PASSED against
// them. Ordering is the whole contract, so the oracle carries a template that
// can see it.
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
	for bi, board := range startingBoards() {
		for ci, patches := range foldCorpus(t) {
			wantOut, wantSnap, wantErrs := oldRenderLoop(board, patches, tmpls)

			var gotOut []string
			var gotErrs []error
			gotSnap := FoldRender(board, patches, tmpls,
				func(r RenderedEntry) { gotOut = append(gotOut, r.Key+"="+r.Body) },
				func(err error) { gotErrs = append(gotErrs, err) })

			if !reflect.DeepEqual(wantOut, gotOut) {
				t.Fatalf("board %d corpus %d: rendered entries diverged\n want %q\n got  %q",
					bi, ci, wantOut, gotOut)
			}
			if len(wantErrs) != len(gotErrs) {
				t.Fatalf("board %d corpus %d: %d errors vs %d", bi, ci, len(wantErrs), len(gotErrs))
			}
			if !reflect.DeepEqual(asMap(wantSnap), asMap(gotSnap)) {
				t.Fatalf("board %d corpus %d: final board diverged\n want %v\n got  %v",
					bi, ci, asMap(wantSnap), asMap(gotSnap))
			}
		}
	}
}

// THE ORDER IS THE POINT. A patch must render against the board BEFORE it is
// applied; rendering after would show a key's new value as its old context and
// no output comparison above would necessarily catch it on a corpus where the
// template ignores the previous value.
// THE ORDER IS THE POINT, asserted through the render itself rather than
// through a loop the test writes for itself -- the first version of this test
// recomputed the boards on the side and would have passed against ANY
// implementation of FoldRender, including one that never called back.
func TestFoldRenderSeesTheBoardBeforeEachPatch(t *testing.T) {
	tmpls := orderSensitiveTemplates(t)
	board := FromMap(map[string]json.RawMessage{"mantra": json.RawMessage(`"first"`)})
	patches := []Patch{
		{Set: map[string]json.RawMessage{"mantra": json.RawMessage(`"second"`)}},
		{Set: map[string]json.RawMessage{"mantra": json.RawMessage(`"third"`)}},
	}
	var bodies []string
	final := FoldRender(board, patches, tmpls,
		func(r RenderedEntry) { bodies = append(bodies, r.Body) },
		func(err error) { t.Fatalf("render: %v", err) })

	want := []string{"first -> second", "second -> third"}
	if !reflect.DeepEqual(bodies, want) {
		t.Fatalf("rendered %q, want %q: each patch must render against the board BEFORE it",
			bodies, want)
	}
	if v, _ := final.Get("mantra"); string(v) != `"third"` {
		t.Fatalf("final board is %s, want \"third\"", v)
	}
}
