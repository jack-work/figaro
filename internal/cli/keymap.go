package cli

import (
	"strings"

	"github.com/mattn/go-runewidth"
)

// The keymap: one declarative table naming every keybinding in the live TTY.

// keyMode is the input mode a keystroke lands in. The pager's sub-modes are
// modes proper, not flags checked ad hoc inside the handler.
type keyMode uint8

const (
	modeIncipit    keyMode = iota // inline streaming; the pager is not up
	modeTranscript                // the pager, no panel, not searching
	modeSearch                    // typing into the '/' box: almost all keys are text
	modeJump                      // typing into the ':' box: the same, for a coordinate
	modePanel                     // a '?'/'!'/'Q' panel is showing
	numKeyModes
)

// keyModeSet is the set of modes a binding is live in.
type keyModeSet uint8

const (
	inIncipit    keyModeSet = 1 << modeIncipit
	inTranscript keyModeSet = 1 << modeTranscript
	inSearchBox  keyModeSet = 1 << modeSearch
	inJumpBox    keyModeSet = 1 << modeJump
	inPanel      keyModeSet = 1 << modePanel

	// inPager is every mode with the pager up. Note that a transcript-mode
	// row is ALSO reachable while a panel is showing: the panel swallows only
	// its own keys and every other key wipes it and acts (see dispatch).
	inPager  = inTranscript | inSearchBox | inJumpBox | inPanel
	inAnyBox = inIncipit | inPager
)

// chordKind distinguishes the four shapes a physical key arrives in.
type chordKind uint8

const (
	chordByte       chordKind = iota // a plain byte (raw, or a CSI-u key that reduces to one)
	chordNav                         // the arrow cluster: Up/Down/PgUp/PgDn/Home/End/Left/Right
	chordCtrlLetter                  // a CSI-u Ctrl+<letter> report, modifiers intact
	chordMeta                        // Alt/Meta + a byte: ESC-prefixed, or CSI-u with the Alt bit
)

// chord is the logical key a binding matches. Terminal encodings are the
// business of key_input.go; by the time a chord exists the bytes are gone.
type chord struct {
	kind chordKind
	b    byte   // chordByte: the byte. chordCtrlLetter: the lowercase letter. chordMeta: the byte Alt was held for.
	nav  navKey // chordNav
}

func byteChord(b byte) chord      { return chord{kind: chordByte, b: b} }
func navChord(n navKey) chord     { return chord{kind: chordNav, nav: n} }
func ctrlChord(letter byte) chord { return chord{kind: chordCtrlLetter, b: letter} }

// metaChord is Alt+<byte>: M-b, M-d, M-<, M-DEL. Only the low 128 are
// addressable, which is every key a terminal can put Meta on.
func metaChord(b byte) chord { return chord{kind: chordMeta, b: b} }

// openPolicy says what a key does when it is pressed during inline (incipit)
// streaming. It is deliberately a tri-state with no usable zero value: a new
// binding must SAY which it is, so the '!' bug, a key that acted in the pager
// but had no way to get there: cannot recur.
type openPolicy uint8

const (
	openUnset   openPolicy = iota // invalid; TestKeymap_EveryRowIsWellFormed rejects it
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
	meta byte   // chordMeta value: the byte Alt was held for

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
	case ev.meta != 0:
		return metaChord(ev.meta)
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
	pager pagerFunc
	input inputFunc
}

func (b *keyBinding) hidden() bool { return b.help == helpNone }

// ---------------------------------------------------------------------------
// The table.
// ---------------------------------------------------------------------------

var keymap = []keyBinding{
	// -- input level: the keys that own the process ------------------------
	//
	// NOTE THE HOLE IN EVERY ONE OF THEM: `&^ inJumpBox`. A command line that
	// answers to the pager's escape hatches is not a command line -- ^D would
	// detach instead of deleting a character, ^C would end the session instead
	// of abandoning the line, ^L would re-enter a pager already up, and ^T
	// would do anything except transpose. Inside the box these keys are
	// readline's; outside it they are unchanged. The escape hatches are not
	// removed, only moved one press further away: ^D on an EMPTY box closes
	// the box, and the next ^D detaches (see cmdDeleteFwd).
	{
		chord: byteChord(0x03), modes: inAnyBox &^ inJumpBox,
		open: staysInline, why: "interrupt; handled whether or not the pager is up",
		help: helpInterrupt, input: inputInterrupt,
	},
	{
		chord: byteChord(0x04), modes: inAnyBox &^ inJumpBox,
		open: staysInline, why: "detach; handled whether or not the pager is up",
		help: helpDetach, input: inputDisconnect,
	},
	{
		// Not live in incipit (nothing to detach from yet, and opening the
		// pager only to tear it down is not a gesture), nor in the search box,
		// where it is literal text.
		// 'q' LEAVES WHAT IS OPEN, AND ONLY THEN THE SESSION. It is the
		// third spelling of Esc and ^[ -- Gluck: "q should only quit when no
		// pit is open" -- and the reason is that a pit is a thing you are IN.
		// Quitting the process from inside a list you opened to read is the
		// same surprise as `less` exiting your shell.
		chord: byteChord('q'), modes: inTranscript | inPanel,
		open: staysInline, why: "detach: it would open the pager and immediately tear it down",
		help: helpLeavePit, input: inputLeavePit,
	},
	{
		chord: byteChord(0x0c), modes: inAnyBox &^ inJumpBox,
		open: staysInline, why: "enters the pager through its own action",
		help: helpListen, input: inputEnterTranscript,
	},
	{
		chord: byteChord(0x14), modes: inAnyBox &^ inJumpBox,
		open: staysInline, why: "^T enters the pager through its own action",
		help: helpNone, input: inputEnterTranscript,
	},
	{
		chord: byteChord(0x0f), modes: inAnyBox &^ inJumpBox,
		open: opensPager, help: helpVerbose, input: inputToggleVerbose,
	},
	{
		// 'm' is MORE: the bar's own detail -- state names, the model, the
		// last-interaction time. A plain letter, on Gluck's instruction, and
		// it earns the promotion: ^V could not have the job anyway, because
		// in the ':' box that chord is PASTE, so the toggle would have been
		// the one key that means two things in the two places you are most
		// likely to press it.
		//
		// A DIFFERENT AXIS FROM ^O, which is verbose TOOL OUTPUT. "What is
		// this session" and "what did that tool say" are different questions
		// and they keep different keys.
		chord: byteChord('m'), modes: inTranscript | inPanel,
		open: opensPager, help: helpBarVerbose, input: inputToggleBarVerbose,
	},
	{
		// In incipit 'y' copies the aria id, a feature of its own, not a
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
		// NOT IN THE COMMAND BOX. inAnyBox includes it, and these are
		// INPUT-level rows, so they ran before the box's own ^N/^P and node
		// selection ate the completion menu the keys were meant to walk. A box
		// that has its own meaning for a chord must be excluded from the rows
		// that claim it globally.
		chord: ctrlChord('n'), modes: inAnyBox &^ inJumpBox,
		open: opensPager, help: helpSelectExtend, input: inputSelectNext,
	},
	{
		chord: ctrlChord('p'), modes: inAnyBox &^ inJumpBox,
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

	// -- pager level: the coordinate jump ----------------------------------
	{
		// ':' is a printable byte, and in incipit a printable byte composes a
		// steer. It is also a gesture that addresses a VIEWPORT, and there is
		// ':' IS THE COMMAND LINE, and it opens the pager to get one. It used to
		// stay inline, on the grounds that "a coordinate needs a viewport to
		// land in" -- true of `:12`, and false of every verb that came after:
		// `:open`, `:attend`, `:send` are things a reader means from anywhere,
		// and the pager is where their result is shown. So the key yanks the
		// pager up first, exactly as '?' and '!' do.
		chord: byteChord(':'), modes: inTranscript,
		open: opensPager,
		help: helpJump, pager: pagerJumpPrompt,
	},

	// -- pager level: panels -----------------------------------------------
	{chord: byteChord('?'), modes: inTranscript, open: opensPager, help: helpHelpPanel, pager: pagerHelpPanel},
	{chord: byteChord('!'), modes: inTranscript, open: opensPager, help: helpStatusPanel, pager: pagerStatusPanel},
	{chord: byteChord('Q'), modes: inTranscript, open: opensPager, help: helpQueuedPanel, pager: pagerQueuedPanel},

	// -- input level: hang up, and stay ------------------------------------
	{
		// The gesture Ctrl-C could never be: stop the conversation HERE and
		// keep watching. Ctrl-C ends the session with it (keyStop, exit 130);
		// this one returns keyHandled, so the pager stays up and the aria
		// stays ready for the next thing you type.
		chord: byteChord('H'), modes: inTranscript | inPanel,
		open: staysInline, why: "it addresses a turn that is streaming in the view you are already in",
		help: helpHangUp, input: inputHangUp,
	},
	{
		// The same gesture, dropping the queue. Deliberately NOT next to 'H'
		// on the keyboard and deliberately not a modifier on it: this is the
		// destructive one, and the two must not be neighbours. What it drops is
		// printed into the pager's notice and reprinted to the shell on the way
		// out, so a slip costs you the queue's PLACE, not its text.
		chord: byteChord('X'), modes: inTranscript | inPanel,
		open: staysInline, why: "it addresses a turn that is streaming in the view you are already in",
		help: helpHangUpDrop, input: inputHangUpDrop,
	},

	// 'x' DROPS THE SELECTED ROW in a pit that has one -- today the queue,
	// where it is `figaro queue rm <id>` under a keystroke. It is inert
	// everywhere else, which is why it is a pager row rather than an input one:
	// a key that means "delete" must not be live in a view with nothing to
	// delete. (Note 'X' above is hang-up-and-drop-the-whole-queue, and the two
	// being one letter apart is uncomfortable; see the plan.)
	{
		chord: byteChord('x'), modes: inPanel,
		open: staysInline, why: "it acts on a row in a pit that is already open",
		help: helpPitDrop, pager: pagerPitDrop,
	},

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

	// -- jump mode: the coordinate line owns the keyboard ------------------
	// Exactly the search box's shape, and for the same reason: anything with
	// no row here is literal text, which is what makes '/' an ordinary
	// character in here as ':' is one in there.
	{
		chord: byteChord(0x0d), modes: inJumpBox,
		open: staysInline, why: "only reachable with the jump prompt already up",
		help: helpJump, pager: jumpAccept,
	},
	{
		chord: byteChord(0x0a), modes: inJumpBox,
		open: staysInline, why: "only reachable with the jump prompt already up",
		help: helpJump, pager: jumpAccept,
	},
	{
		chord: byteChord(0x1b), modes: inJumpBox,
		open: staysInline, why: "only reachable with the jump prompt already up",
		help: helpJump, pager: jumpCancel,
	},
	// -- the command line's EMACS BINDINGS --------------------------------
	//
	// bash/readline, not vim: this is a command line, and the fingers that
	// press these keys learned them at a shell prompt. The set is readline's
	// own emacs keymap -- the manual's four blocks: moving, history, changing
	// text, killing and yanking -- pared to what a ONE-LINE box can honour.
	// What is deliberately absent, and why, is the list at the end of the
	// block. ^N/^P are HISTORY here rather than node selection: the one
	// deliberate remap, and the reason is that a box with history and a box
	// with a node cursor are different boxes.
	//
	// EVERY CHORD IS BOUND TWICE where a terminal has two ways to send it: the
	// raw control byte, and the CSI-u report a modern terminal sends instead.
	// figaro turns modified-key reporting on, so ^N arrives as \x1b[110;5u and
	// never as the byte 0x0e -- a table that binds only the byte silently does
	// nothing there. Measured in a pty: Tab built the completion menu and ^N
	// walked past it into the void, because the row meant to catch it was
	// keyed on an encoding that terminal never sends.

	// Moving.
	{chord: byteChord(0x01), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdHome},
	{chord: byteChord(0x05), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdEnd},
	{chord: byteChord(0x02), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdLeft},
	{chord: byteChord(0x06), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdRight},
	{chord: metaChord('b'), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdWordLeft},
	{chord: metaChord('f'), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdWordRight},
	{chord: byteChord(0x0c), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdRedraw},

	// Changing text.
	{chord: byteChord(0x04), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdDeleteFwd},
	{chord: byteChord(0x14), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdTranspose},
	{chord: metaChord('t'), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdTransposeWord},
	{chord: metaChord('u'), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdUpcase},
	{chord: metaChord('l'), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdDowncase},
	{chord: metaChord('c'), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdCapitalize},

	// Killing and yanking. Every kill feeds the kill ring, which is what makes
	// ^Y paste whatever any of them cut, and M-y walk back through the rest.
	{chord: byteChord(0x0b), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdKillToEnd},
	{chord: byteChord(0x15), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdKillToStart},
	{chord: byteChord(0x17), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdKillWord},
	{chord: metaChord('d'), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdKillWordFwd},
	{chord: metaChord(0x7f), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdKillWordAlpha},
	{chord: metaChord('\\'), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdDeleteSpace},
	{chord: byteChord(0x19), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdYank},
	{chord: metaChord('y'), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdYankPop},

	// History, including the incremental search.
	{chord: byteChord(0x10), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdHistory, pager: cmdHistPrev},
	{chord: byteChord(0x0e), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdHistory, pager: cmdHistNext},
	{chord: metaChord('<'), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdHistFirst},
	{chord: metaChord('>'), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdHistLast},
	{chord: byteChord(0x12), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdHistory, pager: cmdSearchPrev},
	{chord: byteChord(0x13), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdHistory, pager: cmdSearchNext},
	{chord: metaChord('p'), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdPrefixPrev},
	{chord: metaChord('n'), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdPrefixNext},
	{chord: metaChord('.'), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdYankLastArg},
	{chord: metaChord('_'), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdYankLastArg},

	// Undo, and the two ways to abandon a line.
	{chord: byteChord(0x1f), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdUndo},
	{chord: metaChord('r'), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdRevert},
	{chord: byteChord(0x07), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdAbort, pager: cmdAbort},
	{chord: byteChord(0x03), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdAbort, pager: cmdAbort},

	// Completion.
	{chord: byteChord(0x09), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdComplete, pager: cmdComplete},
	{chord: byteChord(0x16), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdPaste, pager: cmdPaste},
	{chord: ctrlChord('v'), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdPaste},
	{chord: metaChord('?'), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdListComplete},
	{chord: metaChord('*'), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdInsertComplete},

	// The same keys as CSI-u chords: see the note at the top of this block.
	// These are the rows that actually fire on a terminal that answers
	// \x1b[>1u, and the byte rows above are the ones that never do.
	{chord: ctrlChord('a'), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdHome},
	{chord: ctrlChord('e'), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdEnd},
	{chord: ctrlChord('b'), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdLeft},
	{chord: ctrlChord('f'), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdRight},
	{chord: ctrlChord('d'), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdDeleteFwd},
	{chord: ctrlChord('t'), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdTranspose},
	{chord: ctrlChord('k'), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdKillToEnd},
	{chord: ctrlChord('u'), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdKillToStart},
	{chord: ctrlChord('w'), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdKillWord},
	{chord: ctrlChord('y'), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdYank},
	{chord: ctrlChord('n'), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdHistNext},
	{chord: ctrlChord('p'), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdHistPrev},
	{chord: ctrlChord('r'), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdSearchPrev},
	{chord: ctrlChord('s'), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdSearchNext},
	{chord: ctrlChord('g'), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdAbort},
	{chord: ctrlChord('l'), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdRedraw},
	{chord: ctrlChord('h'), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: jumpBackspace},

	// NOT BOUND, each for a reason:
	//
	//   ^Q (quoted-insert)      Every printable byte in this box is ALREADY
	//                            literal text -- there is no binding to escape
	//                            past -- and the only thing quoting could add
	//                            is a control character the line can neither
	//                            render nor send. (^V was on this list for the
	//                            same reason and has been taken off it: it is
	//                            PASTE here, which is a different verb with an
	//                            obvious use.)
	//   ^X <anything>            readline's second keymap: macros (^X( ^X) ^Xe),
	//                            ^X^U (undo, which ^_ already is), ^X Del (^U),
	//                            ^X^R (re-read inputrc, meaningless here). A
	//                            prefix map for three aliases and a macro
	//                            recorder is state the keymap cannot see.
	//   M-Space / ^@ / ^X^X      The mark. Nothing in a one-line box acts on a
	//                            region, so a mark is a coordinate with no verb.
	//   ^] / M-^]                Character search: vim's f/F under a chord
	//                            nobody presses, on a line 40 columns wide.
	//   M-#                      Comment the line. There is no history file for
	//                            a comment to be parked in.
	//   M-<digit>, M--           Numeric arguments. Every verb here is one
	//                            press by design; a count is a second grammar.
	//   ^E, ^M-j (editing mode)  There is no vi mode to switch to, and ^E is
	//                            end-of-line.

	// The arrow cluster, which the box means literally: Up/Down are history,
	// Left/Right are the cursor, Home/End are the ends of the line.
	{chord: navChord(navUp), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdHistory, pager: cmdHistPrev},
	{chord: navChord(navDown), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdHistory, pager: cmdHistNext},
	{chord: navChord(navHome), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdHome},
	{chord: navChord(navEnd), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdEnd},
	{chord: navChord(navLeft), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdLeft},
	{chord: navChord(navRight), modes: inJumpBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdRight},

	{
		chord: byteChord(0x7f), modes: inJumpBox,
		open: staysInline, why: "only reachable with the jump prompt already up",
		help: helpNone, pager: jumpBackspace,
	},
	{
		chord: byteChord(0x08), modes: inJumpBox,
		open: staysInline, why: "only reachable with the jump prompt already up",
		help: helpNone, pager: jumpBackspace,
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
	helpJump
	helpYank
	helpVerbose
	helpSelect
	helpSelectExtend
	helpExpand
	helpInterrupt
	helpHangUp
	helpHangUpDrop
	helpEscape
	helpListen
	helpDetach
	helpStatusPanel
	helpQueuedPanel
	helpHelpPanel
	helpCmdHistory
	helpCmdComplete
	helpCmdEdit
	helpCmdPaste
	helpBarVerbose
	helpCmdAbort
	helpPitDrop
	helpLeavePit
)

// helpRow is one line of the panel: the key column and what it does. The key
// column is prose ("j/k · u/d · gg/G" reads better than a mechanical join of
// six labels); what the table enforces is the SET: every visible binding is
// documented by exactly one row, and every row documents at least one live
// binding, so a key cannot be added or removed behind the help panel's back.
type helpRow struct {
	id   helpID
	keys string
	text string
}

var helpRows = []helpRow{
	{helpLeavePit, "q", "close the pit; with none open, exit (the turn keeps running)"},
	{helpDetach, "^D", "exit; keeps the turn running"},
	{helpInterrupt, "^C", "exit by interrupt; stops the turn"},
	{helpHangUp, "H", "hang up: stop the turn, keep listening"},
	{helpHangUpDrop, "X", "hang up and drop queued messages (printed on exit)"},
	{helpScroll, "j/k · u/d · gg/G", "scroll · half-page · top/bottom"},
	{helpArrows, "↑/↓ · PgUp/PgDn", "the same, on the arrow cluster"},
	{helpHomeEnd, "Home / End", "top / bottom"},
	{helpSearch, "/", "search (Enter jump · Esc cancel typing)"},
	{helpSearchRepeat, "n / N", "next / previous match"},
	{helpJump, ":", "command line: any figaro verb, or a coordinate (:12, :12.3, :0)"},
	{helpCmdHistory, "(in :) ^P/^N · ^R", "command history · search it"},
	{helpCmdComplete, "(in :) Tab", "complete the verb, an id, or a flag"},
	// ONE ROW FOR THIRTY BINDINGS, on purpose. The ':' box is readline's
	// emacs keymap (keymap.go), and a panel that lists it key by key stops
	// being the answer to "how do I scroll". The full table is in
	// skills/figaro/reference/ui-stream.md; the promise here is the one a
	// shell user needs, which is that their fingers already know this box.
	{helpCmdEdit, "(in :) ^A ^E ^W ^K ^Y", "emacs/readline editing, the whole set"},
	{helpCmdAbort, "(in :) Esc / ^C / ^G", "abandon the line, close the box"},
	{helpPitDrop, "(in a list) x", "drop the selected entry (queue)"},
	{helpYank, "y", "copy selection (or aria id if none)"},
	{helpVerbose, "^O", "toggle verbose tool output"},
	{helpBarVerbose, "m", "more: state names, model, last interaction"},
	{helpCmdPaste, "(in :) ^V", "paste the clipboard"},
	{helpSelect, "^N/^P", "select next/previous node"},
	{helpSelectExtend, "^N/^P + Shift", "extend node selection (Alt+^N/^P fallback)"},
	{helpExpand, "Enter", "expand tools within the selection"},
	{helpEscape, "Esc", "clear selection / close panel"},
	{helpListen, "^L", "open the transcript (stays open until you close it)"},
	{helpStatusPanel, "!", "figaro status panel"},
	{helpQueuedPanel, "Q", "queued prompts panel"},
	{helpHelpPanel, "?", "close help"},
}

// helpKeyColumn is the display width the key column is padded to; the help
// text then starts at column 22, after the two-space indent.
const helpKeyColumn = 20

// helpBody renders the panel's rows: without the leading blank line, the
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
// The compiled dispatch tables. Built once, at init, from the rows above.

const noBinding = int8(-1)

// pagerFunc/inputFunc name the two action shapes; see keyBinding.
type pagerFunc = func(*transcript)
type inputFunc = func(*interactiveInput, keyEvent) keyVerdict

type pagerActions struct {
	byByte [int(numKeyModes) * 256]pagerFunc
	byNav  [numKeyModes][navCount]pagerFunc
	byCtrl [numKeyModes][26]pagerFunc
	byMeta [numKeyModes][128]pagerFunc
}

type inputActions struct {
	byByte [int(numKeyModes) * 256]inputFunc
	byNav  [numKeyModes][navCount]inputFunc
	byCtrl [numKeyModes][26]inputFunc
	byMeta [numKeyModes][128]inputFunc
}

// keyIndex maps a chord to its row in keymap, per mode. int8 keeps the whole
// index in a few cache lines; the table would have to grow past 127 rows
// before that mattered, and buildKeyIndex panics if it ever does.
type keyIndex struct {
	byByte [numKeyModes][256]int8
	byNav  [numKeyModes][navCount]int8
	byCtrl [numKeyModes][26]int8
	byMeta [numKeyModes][128]int8
}

var (
	pagerAct pagerActions
	inputAct inputActions

	inputIndex keyIndex // rows with an input-level action
	pagerIndex keyIndex // rows with a pager-level action

	openerByte [256]bool
	openerNav  [navCount]bool
	openerCtrl [26]bool
	openerMeta [128]bool

	// ctrlChordLetters marks the letters the table binds as CSI-u Ctrl chords.
	ctrlChordLetters [26]bool

	// metaModes marks the modes that bind Meta at all. See modeBindsMeta.
	metaModes [numKeyModes]bool
)

// navCount bounds the nav index; navRight is the last logical motion.
const navCount = int(navRight) + 1

func init() { buildKeyIndex() }

func buildKeyIndex() {
	if len(keymap) > 127 {
		panic("keymap: more rows than an int8 index can name")
	}
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
		for c := range inputIndex.byMeta[m] {
			inputIndex.byMeta[m][c], pagerIndex.byMeta[m][c] = noBinding, noBinding
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
				idx.byByte[m][bd.chord.b] = int8(i)
				pagerAct.byByte[byteSlot(m, bd.chord.b)] = bd.pager
				inputAct.byByte[byteSlot(m, bd.chord.b)] = bd.input
			case chordNav:
				idx.byNav[m][bd.chord.nav] = int8(i)
				pagerAct.byNav[m][bd.chord.nav] = bd.pager
				inputAct.byNav[m][bd.chord.nav] = bd.input
			case chordCtrlLetter:
				idx.byCtrl[m][bd.chord.b-'a'] = int8(i)
				pagerAct.byCtrl[m][bd.chord.b-'a'] = bd.pager
				inputAct.byCtrl[m][bd.chord.b-'a'] = bd.input
			case chordMeta:
				idx.byMeta[m][bd.chord.b] = int8(i)
				pagerAct.byMeta[m][bd.chord.b] = bd.pager
				inputAct.byMeta[m][bd.chord.b] = bd.input
			}
		}
		if bd.chord.kind == chordCtrlLetter {
			ctrlChordLetters[bd.chord.b-'a'] = true
		}
		if bd.chord.kind == chordMeta {
			for m := keyMode(0); m < numKeyModes; m++ {
				if bd.modes&(1<<m) != 0 {
					metaModes[m] = true
				}
			}
		}
		if bd.open == opensPager {
			switch bd.chord.kind {
			case chordByte:
				openerByte[bd.chord.b] = true
			case chordNav:
				openerNav[bd.chord.nav] = true
			case chordCtrlLetter:
				openerCtrl[bd.chord.b-'a'] = true
			case chordMeta:
				openerMeta[bd.chord.b] = true
			}
		}
	}
}

// pager resolves the pager action bound to a chord in one mode, or nil. The
// byte case: every keystroke that is not an arrow: is a single array load.
func (a *pagerActions) pager(mode keyMode, ev keyEvent) pagerFunc {
	if ev.nav != navNone {
		if int(ev.nav) >= navCount {
			return nil
		}
		return a.byNav[mode][ev.nav]
	}
	if ev.ctrl != 0 {
		if ev.ctrl < 'a' || ev.ctrl > 'z' {
			return nil
		}
		return a.byCtrl[mode][ev.ctrl-'a']
	}
	if ev.meta != 0 {
		if ev.meta >= 128 {
			return nil
		}
		return a.byMeta[mode][ev.meta]
	}
	return a.byByte[byteSlot(mode, ev.b)]
}

// input is the same resolution at the input-loop level.
func (a *inputActions) input(mode keyMode, ev keyEvent) inputFunc {
	if ev.nav != navNone {
		if int(ev.nav) >= navCount {
			return nil
		}
		return a.byNav[mode][ev.nav]
	}
	if ev.ctrl != 0 {
		if ev.ctrl < 'a' || ev.ctrl > 'z' {
			return nil
		}
		return a.byCtrl[mode][ev.ctrl-'a']
	}
	if ev.meta != 0 {
		if ev.meta >= 128 {
			return nil
		}
		return a.byMeta[mode][ev.meta]
	}
	return a.byByte[byteSlot(mode, ev.b)]
}

// byteSlot flattens (mode, byte) into one index, so the hot path is a single
// bounds-checked load rather than two.
func byteSlot(mode keyMode, b byte) int { return int(mode)<<8 | int(b) }

// lookup resolves the ROW behind a chord: metadata, not dispatch.
func (idx *keyIndex) lookup(mode keyMode, ev keyEvent) *keyBinding {
	var i int8
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
	case ev.meta != 0:
		if ev.meta >= 128 {
			return nil
		}
		i = idx.byMeta[mode][ev.meta]
	default:
		i = idx.byByte[mode][ev.b]
	}
	if i == noBinding {
		return nil
	}
	return &keymap[i]
}

// ctrlChordLetter reports whether a CSI-u report should be treated as a
// Ctrl+letter CHORD: modifiers and all: rather than reduced to the control
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

// ctrlChordBoundIn asks the same question OF ONE MODE, and it is the question
// the input loop actually has to answer.
//
// A chord row in one mode used to poison the letter everywhere: the moment the
// ':' box bound CSI-u Ctrl-N, every mode saw Ctrl-N as a chord, and a mode
// with no chord row for it got a dead key instead of the control byte. That is
// the same bug in the other direction as the one the comment above describes,
// and it is why ^D can be delete-forward in the box while staying detach in
// the pager: MODES DISAGREE ABOUT A KEY, so the reduction is per mode.
func ctrlChordBoundIn(mode keyMode, letter byte) bool {
	if letter < 'a' || letter > 'z' {
		return false
	}
	return inputIndex.byCtrl[mode][letter-'a'] != noBinding ||
		pagerIndex.byCtrl[mode][letter-'a'] != noBinding
}

// modeBindsMeta reports whether this mode binds Meta at all.
//
// THE ESC AMBIGUITY LIVES ON THIS FUNCTION. A terminal sends Alt-b as the two
// bytes ESC 'b', which is also what "Esc, then b" looks like when a fast typist
// produces both inside one read. Rather than a timeout, the decoder asks the
// table -- but it asks about the MODE, not about the individual chord, and the
// difference matters:
//
//   - In a mode with no Meta rows (the pager, the search box), an ESC pair is
//     what it has always been: a bare Esc, then an ordinary key. Nothing about
//     those modes changed.
//   - In a mode that HAS Meta rows (the ':' box), ESC <byte> is a Meta chord
//     whether or not that particular chord is bound, and an unbound one is
//     swallowed. Otherwise M-x for an x nobody bound would close the box and
//     type an x -- the two most destructive things it could have meant.
func modeBindsMeta(mode keyMode) bool { return metaModes[mode] }

// metaFold is the case rule for a Meta chord: Alt+Shift+B means Alt+b, because
// no row wants to tell them apart and a user holding Shift by accident should
// not lose the key. Bytes that are not letters pass through untouched, which
// is what keeps M-< and M-> (Shift-heavy by construction) addressable.
func metaFold(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b | 0x20
	}
	return b
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
	case ev.meta != 0:
		return ev.meta < 128 && openerMeta[ev.meta]
	default:
		return openerByte[ev.b]
	}
}

// opensTranscriptFor is the byte-shaped form of the same question.
func opensTranscriptFor(b byte) bool { return openerByte[b] }
