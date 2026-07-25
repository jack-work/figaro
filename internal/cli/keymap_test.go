package cli

import (
	"fmt"
	"testing"
)

// ---------------------------------------------------------------------------
// The keymap as data: things you can only assert about a table.
//
// These are the tests the old five-inline-switches shape could not have. They
// walk the table itself, so a binding added tomorrow is covered by every one
// of them without anyone remembering to extend a list.
// ---------------------------------------------------------------------------

func (c chord) String() string {
	switch c.kind {
	case chordNav:
		return "nav:" + navName(c.nav)
	case chordCtrlLetter:
		return fmt.Sprintf("csi-u ctrl-%c", c.b)
	default:
		if c.b < 0x20 || c.b == 0x7f {
			return fmt.Sprintf("0x%02x", c.b)
		}
		return fmt.Sprintf("%q", string(c.b))
	}
}

func navName(n navKey) string {
	switch n {
	case navUp:
		return "Up"
	case navDown:
		return "Down"
	case navPageUp:
		return "PgUp"
	case navPageDown:
		return "PgDn"
	case navHome:
		return "Home"
	case navEnd:
		return "End"
	}
	return "none"
}

func modeName(m keyMode) string {
	switch m {
	case modeIncipit:
		return "incipit"
	case modeTranscript:
		return "transcript"
	case modeSearch:
		return "search"
	case modePanel:
		return "panel"
	}
	return "?"
}

// navSequence is the canonical terminal encoding of a navigation key — enough
// to drive one through the real input loop. (key_nav_test.go covers the other
// five encodings of each.)
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
	}
	return ""
}

// ctrlSequence is a CSI-u report for Ctrl+<letter>, optionally with Shift.
func ctrlSequence(letter byte, shift bool) string {
	mods := 1 + 4 // ctrl
	if shift {
		mods += 1
	}
	return fmt.Sprintf("\x1b[%d;%du", letter, mods)
}

// bindingInput is the byte string a terminal would send for a binding's chord.
func bindingInput(b *keyBinding) string {
	switch b.chord.kind {
	case chordNav:
		return navSequence(b.chord.nav)
	case chordCtrlLetter:
		return ctrlSequence(b.chord.b, false)
	default:
		return string(b.chord.b)
	}
}

// TestKeymap_NoDuplicateBindingPerMode: two rows claiming the same key in the
// same mode would mean the index silently picks one. That was exactly the old
// failure mode across files; here it is a test.
func TestKeymap_NoDuplicateBindingPerMode(t *testing.T) {
	type slot struct {
		c    chord
		m    keyMode
		leve string
	}
	seen := map[slot]int{}
	for i := range keymap {
		bd := &keymap[i]
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

// TestKeymap_EveryRowIsWellFormed: one action, a declared open policy, a
// reason when it stays inline, a legal chord.
func TestKeymap_EveryRowIsWellFormed(t *testing.T) {
	for i := range keymap {
		bd := &keymap[i]
		switch {
		case bd.pager == nil && bd.input == nil:
			t.Errorf("%s: no action", bd.chord)
		case bd.pager != nil && bd.input != nil:
			t.Errorf("%s: both a pager and an input action", bd.chord)
		}
		if bd.modes == 0 {
			t.Errorf("%s: live in no mode at all", bd.chord)
		}
		switch bd.open {
		case openUnset:
			t.Errorf("%s: no open policy — say whether it opens the pager from incipit", bd.chord)
		case staysInline:
			if bd.why == "" {
				t.Errorf("%s: stays inline without saying why", bd.chord)
			}
		}
		if bd.chord.kind == chordCtrlLetter && (bd.chord.b < 'a' || bd.chord.b > 'z') {
			t.Errorf("%s: a ctrl chord must name a lowercase letter", bd.chord)
		}
		if bd.chord.kind == chordNav && (bd.chord.nav == navNone || int(bd.chord.nav) >= navCount) {
			t.Errorf("%s: not a navigation key", bd.chord)
		}
		// A pager row can never be live in incipit: the pager is not up.
		if bd.pager != nil && bd.modes&inIncipit != 0 {
			t.Errorf("%s: a pager action cannot run in incipit", bd.chord)
		}
	}
}

// TestKeymap_IndexAgreesWithTheTable: every row is reachable through the
// compiled index in every mode it declares, and nowhere else.
func TestKeymap_IndexAgreesWithTheTable(t *testing.T) {
	for i := range keymap {
		bd := &keymap[i]
		idx := &pagerIndex
		other := &inputIndex
		if bd.input != nil {
			idx, other = &inputIndex, &pagerIndex
		}
		ev := keyEvent{}
		switch bd.chord.kind {
		case chordNav:
			ev.nav = bd.chord.nav
		case chordCtrlLetter:
			ev.ctrl = bd.chord.b
		default:
			ev.b = bd.chord.b
		}
		for m := keyMode(0); m < numKeyModes; m++ {
			got := idx.lookup(m, ev)
			want := bd.modes&(1<<m) != 0
			if want && got != bd {
				t.Errorf("%s in %s mode: index resolved to %v, want the declared row",
					bd.chord, modeName(m), got)
			}
			if !want && got == bd {
				t.Errorf("%s: reachable in %s mode, which it does not declare",
					bd.chord, modeName(m))
			}
			if o := other.lookup(m, ev); o == bd {
				t.Errorf("%s: indexed at the wrong dispatch level", bd.chord)
			}
		}
	}
}

// TestOpensTranscript_MatchesTheHandKeptList is the migration oracle: the
// table's opener set is byte-for-byte the list opensTranscriptFor used to
// carry by hand (including the '!' fix that landed just before it).
func TestOpensTranscript_MatchesTheHandKeptList(t *testing.T) {
	old := map[byte]bool{}
	for _, b := range []byte{
		'j', 'k', 'u', 'd', 'g', 'G', // scroll
		'/',           // search prompt
		'?', '!', 'Q', // help / figaro status / queued-prompt panels
		0x0f,       // ^O verbosity
		0x0e, 0x10, // ^N/^P node selection
		0x0d, 0x0a, // Enter: expand tools
	} {
		old[b] = true
	}
	for b := 0; b < 256; b++ {
		if got, want := opensTranscriptFor(byte(b)), old[byte(b)]; got != want {
			t.Errorf("opensTranscriptFor(0x%02x) = %v, want %v", b, got, want)
		}
	}
}

// TestKeymap_OpenersReallyOpenThePager drives every binding marked as an
// opener through the real input loop from incipit and insists the pager comes
// up. Table-driven off the table itself: a new opener is covered for free.
func TestKeymap_OpenersReallyOpenThePager(t *testing.T) {
	for i := range keymap {
		bd := &keymap[i]
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

// TestKeymap_InlineKeysStayInline is the other half: a binding that says it
// does not open the pager must not. The two exceptions are named here because
// the table names them too — ^L and ^T enter through their own action, which
// is why they are not openers in the gate sense.
func TestKeymap_InlineKeysStayInline(t *testing.T) {
	entersByAction := map[byte]bool{0x0c: true, 0x14: true} // ^L, ^T
	stopsTheLoop := map[byte]bool{0x03: true, 0x04: true, 'q': true}
	for i := range keymap {
		bd := &keymap[i]
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
			t.Errorf("%s says it stays inline (%s) but opened the pager", bd.chord, bd.why)
		}
	}
}
