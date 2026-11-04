package form_test

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/jack-work/figaro/api/form"
)

type gen struct{ r *rand.Rand }

var genKeys = []string{"a", "b", "c", "system"}
var genItems = []string{"x", "y", "z"}

func (g gen) key() string { return genKeys[g.r.IntN(len(genKeys))] }

func (g gen) scalar() json.RawMessage {
	switch g.r.IntN(5) {
	case 0:
		return json.RawMessage(`null`)
	case 1:
		return json.RawMessage(fmt.Sprintf("%d", g.r.IntN(5)))
	case 2:
		return json.RawMessage(`true`)
	case 3:
		return json.RawMessage(`[1,2]`)
	default:
		return json.RawMessage(fmt.Sprintf(`"s%d"`, g.r.IntN(5)))
	}
}

func (g gen) list() json.RawMessage {
	keys := append([]string(nil), genItems...)
	g.r.Shuffle(len(keys), func(i, j int) { keys[i], keys[j] = keys[j], keys[i] })
	var items []form.KeyedValue
	for _, k := range keys[:g.r.IntN(len(keys)+1)] {
		items = append(items, form.KeyedValue{Key: k, Value: form.NewValue(g.scalar())})
	}
	b, _ := json.Marshal(items)
	return b
}

func (g gen) value(depth int) json.RawMessage {
	switch {
	case depth > 0 && g.r.IntN(3) == 0:
		m := map[string]json.RawMessage{}
		for n := g.r.IntN(3); n > 0; n-- {
			m[g.key()] = g.value(depth - 1)
		}
		b, _ := json.Marshal(m)
		return b
	case g.r.IntN(5) == 0:
		return g.list()
	default:
		return g.scalar()
	}
}

func (g gen) patch(depth int) form.Patch {
	o := &form.ObjectPatch{}
	for n := 1 + g.r.IntN(4); n > 0; n-- {
		k := g.key()
		switch g.r.IntN(9) {
		case 0, 1:
			if o.Set == nil {
				o.Set = map[string]form.Value{}
			}
			o.Set[k] = form.NewValue(g.value(2))
		case 2:
			if o.Delete == nil {
				o.Delete = map[string]form.Value{}
			}
			o.Delete[k] = form.NewValue(json.RawMessage(`null`))
		case 3, 4:
			if depth < 3 {
				update(o, k, g.patch(depth+1))
			}
		case 5:
			update(o, k, form.Patch{Scalar: &form.ScalarPatch{After: form.NewValue(g.value(2))}})
		case 6:
			update(o, k, form.Patch{Object: &form.ObjectPatch{}})
		case 7:
			update(o, k, form.Diff(form.NewValue(g.list()), form.NewValue(g.list())))
		default:
			if o.Set == nil {
				o.Set = map[string]form.Value{}
			}
			o.Set[k] = form.NewValue(json.RawMessage(`{}`))
		}
	}
	return form.Patch{Object: o}
}

func update(o *form.ObjectPatch, k string, p form.Patch) {
	if o.Update == nil {
		o.Update = map[string]form.Patch{}
	}
	o.Update[k] = p
}

func (g gen) snapshot() form.Snapshot {
	var s form.Snapshot
	for n := g.r.IntN(6); n > 0; n-- {
		s = form.OldApply(s, g.patch(0))
	}
	return s
}

func stateJSON(s form.Snapshot) string {
	b, err := json.Marshal(s)
	if err != nil {
		return "ERR " + err.Error()
	}
	return string(b)
}

func patchJSON(p form.Patch) string {
	b, err := json.Marshal(p)
	if err != nil {
		return "ERR " + err.Error()
	}
	return string(b)
}

// ApplyEffective must report exactly what the old Apply-then-Diff path
// reported, land on the same state, and that state must be what replaying
// the reported patch gives.
func TestApplyEffectiveMatchesApplyThenDiff(t *testing.T) {
	g := gen{r: rand.New(rand.NewPCG(20261104, 7))}
	identities := 0
	for i := 0; i < 50000; i++ {
		base, req := g.snapshot(), g.patch(0)
		next, applied := base.ApplyEffective(req)
		oldNext := form.OldApply(base, req)
		oldApplied := form.OldDiff(oldNext, base)
		where := func() string {
			return fmt.Sprintf("case %d\nbase %s\nreq  %s", i, stateJSON(base), patchJSON(req))
		}
		if stateJSON(next) != stateJSON(oldNext) {
			t.Fatalf("state differs\n%s\nnew %s\nold %s", where(), stateJSON(next), stateJSON(oldNext))
		}
		if plain := base.Apply(req); stateJSON(plain) != stateJSON(oldNext) {
			t.Fatalf("Apply differs from the old Apply\n%s\nnew %s\nold %s", where(), stateJSON(plain), stateJSON(oldNext))
		}
		if patchJSON(applied) != patchJSON(oldApplied) {
			t.Fatalf("applied patch differs\n%s\nnew %s\nold %s", where(), patchJSON(applied), patchJSON(oldApplied))
		}
		if replay := form.OldApply(base, applied); stateJSON(replay) != stateJSON(next) {
			t.Fatalf("replaying the applied patch does not give the state\n%s\nreplay %s\nnext   %s", where(), stateJSON(replay), stateJSON(next))
		}
		if d := next.Diff(base); patchJSON(d) != patchJSON(oldApplied) {
			t.Fatalf("Diff differs from the old Diff\n%s\nnew %s\nold %s", where(), patchJSON(d), patchJSON(oldApplied))
		}
		if applied.IsIdentity() != (stateJSON(next) == stateJSON(base)) {
			t.Fatalf("identity disagrees with the state\n%s", where())
		}
		if applied.IsIdentity() {
			identities++
		}
	}
	if identities == 0 || identities > 40000 {
		t.Fatalf("the generator is degenerate: %d of 50000 cases changed nothing", identities)
	}
}
