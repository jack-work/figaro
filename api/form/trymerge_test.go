package form

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"testing"
)

// A tiny key space, so the same keys collide across a base and two patches.
func tmValue(r *rand.Rand, depth int) json.RawMessage {
	switch n := r.IntN(6); {
	case n == 0:
		return json.RawMessage(`null`)
	case n == 1 || depth >= 3:
		return json.RawMessage(fmt.Sprintf(`"v%d"`, r.IntN(4)))
	case n == 2:
		return json.RawMessage(fmt.Sprintf(`%d`, r.IntN(3)))
	default:
		m := map[string]json.RawMessage{}
		for _, k := range []string{"a", "b", "c"} {
			if r.IntN(2) == 0 {
				m[k] = tmValue(r, depth+1)
			}
		}
		b, _ := json.Marshal(m)
		return b
	}
}

func tmSnapshot(r *rand.Rand) Snapshot {
	var s Snapshot
	return s.Apply(Patch{Scalar: &ScalarPatch{After: NewValue(tmObject(r))}})
}

func tmObject(r *rand.Rand) json.RawMessage {
	for {
		v := tmValue(r, 0)
		if _, ok := NewValue(v).members(); ok {
			return v
		}
	}
}

func tmPatch(r *rand.Rand, base Snapshot) Patch {
	return tmSnapshot(r).Diff(base)
}

func encoded(t *testing.T, s Snapshot) string {
	t.Helper()
	b, err := s.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Defends the replica's draft: a merge it accepts must equal applying the two
// patches in turn, on any base, not just the one the edits were made against.
func TestTryMergeIsExactOnEveryBase(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	merged, refused := 0, 0
	for i := 0; i < 3000; i++ {
		b0 := tmSnapshot(r)
		p := tmPatch(r, b0)
		q := tmPatch(r, b0.Apply(p))
		m, ok := TryMerge(p, q)
		if !ok {
			refused++
			continue
		}
		merged++
		for j := 0; j < 8; j++ {
			a := b0
			if j > 0 {
				a = tmSnapshot(r)
			}
			want := encoded(t, a.Apply(p).Apply(q))
			got := encoded(t, a.Apply(m))
			if got != want {
				pj, _ := json.Marshal(p)
				qj, _ := json.Marshal(q)
				mj, _ := json.Marshal(m)
				t.Fatalf("base %s\np %s\nq %s\nm %s\nseq %s\nmerged %s", encoded(t, a), pj, qj, mj, want, got)
			}
		}
	}
	if merged < refused {
		t.Fatalf("merged %d, refused %d: the draft would flush on most edits", merged, refused)
	}
}

// Defends one rule set: a patch applied to a value and to the tree agree.
func TestApplyAgreesOnValueAndTree(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	for i := 0; i < 3000; i++ {
		b0 := tmSnapshot(r)
		p := tmPatch(r, b0)
		a := tmSnapshot(r)
		raw, err := a.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		v, err := p.Apply(NewValue(raw))
		if err != nil {
			t.Fatal(err)
		}
		want := encoded(t, a.Apply(p))
		got := encoded(t, FromValue(v))
		if got != want {
			pj, _ := json.Marshal(p)
			t.Fatalf("base %s\np %s\ntree %s\nvalue %s", raw, pj, want, got)
		}
	}
}

// Defends apply(diff(A,B), A) = B, including a branch that becomes a leaf.
func TestDiffThenApplyReachesTheTarget(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	for i := 0; i < 3000; i++ {
		a, b := tmSnapshot(r), tmSnapshot(r)
		if got, want := encoded(t, a.Apply(b.Diff(a))), encoded(t, b); got != want {
			t.Fatalf("from %s to %s got %s", encoded(t, a), want, got)
		}
	}
}
