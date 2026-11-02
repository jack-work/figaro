package form_test

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/stretchr/testify/require"
)

func deleteSnap(t *testing.T, s string) form.Snapshot {
	t.Helper()
	var x form.Snapshot
	require.NoError(t, json.Unmarshal([]byte(s), &x))
	return x
}

func deleteShow(t *testing.T, s form.Snapshot) string {
	t.Helper()
	b, err := s.MarshalJSON()
	require.NoError(t, err)
	return string(b)
}

// An AVL delete of a node with two children rebuilds it from its in-order
// successor. A successor holding a subtree must keep it: the server logs
// Diff(after, before), so a lost subtree is written down as a change.
func TestDelete_KeepsTheSuccessorsSubtree(t *testing.T) {
	s := deleteSnap(t, `{"a":1,"b":2,"c":{"d":3}}`)
	p := form.Build(s, nil, []string{"b"})
	require.JSONEq(t, `{"a":1,"c":{"d":3}}`, deleteShow(t, s.Apply(p)))
	require.JSONEq(t, `{"object":{"Delete":{"b":2}}}`, deletePatchJSON(t, s.Apply(p).Diff(s)),
		"the canonical patch names only the removed key")
}

// The shape the bug was seen in: a bound form losing skills to an unset of name.
func TestDelete_ABoundFormKeepsItsSiblings(t *testing.T) {
	s := deleteSnap(t, `{"aria_id":"x","cwd":"/","mantra":"m","name":"n","skills":{"a":{"filePath":"/p"}},"system":{"model":"m"}}`)
	got := s.Apply(form.Build(s, nil, []string{"name"}))
	require.JSONEq(t, `{"aria_id":"x","cwd":"/","mantra":"m","skills":{"a":{"filePath":"/p"}},"system":{"model":"m"}}`, deleteShow(t, got))
}

// Every key removed from a level of branches, at every tree shape: nothing
// but the removed key may change.
func TestDelete_EveryKeyOfEverySize(t *testing.T) {
	for n := 1; n <= 40; n++ {
		obj := map[string]any{}
		for i := 0; i < n; i++ {
			obj[fmt.Sprintf("k%02d", i)] = map[string]any{"v": i}
		}
		raw, err := json.Marshal(obj)
		require.NoError(t, err)
		s := deleteSnap(t, string(raw))
		for i := 0; i < n; i++ {
			key := fmt.Sprintf("k%02d", i)
			want := map[string]any{}
			for k, v := range obj {
				if k != key {
					want[k] = v
				}
			}
			wantRaw, err := json.Marshal(want)
			require.NoError(t, err)
			require.JSONEq(t, string(wantRaw), deleteShow(t, s.Apply(form.Build(s, nil, []string{key}))), "n=%d remove %s", n, key)
		}
	}
}

func deletePatchJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

func randomForm(r *rand.Rand, depth int) map[string]any {
	out := map[string]any{}
	n := 1 + r.IntN(9)
	for len(out) < n {
		k := fmt.Sprintf("%c%d", 'a'+rune(r.IntN(6)), r.IntN(4))
		if depth < 3 && r.IntN(3) == 0 {
			out[k] = randomForm(r, depth+1)
			continue
		}
		switch r.IntN(4) {
		case 0:
			out[k] = r.IntN(100)
		case 1:
			out[k] = fmt.Sprintf("s%d", r.IntN(100))
		case 2:
			out[k] = []any{r.IntN(5), "x"}
		default:
			out[k] = r.IntN(2) == 0
		}
	}
	return out
}

func pathsOf(m map[string]any, prefix string, out *[]string) {
	for k, v := range m {
		p := k
		if prefix != "" {
			p = prefix + "." + k
		}
		*out = append(*out, p)
		if sub, ok := v.(map[string]any); ok {
			pathsOf(sub, p, out)
		}
	}
}

func leavesOf(t *testing.T, s form.Snapshot) map[string]string {
	t.Helper()
	out := map[string]string{}
	for k, v := range s.All() {
		out[k] = string(v)
	}
	return out
}

// Random nested forms, every existing key removed in turn at every depth:
// every leaf outside the removed key keeps its bytes, and nothing else goes.
func TestDelete_EveryOtherPathSurvivesByteForByte(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 11))
	for trial := 0; trial < 400; trial++ {
		m := randomForm(r, 0)
		raw, err := json.Marshal(m)
		require.NoError(t, err)
		s := deleteSnap(t, string(raw))
		all := leavesOf(t, s)
		var paths []string
		pathsOf(m, "", &paths)
		for _, p := range paths {
			got := leavesOf(t, s.Apply(form.Build(s, nil, []string{p})))
			for leaf, v := range all {
				under := leaf == p || strings.HasPrefix(leaf, p+".")
				gv, kept := got[leaf]
				switch {
				case under && kept:
					t.Fatalf("form %s: removing %s kept %s", raw, p, leaf)
				case !under && !kept:
					t.Fatalf("form %s: removing %s lost %s", raw, p, leaf)
				case !under && gv != v:
					t.Fatalf("form %s: removing %s changed %s from %s to %s", raw, p, leaf, v, gv)
				}
			}
			for leaf := range got {
				if _, was := all[leaf]; !was {
					t.Fatalf("form %s: removing %s made %s", raw, p, leaf)
				}
			}
		}
	}
}
