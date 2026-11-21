package glyph

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestParityGlyphTableMatchesThePagerTable(t *testing.T) {
	path := filepath.Join("..", "..", "scripts", "parity", "glyphs.json")
	want := map[string]string{}
	for _, g := range All() {
		want[g.Name] = g.Symbol
	}
	if os.Getenv("PARITY_GLYPHS_WRITE") == "1" {
		raw, err := json.MarshalIndent(want, "", " ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v; regenerate with PARITY_GLYPHS_WRITE=1 go test ./api/glyph", err)
	}
	got := map[string]string{}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for name, symbol := range want {
		if got[name] != symbol {
			t.Errorf("scripts/parity/glyphs.json: %s is %q, the pager's table says %q; regenerate with PARITY_GLYPHS_WRITE=1 go test ./api/glyph", name, got[name], symbol)
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("scripts/parity/glyphs.json names %s, which the pager's table no longer has", name)
		}
	}
}
