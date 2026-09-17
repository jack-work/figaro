package cli

import (
	"reflect"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/keyconf"
)

// The user layer is only safe if applying it is a pure function over the
// default table: these are the tests that say so.

func withUserKeys(t *testing.T, rows ...keyconf.Row) []keyDiag {
	t.Helper()
	t.Cleanup(func() { applyUserKeys(nil) })
	return applyUserKeys(rows)
}

// TestKeySpell_RoundTripsEveryRow: the spelling the help pit prints is the
// spelling keys.toml accepts. Every chord figaro ships must survive the trip.
func TestKeySpell_RoundTripsEveryRow(t *testing.T) {
	for i := range keymap {
		c := keymap[i].chord
		spec, err := parseKeySpec(c.spell())
		if err != nil {
			t.Errorf("%s spells %q, which does not parse: %s", c, c.spell(), err)
			continue
		}
		found := false
		for _, got := range spec.chords {
			found = found || got == c
		}
		if !found {
			t.Errorf("%s spells %q, which parses to %v", c, c.spell(), spec.chords)
		}
	}
}

// TestKeyActions_NameEveryRow: a binding a user can see is a binding a user
// can name. Function pointers, so a renamed handler cannot slip out of the
// registry unnoticed.
func TestKeyActions_NameEveryRow(t *testing.T) {
	used := map[string]bool{}
	for i := range keymap {
		bd := &keymap[i]
		a := actionFor(bd)
		if a == nil {
			t.Errorf("%s in %s: no action name (add it to keyActions)", bd.chord, modeList(bd.modes))
			continue
		}
		used[a.name] = true
	}
	for _, a := range keyActions {
		if !used[a.name] {
			t.Errorf("action %q names no live binding", a.name)
		}
	}
}

// TestApplyUserKeys_EmptyLayerIsIdentity: the defaults are what they were.
func TestApplyUserKeys_EmptyLayerIsIdentity(t *testing.T) {
	before := helpBody()
	withUserKeys(t)
	if &liveRows[0] != &keymap[0] {
		t.Fatal("an empty layer must leave the default table in place, not a copy of it")
	}
	if got := helpBody(); !reflect.DeepEqual(got, before) {
		t.Errorf("an empty layer changed the help panel")
	}
}

// TestApplyUserKeys_RebindsAndTheHelpPitFollows is the whole feature in one
// test: a row in keys.toml moves a key, and the panel says so.
func TestApplyUserKeys_RebindsAndTheHelpPitFollows(t *testing.T) {
	if diags := withUserKeys(t, keyconf.Row{Chord: "J", Action: "half-down"}); len(diags) > 0 {
		t.Fatalf("refused: %v", diags[0])
	}
	act := pagerAct.pager(modeTranscript, keyEvent{b: 'J'})
	if act == nil {
		t.Fatal("J is not bound in the transcript after the rebind")
	}
	if reflect.ValueOf(act).Pointer() != reflect.ValueOf(pagerHalfDown).Pointer() {
		t.Error("J is bound, but not to half-down")
	}
	panel := strings.Join(helpBody(), "\n")
	scroll := ""
	for _, line := range helpBody() {
		if strings.Contains(line, "scroll · half-page") {
			scroll = line
		}
	}
	if scroll == "" {
		t.Fatalf("no scroll row in the panel:\n%s", panel)
	}
	if !strings.Contains(scroll, "J") {
		t.Errorf("the scroll row does not show the new key: %q", scroll)
	}
	if strings.Contains(scroll, "gg/G") {
		t.Errorf("the hand-written column survived a remap: %q", scroll)
	}
}

// TestApplyUserKeys_UnbindDropsTheHelpRow: a row nobody can press is a row
// the panel must not promise.
func TestApplyUserKeys_UnbindDropsTheHelpRow(t *testing.T) {
	withUserKeys(t, keyconf.Row{Chord: "M-m", Mode: "all", Action: "none"})
	if pagerAct.byMeta[modeTranscript]['m'] != nil || inputAct.byMeta[modeTranscript]['m'] != nil {
		t.Error("M-m still dispatches after being unbound")
	}
	if metaBound[modeTranscript]['m'] {
		t.Error("the decoder still claims ESC m as a Meta chord in the transcript")
	}
	for _, line := range helpBody() {
		if strings.Contains(line, "verbose tool output") {
			t.Errorf("the panel still documents an unbound key: %q", line)
		}
	}
}

func TestApplyUserKeys_Refusals(t *testing.T) {
	cases := []struct {
		name string
		row  keyconf.Row
		want string
	}{
		{"interrupt", keyconf.Row{Chord: "^C", Action: "line-down"}, "guaranteed way out"},
		{"welded", keyconf.Row{Chord: "^M", Action: "line-down"}, "write Enter"},
		{"text in the box", keyconf.Row{Chord: "z", Mode: "command", Action: "cmd-home"}, "literal text"},
		{"enter in the box", keyconf.Row{Chord: "Enter", Mode: "search", Action: "line-down"}, "cannot be rebound"},
		{"unknown action", keyconf.Row{Chord: "z", Action: "scroll-sideways"}, "no action named"},
		{"unknown mode", keyconf.Row{Chord: "z", Mode: "sideways", Action: "line-down"}, "unknown mode"},
		{"shadowed by an input row", keyconf.Row{Chord: "^L", Action: "line-down"}, "would never fire"},
		{"shadows a pager row", keyconf.Row{Chord: "j", Action: "toggle-verbose"}, "would silently kill"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diags := withUserKeys(t, tc.row)
			if len(diags) != 1 {
				t.Fatalf("want one refusal, got %v", diags)
			}
			if !strings.Contains(diags[0].text, tc.want) {
				t.Errorf("refusal says %q, want it to mention %q", diags[0].text, tc.want)
			}
			if &liveRows[0] != &keymap[0] {
				t.Error("a refused row still rebuilt the table")
			}
		})
	}
}

// TestApplyUserKeys_BothEncodings: a reader writes ^E once and gets the key
// their terminal actually sends, whichever of the two that is.
func TestApplyUserKeys_BothEncodings(t *testing.T) {
	withUserKeys(t, keyconf.Row{Chord: "^E", Action: "line-down"})
	if pagerAct.byByte[byteSlot(modeTranscript, 0x05)] == nil {
		t.Error("the raw control byte is not bound")
	}
	if pagerAct.byCtrl[modeTranscript]['e'-'a'] == nil {
		t.Error("the CSI-u chord is not bound")
	}
	if !ctrlChordBoundIn(modeTranscript, 'e') {
		t.Error("the decoder does not know ^E is a chord in the transcript")
	}
}

// TestApplyUserKeys_StructuralTestsHoldOverALayer: the four table invariants
// are properties of ANY map, not just the shipped one.
func TestApplyUserKeys_StructuralTestsHoldOverALayer(t *testing.T) {
	withUserKeys(t,
		keyconf.Row{Chord: "J", Action: "half-down"},
		keyconf.Row{Chord: "M-m", Mode: "all", Action: "none"},
		keyconf.Row{Chord: "^E", Mode: "transcript,visual", Action: "line-down"},
	)
	for i := range liveRows {
		bd := &liveRows[i]
		if bd.modes == 0 {
			continue
		}
		if bd.pager == nil && bd.input == nil {
			t.Errorf("%s: no action", bd.chord)
		}
		if bd.pager != nil && bd.input != nil {
			t.Errorf("%s: both levels", bd.chord)
		}
		if bd.open == openUnset {
			t.Errorf("%s: no open policy", bd.chord)
		}
		if bd.pager != nil && bd.modes&inIncipit != 0 {
			t.Errorf("%s: a pager action in incipit", bd.chord)
		}
	}
	type slot struct {
		c chord
		m keyMode
		l string
	}
	seen := map[slot]bool{}
	for i := range liveRows {
		bd := &liveRows[i]
		level := "pager"
		if bd.input != nil {
			level = "input"
		}
		for m := keyMode(0); m < numKeyModes; m++ {
			if bd.modes&(1<<m) == 0 {
				continue
			}
			s := slot{bd.chord, m, level}
			if seen[s] {
				t.Errorf("%s bound twice at the %s level in %s", bd.chord, level, modeName(m))
			}
			seen[s] = true
		}
	}
}
