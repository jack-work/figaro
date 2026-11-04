package store

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/stretchr/testify/require"
)

var reduceKeys = []string{
	targetAriaKey, "mantra", "skills.a.body", "skills.b.body", "system.model",
	SystemTTLKey, SystemKeepaliveKey, SystemRetiredKey, keyOutfitName, keyOutfitVer,
}

// A write publishes the snapshot it reduced to instead of applying the
// canonical patch a second time; the two must be the same form, byte for byte,
// including requests that set equal values in other spellings and remove
// keys that are absent.
func TestReduceKeepsTheStateTheCanonicalPatchWouldBuild(t *testing.T) {
	rng := rand.New(rand.NewSource(22))
	for i := 0; i < 5000; i++ {
		snap := form.FromMap(reduceSet(rng))
		req := reduceRequest(rng, snap)
		f := &Form{}
		st := &formState{snap: snap}
		res := f.judge(st, &formWrite{patch: req})
		require.NoError(t, res.err)
		want, err := snap.Apply(res.applied).MarshalJSON()
		require.NoError(t, err)
		got, err := res.next.MarshalJSON()
		require.NoError(t, err)
		require.Equal(t, string(want), string(got), "trial %d\n snap %s\n req %s", i, reduceJSON(snap), reducePatchJSON(req))
		require.Equal(t, reducePatchJSON(res.next.Diff(snap)), reducePatchJSON(res.applied), "trial %d", i)
	}
}

func reduceRequest(rng *rand.Rand, base form.Snapshot) message.Patch {
	set := reduceSet(rng)
	for k := range set {
		if rng.Intn(3) == 0 {
			if cur, ok := base.Get(k); ok {
				set[k] = []byte(" " + string(cur) + " ")
			}
		}
	}
	p := form.Build(form.Snapshot{}, set, reduceRemovals(rng))
	if rng.Intn(3) == 0 {
		p = form.Merge(p, form.Build(base, nil, reduceRemovals(rng)))
	}
	return p
}

func reduceSet(rng *rand.Rand) map[string]json.RawMessage {
	m := map[string]json.RawMessage{}
	for _, k := range reduceKeys {
		if rng.Intn(2) == 0 {
			m[k] = reduceValue(rng)
		}
	}
	return m
}

func reduceRemovals(rng *rand.Rand) []string {
	var out []string
	for _, k := range append([]string{"system", "skills"}, reduceKeys...) {
		if rng.Intn(5) == 0 {
			out = append(out, k)
		}
	}
	return out
}

func reduceValue(rng *rand.Rand) json.RawMessage {
	switch rng.Intn(5) {
	case 0:
		return json.RawMessage(fmt.Sprintf("%d", rng.Intn(1000)))
	case 1:
		return json.RawMessage(`true`)
	case 2:
		return json.RawMessage(`{"x":1,"y":{"z":"w"}}`)
	default:
		raw, _ := json.Marshal(fmt.Sprintf("%dh", 1+rng.Intn(48)))
		return raw
	}
}

func reduceJSON(s form.Snapshot) string {
	raw, _ := s.MarshalJSON()
	return string(raw)
}

func reducePatchJSON(p message.Patch) string {
	raw, _ := json.Marshal(p)
	return string(raw)
}
