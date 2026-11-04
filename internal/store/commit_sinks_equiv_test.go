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

// The commit sinks answer from direct key lookups now. They must give the
// answers the expanding versions gave, on the patches the drainer sees:
// canonical diffs that set, update and remove the keys the sinks read. Entry
// itself is pinned against its old expanding form in api/form.
func TestCommitSinksAnswerAsTheExpandingLookupsDid(t *testing.T) {
	rng := rand.New(rand.NewSource(21))
	keys := append([]string{targetAriaKey}, sinkKeys...)
	for i := 0; i < 3000; i++ {
		p := randSinkPatch(rng)
		require.Equal(t, expandingNamesOutfit(p), namesOutfit(p), "trial %d: namesOutfit\n %s", i, patchJSON(p))
		for _, key := range keys {
			wantRaw, wantSpoke := expandingStringKeyOf(p, key)
			gotRaw, gotSpoke := stringKeyOf(p, key)
			require.Equal(t, wantSpoke, gotSpoke, "trial %d key %q\n %s", i, key, patchJSON(p))
			require.Equal(t, wantRaw, gotRaw, "trial %d key %q\n %s", i, key, patchJSON(p))
		}
		wantTarget, wantSpoke := expandingStringKeyOf(p, targetAriaKey)
		gotTarget, gotSpoke := targetOf(p)
		require.Equal(t, wantSpoke, gotSpoke, "trial %d: targetOf\n %s", i, patchJSON(p))
		require.Equal(t, wantTarget, gotTarget, "trial %d: targetOf\n %s", i, patchJSON(p))
	}
}

var sinkKeys = []string{
	SystemTTLKey, SystemKeepaliveKey, SystemRetiredKey, SystemReapedKey, SystemReapedUntilKey,
	keyOutfitName, keyOutfitVer, keyLegacyName, keyLegacyVer,
}

func expandingStringKeyOf(patch message.Patch, key string) (string, bool) {
	if e, ok := patch.Entry(key); ok && !e.IsRemoval() {
		return lifetimeString(e.New), true
	}
	for _, ent := range patch.Entries() {
		if ent.IsRemoval() && ent.Key == key {
			return "", true
		}
	}
	return "", false
}

func expandingNamesOutfit(patch message.Patch) bool {
	for _, k := range []string{keyOutfitName, keyOutfitVer, keyLegacyName, keyLegacyVer} {
		if _, ok := patch.Entry(k); ok {
			return true
		}
		for _, ent := range patch.Entries() {
			if ent.IsRemoval() && ent.Key == k {
				return true
			}
		}
	}
	return false
}

func randSinkPatch(rng *rand.Rand) message.Patch {
	a := randSinkForm(rng)
	b := randSinkForm(rng)
	if rng.Intn(3) == 0 {
		b = a.Apply(form.Build(a, randSinkSet(rng), randSinkRemovals(rng)))
	}
	p := b.Diff(a)
	if rng.Intn(4) == 0 {
		c := randSinkForm(rng)
		p = form.Merge(p, c.Diff(b))
	}
	return p
}

func randSinkForm(rng *rand.Rand) form.Snapshot {
	return form.FromMap(randSinkSet(rng))
}

func randSinkSet(rng *rand.Rand) map[string]json.RawMessage {
	pool := append([]string{targetAriaKey, "mantra", "skills.a.body", "skills.b.body", "system.model"}, sinkKeys...)
	m := map[string]json.RawMessage{}
	for _, k := range pool {
		if rng.Intn(2) == 0 {
			m[k] = randSinkValue(rng)
		}
	}
	return m
}

func randSinkRemovals(rng *rand.Rand) []string {
	var out []string
	for _, k := range append([]string{targetAriaKey, "system", "skills"}, sinkKeys...) {
		if rng.Intn(5) == 0 {
			out = append(out, k)
		}
	}
	return out
}

func randSinkValue(rng *rand.Rand) json.RawMessage {
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

func patchJSON(p message.Patch) string {
	raw, _ := json.Marshal(p)
	return string(raw)
}
