package cli

import (
	"fmt"
	"reflect"
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

func TestKeymap_IndexAgreesWithTheTable(t *testing.T) {
	for i := range keymap {
		bd := &keymap[i]
		if bd.prefix != "" {
			continue
		}
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
		case chordMeta:
			ev.meta = bd.chord.b
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

// The opener set is pinned by hand, so a row cannot become an opener unnoticed.
func TestOpensTranscript_MatchesTheHandKeptList(t *testing.T) {
	old := map[byte]bool{}
	for _, b := range []byte{
		'j', 'k', 'u', 'd', 'g', 'G', // scroll
		'/',           // search prompt
		':',           // command line
		'>',           // the draft: a prompt is dispatched from the pager, so it opens one
		'?', '!', 'Q', // backward search / figaro status / queued-prompt panels
		'h',      // the help panel
		'l', 'L', // the listing, and the home listing
		'^',        // attend the parent
		'S',        // the form, in the pit
		'D',        // the last decision, in the pit
		'T',        // focus the conversation, keeping the pit
		0x0f, 0x09, // ^O / Tab: the jumplist, back and forward
		'a',        // attend the fork point (or the selected list row)
		'f',        // arm the fork jump: f j / f k
		'c',        // arm the comment jump: c j / c k
		'm',        // more: the status bar's own detail
		's',        // the pinned question, a pager toggle and so an opener too
		0x0e, 0x10, // ^N/^P node selection
		0x0d, 0x0a, // Enter: open tool bodies, quoted passages and form deltas
		'e',      // the tool body alone
		'v', 'V', // visual selection: a gesture about rows, which only the pager has
		' ', // the leader: `space n` opens notifications, a pit
	} {
		old[b] = true
	}
	for b := 0; b < 256; b++ {
		if got, want := opensTranscriptFor(byte(b)), old[byte(b)]; got != want {
			t.Errorf("opensTranscriptFor(0x%02x) = %v, want %v", b, got, want)
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

// The rendered panel is pinned glyph for glyph.
func TestHelpBody_MatchesTheOldHandWrittenPanel(t *testing.T) {
	want := []string{
		"  q                   close the pit; with none open, exit (the turn keeps running)",
		"  ^D                  exit; keeps the turn running",
		"  ^C                  exit by interrupt; stops the turn",
		"  H                   hang up: stop the turn, keep listening",
		"  X                   hang up and drop queued messages (printed on exit)",
		"  j/k · u/d · gg/G    scroll · half-page · top/bottom",
		"  ↑/↓ · PgUp/PgDn     the same, on the arrow cluster",
		"  Home / End          top / bottom",
		"  / · ?               search forward / backward (in a pit, its rows)",
		"  n / N               next / previous match, with the cursor on it",
		"  :                   command line: any figaro verb, or a coordinate (:12, :12.3, :0)",
		"  >                   the composer: type or paste a prompt, newlines and quotes intact (in v it quotes the highlight)",
		"  (in >) ^S · M-Enter send the draft. ^S everywhere; M-Enter where the terminal does not eat it (Windows claims it)",
		"  (in >) Esc · i a I A normal mode (✎) and back to insert; Esc again puts the drawer away, keeping the draft",
		"  (in ✎) h j k l w b e 0 ^ $ gg G { } the transcript's motions, over the draft; / ? n N search it; F takes the pane; : is the command line",
		"  (in ✎) v / V · y    mark by character / by line; y copies the mark, or the whole draft",
		"  (in :) ^P/^N · ^R   command history · search it",
		"  (in :) Tab          complete the verb, an id, or a flag",
		"  (in :) ^A ^E ^W ^K ^Y emacs/readline editing, the whole set",
		"  (in :) Esc / ^C / ^G abandon the line, close the box",
		"  (in a list) x / X   drop or kill the selected row, asking first / at once",
		"  y · M-y             copy the selection, else the aria's name · its full id",
		"  M-m                 toggle verbose tool output and the node addresses",
		"  s                   pin the question of the turn you are inside",
		"  m                   more: state names, model, last interaction",
		"  (in :) ^V           paste the clipboard",
		"  ^N/^P               select next/previous node (each form delta is one)",
		"  f j / f k           next / previous fork point",
		"  c j / c k           next / previous comment",
		"  x                   on a comment or its message: delete the whole thread (asks first)",
		"  a                   attend the fork point's aria (in a list, the selected row's)",
		"  ^O / ^I             jumplist: back / forward through attended arias",
		"  M-n / M-p           travel to the next / previous question",
		"  ^N/^P + Shift       travel between questions (Alt+^N/^P extends a selection)",
		"  Enter               open tool bodies, quoted passages and form deltas within the selection",
		"  e                   open the tool bodies within the selection",
		"  (in a list) Enter   the row's own action: open a form value or a notification, attend an aria",
		"  (in v) Enter        select the node under the cursor (then e expands it)",
		"  (in v) a            attend the highlighted id, or the word under the cursor; a path opens in your editor",
		"  Esc                 clear selection / close panel",
		"  v / V               visual mode: a cursor; again to mark by character / by line (y yanks, : commands it)",
		"  (in v) h/l · ←/→    move the cursor's column",
		"  (in v) Y            copy the selection's coordinate (<lt.block:a-b>!)",
		"  (in v) w b e · 0 ^ $ · H M L · { } vim motions over the cursor; / n N land it on a match",
		"  (in :) M-Enter      submit, leave visual mode, and snap to the live tail",
		"  ^L                  open the transcript (stays open until you close it)",
		"  !                   figaro status panel",
		"  Q                   queued prompts panel; with nothing queued it says so on the bar and opens nothing",
		"  space n             notifications: what figaro said, and what went wrong",
		"  S                   the form (state) in the pit",
		"  D                   the last decision (a ?? draft or :decide) in the pit; with none yet it says so on the bar",
		"  T                   read the conversation without closing the pit (again to go back)",
		"  l / L               list this aria's tree / the home tree, in the pit (Enter attends, x kills)",
		"  y / n               answer a confirmation (Esc is no); X on a row kills with no question",
		"  h                   close help",
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
