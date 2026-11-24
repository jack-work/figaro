package cli

import (
	"fmt"
	"strings"
	"testing"
)

// The canonical encoding; key_nav_test.go covers the others.
func navSequence(n navKey) string {
	switch n {
	case navUp:
		return "\x1b[A"
	case navDown:
		return "\x1b[B"
	case navPageUp:
		return "\x1b[5~"
	case navPageDown:
		return "\x1b[6~"
	case navHome:
		return "\x1b[H"
	case navEnd:
		return "\x1b[F"
	case navLeft:
		return "\x1b[D"
	case navRight:
		return "\x1b[C"
	}
	return ""
}

func metaSequence(b byte) string { return "\x1b" + string(b) }

func ctrlSequence(letter byte, shift bool) string {
	mods := 1 + 4 // ctrl
	if shift {
		mods += 1
	}
	return fmt.Sprintf("\x1b[%d;%du", letter, mods)
}

func bindingInput(b *keyBinding) string {
	switch b.chord.kind {
	case chordNav:
		return navSequence(b.chord.nav)
	case chordCtrlLetter:
		return ctrlSequence(b.chord.b, false)
	case chordMeta:
		return metaSequence(b.chord.b)
	default:
		return string(b.chord.b)
	}
}

func TestKeymap_NoDuplicateBindingPerMode(t *testing.T) {
	type slot struct {
		c    chord
		m    keyMode
		leve string
	}
	seen := map[slot]int{}
	for i := range keymap {
		bd := &keymap[i]
		if bd.prefix != "" {
			continue
		}
		level := "pager"
		if bd.input != nil {
			level = "input"
		}
		for m := keyMode(0); m < numKeyModes; m++ {
			if bd.modes&(1<<m) == 0 {
				continue
			}
			s := slot{bd.chord, m, level}
			if prev, dup := seen[s]; dup {
				t.Fatalf("%s bound twice at the %s level in %s mode (rows %d and %d)",
					bd.chord, level, modeName(m), prev, i)
			}
			seen[s] = i
		}
	}
}

func TestKeymap_EveryRowIsWellFormed(t *testing.T) {
	for i := range keymap {
		bd := &keymap[i]
		if bd.prefix != "" {
			continue
		}
		switch {
		case bd.pager == nil && bd.input == nil:
			t.Errorf("%s: no action", bd.chord)
		case bd.pager != nil && bd.input != nil:
			t.Errorf("%s: both a pager and an input action", bd.chord)
		}
		if bd.modes == 0 {
			t.Errorf("%s: live in no mode at all", bd.chord)
		}
		if bd.open == openUnset {
			t.Errorf("%s: no open policy: say whether it opens the pager from incipit", bd.chord)
		}
		if bd.chord.kind == chordCtrlLetter && (bd.chord.b < 'a' || bd.chord.b > 'z') {
			t.Errorf("%s: a ctrl chord must name a lowercase letter", bd.chord)
		}
		if bd.chord.kind == chordNav && (bd.chord.nav == navNone || int(bd.chord.nav) >= navCount) {
			t.Errorf("%s: not a navigation key", bd.chord)
		}
		// A Meta chord is a low, non-control byte, except Alt+Enter: ESC
		// CR, or CSI-u 13;3u.
		if bd.chord.kind == chordMeta && (bd.chord.b >= 128 || (bd.chord.b < 0x20 && bd.chord.b != 0x7f && bd.chord.b != 0x0d)) {
			t.Errorf("%s: not a key Meta can be held for", bd.chord)
		}
		// A pager row can never be live in incipit: the pager is not up.
		if bd.pager != nil && bd.modes&inIncipit != 0 {
			t.Errorf("%s: a pager action cannot run in incipit", bd.chord)
		}
	}
}

func TestKeymap_OpenersReallyOpenThePager(t *testing.T) {
	for i := range keymap {
		bd := &keymap[i]
		if bd.prefix != "" {
			continue
		}
		if bd.open != opensPager {
			continue
		}
		in, lt := navInput(t, &countingWriter{}, false)
		if rest := feed(t, in, bindingInput(bd)); len(rest) != 0 {
			t.Fatalf("%s: consume held %q", bd.chord, rest)
		}
		if !lt.transcriptActive() {
			t.Errorf("%s is marked as an opener but did not open the pager", bd.chord)
		}
	}
}

// ^L and ^T enter through their own action, so they are not openers in the gate sense.
func TestKeymap_InlineKeysStayInline(t *testing.T) {
	entersByAction := map[byte]bool{0x0c: true, 0x14: true} // ^L, ^T
	stopsTheLoop := map[byte]bool{0x03: true, 0x04: true, 'q': true}
	for i := range keymap {
		bd := &keymap[i]
		if bd.prefix != "" {
			continue
		}
		if bd.open != staysInline || bd.chord.kind != chordByte {
			continue
		}
		if entersByAction[bd.chord.b] || stopsTheLoop[bd.chord.b] {
			continue
		}
		if bd.modes&inIncipit == 0 {
			continue // not even live there
		}
		in, lt := navInput(t, &countingWriter{}, false)
		in.tc = newRecordingTerminal() // 'y' copies the aria id in incipit
		if rest := feed(t, in, bindingInput(bd)); len(rest) != 0 {
			t.Fatalf("%s: consume held %q", bd.chord, rest)
		}
		if lt.transcriptActive() {
			t.Errorf("%s (%s) says it stays inline but opened the pager", bd.chord, bd.action)
		}
	}
}

func TestHelp_EveryVisibleBindingIsDocumented(t *testing.T) {
	rows := map[helpID]helpRow{}
	for _, r := range helpRows {
		if _, dup := rows[r.id]; dup {
			t.Fatalf("help row %v declared twice", r.id)
		}
		rows[r.id] = r
	}
	documented := map[helpID]int{}
	for i := range keymap {
		bd := &keymap[i]
		if bd.prefix != "" {
			continue
		}
		if bd.hidden() {
			continue
		}
		if _, ok := rows[bd.help]; !ok {
			t.Errorf("%s points at help row %d, which does not exist", bd.chord, bd.help)
			continue
		}
		documented[bd.help]++
	}
	for _, r := range helpRows {
		if documented[r.id] == 0 {
			t.Errorf("help row %q documents no live binding: it should be deleted", r.text)
		}
	}
}

func TestHelp_PanelRendersTheTable(t *testing.T) {
	tr := &transcript{w: 200, h: 100}
	got := tr.helpLines()
	if len(got) < len(helpRows)+1 {
		t.Fatalf("help panel rendered %d rows, want at least %d", len(got), len(helpRows)+1)
	}
	if stripANSI(got[0]) != "" {
		t.Errorf("the panel must open with a blank spacer row, got %q", got[0])
	}
	for i, want := range helpBody() {
		if line := stripANSI(got[i+1]); line != want {
			t.Errorf("panel row %d = %q, want %q", i, line, want)
		}
		if !strings.HasPrefix(got[i+1], "\x1b[2m") {
			t.Errorf("panel row %d is not dim: %q", i, got[i+1])
		}
	}
}

// ^T is deliberately undocumented: ^L covers entering the pager.
func TestHelp_HiddenBindingsStayOffTheList(t *testing.T) {
	hidden := 0
	for i := range keymap {
		if keymap[i].hidden() {
			hidden++
		}
	}
	if hidden == 0 {
		t.Skip("no hidden bindings to check")
	}
	panel := strings.Join(helpBody(), "\n")
	if strings.Contains(panel, "^T") {
		t.Errorf("^T is a hidden binding but appears in the help panel:\n%s", panel)
	}
}
