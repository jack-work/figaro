package formgen_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/formgen"
)

// Laws a replica leans on, over snapshots rather than bare values, and over
// patches applied to states other than the one they were made against: a
// replica's draft lands on whatever the server holds by then.

const replicaTrials = 3000

type trial struct {
	t    *testing.T
	seed uint64
	g    *formgen.Gen
}

func trials(t *testing.T, base uint64, n int, f func(tr trial) string) {
	t.Helper()
	failed := 0
	for i := 0; i < n; i++ {
		seed := base*1_000_003 + uint64(i)
		if msg := f(trial{t: t, seed: seed, g: formgen.New(seed)}); msg != "" {
			failed++
			if failed <= 3 {
				t.Errorf("seed %d: %s", seed, msg)
			}
		}
	}
	if failed > 3 {
		t.Errorf("%d of %d trials failed (first three shown)", failed, n)
	}
}

func show(s form.Snapshot) string { return formgen.JSON(s) }
func showP(p form.Patch) string   { return formgen.PatchJSON(p) }

// The two apply paths, on a Value and on the tree, must be one rule set: the
// server folds through the tree, a client may hold a Value, and a replica
// that disagrees with its server jumps when the canonical patch arrives.
func TestReplicaLaw_TreeApplyAgreesWithValueApply(t *testing.T) {
	trials(t, 1, replicaTrials, func(tr trial) string {
		s := tr.g.Snapshot()
		base := s
		if tr.g.R.IntN(2) == 0 {
			base = tr.g.Snapshot()
		}
		p := tr.g.Patch(base)
		tree := s.Apply(p)
		v, err := p.Apply(s.Root())
		if err != nil {
			return fmt.Sprintf("value apply refused (%v)\n s=%s\n p=%s\n tree=%s", err, show(s), showP(p), show(tree))
		}
		if !v.Equal(tree.Root()) {
			return fmt.Sprintf("disagree\n s=%s\n p=%s\n value=%s\n tree=%s", show(s), showP(p), v.Raw(), show(tree))
		}
		return ""
	})
}

// The same check restricted to patches made against the state they land on:
// what every single-writer form does today.
func TestReplicaLaw_TreeApplyAgreesOnOwnBase(t *testing.T) {
	trials(t, 2, replicaTrials, func(tr trial) string {
		s := tr.g.Snapshot()
		p := tr.g.Patch(s)
		tree := s.Apply(p)
		v, err := p.Apply(s.Root())
		if err != nil {
			return fmt.Sprintf("value apply refused (%v)\n s=%s\n p=%s", err, show(s), showP(p))
		}
		if !v.Equal(tree.Root()) {
			return fmt.Sprintf("disagree\n s=%s\n p=%s\n value=%s\n tree=%s", show(s), showP(p), v.Raw(), show(tree))
		}
		return ""
	})
}

func TestReplicaLaw_SnapshotDiff(t *testing.T) {
	trials(t, 3, replicaTrials, func(tr trial) string {
		a, b := tr.g.Snapshot(), tr.g.Snapshot()
		if tr.g.R.IntN(2) == 0 {
			b = a.Apply(tr.g.Patch(a))
		}
		d := b.Diff(a)
		if got := a.Apply(d); !formgen.Same(got, b) {
			return fmt.Sprintf("apply(diff(A,B),A) != B\n a=%s\n b=%s\n d=%s\n got=%s", show(a), show(b), showP(d), show(got))
		}
		if !a.Diff(a).IsIdentity() {
			return "diff(A,A) is not identity: " + show(a)
		}
		if back := b.Apply(d.Inverse()); !formgen.Same(back, a) {
			return fmt.Sprintf("apply(inverse(diff(A,B)),B) != A\n a=%s\n b=%s\n d=%s\n back=%s", show(a), show(b), showP(d), show(back))
		}
		return ""
	})
}

// Merge on the state the first patch was made against: a debounce window's
// draft, applied where it was typed.
func TestReplicaLaw_MergeOnOwnBase(t *testing.T) {
	trials(t, 4, replicaTrials, func(tr trial) string {
		s := tr.g.Snapshot()
		p := tr.g.Patch(s)
		s1 := s.Apply(p)
		q := tr.g.Patch(s1)
		want := s1.Apply(q)
		if got := s.Apply(form.Merge(p, q)); !formgen.Same(got, want) {
			return fmt.Sprintf("apply(merge(P,Q),S) != apply(Q,apply(P,S))\n s=%s\n p=%s\n q=%s\n merged=%s\n got=%s\n want=%s",
				show(s), showP(p), showP(q), showP(form.Merge(p, q)), show(got), show(want))
		}
		return ""
	})
}

// Merge on a state that moved under the draft: the server applies the merged
// draft to whatever it holds, while the replica showed the edits one by one.
func TestReplicaLaw_MergeOffBase(t *testing.T) {
	trials(t, 5, replicaTrials, func(tr trial) string {
		s := tr.g.Snapshot()
		p := tr.g.Patch(s)
		q := tr.g.Patch(s.Apply(p))
		other := s.Apply(tr.g.Patch(s))
		want := other.Apply(p).Apply(q)
		if got := other.Apply(form.Merge(p, q)); !formgen.Same(got, want) {
			return fmt.Sprintf("off base: apply(merge(P,Q),T) != apply(Q,apply(P,T))\n t=%s\n p=%s\n q=%s\n got=%s\n want=%s",
				show(other), showP(p), showP(q), show(got), show(want))
		}
		return ""
	})
}

// What the server broadcasts is apply-then-diff against its own state. A
// replica that missed nothing applies it to an identical state, so the patch
// must describe that state exactly: sets only of absent keys, deletes and
// updates only of present ones, every Before equal to what is there.
func TestReplicaLaw_CanonicalPatchTouchesOnlyWhatExists(t *testing.T) {
	trials(t, 6, replicaTrials, func(tr trial) string {
		s := tr.g.Snapshot()
		base := s
		if tr.g.R.IntN(2) == 0 {
			base = tr.g.Snapshot()
		}
		next := s.Apply(tr.g.Patch(base))
		c := next.Diff(s)
		if got := s.Apply(c); !formgen.Same(got, next) {
			return fmt.Sprintf("canonical does not reproduce\n s=%s\n c=%s", show(s), showP(c))
		}
		if bad := describes(s.Root(), c, ""); bad != "" {
			return fmt.Sprintf("%s\n s=%s\n c=%s", bad, show(s), showP(c))
		}
		if back := next.Apply(c.Inverse()); !formgen.Same(back, s) {
			return fmt.Sprintf("inverse of canonical does not restore\n s=%s\n c=%s\n back=%s", show(s), showP(c), show(back))
		}
		raw, err := json.Marshal(c)
		if err != nil {
			return err.Error()
		}
		var wire form.Patch
		if err := json.Unmarshal(raw, &wire); err != nil {
			return err.Error()
		}
		if got := s.Apply(wire); !formgen.Same(got, next) {
			return fmt.Sprintf("canonical changed meaning on the wire\n %s", raw)
		}
		return ""
	})
}

func describes(v form.Value, p form.Patch, at string) string {
	switch {
	case p.Scalar != nil:
		if !p.Scalar.Before.Equal(v) {
			return fmt.Sprintf("%s: scalar Before %s, state holds %s", at, p.Scalar.Before.Raw(), v.Raw())
		}
	case p.Object != nil:
		var members map[string]json.RawMessage
		if json.Unmarshal(v.Raw(), &members) != nil {
			return fmt.Sprintf("%s: object patch on %s", at, v.Raw())
		}
		for k := range p.Object.Set {
			if _, ok := members[k]; ok {
				return fmt.Sprintf("%s: set of present key %q", at, k)
			}
		}
		for k, old := range p.Object.Delete {
			cur, ok := members[k]
			if !ok {
				return fmt.Sprintf("%s: delete of absent key %q", at, k)
			}
			if !old.Equal(form.NewValue(cur)) {
				return fmt.Sprintf("%s.%s: delete Before %s, state holds %s", at, k, old.Raw(), cur)
			}
		}
		for k, c := range p.Object.Update {
			cur, ok := members[k]
			switch {
			case !ok && !p.Object.New[k]:
				return fmt.Sprintf("%s: update of absent key %q not marked New", at, k)
			case ok && p.Object.New[k]:
				return fmt.Sprintf("%s: update of present key %q marked New", at, k)
			case !ok:
				continue
			}
			if bad := describes(form.NewValue(cur), c, strings.TrimPrefix(at+"."+k, ".")); bad != "" {
				return bad
			}
		}
	case p.List != nil:
		return fmt.Sprintf("%s: a canonical patch carries a list patch", at)
	}
	return ""
}

func FuzzReplicaLaws(f *testing.F) {
	for _, s := range []uint64{1, 2, 3, 42, 1 << 40} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, seed uint64) {
		g := formgen.New(seed)
		s := g.Snapshot()
		p := g.Patch(g.Snapshot())
		next := s.Apply(p)
		c := next.Diff(s)
		if got := s.Apply(c); !formgen.Same(got, next) {
			t.Fatalf("canonical does not reproduce\n s=%s\n c=%s", show(s), showP(c))
		}
		if bad := describes(s.Root(), c, ""); bad != "" {
			t.Fatalf("%s\n s=%s\n c=%s", bad, show(s), showP(c))
		}
		q := g.Patch(next)
		if got, want := s.Apply(form.Merge(c, q)), next.Apply(q); !formgen.Same(got, want) {
			t.Fatalf("merge law\n s=%s\n c=%s\n q=%s", show(s), showP(c), showP(q))
		}
	})
}
