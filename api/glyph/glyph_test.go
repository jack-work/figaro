package glyph

import "testing"

func TestNamesAreUniqueAndSymbolsNonEmpty(t *testing.T) {
	seen := map[string]bool{}
	for _, g := range All() {
		if g.Name == "" {
			t.Errorf("glyph with symbol %q has no name", g.Symbol)
		}
		if seen[g.Name] {
			t.Errorf("name %q appears twice", g.Name)
		}
		seen[g.Name] = true
		if g.Symbol == "" {
			t.Errorf("glyph %q has an empty symbol", g.Name)
		}
		if g.Marks == "" {
			t.Errorf("glyph %q says nothing about what it marks", g.Name)
		}
	}
}

func TestSymbolLooksUpByName(t *testing.T) {
	if s, ok := Symbol("fork"); !ok || s != Fork {
		t.Fatalf("Symbol(fork) = %q, %v", s, ok)
	}
	if _, ok := Symbol("nope"); ok {
		t.Fatal("Symbol(nope) found something")
	}
}
