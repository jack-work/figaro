package rpc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"testing"

	"github.com/jack-work/figaro/api/form"
)

// A CLI folds form.delta patches with its own copy of api/form. If the fold
// changes, a CLI and a daemon of different builds disagree on a form's state
// without either noticing; FormDeltaSchema is what tells them apart. This
// pins the fold of a fixed corpus to the schema number: change how a patch
// folds and this fails until FormDeltaSchema is bumped and its digest added.
var foldDigests = map[int]string{
	1: "c44385b83f4528677c816e09e5a3202eeec6c770b3f964ec3413f32e6176c313",
	2: "03d49112826bbc3effd53edee9bd856e365d853327c94d2b99cdc3e3e6cd0062",
}

var foldKeys = []string{"a", "b", "c"}

func foldValue(r *rand.Rand, depth int) json.RawMessage {
	switch n := r.IntN(7); {
	case n == 0:
		return json.RawMessage(fmt.Sprintf("%d", r.IntN(100)))
	case n == 1:
		return json.RawMessage(fmt.Sprintf("%q", fmt.Sprintf("s%d", r.IntN(100))))
	case n == 2:
		return json.RawMessage(`{}`)
	case n == 3:
		return json.RawMessage(fmt.Sprintf("[%d,%d]", r.IntN(9), r.IntN(9)))
	case n == 4:
		return json.RawMessage(`true`)
	default:
		if depth >= 2 {
			return json.RawMessage(fmt.Sprintf("%d", r.IntN(100)))
		}
		obj := map[string]json.RawMessage{}
		for range 1 + r.IntN(3) {
			obj[foldKeys[r.IntN(len(foldKeys))]] = foldValue(r, depth+1)
		}
		raw, _ := json.Marshal(obj)
		return raw
	}
}

func foldPath(r *rand.Rand) string {
	p := foldKeys[r.IntN(len(foldKeys))]
	for range r.IntN(3) {
		p += "." + foldKeys[r.IntN(len(foldKeys))]
	}
	return p
}

func foldPatch(r *rand.Rand, base form.Snapshot) form.Patch {
	set := map[string]json.RawMessage{}
	var remove []string
	for range 1 + r.IntN(3) {
		if r.IntN(3) == 0 {
			remove = append(remove, foldPath(r))
		} else {
			set[foldPath(r)] = foldValue(r, 0)
		}
	}
	return form.Build(base, set, remove)
}

func buildFoldCorpus() [][]form.Patch {
	var chains [][]form.Patch

	deleteRepro := form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"aria_id": json.RawMessage(`"x"`), "cwd": json.RawMessage(`"/"`), "mantra": json.RawMessage(`"m"`),
		"name": json.RawMessage(`"n"`), "skills": json.RawMessage(`{"a":{"filePath":"/p"}}`),
		"system": json.RawMessage(`{"model":"m"}`),
	}, nil)
	chains = append(chains, []form.Patch{deleteRepro, form.Build(form.Snapshot{}.Apply(deleteRepro), nil, []string{"name"})})

	branch := form.Build(form.Snapshot{}, map[string]json.RawMessage{"a": json.RawMessage(`{"b":1,"c":2}`)}, nil)
	stale := form.Snapshot{}.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{"a": json.RawMessage(`0`)}, nil))
	chains = append(chains, []form.Patch{branch, form.Build(stale, map[string]json.RawMessage{"a": json.RawMessage(`7`)}, nil)})

	for seed := range uint64(8) {
		r := rand.New(rand.NewPCG(seed, 0x5eed))
		var history []form.Snapshot
		var chain []form.Patch
		s := form.Snapshot{}
		for range 300 {
			base := s
			if len(history) > 0 && r.IntN(4) == 0 {
				base = history[r.IntN(len(history))]
			}
			p := foldPatch(r, base)
			if r.IntN(5) == 0 {
				p = form.Merge(p, foldPatch(r, base.Apply(p)))
			}
			chain = append(chain, p)
			s = s.Apply(p)
			history = append(history, s)
		}
		chains = append(chains, chain)
	}
	return chains
}

const foldCorpusFile = "testdata/fold_corpus.json"

func loadFoldCorpus(t *testing.T) [][]form.Patch {
	t.Helper()
	raw, err := os.ReadFile(foldCorpusFile)
	if err != nil {
		t.Fatal(err)
	}
	var chains [][]form.Patch
	if err := json.Unmarshal(raw, &chains); err != nil {
		t.Fatal(err)
	}
	return chains
}

func foldCorpusDigest(t *testing.T, chains [][]form.Patch) string {
	t.Helper()
	h := sha256.New()
	for _, chain := range chains {
		s := form.Snapshot{}
		for _, p := range chain {
			s = s.Apply(p)
			raw, err := s.MarshalJSON()
			if err != nil {
				t.Fatal(err)
			}
			h.Write(raw)
			h.Write([]byte{'\n'})
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// The corpus is stored, not rebuilt: what is pinned is how a patch folds,
// not how api/form builds one. FOLD_CORPUS_WRITE=1 regenerates it.
func TestFoldCorpusFile(t *testing.T) {
	if os.Getenv("FOLD_CORPUS_WRITE") == "" {
		t.Skip("set FOLD_CORPUS_WRITE=1 to regenerate " + foldCorpusFile)
	}
	raw, err := json.Marshal(buildFoldCorpus())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(foldCorpusFile, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTheFoldIsPinnedToTheDeltaSchema(t *testing.T) {
	got := foldCorpusDigest(t, loadFoldCorpus(t))
	want, ok := foldDigests[FormDeltaSchema]
	if !ok || want != got {
		t.Fatalf("the fold of the corpus under FormDeltaSchema %d is %s, recorded %q.\n"+
			"If the fold rules changed on purpose, bump FormDeltaSchema and record the new digest under it;\n"+
			"an older CLI must stop and refetch rather than fold the new way's patches the old way.",
			FormDeltaSchema, got, want)
	}
}

func TestTheFoldCorpusIsDeterministic(t *testing.T) {
	chains := loadFoldCorpus(t)
	if a, b := foldCorpusDigest(t, chains), foldCorpusDigest(t, chains); a != b {
		t.Fatalf("two folds of the same corpus differ: %s, %s", a, b)
	}
}
