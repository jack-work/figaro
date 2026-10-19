package keyconf

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMissingFileIsTheDefaults(t *testing.T) {
	f, warnings, err := Load(filepath.Join(t.TempDir(), "keys.toml"))
	if err != nil || len(warnings) != 0 || !f.Empty() {
		t.Fatalf("a missing file must mean no overrides: %+v %v %v", f, warnings, err)
	}
}

func TestLoadReadsBindAndLayer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.toml")
	os.WriteFile(path, []byte(`
[bind.motions]
J = "half-down"
d = ""

[layer.mine]
modes = ["transcript", "visual"]
above = "transcript"

[layer.mine.bind]
z = "sticky"

[typo]
x = 1
`), 0o600)
	f, warnings, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if f.Bind["motions"]["J"] != "half-down" || f.Bind["motions"]["d"] != "" {
		t.Fatalf("bind.motions: %+v", f.Bind)
	}
	l := f.Layer["mine"]
	if strings.Join(l.Modes, ",") != "transcript,visual" || l.Above != "transcript" || l.Bind["z"] != "sticky" {
		t.Fatalf("layer.mine: %+v", l)
	}
	// A key nothing reads is a setting that silently does not apply; it must
	// be named.
	if len(warnings) == 0 || !strings.Contains(strings.Join(warnings, " "), "typo") {
		t.Fatalf("an unread table must be reported: %v", warnings)
	}
}

// Save then Load is the identity, which is what `figaro keys bind` relies on
// to edit a file without rewriting what it did not touch.
func TestSaveRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.toml")
	var f File
	f.Set("motions", "J", "half-down")
	f.Set("user", "^", "attend-parent")
	f.Set("user", "Space", "")
	f.Layer = map[string]Layer{"mine": {Modes: []string{"transcript"}, Above: "pager", Bind: map[string]string{"z": "sticky"}}}
	if err := Save(path, f); err != nil {
		t.Fatal(err)
	}
	got, warnings, err := Load(path)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("%v %v", warnings, err)
	}
	if Render(got) != Render(f) {
		t.Fatalf("round trip changed the file:\n%s\n---\n%s", Render(f), Render(got))
	}
	if got.Bind["user"]["^"] != "attend-parent" || got.Bind["user"]["Space"] != "" {
		t.Fatalf("punctuation keys and unbinds must survive quoting: %+v", got.Bind)
	}
}

func TestSetIntoAUserLayerEditsThatLayer(t *testing.T) {
	f := File{Layer: map[string]Layer{"mine": {Modes: []string{"transcript"}}}}
	f.Set("mine", "z", "sticky")
	if f.Layer["mine"].Bind["z"] != "sticky" || f.Bind != nil {
		t.Fatalf("a binding into a declared layer belongs in that layer: %+v", f)
	}
}

func TestForget(t *testing.T) {
	var f File
	f.Set("motions", "J", "half-down")
	if !f.Forget("motions", "J") || !f.Empty() {
		t.Fatalf("forget left %+v", f)
	}
	if f.Forget("motions", "J") {
		t.Fatal("forgetting what is not there must say so")
	}
}

// A binding edit rewrites the whole file; the display table must ride along.
func TestDisplaySurvivesARewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.toml")
	if err := os.WriteFile(path, []byte("[display]\nids = \"short\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, warnings, err := Load(path)
	if err != nil || len(warnings) != 0 || f.Display.IDs != "short" {
		t.Fatalf("%+v %v %v", f, warnings, err)
	}
	if f.HasBindings() || f.Empty() {
		t.Fatalf("a display-only file has no bindings and is not empty: %+v", f)
	}
	f.Set("user", "J", "half-down")
	if err := Save(path, f); err != nil {
		t.Fatal(err)
	}
	got, _, err := Load(path)
	if err != nil || got.Display.IDs != "short" || got.Bind["user"]["J"] != "half-down" {
		t.Fatalf("rewrite lost something: %+v %v", got, err)
	}
}
