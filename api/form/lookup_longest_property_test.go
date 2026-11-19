package form

import (
	"math/rand"
	"strings"
	"testing"
)

func bruteLongest(keys []string, rest string) string {
	best := ""
	for _, k := range keys {
		if k == rest {
			return k
		}
		if strings.HasPrefix(rest, k+".") && len(k) > len(best) {
			best = k
		}
	}
	return best
}

// Every inserted key is found, and the longest dotted prefix member wins,
// whatever order the keys went in and however the AVL rotated.
func TestLookupLongestMatchesBruteForceInAnyInsertionOrder(t *testing.T) {
	segs := []string{"a", "b", "claude-opus-5", "5", "5-5", "glm-5", "3", "x/y", "context"}
	seps := []string{".", "-", "/"}
	rng := rand.New(rand.NewSource(177))
	for round := 0; round < 400; round++ {
		seen := map[string]bool{}
		var keys []string
		for len(keys) < 2+rng.Intn(24) {
			k := segs[rng.Intn(len(segs))]
			for n := rng.Intn(3); n > 0; n-- {
				k += seps[rng.Intn(len(seps))] + segs[rng.Intn(len(segs))]
			}
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
		var tr ptree
		for _, i := range rng.Perm(len(keys)) {
			tr = ptree{root: setEntry(tr.root, &node{key: keys[i], value: Value{}})}
		}
		queries := append([]string{}, keys...)
		for _, k := range keys {
			queries = append(queries, k+".context", k+".5.default", k+"-5.context", k+"x")
		}
		for _, q := range queries {
			want := bruteLongest(keys, q)
			n := lookupLongest(tr.root, q)
			got := ""
			if n != nil {
				got = n.key
			}
			if got != want {
				t.Fatalf("round %d keys %q: lookupLongest(%q) = %q, want %q", round, keys, q, got, want)
			}
		}
	}
}
