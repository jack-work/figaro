package form

import (
	"encoding/json"
	"math/rand"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// EntriesAt must hold exactly what Entries holds at a key, for every key a
// sink could ask about: the patch's own keys, their prefixes, and keys it
// never touches.
func TestEntriesAtIsEntriesFilteredToOneKey(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	for i := 0; i < lawTrials; i++ {
		p := randPatch(rng)
		all := p.Entries()
		for _, key := range probeKeys(rng, all) {
			want := filterEntries(all, key)
			got := p.EntriesAt(key)
			require.Equal(t, entryStrings(want), entryStrings(got), "trial %d key %q\n patch %s", i, key, mustJSON(p))
		}
	}
}

// Entry answered from Entries before EntriesAt existed; it must give the
// same answer now, for keys present and absent.
func TestEntryMatchesTheExpandingLookup(t *testing.T) {
	rng := rand.New(rand.NewSource(12))
	for i := 0; i < lawTrials; i++ {
		p := randPatch(rng)
		for _, key := range probeKeys(rng, p.Entries()) {
			want, wantOK := oldEntry(p, key)
			got, gotOK := p.Entry(key)
			require.Equal(t, wantOK, gotOK, "trial %d key %q\n patch %s", i, key, mustJSON(p))
			require.Equal(t, entryString(want), entryString(got), "trial %d key %q\n patch %s", i, key, mustJSON(p))
		}
	}
}

// oldEntry is Entry as it stood before EntriesAt: the fallback expanded the
// whole patch.
func oldEntry(p Patch, key string) (Entry, bool) {
	cur, rest := p, key
	for {
		if cur.Object == nil {
			break
		}
		seg, remainder, ok := longestPatchMember(cur.Object, rest)
		if !ok {
			break
		}
		if v, isSet := cur.Object.Set[seg]; isSet {
			if remainder == "" {
				return Entry{Key: key, New: v.Raw()}, true
			}
			if inner, ok := valueAt(v, remainder); ok {
				return Entry{Key: key, New: inner.Raw()}, true
			}
			return Entry{}, false
		}
		if v, isDel := cur.Object.Delete[seg]; isDel && remainder == "" {
			return Entry{Key: key, Old: v.Raw()}, true
		}
		child, isUpd := cur.Object.Update[seg]
		if !isUpd {
			break
		}
		if remainder == "" {
			if child.Scalar != nil {
				return Entry{Key: key, Old: child.Scalar.Before.Raw(), New: child.Scalar.After.Raw()}, true
			}
			if cur.Object.New[seg] {
				if v, err := child.Apply(NewValue(json.RawMessage(`{}`))); err == nil {
					return Entry{Key: key, New: v.Raw()}, true
				}
			}
			break
		}
		cur, rest = child, remainder
	}
	for _, e := range p.Entries() {
		if e.Key == key {
			return e, true
		}
	}
	return Entry{}, false
}

func randPatch(rng *rand.Rand) Patch {
	a := randObject(rng)
	b := mutate(rng, a, 3)
	p := Diff(a, b)
	if rng.Intn(3) == 0 {
		c := mutate(rng, b, 3)
		p = Merge(p, Diff(b, c))
	}
	return p
}

func randObject(rng *rand.Rand) Value {
	m := map[string]json.RawMessage{}
	for i, n := 0, 1+rng.Intn(4); i < n; i++ {
		m[randKey(rng)] = randValue(rng, 3).Raw()
	}
	raw, _ := json.Marshal(m)
	return NewValue(raw)
}

func probeKeys(rng *rand.Rand, all []Entry) []string {
	seen := map[string]bool{}
	var keys []string
	add := func(k string) {
		if k != "" && !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	for _, e := range all {
		add(e.Key)
		parts := strings.Split(e.Key, ".")
		for j := 1; j < len(parts); j++ {
			add(strings.Join(parts[:j], "."))
		}
		add(e.Key + ".k0")
	}
	for j := 0; j < 4; j++ {
		add(randKey(rng))
		add(randKey(rng) + "." + randKey(rng))
	}
	sort.Strings(keys)
	return keys
}

func filterEntries(all []Entry, key string) []Entry {
	var out []Entry
	for _, e := range all {
		if e.Key == key {
			out = append(out, e)
		}
	}
	return out
}

func entryStrings(es []Entry) []string {
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, entryString(e))
	}
	sort.Strings(out)
	return out
}

func entryString(e Entry) string {
	return e.Key + "\x00" + string(e.Old) + "\x00" + string(e.New) + "\x00" + boolString(e.New == nil)
}

func boolString(b bool) string {
	if b {
		return "removal"
	}
	return "set"
}

func mustJSON(p Patch) string {
	raw, _ := json.Marshal(p)
	return string(raw)
}
