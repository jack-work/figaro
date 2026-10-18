package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/keyconf"
)

// THE RECIPES ARE THE EXAMPLE FILE AND ITS TEST AT ONCE. Each is a title, the
// TOML a reader would paste, and what that TOML must do. The test applies
// every recipe alone and all of them together; -update-keymap writes them,
// titles as comments, to skills/figaro/reference/keys.example.toml, which is
// the file `figaro keys init` installs. A recipe that stops working fails
// here, and the file a reader copies cannot say something the tree does not.

type keyRecipe struct {
	title string
	toml  string
	check func(t *testing.T)
}

var keyRecipes = []keyRecipe{
	{
		title: "Fill the leader: Space w for status, Space q for the queue, Space l for the tree.",
		toml: `[bind.leader]
w = "status"
q = "queue"
l = "list"
`,
		check: func(t *testing.T) {
			for k, want := range map[byte]string{'w': "status", 'q': "queue", 'l': "list"} {
				bd := prefixBinding("leader", modeTranscript, keyEvent{b: k})
				if bd == nil || bd.action != want {
					t.Errorf("Space %c reaches %+v, want %s", k, bd, want)
				}
			}
			tr := oracleTranscript()
			tr.key(' ')
			tr.key('w')
			if !tr.showing("status") {
				t.Error("Space w did not open the status pit")
			}
		},
	},
	{
		title: "Page with ^F and ^B, as in less and vim. The command line keeps them for the cursor.",
		toml: `[bind.motions]
"^F" = "half-down"
"^B" = "half-up"
`,
		check: func(t *testing.T) {
			for _, m := range []keyMode{modeTranscript, modeVisual, modeList} {
				if got := reached(m, byteChord(0x06)); got != "half-down" {
					t.Errorf("^F in %s reaches %q", modeName(m), got)
				}
				if got := reached(m, byteChord(0x02)); got != "half-up" {
					t.Errorf("^B in %s reaches %q", modeName(m), got)
				}
			}
			if got := reached(modeBox, byteChord(0x06)); got != "cmd-right" {
				t.Errorf("^F in the command line reaches %q, want cmd-right", got)
			}
		},
	},
	{
		title: "Move the panels behind a prefix of your own: p h, p !, p Q. Alone, h and ! do nothing.",
		toml: `[layer.panels]
modes = ["transcript", "pit"]
prefix = true

[layer.panels.bind]
h = "help"
"!" = "status"
Q = "queue"

[bind.pager]
h = ""
"!" = ""
Q = ""
p = "prefix:panels"
`,
		check: func(t *testing.T) {
			tr := oracleTranscript()
			tr.key('h')
			if tr.showing("help") {
				t.Error("h alone still opens help")
			}
			tr.key('p')
			tr.key('h')
			if !tr.showing("help") {
				t.Error("p h did not open help")
			}
		},
	},
	{
		title: "A key of your own in every mode it means something: M-s pins the question.",
		toml: `[bind.user]
M-s = "sticky"
`,
		check: func(t *testing.T) {
			for _, m := range []keyMode{modeTranscript, modeList} {
				if got := reached(m, metaChord('s')); got != "sticky" {
					t.Errorf("M-s in %s reaches %q", modeName(m), got)
				}
			}
			if got := reached(modeBox, metaChord('s')); got != "" {
				t.Errorf("M-s in the command line reaches %q; sticky means nothing there", got)
			}
		},
	},
}

func renderKeyExample() string {
	var b strings.Builder
	b.WriteString("# keys.toml: copy what you want into ~/.config/figaro/keys.toml.\n")
	b.WriteString("# `figaro keys` prints the result; `figaro doctor keys` what it refused.\n")
	for _, r := range keyRecipes {
		fmt.Fprintf(&b, "\n# %s\n%s", r.title, r.toml)
	}
	return b.String()
}

func TestKeyRecipesEachWorkAlone(t *testing.T) {
	for _, r := range keyRecipes {
		t.Run(r.title, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "keys.toml")
			os.WriteFile(path, []byte(r.toml), 0o600)
			f, warnings, err := keyconf.Load(path)
			if err != nil || len(warnings) > 0 {
				t.Fatal(err, warnings)
			}
			if diags := withUserKeys(t, f); len(diags) > 0 {
				t.Fatalf("refused: %v", diags)
			}
			r.check(t)
		})
	}
}

// Together, because a reader pastes the whole file: no recipe may refuse or
// undo another.
func TestTheKeyExampleFileIsTheRecipesAndWorks(t *testing.T) {
	path := filepath.Join("..", "..", "skills", "figaro", "reference", "keys.example.toml")
	if *updateKeymap {
		if err := os.WriteFile(path, []byte(renderKeyExample()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update-keymap)", err)
	}
	if string(data) != renderKeyExample() {
		t.Fatalf("%s is not the recipes; run with -update-keymap", path)
	}
	f, warnings, err := keyconf.Load(path)
	if err != nil || len(warnings) > 0 {
		t.Fatal(err, warnings)
	}
	if diags := withUserKeys(t, f); len(diags) > 0 {
		t.Fatalf("the example file is refused: %v", diags)
	}
	for _, r := range keyRecipes {
		t.Run(r.title, r.check)
	}
}

// The tables in reference/keys.md are rendered from the defaults, between
// markers, by the same flag. A layer or action added to the code and not
// to the page fails here.
func TestTheKeysPageTablesAreTheDefaults(t *testing.T) {
	path := filepath.Join("..", "..", "skills", "figaro", "reference", "keys.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	page := string(data)
	want := page
	for _, s := range []struct{ name, body string }{
		{"layers", renderLayerTable()},
		{"actions", renderActionTable()},
	} {
		open, close := "<!-- generated: "+s.name+" -->\n", "<!-- end: "+s.name+" -->"
		i, j := strings.Index(want, open), strings.Index(want, close)
		if i < 0 || j < i {
			t.Fatalf("%s has no %q ... %q markers", path, open, close)
		}
		want = want[:i+len(open)] + s.body + want[j:]
	}
	if *updateKeymap && want != page {
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if want != page {
		t.Fatalf("the tables in %s are not the defaults; run with -update-keymap", path)
	}
}

func renderLayerTable() string {
	var b strings.Builder
	b.WriteString("| Layer | Modes | Bindings |\n|---|---|---|\n")
	for _, l := range defaultLayers {
		var parts []string
		for _, bd := range l.binds {
			parts = append(parts, fmt.Sprintf("`%s` %s", bd.keys, bd.action))
		}
		name := l.name
		if l.prefix {
			name += " (prefix)"
		}
		fmt.Fprintf(&b, "| %s | %s | %s |\n", name, spellModes(l.modes), strings.Join(parts, " · "))
	}
	return b.String()
}

func renderActionTable() string {
	var b strings.Builder
	b.WriteString("| Action | Means something in |\n|---|---|\n")
	for _, a := range keyActions {
		fmt.Fprintf(&b, "| `%s` | %s |\n", a.name, spellModes(a.modes()))
	}
	return b.String()
}
