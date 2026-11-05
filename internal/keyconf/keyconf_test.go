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
	if err := Save(path, File{}, f); err != nil {
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

const commented = `# my keys: written by hand, kept by figaro

[bind.motions]
# half pages on J and K
J = "half-down"  # like less
K = "half-up"

[layer.mine]
modes = ["transcript"]  # only while reading

[layer.mine.bind]
z = "sticky"
`

func edited(t *testing.T, src string, edit func(*File)) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "keys.toml")
	os.WriteFile(path, []byte(src), 0o600)
	before, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	after, _, _ := Load(path)
	edit(&after)
	if err := Save(path, before, after); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	reread, _, err := Load(path)
	if err != nil {
		t.Fatalf("the saved file does not parse: %v\n%s", err, got)
	}
	if Render(reread) != Render(after) {
		t.Fatalf("the saved file binds\n%s\nwant\n%s", Render(reread), Render(after))
	}
	return string(got)
}

// Saving rewrote keys.toml from the parsed bindings, so every comment and the
// user's own order and spacing were lost on the first figaro keys bind. A
// save edits the lines that changed and leaves every other line as it was.
func TestASaveKeepsTheUsersCommentsAndLines(t *testing.T) {
	keep := func(t *testing.T, got string) {
		t.Helper()
		for _, line := range []string{
			"# my keys: written by hand, kept by figaro",
			"# half pages on J and K",
			`modes = ["transcript"]  # only while reading`,
		} {
			if !strings.Contains(got, line) {
				t.Errorf("lost %q:\n%s", line, got)
			}
		}
	}
	t.Run("bind a new key in an existing table", func(t *testing.T) {
		got := edited(t, commented, func(f *File) { f.Set("motions", "^E", "scroll-down") })
		keep(t, got)
		if !strings.Contains(got, `J = "half-down"  # like less`) {
			t.Errorf("an untouched binding changed:\n%s", got)
		}
	})
	t.Run("rebind keeps the line's comment", func(t *testing.T) {
		got := edited(t, commented, func(f *File) { f.Set("motions", "J", "down") })
		keep(t, got)
		if !strings.Contains(got, `J = "down"  # like less`) {
			t.Errorf("the rebound line lost its comment:\n%s", got)
		}
	})
	t.Run("a new table is appended", func(t *testing.T) {
		got := edited(t, commented, func(f *File) { f.Set("user", "Q", "") })
		keep(t, got)
		if !strings.HasPrefix(got, commented) {
			t.Errorf("appending a table changed what was there:\n%s", got)
		}
	})
	t.Run("a custom layer's bind table", func(t *testing.T) {
		got := edited(t, commented, func(f *File) { f.Set("mine", "y", "attend") })
		keep(t, got)
	})
	t.Run("forget removes only its line", func(t *testing.T) {
		got := edited(t, commented, func(f *File) { f.Forget("motions", "K") })
		keep(t, got)
		if strings.Contains(got, "half-up") {
			t.Errorf("K is still bound:\n%s", got)
		}
	})
}

// A quoted key is found by what it spells, and its line rebound in place.
func TestAQuotedKeyIsReboundInPlace(t *testing.T) {
	src := "[bind.user]\n\"^E\" = \"scroll-down\" # mine\n"
	got := edited(t, src, func(f *File) { f.Set("user", "^E", "") })
	if got != "[bind.user]\n\"^E\" = \"\" # mine\n" {
		t.Errorf("got %q", got)
	}
}

// A file whose shape the line editor does not know (an inline table) is not
// rewritten: the save refuses and the file is left as it was.
func TestASaveRefusesAShapeItCannotEditInPlace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.toml")
	src := "bind = { motions = { J = \"half-down\" } } # inline\n"
	os.WriteFile(path, []byte(src), 0o600)
	before, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	after, _, _ := Load(path)
	after.Set("motions", "K", "half-up")
	if err := Save(path, before, after); err == nil {
		t.Fatal("an inline table was edited in place")
	}
	if got, _ := os.ReadFile(path); string(got) != src {
		t.Errorf("the refused save changed the file: %q", got)
	}
}

// keys reset with nothing named empties the file, as it says it does.
func TestSavingNothingEmptiesTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.toml")
	os.WriteFile(path, []byte(commented), 0o600)
	before, _, _ := Load(path)
	if err := Save(path, before, File{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); len(got) != 0 {
		t.Errorf("reset left %q", got)
	}
}
