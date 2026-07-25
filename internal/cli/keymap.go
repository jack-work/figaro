package cli

import (
	"strings"

	"github.com/mattn/go-runewidth"
)

// The keymap: one declarative table naming every keybinding in the live TTY.
//
// Before this file the answer to "what does this key do?" was spread across
// five places that had to agree by hand — the input loop's control-key switch,
// the pager's key switch, the search sub-mode switch, a hand-kept list of keys
// that may open the pager, and a hand-written help panel. They did not agree:
// '!' acted inside the pager but no key could get you there.
//
// Now a binding is DATA: the chord it matches, the modes it is live in,
// whether it opens the pager from incipit, its action, and the help row it is
// documented by. The opener set and the help panel are derived from the table,
// so neither can drift from the behaviour again.
//
// Dispatch is two-level, because the keyboard genuinely is:
//
//	input level   (interactiveInput) — keys that own the process: interrupt,
//	              detach, listen, clipboard. Live in incipit AND in the pager.
//	pager level   (transcript)       — motions, search, panels, selection.
//	              Only reachable with the pager up.
//
// A key is looked up at the input level first; if no row is live in the
// current mode, it falls through to the pager (which is how 'y' and 'q' become
// literal text while the search prompt is up — they have no search-mode row).
//
// Performance: the table is compiled once, at init, into fixed-size arrays of
// int16 indices. A keystroke costs an array index and a call through a
// package-level func value — no map, no closure, no allocation.

// keyMode is the input mode a keystroke lands in. The pager's sub-modes are
// modes proper, not flags checked ad hoc inside the handler.
type keyMode uint8

const (
	modeIncipit    keyMode = iota // inline streaming; the pager is not up
	modeTranscript                // the pager, no panel, not searching
	modeSearch                    // typing into the '/' box: almost all keys are text
	modePanel                     // a '?'/'!'/'Q' panel is showing
	numKeyModes
)

// keyModeSet is the set of modes a binding is live in.
type keyModeSet uint8

const (
	inIncipit    keyModeSet = 1 << modeIncipit
	inTranscript keyModeSet = 1 << modeTranscript
	inSearchBox  keyModeSet = 1 << modeSearch
	inPanel      keyModeSet = 1 << modePanel

	// inPager is every mode with the pager up. Note that a transcript-mode
	// row is ALSO reachable while a panel is showing: the panel swallows only
	// its own keys and every other key wipes it and acts (see dispatch).
	inPager  = inTranscript | inSearchBox | inPanel
	inAnyBox = inIncipit | inPager
)

// chordKind distinguishes the three shapes a physical key arrives in.
type chordKind uint8

const (
	chordByte       chordKind = iota // a plain byte (raw, or a CSI-u key that reduces to one)
	chordNav                         // the arrow cluster: Up/Down/PgUp/PgDn/Home/End
	chordCtrlLetter                  // a CSI-u Ctrl+<letter> report, modifiers intact
)

// chord is the logical key a binding matches. Terminal encodings are the
// business of key_input.go; by the time a chord exists the bytes are gone.
type chord struct {
	kind chordKind
	b    byte   // chordByte: the byte. chordCtrlLetter: the lowercase letter.
	nav  navKey // chordNav
}

func byteChord(b byte) chord      { return chord{kind: chordByte, b: b} }
func navChord(n navKey) chord     { return chord{kind: chordNav, nav: n} }
func ctrlChord(letter byte) chord { return chord{kind: chordCtrlLetter, b: letter} }

// openPolicy says what a key does when it is pressed during inline (incipit)
// streaming. It is deliberately a tri-state with no usable zero value: a new
// binding must SAY which it is, so the '!' bug — a key that acted in the pager
// but had no way to get there — cannot recur.
type openPolicy uint8

const (
	openUnset   openPolicy = iota // invalid; TestKeymap_EveryBindingDeclaresAnOpenPolicy rejects it
	opensPager                    // pressing it in incipit yanks the pager up first
	staysInline                   // it does not open the pager; `why` says so
)

// keyVerdict is what an input-level action tells the read loop.
type keyVerdict uint8

const (
	keyHandled keyVerdict = iota // consumed; go on to the next key
	keyStop                      // the input loop is finished (interrupt/detach)
)

// keyEvent is one decoded keystroke plus the mode it arrived in.
type keyEvent struct {
	b    byte   // chordByte value (0 for the other kinds)
	nav  navKey // chordNav value
	ctrl byte   // chordCtrlLetter value (lowercase letter)

	shift bool // carried for the rows that read modifiers (node-selection extend)
	alt   bool

	mode keyMode
}

func (ev keyEvent) chord() chord {
	switch {
	case ev.nav != navNone:
		return navChord(ev.nav)
	case ev.ctrl != 0:
		return ctrlChord(ev.ctrl)
	default:
		return byteChord(ev.b)
	}
}

// keyBinding is one row of the keymap.
type keyBinding struct {
	chord chord
	modes keyModeSet

	open openPolicy
	why  string // staysInline: the reason, kept where the behaviour is

	help helpID // helpNone marks a binding the user is not shown

	// Exactly one of pager/input is set. pager rows run inside the transcript
	// under the render lock; input rows run on the read loop and may stop it.
	pager func(*transcript)
	input func(*interactiveInput, keyEvent) keyVerdict
}

func (b *keyBinding) hidden() bool { return b.help == helpNone }

// ---------------------------------------------------------------------------
// The table.
// ---------------------------------------------------------------------------

var keymap = []keyBinding{
	// -- input level: the keys that own the process ------------------------
	{
		chord: byteChord(0x03), modes: inAnyBox,
		open: staysInline, why: "interrupt; handled whether or not the pager is up",
		help: helpInterrupt, input: inputInterrupt,
	},
	{
		chord: byteChord(0x04), modes: inAnyBox,
		open: staysInline, why: "detach; handled whether or not the pager is up",
		help: helpDetach, input: inputDisconnect,
	},
	{
		// Not live in incipit (nothing to detach from yet, and opening the
		// pager only to tear it down is not a gesture), nor in the search box,
		// where it is literal text.
		chord: byteChord('q'), modes: inTranscript | inPanel,
		open: staysInline, why: "detach: it would open the pager and immediately tear it down",
		help: helpDetach, input: inputDisconnect,
	},
	{
		chord: byteChord(0x0c), modes: inAnyBox,
		open: staysInline, why: "listen enters the pager through its own action",
		help: helpListen, input: inputListen,
	},
	{
		chord: byteChord(0x14), modes: inAnyBox,
		open: staysInline, why: "^T enters the pager through its own action",
		help: helpNone, input: inputEnterTranscript,
	},
	{
		chord: byteChord(0x0f), modes: inAnyBox,
		open: opensPager, help: helpVerbose, input: inputToggleVerbose,
	},
	{
		// In incipit 'y' copies the aria id — a feature of its own, not a
		// reason to open the pager. In the search box it is literal text.
		chord: byteChord('y'), modes: inIncipit | inTranscript | inPanel,
		open: staysInline, why: "in incipit it already copies the aria id",
		help: helpYank, input: inputYank,
	},
	{
		// CSI-u Ctrl-N/Ctrl-P: modifiers survive, so Shift (or Alt, the
		// portable fallback) extends the selection instead of moving it.
		// Distinct from the raw 0x0e/0x10 bytes below, which cannot carry a
		// modifier and go through the pager.
		chord: ctrlChord('n'), modes: inAnyBox,
		open: opensPager, help: helpSelectExtend, input: inputSelectNext,
	},
	{
		chord: ctrlChord('p'), modes: inAnyBox,
		open: opensPager, help: helpSelectExtend, input: inputSelectPrev,
	},

	// -- pager level: motions ----------------------------------------------
	{chord: byteChord('j'), modes: inTranscript, open: opensPager, help: helpScroll, pager: pagerLineDown},
	{chord: byteChord('k'), modes: inTranscript, open: opensPager, help: helpScroll, pager: pagerLineUp},
	{chord: byteChord('d'), modes: inTranscript, open: opensPager, help: helpScroll, pager: pagerHalfDown},
	{chord: byteChord('u'), modes: inTranscript, open: opensPager, help: helpScroll, pager: pagerHalfUp},
	{chord: byteChord('G'), modes: inTranscript, open: opensPager, help: helpScroll, pager: pagerTail},
	{chord: byteChord('g'), modes: inTranscript, open: opensPager, help: helpScroll, pager: pagerPendingTop},

	// The arrow cluster shares the motions, as peers of the letters rather
	// than by impersonating them.
	{chord: navChord(navUp), modes: inTranscript, open: opensPager, help: helpArrows, pager: pagerLineUp},
	{chord: navChord(navDown), modes: inTranscript, open: opensPager, help: helpArrows, pager: pagerLineDown},
	{chord: navChord(navPageUp), modes: inTranscript, open: opensPager, help: helpArrows, pager: pagerHalfUp},
	{chord: navChord(navPageDown), modes: inTranscript, open: opensPager, help: helpArrows, pager: pagerHalfDown},
	// Home is the whole of the two-key gg gesture in one press.
	{chord: navChord(navHome), modes: inTranscript, open: opensPager, help: helpHomeEnd, pager: pagerTop},
	{chord: navChord(navEnd), modes: inTranscript, open: opensPager, help: helpHomeEnd, pager: pagerTail},

	// -- pager level: search -----------------------------------------------
	{chord: byteChord('/'), modes: inTranscript, open: opensPager, help: helpSearch, pager: pagerSearchPrompt},
	{
		chord: byteChord('n'), modes: inTranscript,
		open: staysInline, why: "repeat search with no query yet: opens onto a no-op",
		help: helpSearchRepeat, pager: pagerFindNext,
	},
	{
		chord: byteChord('N'), modes: inTranscript,
		open: staysInline, why: "repeat search with no query yet: opens onto a no-op",
		help: helpSearchRepeat, pager: pagerFindPrev,
	},

	// -- pager level: panels -----------------------------------------------
	{chord: byteChord('?'), modes: inTranscript, open: opensPager, help: helpHelpPanel, pager: pagerHelpPanel},
	{chord: byteChord('!'), modes: inTranscript, open: opensPager, help: helpStatusPanel, pager: pagerStatusPanel},
	{chord: byteChord('Q'), modes: inTranscript, open: opensPager, help: helpQueuedPanel, pager: pagerQueuedPanel},

	// -- pager level: selection --------------------------------------------
	{chord: byteChord(0x0e), modes: inTranscript, open: opensPager, help: helpSelect, pager: pagerSelectNext},
	{chord: byteChord(0x10), modes: inTranscript, open: opensPager, help: helpSelect, pager: pagerSelectPrev},
	{chord: byteChord(0x0d), modes: inTranscript, open: opensPager, help: helpExpand, pager: pagerToggleTools},
	{chord: byteChord(0x0a), modes: inTranscript, open: opensPager, help: helpExpand, pager: pagerToggleTools},
	{
		chord: byteChord(0x1b), modes: inTranscript,
		open: staysInline, why: "clears a selection there is none of, and is a sequence prefix besides",
		help: helpEscape, pager: pagerClearSelection,
	},

	// -- panel mode: the panel keys swallow their own keys -----------------
	// Every OTHER key wipes the panel and then acts normally; that fallthrough
	// lives in dispatch, not here, because it is a property of the mode.
	{chord: byteChord('?'), modes: inPanel, open: opensPager, help: helpHelpPanel, pager: panelToggleHelp},
	{chord: byteChord('!'), modes: inPanel, open: opensPager, help: helpStatusPanel, pager: panelToggleStatus},
	{chord: byteChord('Q'), modes: inPanel, open: opensPager, help: helpQueuedPanel, pager: panelToggleQueued},
	{
		chord: byteChord(0x1b), modes: inPanel,
		open: staysInline, why: "closing a panel that is not showing is not an opening gesture",
		help: helpEscape, pager: panelDismiss,
	},

	// -- search mode: the query line owns the keyboard ---------------------
	// Anything with no row here and no input-level row is literal text (see
	// dispatch); the arrow cluster is swallowed whole.
	{
		chord: byteChord(0x0d), modes: inSearchBox,
		open: staysInline, why: "only reachable with the search prompt already up",
		help: helpSearch, pager: searchAccept,
	},
	{
		chord: byteChord(0x0a), modes: inSearchBox,
		open: staysInline, why: "only reachable with the search prompt already up",
		help: helpSearch, pager: searchAccept,
	},
	{
		chord: byteChord(0x1b), modes: inSearchBox,
		open: staysInline, why: "only reachable with the search prompt already up",
		help: helpSearch, pager: searchCancel,
	},
	{
		chord: byteChord(0x7f), modes: inSearchBox,
		open: staysInline, why: "only reachable with the search prompt already up",
		help: helpNone, pager: searchBackspace,
	},
	{
		chord: byteChord(0x08), modes: inSearchBox,
		open: staysInline, why: "only reachable with the search prompt already up",
		help: helpNone, pager: searchBackspace,
	},
}

// ---------------------------------------------------------------------------
// Help rows: the '?' panel, generated.
// ---------------------------------------------------------------------------

// helpID names a row of the help panel. Every visible binding points at one;
// a row may document several bindings (j and k share "scroll"), and the panel
// is rendered in the order the rows are declared here.
type helpID uint8

const (
	helpNone helpID = iota // the binding is deliberately not shown
	helpScroll
	helpArrows
	helpHomeEnd
	helpSearch
	helpSearchRepeat
	helpYank
	helpVerbose
	helpSelect
	helpSelectExtend
	helpExpand
	helpInterrupt
	helpEscape
	helpListen
	helpDetach
	helpStatusPanel
	helpQueuedPanel
	helpHelpPanel
)

// helpRow is one line of the panel: the key column and what it does. The key
// column is prose ("j/k · u/d · gg/G" reads better than a mechanical join of
// six labels); what the table enforces is the SET — every visible binding is
// documented by exactly one row, and every row documents at least one live
// binding, so a key cannot be added or removed behind the help panel's back.
type helpRow struct {
	id   helpID
	keys string
	text string
}

var helpRows = []helpRow{
	{helpScroll, "j/k · u/d · gg/G", "scroll · half-page · top/bottom"},
	{helpArrows, "↑/↓ · PgUp/PgDn", "the same, on the arrow cluster"},
	{helpHomeEnd, "Home / End", "top / bottom"},
	{helpSearch, "/", "search (Enter jump · Esc cancel typing)"},
	{helpSearchRepeat, "n / N", "next / previous match"},
	{helpYank, "y", "copy selection (or aria id if none)"},
	{helpVerbose, "^O", "toggle verbose tool output"},
	{helpSelect, "^N/^P", "select next/previous node"},
	{helpSelectExtend, "^N/^P + Shift", "extend node selection (Alt+^N/^P fallback)"},
	{helpExpand, "Enter", "expand tools within the selection"},
	{helpInterrupt, "^C", "copy selected node(s) / interrupt turn"},
	{helpEscape, "Esc", "clear selection / close panel"},
	{helpListen, "^L", "listen — stay open after the turn ends"},
	{helpDetach, "q / ^D", "detach; the turn keeps running"},
	{helpStatusPanel, "!", "figaro status panel"},
	{helpQueuedPanel, "Q", "queued prompts panel"},
	{helpHelpPanel, "?", "close help"},
}

// helpKeyColumn is the display width the key column is padded to; the help
// text then starts at column 22, after the two-space indent.
const helpKeyColumn = 20

// helpBody renders the panel's rows — without the leading blank line, the
// version footer, the height clamp or the dimming, which belong to the pager
// that knows its own geometry.
func helpBody() []string {
	rows := make([]string, 0, len(helpRows))
	for _, r := range helpRows {
		pad := helpKeyColumn - runewidth.StringWidth(r.keys)
		if pad < 1 {
			pad = 1
		}
		rows = append(rows, "  "+r.keys+strings.Repeat(" ", pad)+r.text)
	}
	return rows
}

// ---------------------------------------------------------------------------
// The compiled index. Built once; a keystroke is an array index.
// ---------------------------------------------------------------------------

const noBinding = int16(-1)

type keyIndex struct {
	byByte [numKeyModes][256]int16
	byNav  [numKeyModes][navCount]int16
	byCtrl [numKeyModes][26]int16
}

var (
	inputIndex keyIndex // rows with an input-level action
	pagerIndex keyIndex // rows with a pager-level action

	openerByte [256]bool
	openerNav  [navCount]bool
	openerCtrl [26]bool

	// ctrlChordLetters marks the letters the table binds as CSI-u Ctrl chords.
	ctrlChordLetters [26]bool
)

// navCount bounds the nav index; navEnd is the last logical motion.
const navCount = int(navEnd) + 1

func init() { buildKeyIndex() }

func buildKeyIndex() {
	for m := range numKeyModes {
		for b := range inputIndex.byByte[m] {
			inputIndex.byByte[m][b], pagerIndex.byByte[m][b] = noBinding, noBinding
		}
		for n := range inputIndex.byNav[m] {
			inputIndex.byNav[m][n], pagerIndex.byNav[m][n] = noBinding, noBinding
		}
		for c := range inputIndex.byCtrl[m] {
			inputIndex.byCtrl[m][c], pagerIndex.byCtrl[m][c] = noBinding, noBinding
		}
	}
	for i := range keymap {
		bd := &keymap[i]
		idx := &pagerIndex
		if bd.input != nil {
			idx = &inputIndex
		}
		for m := keyMode(0); m < numKeyModes; m++ {
			if bd.modes&(1<<m) == 0 {
				continue
			}
			switch bd.chord.kind {
			case chordByte:
				idx.byByte[m][bd.chord.b] = int16(i)
			case chordNav:
				idx.byNav[m][bd.chord.nav] = int16(i)
			case chordCtrlLetter:
				idx.byCtrl[m][bd.chord.b-'a'] = int16(i)
			}
		}
		if bd.chord.kind == chordCtrlLetter {
			ctrlChordLetters[bd.chord.b-'a'] = true
		}
		if bd.open == opensPager {
			switch bd.chord.kind {
			case chordByte:
				openerByte[bd.chord.b] = true
			case chordNav:
				openerNav[bd.chord.nav] = true
			case chordCtrlLetter:
				openerCtrl[bd.chord.b-'a'] = true
			}
		}
	}
}

func (idx *keyIndex) lookup(mode keyMode, ev keyEvent) *keyBinding {
	var i int16
	switch {
	case ev.nav != navNone:
		if int(ev.nav) >= navCount {
			return nil
		}
		i = idx.byNav[mode][ev.nav]
	case ev.ctrl != 0:
		if ev.ctrl < 'a' || ev.ctrl > 'z' {
			return nil
		}
		i = idx.byCtrl[mode][ev.ctrl-'a']
	default:
		i = idx.byByte[mode][ev.b]
	}
	if i == noBinding {
		return nil
	}
	return &keymap[i]
}

// ctrlChordLetter reports whether a CSI-u report should be treated as a
// Ctrl+letter CHORD — modifiers and all — rather than reduced to the control
// byte it would otherwise arrive as. The table decides: only letters some row
// binds as a chordCtrlLetter qualify, so CSI-u Ctrl-D still detaches through
// the 0x04 row exactly as a raw byte does.
func ctrlChordLetter(key modifiedKey) (byte, bool) {
	if !key.ctrl || key.nav != navNone {
		return 0, false
	}
	letter := key.code | 0x20 // fold Ctrl-Shift-N's 'N' onto 'n'
	if letter < 'a' || letter > 'z' {
		return 0, false
	}
	return letter, ctrlChordLetters[letter-'a']
}

// opensTranscript reports whether a key pressed during inline (incipit)
// streaming should yank the pager up first, so it acts on arrival instead of
// looking like a dead keyboard. It is now a table lookup: a binding says so on
// its own row, next to the action it performs.
func opensTranscript(ev keyEvent) bool {
	switch {
	case ev.nav != navNone:
		return int(ev.nav) < navCount && openerNav[ev.nav]
	case ev.ctrl != 0:
		return ev.ctrl >= 'a' && ev.ctrl <= 'z' && openerCtrl[ev.ctrl-'a']
	default:
		return openerByte[ev.b]
	}
}

// opensTranscriptFor is the byte-shaped form of the same question.
func opensTranscriptFor(b byte) bool { return openerByte[b] }
