package cli

import (
	"fmt"
	"reflect"
	"strings"
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
	case modeJump:
		return "jump"
	case modePanel:
		return "panel"
	}
	return "?"
}

// navSequence is the canonical terminal encoding of a navigation key: enough
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
			t.Errorf("%s: no open policy: say whether it opens the pager from incipit", bd.chord)
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
//
// ONE DELIBERATE ADDITION SINCE: ':'. It was a coordinate box, which needs a
// viewport to land in and so stayed inline; it is now the COMMAND LINE, and
// :open/:attend/:send are things a reader means from anywhere. So it yanks the
// pager up exactly as '?' and '!' do.
func TestOpensTranscript_MatchesTheHandKeptList(t *testing.T) {
	old := map[byte]bool{}
	for _, b := range []byte{
		'j', 'k', 'u', 'd', 'g', 'G', // scroll
		'/',           // search prompt
		':',           // command line (see above)
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
// the table names them too: ^L and ^T enter through their own action, which
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

// ---------------------------------------------------------------------------
// The help panel, generated.
// ---------------------------------------------------------------------------

// TestHelpBody_MatchesTheOldHandWrittenPanel: the generated rows are, glyph
// for glyph, the panel that used to be a hand-kept []string in helpLines. The
// user must not see a difference: only correctness by construction.
//
// The 'i' row is the one deliberate addition since: the steer composer is a
// genuinely new binding, not drift. Everything else is unchanged.
//
// The ':' row is the second: the coordinate jump (transcript_jump.go). It is
// listed directly under the search rows because it is the same gesture family
// : type something into the footer, land somewhere, and because that keeps
// "how do I get to a place I can see the address of" adjacent to "how do I
// find one".
func TestHelpBody_MatchesTheOldHandWrittenPanel(t *testing.T) {
	want := []string{
		"  q                   exit; keeps the turn running",
		"  ^C                  exit by interrupt; stops the turn",
		"  H                   hang up: stop the turn, keep listening",
		"  X                   hang up and drop queued messages (printed on exit)",
		"  j/k · u/d · gg/G    scroll · half-page · top/bottom",
		"  ↑/↓ · PgUp/PgDn     the same, on the arrow cluster",
		"  Home / End          top / bottom",
		"  /                   search (Enter jump · Esc cancel typing)",
		"  n / N               next / previous match",
		"  :                   command line: any figaro verb, or a coordinate (:12, :12.3, :0)",
		"  (in :) ^P/^N · Up/Down command history",
		"  (in :) Tab          complete the verb, an id, or a flag",
		"  (in a list) x       drop the selected entry (queue)",
		"  y                   copy selection (or aria id if none)",
		"  ^O                  toggle verbose tool output",
		"  ^N/^P               select next/previous node",
		"  ^N/^P + Shift       extend node selection (Alt+^N/^P fallback)",
		"  Enter               expand tools within the selection",
		"  Esc                 clear selection / close panel",
		"  ^L                  open the transcript (stays open until you close it)",
		"  !                   figaro status panel",
		"  Q                   queued prompts panel",
		"  ?                   close help",
	}
	got := helpBody()
	if len(got) != len(want) {
		t.Fatalf("help panel has %d rows, want %d:\n%s", len(got), len(want), strings.Join(got, "\n"))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d:\n got %q\nwant %q", i, got[i], want[i])
		}
	}
}

// TestHelp_EveryVisibleBindingIsDocumented is the anti-drift test: a binding
// the user can press either points at a help row or is explicitly hidden, and
// every help row documents at least one binding that is actually live.
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

// TestHelp_PanelRendersTheTable: what reaches the screen is the table, in
// table order, once the pager has dimmed and clipped it.
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

// TestHelp_HiddenBindingsStayOffTheList: ^T is deliberately undocumented (^L
// covers entering the pager in the panel's telling). A hidden binding must
// not sneak into the rendered help.
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

// TestKeymap_ActionArraysAgreeWithTheIndex: dispatch reads the compiled action
// arrays, everything else reads the row index. Both are built from the same
// table in the same pass: this asserts they cannot have drifted anyway.
func TestKeymap_ActionArraysAgreeWithTheIndex(t *testing.T) {
	same := func(a, b any) bool {
		return reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer()
	}
	for m := keyMode(0); m < numKeyModes; m++ {
		for b := 0; b < 256; b++ {
			ev := keyEvent{b: byte(b)}
			checkSlot(t, m, ev, pagerAct.pager(m, ev), inputAct.input(m, ev), same)
		}
		for n := navUp; n <= navEnd; n++ {
			ev := keyEvent{nav: n}
			checkSlot(t, m, ev, pagerAct.pager(m, ev), inputAct.input(m, ev), same)
		}
		for c := byte('a'); c <= 'z'; c++ {
			ev := keyEvent{ctrl: c}
			checkSlot(t, m, ev, pagerAct.pager(m, ev), inputAct.input(m, ev), same)
		}
	}
}

func checkSlot(t *testing.T, m keyMode, ev keyEvent, pact pagerFunc, iact inputFunc, same func(a, b any) bool) {
	t.Helper()
	if bd := pagerIndex.lookup(m, ev); bd != nil {
		if pact == nil || !same(pact, bd.pager) {
			t.Errorf("%s in %s mode: the pager action array disagrees with the row", ev.chord(), modeName(m))
		}
	} else if pact != nil {
		t.Errorf("%s in %s mode: a pager action with no row behind it", ev.chord(), modeName(m))
	}
	if bd := inputIndex.lookup(m, ev); bd != nil {
		if iact == nil || !same(iact, bd.input) {
			t.Errorf("%s in %s mode: the input action array disagrees with the row", ev.chord(), modeName(m))
		}
	} else if iact != nil {
		t.Errorf("%s in %s mode: an input action with no row behind it", ev.chord(), modeName(m))
	}
}
