package topo

import (
	"math/rand"
	"reflect"
	"testing"
)

var redundantParents = map[string]string{
	"null": "", "@v1": "null", "F": "null", "G": "null",
	"a": "@v1", "b": "a", "c": "b", "d": "null", "x": "F", "y": "G",
}

func redundantReasons(edges map[string]string) map[string]string {
	out := map[string]string{}
	for _, st := range Redundant(redundantParents, edges) {
		out[st.ID] = st.Reason
	}
	return out
}

// Only an override that names the history parent exactly, or whose node is
// gone, goes: anything else changes some delete set, even where it does not
// change what is drawn.
func TestRedundantNamesOnlyWhatChangesNoDeleteSet(t *testing.T) {
	cases := []struct {
		name  string
		edges map[string]string
		want  map[string]string
	}{
		{"an override to the history parent", map[string]string{"b": "a"}, map[string]string{"b": StaleHistoryAgrees}},
		{"the root where history says the root", map[string]string{"d": "null"}, map[string]string{"d": StaleHistoryAgrees}},
		{"an aria that is gone", map[string]string{"ghost": "a"}, map[string]string{"ghost": StaleAriaGone}},
		{"a key on a node that is not an aria", map[string]string{"F": "a"}, map[string]string{}},
		{"the top, away from the birth form", map[string]string{"x": ""}, map[string]string{}},
		{"the root, away from the birth form", map[string]string{"x": "null"}, map[string]string{}},
		{"another form than the birth form", map[string]string{"x": "G"}, map[string]string{}},
		{"an outfit version where history says the root", map[string]string{"d": "@v1"}, map[string]string{}},
		{"the top where history says the outfit version", map[string]string{"a": ""}, map[string]string{}},
		{"a target that is gone", map[string]string{"c": "@v0"}, map[string]string{}},
		{"an edge closing a cycle", map[string]string{"a": "c"}, map[string]string{}},
		{"a promote out from under an aria", map[string]string{"c": "a"}, map[string]string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := redundantReasons(tc.edges); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

type mapTopology map[string]string

func (m mapTopology) From(id string) (string, bool) { p, ok := m[id]; return p, ok }
func (m mapTopology) Nodes() []string {
	out := make([]string, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	return out
}
func (m mapTopology) ChildrenOf(string) []string { return nil }

func deleteSets(parent, edges map[string]string) map[string][]string {
	t := mapTopology(parent)
	parentOf := func(id string) (string, bool) {
		if p, ok := edges[id]; ok {
			return p, true
		}
		return t.From(id)
	}
	kids := ChildIndex(t, parentOf)
	out := map[string][]string{}
	for id := range parent {
		set := DescendantClosure(kids, id)
		sortStrings(set)
		out[id] = set
	}
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// The property the clean rests on: dropping every override Redundant names
// leaves every node's delete set exactly as it was, so no ttl, reap, kill -r
// or form rm -r takes anything it would not have taken before.
func TestDroppingRedundantOverridesKeepsEveryDeleteSet(t *testing.T) {
	ids := []string{"", "null", "@v1", "@v0", "F", "G", "a", "b", "c", "d", "x", "y", "ghost"}
	rows := []string{"a", "b", "c", "d", "x", "y", "F", "ghost"}
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 20000; i++ {
		edges := map[string]string{}
		for _, id := range rows {
			if rng.Intn(2) == 0 {
				edges[id] = ids[rng.Intn(len(ids))]
			}
		}
		kept := map[string]string{}
		for id, up := range edges {
			kept[id] = up
		}
		for _, st := range Redundant(redundantParents, edges) {
			delete(kept, st.ID)
		}
		before, after := deleteSets(redundantParents, edges), deleteSets(redundantParents, kept)
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("edges %v: delete sets\n %v\nafter the clean\n %v", edges, before, after)
		}
		if again := Redundant(redundantParents, kept); len(again) != 0 {
			t.Fatalf("edges %v: the clean left %v redundant", edges, again)
		}
	}
}
