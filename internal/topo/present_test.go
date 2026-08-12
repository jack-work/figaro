package topo

import "testing"

func TestPresentAppliesAnOverride(t *testing.T) {
	parent := map[string]string{"root": "", "a": "root", "b": "a", "c": "b"}
	got := Present(parent, map[string]string{"c": "a", "b": "c"})
	if got["c"] != "a" || got["b"] != "c" {
		t.Fatalf("promote not applied: %v", got)
	}
}

// A state file written by an older figaro can name an aria that has since
// been deleted. The row must fall back to its history rather than hang off
// nothing.
func TestPresentRefusesAnEdgeToAnUnknownAria(t *testing.T) {
	parent := map[string]string{"root": "", "a": "root", "b": "a"}
	got := Present(parent, map[string]string{"b": "ghost"})
	if got["b"] != "a" {
		t.Fatalf("b drawn under %q, want its history parent", got["b"])
	}
}

// A cycle is the one thing a listing cannot survive: it walks parents.
func TestPresentRefusesACycle(t *testing.T) {
	parent := map[string]string{"root": "", "a": "root", "b": "a", "c": "b"}
	got := Present(parent, map[string]string{"a": "c"})
	if got["a"] != "root" {
		t.Fatalf("cycle accepted: %v", got)
	}
	for id := range got {
		seen := map[string]bool{}
		for cur := id; cur != ""; cur = got[cur] {
			if seen[cur] {
				t.Fatalf("cycle through %s: %v", id, got)
			}
			seen[cur] = true
		}
	}
}

func TestPresentWithoutEdgesIsTheTopology(t *testing.T) {
	parent := map[string]string{"root": "", "a": "root", "b": "a"}
	got := Present(parent, nil)
	for id, up := range parent {
		if got[id] != up {
			t.Fatalf("%s: got %q want %q", id, got[id], up)
		}
	}
}
