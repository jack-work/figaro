package cli

import (
	"fmt"
	"strings"

	"github.com/mattn/go-runewidth"
)

type keyMode uint8

const (
	modeIncipit    keyMode = iota // inline streaming; the pager is not up
	modeTranscript                // the pager, no panel, not searching
	modeSearch                    // typing into the '/' box: almost all keys are text
	modeBox                       // typing into the ':' box: the same, for a coordinate
	modePanel                     // a '?'/'!'/'Q' panel is showing
	modeVisual                    // v/V: a visual selection is up and owns the motions
	modeFork                      // 'f' is down, waiting for the direction of the fork jump
	modeConfirm                   // a y/n question owns the keyboard until it is answered
	modeComposer                  // the composer's NORMAL mode: motions over the draft
	modeComment
	numKeyModes
)

type keyModeSet uint16

const (
	inIncipit    keyModeSet = 1 << modeIncipit
	inTranscript keyModeSet = 1 << modeTranscript
	inSearchBox  keyModeSet = 1 << modeSearch
	inBox        keyModeSet = 1 << modeBox
	inPanel      keyModeSet = 1 << modePanel
	inVisual     keyModeSet = 1 << modeVisual
	inFork       keyModeSet = 1 << modeFork
	inConfirm    keyModeSet = 1 << modeConfirm
	inComposer   keyModeSet = 1 << modeComposer
	inComment    keyModeSet = 1 << modeComment

	// A transcript-mode row is also reachable with a panel up: the panel
	// swallows only its own keys.
	inPager  = inTranscript | inSearchBox | inBox | inPanel | inVisual | inFork | inConfirm | inComposer | inComment
	inAnyBox = inIncipit | inPager
)

type chordKind uint8

const (
	chordByte       chordKind = iota // a plain byte (raw, or a CSI-u key that reduces to one)
	chordNav                         // the arrow cluster: Up/Down/PgUp/PgDn/Home/End/Left/Right
	chordCtrlLetter                  // a CSI-u Ctrl+<letter> report, modifiers intact
	chordMeta                        // Alt/Meta + a byte: ESC-prefixed, or CSI-u with the Alt bit
)

type chord struct {
	kind chordKind
	b    byte   // chordByte: the byte. chordCtrlLetter: the lowercase letter. chordMeta: the byte Alt was held for.
	nav  navKey // chordNav
}

func byteChord(b byte) chord      { return chord{kind: chordByte, b: b} }
func navChord(n navKey) chord     { return chord{kind: chordNav, nav: n} }
func ctrlChord(letter byte) chord { return chord{kind: chordCtrlLetter, b: letter} }

func metaChord(b byte) chord { return chord{kind: chordMeta, b: b} }

// openPolicy has no usable zero value: every row must say whether it opens the pager.
type openPolicy uint8

const (
	openUnset   openPolicy = iota
	opensPager             // pressing it in incipit yanks the pager up first
	staysInline            // it does not open the pager; `why` says so
)

type keyVerdict uint8

const (
	keyHandled keyVerdict = iota // consumed; go on to the next key
	keyStop                      // the input loop is finished (interrupt/detach)
)

type keyEvent struct {
	b    byte   // chordByte value (0 for the other kinds)
	nav  navKey // chordNav value
	ctrl byte   // chordCtrlLetter value (lowercase letter)
	meta byte   // chordMeta value: the byte Alt was held for

	shift bool
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

type keyBinding struct {
	chord chord
	modes keyModeSet

	open openPolicy
	why  string

	help helpID

	// Exactly one of pager/input is set. pager rows run inside the transcript
	// under the render lock; input rows run on the read loop and may stop it.
	pager pagerFunc
	input inputFunc
}

func (b *keyBinding) hidden() bool { return b.help == helpNone }

// Ctrl+M is Enter (0x0d) and Tab is ^I: neither can be bound apart from the other.
var keymap = []keyBinding{
	// Input level. Inside the ':' box these chords are readline's, hence `&^
	// inBox`.
	{
		chord: byteChord(0x03), modes: inAnyBox &^ inBox,
		open: staysInline, why: "interrupt; handled whether or not the pager is up",
		help: helpInterrupt, input: inputInterrupt,
	},
	{
		chord: byteChord(0x04), modes: inAnyBox &^ inBox,
		open: staysInline, why: "detach; handled whether or not the pager is up",
		help: helpDetach, input: inputDisconnect,
	},
	{
		chord: byteChord('q'), modes: inTranscript | inPanel | inVisual,
		open: staysInline, why: "detach: it would open the pager and immediately tear it down",
		help: helpLeavePit, input: inputLeavePit,
	},
	{
		chord: byteChord(0x0c), modes: inAnyBox &^ inBox,
		open: staysInline, why: "enters the pager through its own action",
		help: helpListen, input: inputEnterTranscript,
	},
	{
		chord: byteChord(0x14), modes: inAnyBox &^ inBox,
		open: staysInline, why: "^T enters the pager through its own action",
		help: helpNone, input: inputEnterTranscript,
	},
	{
		// Alt, not a plain letter: in incipit a printable byte composes a
		// steer.
		chord: metaChord('m'), modes: inAnyBox,
		open: opensPager, help: helpVerbose, input: inputToggleVerbose,
	},
	{
		// Alt arrives as an ESC prefix on every terminal; Ctrl+Shift needs
		// a protocol tmux must opt into.
		chord: metaChord('n'), modes: inTranscript | inPanel,
		open: opensPager, help: helpQuestionTravel, input: inputQuestionNext,
	},
	{
		chord: metaChord('p'), modes: inTranscript | inPanel,
		open: opensPager, help: helpNone, input: inputQuestionPrev,
	},
	{
		chord: byteChord('s'), modes: inTranscript | inPanel,
		open: opensPager, help: helpSticky, input: inputToggleSticky,
	},
	{
		chord: byteChord('m'), modes: inTranscript | inPanel | inVisual,
		open: opensPager, help: helpBarVerbose, input: inputToggleBarVerbose,
	},
	{
		chord: byteChord('y'), modes: inIncipit | inTranscript | inPanel | inVisual | inComposer,
		open: staysInline, why: "in incipit it already copies the aria id",
		help: helpYank, input: inputYank,
	},
	{
		// CSI-u Ctrl-N/P keep their modifiers (Shift/Alt extends); the raw
		// 0x0e/0x10 bytes cannot. Input-level rows run before the box's
		// own, so a box that binds the chord must be excluded here.
		chord: ctrlChord('n'), modes: inAnyBox &^ inBox,
		open: opensPager, help: helpSelectExtend, input: inputSelectNext,
	},
	{
		chord: ctrlChord('p'), modes: inAnyBox &^ inBox,
		open: opensPager, help: helpSelectExtend, input: inputSelectPrev,
	},

	{chord: byteChord('j'), modes: inTranscript, open: opensPager, help: helpScroll, pager: pagerLineDown},
	{chord: byteChord('k'), modes: inTranscript, open: opensPager, help: helpScroll, pager: pagerLineUp},
	{chord: byteChord('d'), modes: inTranscript, open: opensPager, help: helpScroll, pager: pagerHalfDown},
	{chord: byteChord('u'), modes: inTranscript, open: opensPager, help: helpScroll, pager: pagerHalfUp},
	{chord: byteChord('G'), modes: inTranscript, open: opensPager, help: helpScroll, pager: pagerTail},
	{chord: byteChord('g'), modes: inTranscript, open: opensPager, help: helpScroll, pager: pagerPendingTop},

	// Fork points: with 'f' down the next key is read from the inFork rows,
	// else falls through.
	{chord: byteChord('f'), modes: inTranscript, open: opensPager, help: helpForkJump, pager: pagerForkPending},
	{chord: byteChord('j'), modes: inFork, open: opensPager, help: helpNone, pager: pagerForkNext},
	{chord: byteChord('k'), modes: inFork, open: opensPager, help: helpNone, pager: pagerForkPrev},
	{chord: navChord(navDown), modes: inFork, open: opensPager, help: helpNone, pager: pagerForkNext},
	{chord: navChord(navUp), modes: inFork, open: opensPager, help: helpNone, pager: pagerForkPrev},
	{
		chord: byteChord(0x1b), modes: inFork,
		open: staysInline, why: "it abandons a gesture that is only half typed",
		help: helpNone, pager: pagerForkCancel,
	},

	{chord: byteChord('c'), modes: inTranscript, open: opensPager, help: helpCommentJump, pager: pagerCommentPending},
	{chord: byteChord('j'), modes: inComment, open: opensPager, help: helpNone, pager: pagerCommentNext},
	{chord: byteChord('k'), modes: inComment, open: opensPager, help: helpNone, pager: pagerCommentPrev},
	{chord: navChord(navDown), modes: inComment, open: opensPager, help: helpNone, pager: pagerCommentNext},
	{chord: navChord(navUp), modes: inComment, open: opensPager, help: helpNone, pager: pagerCommentPrev},
	{
		chord: byteChord(0x1b), modes: inComment,
		open: staysInline, why: "it abandons a gesture that is only half typed",
		help: helpNone, pager: pagerCommentCancel,
	},

	// Confirm mode: dispatch swallows every key that is not an answer.
	{chord: byteChord('y'), modes: inConfirm, open: staysInline, why: "only reachable with a question up", help: helpConfirm, pager: confirmYes},
	{chord: byteChord('Y'), modes: inConfirm, open: staysInline, why: "only reachable with a question up", help: helpNone, pager: confirmYes},
	{chord: byteChord('n'), modes: inConfirm, open: staysInline, why: "only reachable with a question up", help: helpNone, pager: confirmNo},
	{chord: byteChord('N'), modes: inConfirm, open: staysInline, why: "only reachable with a question up", help: helpNone, pager: confirmNo},
	{chord: byteChord(0x1b), modes: inConfirm, open: staysInline, why: "only reachable with a question up", help: helpNone, pager: confirmNo},

	{chord: byteChord('a'), modes: inTranscript, open: opensPager, help: helpAttend, pager: pagerAttendFork},
	{chord: byteChord('a'), modes: inPanel, open: opensPager, help: helpAttend, pager: pagerAttendRow},
	{chord: byteChord(0x0d), modes: inPanel, open: opensPager, help: helpPitEnter, pager: pagerPitActivate},
	{chord: byteChord(0x0a), modes: inPanel, open: opensPager, help: helpPitEnter, pager: pagerPitActivate},
	{chord: byteChord('^'), modes: inTranscript, open: opensPager, help: helpAttend, pager: pagerAttendParent},

	// Jumplist. ^I is Tab, which is completion in the ':' box, so these rows
	// do not name it.
	{chord: byteChord(0x0f), modes: inTranscript, open: opensPager, help: helpAriaJump, pager: pagerAriaBack},
	{chord: ctrlChord('o'), modes: inTranscript, open: opensPager, help: helpNone, pager: pagerAriaBack},
	{chord: byteChord(0x09), modes: inTranscript, open: opensPager, help: helpNone, pager: pagerAriaForward},
	{chord: ctrlChord('i'), modes: inTranscript, open: opensPager, help: helpNone, pager: pagerAriaForward},

	{chord: navChord(navUp), modes: inTranscript, open: opensPager, help: helpArrows, pager: pagerLineUp},
	{chord: navChord(navDown), modes: inTranscript, open: opensPager, help: helpArrows, pager: pagerLineDown},
	{chord: navChord(navPageUp), modes: inTranscript, open: opensPager, help: helpArrows, pager: pagerHalfUp},
	{chord: navChord(navPageDown), modes: inTranscript, open: opensPager, help: helpArrows, pager: pagerHalfDown},
	{chord: navChord(navHome), modes: inTranscript, open: opensPager, help: helpHomeEnd, pager: pagerTop},
	{chord: navChord(navEnd), modes: inTranscript, open: opensPager, help: helpHomeEnd, pager: pagerTail},

	// Search; in a pit the same box walks the rows.
	{chord: byteChord('/'), modes: inTranscript | inPanel, open: opensPager, help: helpSearch, pager: pagerSearchPrompt},
	{chord: byteChord('?'), modes: inTranscript | inPanel, open: opensPager, help: helpSearch, pager: pagerSearchPromptBack},
	// n/N enter visual mode with the cursor on the hit.
	{
		chord: byteChord('n'), modes: inTranscript,
		open: staysInline, why: "repeat search with no query yet: opens onto a no-op",
		help: helpSearchRepeat, pager: pagerFindNextVisual,
	},
	{
		chord: byteChord('n'), modes: inPanel,
		open: staysInline, why: "repeat search with no query yet: opens onto a no-op",
		help: helpNone, pager: pagerFindRowNext,
	},
	{
		chord: byteChord('N'), modes: inPanel,
		open: staysInline, why: "repeat search with no query yet: opens onto a no-op",
		help: helpNone, pager: pagerFindRowPrev,
	},
	{
		chord: byteChord('N'), modes: inTranscript,
		open: staysInline, why: "repeat search with no query yet: opens onto a no-op",
		help: helpSearchRepeat, pager: pagerFindPrevVisual,
	},

	{
		chord: byteChord(':'), modes: inTranscript,
		open: opensPager,
		help: helpJump, pager: pagerCommandBox,
	},

	{
		chord: byteChord('>'), modes: inTranscript,
		open: opensPager,
		help: helpDraft, pager: pagerComposerBox,
	},
	{
		// With a highlight up the draft opens holding the range.
		chord: byteChord('>'), modes: inVisual,
		open: opensPager,
		help: helpDraft, pager: pagerVisualComposer,
	},

	{chord: byteChord('h'), modes: inTranscript, open: opensPager, help: helpHelpPanel, pager: pagerHelpPanel},
	{chord: byteChord('!'), modes: inTranscript, open: opensPager, help: helpStatusPanel, pager: pagerStatusPanel},
	{chord: byteChord('Q'), modes: inTranscript, open: opensPager, help: helpQueuedPanel, pager: pagerQueuedPanel},
	{chord: byteChord('D'), modes: inTranscript | inPanel, open: opensPager, help: helpDecisionPit, pager: pagerDecisionPit},
	// Space is the leader; dispatch (transcript.leaderChord) takes the second
	// key before the mode's rows.
	{chord: byteChord(' '), modes: inTranscript | inPanel, open: opensPager, help: helpNotifications, pager: pagerLeader},
	{chord: byteChord('l'), modes: inTranscript | inPanel, open: opensPager, help: helpListPit, pager: pagerListPit},
	{chord: byteChord('L'), modes: inTranscript | inPanel, open: opensPager, help: helpListPit, pager: pagerListHome},
	{chord: byteChord('S'), modes: inTranscript, open: opensPager, help: helpFormPit, pager: pagerFormPit},
	{chord: byteChord('T'), modes: inTranscript, open: opensPager, help: helpFocus, pager: pagerFocusTranscript},

	{
		// keyHandled, not keyStop: the pager stays up.
		chord: byteChord('H'), modes: inTranscript | inPanel,
		open: staysInline, why: "it addresses a turn that is streaming in the view you are already in",
		help: helpHangUp, input: inputHangUp,
	},
	{
		// Not in a pit, where 'X' drops the selected row.
		chord: byteChord('X'), modes: inTranscript | inVisual,
		open: staysInline, why: "it addresses a turn that is streaming in the view you are already in",
		help: helpHangUpDrop, input: inputHangUpDrop,
	},

	{
		chord: byteChord('x'), modes: inPanel,
		open: staysInline, why: "it acts on a row in a pit that is already open",
		help: helpPitDrop, pager: pagerPitDrop,
	},
	{
		chord: byteChord('x'), modes: inTranscript,
		open: staysInline, why: "it acts on a comment already selected in the pager",
		help: helpCommentDelete, pager: pagerDeleteComment,
	},
	{
		chord: byteChord('X'), modes: inPanel,
		open: staysInline, why: "it acts on a row in a pit that is already open",
		help: helpPitDrop, pager: pagerPitDropNow,
	},

	{chord: byteChord(0x0e), modes: inTranscript, open: opensPager, help: helpSelect, pager: pagerSelectNext},
	{chord: byteChord(0x10), modes: inTranscript, open: opensPager, help: helpSelect, pager: pagerSelectPrev},
	{chord: byteChord(0x0d), modes: inTranscript, open: opensPager, help: helpExpand, pager: pagerToggleBlocks},
	{chord: byteChord(0x0a), modes: inTranscript, open: opensPager, help: helpExpand, pager: pagerToggleBlocks},
	// Enter is the only key that opens form deltas; 'e' opens tool bodies alone.
	{chord: byteChord('e'), modes: inTranscript, open: opensPager, help: helpToolBody, pager: pagerToggleBodies},
	{
		chord: byteChord(0x1b), modes: inTranscript,
		open: staysInline, why: "clears a selection there is none of, and is a sequence prefix besides",
		help: helpEscape, pager: pagerClearSelection,
	},

	// Visual mode: the motions move the cursor; every unbound key is inert so
	// a mark cannot be lost.
	{chord: byteChord('v'), modes: inTranscript | inVisual, open: opensPager, help: helpVisual, pager: pagerVisualChar},
	{chord: byteChord('V'), modes: inTranscript | inVisual, open: opensPager, help: helpVisual, pager: pagerVisualLine},
	{
		chord: byteChord(0x1b), modes: inVisual,
		open: staysInline, why: "only reachable with a visual selection up",
		help: helpEscape, pager: pagerVisualLeave,
	},
	{chord: byteChord('j'), modes: inVisual, open: opensPager, help: helpScroll, pager: pagerVisualDown},
	{chord: byteChord('k'), modes: inVisual, open: opensPager, help: helpScroll, pager: pagerVisualUp},
	{chord: byteChord('h'), modes: inVisual, open: staysInline, why: "a column motion with no selection to move", help: helpVisualCols, pager: pagerVisualLeft},
	{chord: byteChord('l'), modes: inVisual, open: staysInline, why: "a column motion with no selection to move", help: helpVisualCols, pager: pagerVisualRight},
	{chord: byteChord('d'), modes: inVisual, open: opensPager, help: helpScroll, pager: pagerVisualHalfDown},
	{chord: byteChord('u'), modes: inVisual, open: opensPager, help: helpScroll, pager: pagerVisualHalfUp},
	{chord: byteChord('G'), modes: inVisual, open: opensPager, help: helpScroll, pager: pagerVisualEnd},
	{chord: byteChord('g'), modes: inVisual, open: opensPager, help: helpScroll, pager: pagerVisualPendingTop},
	{chord: navChord(navUp), modes: inVisual, open: opensPager, help: helpArrows, pager: pagerVisualUp},
	{chord: navChord(navDown), modes: inVisual, open: opensPager, help: helpArrows, pager: pagerVisualDown},
	{chord: navChord(navLeft), modes: inVisual, open: staysInline, why: "a column motion with no selection to move", help: helpVisualCols, pager: pagerVisualLeft},
	{chord: navChord(navRight), modes: inVisual, open: staysInline, why: "a column motion with no selection to move", help: helpVisualCols, pager: pagerVisualRight},
	{chord: navChord(navPageUp), modes: inVisual, open: opensPager, help: helpArrows, pager: pagerVisualHalfUp},
	{chord: navChord(navPageDown), modes: inVisual, open: opensPager, help: helpArrows, pager: pagerVisualHalfDown},
	{chord: navChord(navHome), modes: inVisual, open: opensPager, help: helpHomeEnd, pager: pagerVisualTop},
	{chord: navChord(navEnd), modes: inVisual, open: opensPager, help: helpHomeEnd, pager: pagerVisualEnd},
	{chord: byteChord('w'), modes: inVisual, open: staysInline, why: "a cursor motion", help: helpVisualMotions, pager: pagerVisualWordNext},
	{chord: byteChord('b'), modes: inVisual, open: staysInline, why: "a cursor motion", help: helpVisualMotions, pager: pagerVisualWordPrev},
	{chord: byteChord('e'), modes: inVisual, open: staysInline, why: "a cursor motion", help: helpVisualMotions, pager: pagerVisualWordEnd},
	{chord: byteChord(0x0d), modes: inVisual, open: staysInline, why: "there is no cursor to point with", help: helpVisualSelect, pager: pagerVisualSelect},
	{chord: byteChord(0x0a), modes: inVisual, open: staysInline, why: "there is no cursor to point with", help: helpVisualSelect, pager: pagerVisualSelect},
	{chord: byteChord('a'), modes: inVisual, open: staysInline, why: "there is no cursor to read under", help: helpVisualAttend, pager: pagerVisualAttend},
	{chord: byteChord('0'), modes: inVisual, open: staysInline, why: "a cursor motion", help: helpVisualMotions, pager: pagerVisualRowStart},
	{chord: byteChord('^'), modes: inVisual, open: staysInline, why: "a cursor motion", help: helpVisualMotions, pager: pagerVisualFirstText},
	{chord: byteChord('$'), modes: inVisual, open: staysInline, why: "a cursor motion", help: helpVisualMotions, pager: pagerVisualRowEnd},
	{chord: byteChord('H'), modes: inVisual, open: staysInline, why: "a cursor motion", help: helpVisualMotions, pager: pagerVisualScreenTop},
	{chord: byteChord('M'), modes: inVisual, open: staysInline, why: "a cursor motion", help: helpVisualMotions, pager: pagerVisualScreenMid},
	{chord: byteChord('L'), modes: inVisual, open: staysInline, why: "a cursor motion", help: helpVisualMotions, pager: pagerVisualScreenBot},
	{chord: byteChord('{'), modes: inVisual, open: staysInline, why: "a cursor motion", help: helpVisualMotions, pager: pagerVisualParaPrev},
	{chord: byteChord('}'), modes: inVisual, open: staysInline, why: "a cursor motion", help: helpVisualMotions, pager: pagerVisualParaNext},
	{chord: byteChord('/'), modes: inVisual, open: opensPager, help: helpSearch, pager: pagerSearchPrompt},
	{chord: byteChord('?'), modes: inVisual, open: opensPager, help: helpSearch, pager: pagerSearchPromptBack},
	{chord: byteChord('n'), modes: inVisual, open: staysInline, why: "repeat search with no query yet: opens onto a no-op", help: helpSearchRepeat, pager: pagerFindNext},
	{chord: byteChord('N'), modes: inVisual, open: staysInline, why: "repeat search with no query yet: opens onto a no-op", help: helpSearchRepeat, pager: pagerFindPrev},
	{chord: byteChord(':'), modes: inVisual, open: opensPager, help: helpJump, pager: pagerVisualCommand},
	{
		chord: byteChord('Y'), modes: inVisual,
		open: staysInline, why: "there is no selection to name until one is made",
		help: helpVisualYankCoord, input: inputYankCoordinate,
	},

	// Panel mode: any other key wipes the panel and then acts (in dispatch).
	{chord: byteChord('h'), modes: inPanel, open: opensPager, help: helpHelpPanel, pager: panelToggleHelp},
	{chord: byteChord('!'), modes: inPanel, open: opensPager, help: helpStatusPanel, pager: panelToggleStatus},
	{chord: byteChord('Q'), modes: inPanel, open: opensPager, help: helpQueuedPanel, pager: panelToggleQueued},
	{chord: byteChord('S'), modes: inPanel, open: opensPager, help: helpFormPit, pager: pagerFormPit},
	{chord: byteChord('T'), modes: inPanel, open: opensPager, help: helpFocus, pager: pagerFocusTranscript},
	{
		chord: byteChord(0x1b), modes: inPanel,
		open: staysInline, why: "closing a panel that is not showing is not an opening gesture",
		help: helpEscape, pager: panelDismiss,
	},

	// Search box: anything with no row here and no input-level row is literal text.
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

	// ':' box: same shape as the search box.
	{
		chord: byteChord(0x0d), modes: inBox,
		open: staysInline, why: "only reachable with the jump prompt already up",
		help: helpJump, pager: boxAccept,
	},
	{
		chord: byteChord(0x0a), modes: inBox,
		open: staysInline, why: "only reachable with the jump prompt already up",
		help: helpJump, pager: boxAccept,
	},
	{
		chord: byteChord(0x1b), modes: inBox,
		open: staysInline, why: "only reachable with the jump prompt already up",
		help: helpJump, pager: boxCancel,
	},

	// Composer normal mode: motions, marks and search only; no editing verbs.
	{chord: byteChord('i'), modes: inComposer, open: staysInline, why: "only reachable with the drawer open", help: helpComposerNormal, pager: composerInsertHere},
	{chord: byteChord('a'), modes: inComposer, open: staysInline, why: "only reachable with the drawer open", help: helpNone, pager: composerInsertAfter},
	{chord: byteChord('I'), modes: inComposer, open: staysInline, why: "only reachable with the drawer open", help: helpNone, pager: composerInsertHome},
	{chord: byteChord('A'), modes: inComposer, open: staysInline, why: "only reachable with the drawer open", help: helpNone, pager: composerInsertEnd},
	{chord: byteChord('h'), modes: inComposer, open: staysInline, why: "only reachable with the drawer open", help: helpComposerMotions, pager: composerLeft},
	{chord: byteChord('l'), modes: inComposer, open: staysInline, why: "only reachable with the drawer open", help: helpNone, pager: composerRight},
	{chord: byteChord('k'), modes: inComposer, open: staysInline, why: "only reachable with the drawer open", help: helpNone, pager: composerUpLine},
	{chord: byteChord('j'), modes: inComposer, open: staysInline, why: "only reachable with the drawer open", help: helpNone, pager: composerDownLine},
	{chord: navChord(navLeft), modes: inComposer, open: staysInline, why: "only reachable with the drawer open", help: helpNone, pager: composerLeft},
	{chord: navChord(navRight), modes: inComposer, open: staysInline, why: "only reachable with the drawer open", help: helpNone, pager: composerRight},
	{chord: navChord(navUp), modes: inComposer, open: staysInline, why: "only reachable with the drawer open", help: helpNone, pager: composerUpLine},
	{chord: navChord(navDown), modes: inComposer, open: staysInline, why: "only reachable with the drawer open", help: helpNone, pager: composerDownLine},
	{chord: byteChord('w'), modes: inComposer, open: staysInline, why: "only reachable with the drawer open", help: helpNone, pager: composerWordFwd},
	{chord: byteChord('b'), modes: inComposer, open: staysInline, why: "only reachable with the drawer open", help: helpNone, pager: composerWordBack},
	{chord: byteChord('e'), modes: inComposer, open: staysInline, why: "only reachable with the drawer open", help: helpNone, pager: composerWordEnd},
	{chord: byteChord('0'), modes: inComposer, open: staysInline, why: "only reachable with the drawer open", help: helpNone, pager: composerLineStart},
	{chord: byteChord('^'), modes: inComposer, open: staysInline, why: "only reachable with the drawer open", help: helpNone, pager: composerFirstText},
	{chord: byteChord('$'), modes: inComposer, open: staysInline, why: "only reachable with the drawer open", help: helpNone, pager: composerLineEnd},
	{chord: byteChord('g'), modes: inComposer, open: staysInline, why: "only reachable with the drawer open", help: helpNone, pager: composerTop},
	{chord: byteChord('G'), modes: inComposer, open: staysInline, why: "only reachable with the drawer open", help: helpNone, pager: composerBottom},
	{chord: byteChord('{'), modes: inComposer, open: staysInline, why: "only reachable with the drawer open", help: helpNone, pager: composerParaPrev},
	{chord: byteChord('}'), modes: inComposer, open: staysInline, why: "only reachable with the drawer open", help: helpNone, pager: composerParaNext},
	{chord: navChord(navHome), modes: inComposer, open: staysInline, why: "only reachable with the drawer open", help: helpNone, pager: composerLineStart},
	{chord: navChord(navEnd), modes: inComposer, open: staysInline, why: "only reachable with the drawer open", help: helpNone, pager: composerLineEnd},
	{chord: byteChord('v'), modes: inComposer, open: staysInline, why: "only reachable with the drawer open", help: helpComposerMark, pager: composerMarkChar},
	{chord: byteChord('V'), modes: inComposer, open: staysInline, why: "only reachable with the drawer open", help: helpNone, pager: composerMarkLine},
	{chord: byteChord('/'), modes: inComposer, open: staysInline, why: "only reachable with the drawer open", help: helpNone, pager: composerSearchFwd},
	{chord: byteChord('?'), modes: inComposer, open: staysInline, why: "only reachable with the drawer open", help: helpNone, pager: composerSearchBack},
	{chord: byteChord('n'), modes: inComposer, open: staysInline, why: "only reachable with the drawer open", help: helpNone, pager: composerFindNext},
	{chord: byteChord('N'), modes: inComposer, open: staysInline, why: "only reachable with the drawer open", help: helpNone, pager: composerFindPrev},
	{chord: byteChord('F'), modes: inComposer, open: staysInline, why: "only reachable with the drawer open", help: helpNone, pager: composerFullscreen},
	{chord: byteChord(':'), modes: inComposer, open: staysInline, why: "only reachable with the drawer open", help: helpNone, pager: composerCommand},
	{chord: byteChord(0x1b), modes: inComposer, open: staysInline, why: "only reachable with the drawer open", help: helpNone, pager: composerEscape},
	{chord: metaChord(0x0d), modes: inComposer, open: staysInline, why: "only reachable with the composer open", help: helpNone, pager: composerSubmit},
	// ^S is the submit that works on Windows, where the terminal claims Alt+Enter.
	{chord: byteChord(0x13), modes: inComposer, open: staysInline, why: "only reachable with the composer open", help: helpComposerSend, pager: composerSubmit},
	{chord: ctrlChord('s'), modes: inComposer, open: staysInline, why: "only reachable with the composer open", help: helpNone, pager: composerSubmit},
	{
		// Also Ctrl+Enter on a CSI-u terminal: the input loop folds it
		// onto Alt+Enter.
		chord: metaChord(0x0d), modes: inBox,
		open: staysInline, why: "only reachable with the jump prompt already up",
		help: helpCmdSubmitSnap, pager: boxAcceptSnap,
	},
	// The ':' box is readline's emacs keymap. Every chord is bound twice: the raw
	// control byte, and the CSI-u report a terminal with modified-key reporting
	// sends instead (^N arrives as \x1b[110;5u, never 0x0e).
	{chord: byteChord(0x01), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdHome},
	{chord: byteChord(0x05), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdEnd},
	{chord: byteChord(0x02), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdLeft},
	{chord: byteChord(0x06), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdRight},
	{chord: metaChord('b'), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdWordLeft},
	{chord: metaChord('f'), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdWordRight},
	{chord: byteChord(0x0c), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdRedraw},

	{chord: byteChord(0x04), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdDeleteFwd},
	{chord: byteChord(0x14), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdTranspose},
	{chord: metaChord('t'), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdTransposeWord},
	{chord: metaChord('u'), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdUpcase},
	{chord: metaChord('l'), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdDowncase},
	{chord: metaChord('c'), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdCapitalize},

	{chord: byteChord(0x0b), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdKillToEnd},
	{chord: byteChord(0x15), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdKillToStart},
	{chord: byteChord(0x17), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdKillWord},
	{chord: metaChord('d'), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdKillWordFwd},
	{chord: metaChord(0x7f), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdKillWordAlpha},
	{chord: metaChord('\\'), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdDeleteSpace},
	{chord: byteChord(0x19), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdYank},
	{chord: metaChord('y'), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdYankPop},

	{chord: byteChord(0x10), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdHistory, pager: cmdHistPrev},
	{chord: byteChord(0x0e), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdHistory, pager: cmdHistNext},
	{chord: metaChord('<'), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdHistFirst},
	{chord: metaChord('>'), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdHistLast},
	{chord: byteChord(0x12), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdHistory, pager: cmdSearchPrev},
	{chord: byteChord(0x13), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdHistory, pager: cmdSearchNext},
	{chord: metaChord('p'), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdPrefixPrev},
	{chord: metaChord('n'), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdPrefixNext},
	{chord: metaChord('.'), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdYankLastArg},
	{chord: metaChord('_'), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdYankLastArg},

	{chord: byteChord(0x1f), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdUndo},
	{chord: metaChord('r'), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdRevert},
	{chord: byteChord(0x07), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdAbort, pager: cmdAbort},
	{chord: byteChord(0x03), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdAbort, pager: cmdAbort},

	{chord: byteChord(0x09), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdComplete, pager: cmdComplete},
	{chord: byteChord(0x16), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdPaste, pager: cmdPaste},
	{chord: ctrlChord('v'), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdPaste},
	{chord: metaChord('?'), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdListComplete},
	{chord: metaChord('*'), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdInsertComplete},

	// The CSI-u rows.
	{chord: ctrlChord('a'), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdHome},
	{chord: ctrlChord('e'), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdEnd},
	{chord: ctrlChord('b'), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdLeft},
	{chord: ctrlChord('f'), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdRight},
	{chord: ctrlChord('d'), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdDeleteFwd},
	{chord: ctrlChord('t'), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdTranspose},
	{chord: ctrlChord('k'), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdKillToEnd},
	{chord: ctrlChord('u'), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdKillToStart},
	{chord: ctrlChord('w'), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdKillWord},
	{chord: ctrlChord('y'), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdYank},
	{chord: ctrlChord('n'), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdHistNext},
	{chord: ctrlChord('p'), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdHistPrev},
	{chord: ctrlChord('r'), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdSearchPrev},
	{chord: ctrlChord('s'), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdSearchNext},
	{chord: ctrlChord('g'), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdAbort},
	{chord: ctrlChord('l'), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdRedraw},
	{chord: ctrlChord('h'), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: boxBackspace},

	{chord: navChord(navUp), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdHistory, pager: cmdHistPrev},
	{chord: navChord(navDown), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdHistory, pager: cmdHistNext},
	{chord: navChord(navHome), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdHome},
	{chord: navChord(navEnd), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpNone, pager: cmdEnd},
	{chord: navChord(navLeft), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdLeft},
	{chord: navChord(navRight), modes: inBox, open: staysInline, why: "only reachable with the command line up", help: helpCmdEdit, pager: cmdRight},

	{
		chord: byteChord(0x7f), modes: inBox,
		open: staysInline, why: "only reachable with the jump prompt already up",
		help: helpNone, pager: boxBackspace,
	},
	{
		chord: byteChord(0x08), modes: inBox,
		open: staysInline, why: "only reachable with the jump prompt already up",
		help: helpNone, pager: boxBackspace,
	},
}

// helpID names a help-panel row; the panel renders in declaration order.
type helpID uint8

const (
	helpNone helpID = iota
	helpScroll
	helpArrows
	helpHomeEnd
	helpSearch
	helpSearchRepeat
	helpJump
	helpDraft
	helpComposerSend
	helpComposerNormal
	helpComposerMotions
	helpComposerMark
	helpYank
	helpVerbose
	helpSticky
	helpQuestionTravel
	helpSelect
	helpSelectExtend
	helpExpand
	helpToolBody
	helpInterrupt
	helpHangUp
	helpHangUpDrop
	helpEscape
	helpListen
	helpDetach
	helpStatusPanel
	helpQueuedPanel
	helpDecisionPit
	helpNotifications
	helpHelpPanel
	helpListPit
	helpConfirm
	helpPitEnter
	helpVisualSelect
	helpVisualAttend
	helpCmdHistory
	helpCmdComplete
	helpCmdEdit
	helpCmdPaste
	helpBarVerbose
	helpCmdAbort
	helpPitDrop
	helpLeavePit
	helpFormPit
	helpFocus
	helpVisual
	helpVisualCols
	helpVisualYankCoord
	helpVisualMotions
	helpCmdSubmitSnap
	helpForkJump
	helpCommentJump
	helpCommentDelete
	helpAttend
	helpAriaJump
)

// Every visible binding is documented by exactly one row, and every row documents
// a live binding.
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
	{helpSearch, "/ · ?", "search forward / backward (in a pit, its rows)"},
	{helpSearchRepeat, "n / N", "next / previous match, with the cursor on it"},
	{helpJump, ":", "command line: any figaro verb, or a coordinate (:12, :12.3, :0)"},
	{helpDraft, ">", "the composer: type or paste a prompt, newlines and quotes intact (in v it quotes the highlight)"},
	{helpComposerSend, "(in >) ^S · M-Enter", "send the draft. ^S everywhere; M-Enter where the terminal does not eat it (Windows claims it)"},
	{helpComposerNormal, "(in >) Esc · i a I A", "normal mode (✎) and back to insert; Esc again puts the drawer away, keeping the draft"},
	{helpComposerMotions, "(in ✎) h j k l w b e 0 ^ $ gg G { }", "the transcript's motions, over the draft; / ? n N search it; F takes the pane; : is the command line"},
	{helpComposerMark, "(in ✎) v / V · y", "mark by character / by line; y copies the mark, or the whole draft"},
	{helpCmdHistory, "(in :) ^P/^N · ^R", "command history · search it"},
	{helpCmdComplete, "(in :) Tab", "complete the verb, an id, or a flag"},
	{helpCmdEdit, "(in :) ^A ^E ^W ^K ^Y", "emacs/readline editing, the whole set"},
	{helpCmdAbort, "(in :) Esc / ^C / ^G", "abandon the line, close the box"},
	{helpPitDrop, "(in a list) x / X", "drop or kill the selected row, asking first / at once"},
	{helpYank, "y", "copy selection (or aria id if none)"},
	{helpVerbose, "M-m", "toggle verbose tool output and the node addresses"},
	{helpSticky, "s", "pin the question of the turn you are inside"},
	{helpBarVerbose, "m", "more: state names, model, last interaction"},
	{helpCmdPaste, "(in :) ^V", "paste the clipboard"},
	{helpSelect, "^N/^P", "select next/previous node (each form delta is one)"},
	{helpForkJump, "f j / f k", "next / previous fork point"},
	{helpCommentJump, "c j / c k", "next / previous comment"},
	{helpCommentDelete, "x", "on a comment or its message: delete the whole thread (asks first)"},
	{helpAttend, "a", "attend the fork point's aria (in a list, the selected row's)"},
	{helpAriaJump, "^O / ^I", "jumplist: back / forward through attended arias"},
	{helpQuestionTravel, "M-n / M-p", "travel to the next / previous question"},
	{helpSelectExtend, "^N/^P + Shift", "travel between questions (Alt+^N/^P extends a selection)"},
	{helpExpand, "Enter", "open tool bodies, quoted passages and form deltas within the selection"},
	{helpToolBody, "e", "open the tool bodies within the selection"},
	{helpPitEnter, "(in a list) Enter", "the row's own action: open a form value or a notification, attend an aria"},
	{helpVisualSelect, "(in v) Enter", "select the node under the cursor (then e expands it)"},
	{helpVisualAttend, "(in v) a", "attend the highlighted id, or the word under the cursor; a path opens in your editor"},
	{helpEscape, "Esc", "clear selection / close panel"},
	{helpVisual, "v / V", "visual mode: a cursor; again to mark by character / by line (y yanks, : commands it)"},
	{helpVisualCols, "(in v) h/l · ←/→", "move the cursor's column"},
	{helpVisualYankCoord, "(in v) Y", "copy the selection's coordinate (<lt.block:a-b>!)"},
	{helpVisualMotions, "(in v) w b e · 0 ^ $ · H M L · { }", "vim motions over the cursor; / n N land it on a match"},
	{helpCmdSubmitSnap, "(in :) M-Enter", "submit, leave visual mode, and snap to the live tail"},
	{helpListen, "^L", "open the transcript (stays open until you close it)"},
	{helpStatusPanel, "!", "figaro status panel"},
	{helpQueuedPanel, "Q", "queued prompts panel; with nothing queued it says so on the bar and opens nothing"},
	{helpNotifications, "space n", "notifications: what figaro said, and what went wrong"},
	{helpFormPit, "S", "the form (state) in the pit"},
	{helpDecisionPit, "D", "the last decision (a ?? draft or :decide) in the pit; with none yet it says so on the bar"},
	{helpFocus, "T", "read the conversation without closing the pit (again to go back)"},
	{helpListPit, "l / L", "list this aria's tree / the home tree, in the pit (Enter attends, x kills)"},
	{helpConfirm, "y / n", "answer a confirmation (Esc is no); X on a row kills with no question"},
	{helpHelpPanel, "h", "close help"},
}

const helpKeyColumn = 20

// helpBody omits the blank line, footer, clamp and dimming, which are the pager's.
func helpBody() []string {
	rows := make([]string, 0, len(helpRows))
	for _, r := range helpRows {
		keys := helpKeysFor(r.id)
		if keys == "" {
			continue // every binding under this row has been unbound
		}
		if r.keys != "" && !userTouchedHelp[r.id] {
			keys = r.keys
		}
		pad := helpKeyColumn - runewidth.StringWidth(keys)
		if pad < 1 {
			pad = 1
		}
		rows = append(rows, "  "+keys+strings.Repeat(" ", pad)+r.text)
	}
	if line := helpDiagLine(); line != "" {
		rows = append(rows, "  "+line)
	}
	return rows
}

func helpKeysFor(id helpID) string {
	var spelled []string
	seen := map[string]bool{}
	confined := keyModeSet(0)
	for i := range liveRows {
		bd := &liveRows[i]
		if bd.help != id || bd.modes == 0 {
			continue
		}
		confined |= bd.modes
		s := bd.chord.spell()
		if seen[s] {
			continue
		}
		seen[s] = true
		spelled = append(spelled, s)
	}
	if len(spelled) == 0 {
		return ""
	}
	return helpModeQualifier(confined) + strings.Join(spelled, " ")
}

func helpModeQualifier(modes keyModeSet) string {
	switch modes {
	case inBox:
		return "(in :) "
	case inVisual:
		return "(in v) "
	case inPanel:
		return "(in a list) "
	case inSearchBox:
		return "(in /) "
	case inConfirm:
		return "(asked) "
	case inComposer:
		return "(in ✎) "
	case inComment:
		return "(in comment) "
	}
	return ""
}

const noBinding = int16(-1)

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

type keyIndex struct {
	byByte [numKeyModes][256]int16
	byNav  [numKeyModes][navCount]int16
	byCtrl [numKeyModes][26]int16
	byMeta [numKeyModes][128]int16
}

var (
	pagerAct pagerActions
	inputAct inputActions

	inputIndex keyIndex
	pagerIndex keyIndex

	openerByte [256]bool
	openerNav  [navCount]bool
	openerCtrl [26]bool
	openerMeta [128]bool

	ctrlChordLetters [26]bool

	metaBound [numKeyModes][128]bool
)

const navCount = int(navRight) + 1

// liveRows is the table dispatch actually reads: the declared keymap until a
// keys.toml is applied over it (see keymap_user.go). It is assigned from a
// func init rather than a var initializer so that the help panel may read it
// without the initializer graph closing into a cycle.
var liveRows []keyBinding

func init() {
	liveRows = keymap
	buildKeyIndex()
}

func buildKeyIndex() {
	if len(liveRows) > 32767 {
		panic("keymap: more rows than an int16 index can name")
	}
	pagerAct, inputAct = pagerActions{}, inputActions{}
	openerByte, openerNav, openerCtrl, openerMeta = [256]bool{}, [navCount]bool{}, [26]bool{}, [128]bool{}
	ctrlChordLetters = [26]bool{}
	metaBound = [numKeyModes][128]bool{}
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
	for i := range liveRows {
		bd := &liveRows[i]
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
				pagerAct.byByte[byteSlot(m, bd.chord.b)] = bd.pager
				inputAct.byByte[byteSlot(m, bd.chord.b)] = bd.input
			case chordNav:
				idx.byNav[m][bd.chord.nav] = int16(i)
				pagerAct.byNav[m][bd.chord.nav] = bd.pager
				inputAct.byNav[m][bd.chord.nav] = bd.input
			case chordCtrlLetter:
				idx.byCtrl[m][bd.chord.b-'a'] = int16(i)
				pagerAct.byCtrl[m][bd.chord.b-'a'] = bd.pager
				inputAct.byCtrl[m][bd.chord.b-'a'] = bd.input
			case chordMeta:
				idx.byMeta[m][bd.chord.b] = int16(i)
				pagerAct.byMeta[m][bd.chord.b] = bd.pager
				inputAct.byMeta[m][bd.chord.b] = bd.input
			}
		}
		if bd.chord.kind == chordCtrlLetter {
			ctrlChordLetters[bd.chord.b-'a'] = true
		}
		if bd.chord.kind == chordMeta && bd.chord.b < 128 {
			for m := keyMode(0); m < numKeyModes; m++ {
				if bd.modes&(1<<m) != 0 {
					metaBound[m][bd.chord.b] = true
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

func byteSlot(mode keyMode, b byte) int { return int(mode)<<8 | int(b) }

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
	return &liveRows[i]
}

// ctrlChordLetter reports whether a CSI-u Ctrl report is a chord some row binds,
// rather than one to reduce to its control byte.
func ctrlChordLetter(key modifiedKey) (byte, bool) {
	if !key.ctrl || key.nav != navNone {
		return 0, false
	}
	letter := key.code | 0x20
	if letter < 'a' || letter > 'z' {
		return 0, false
	}
	return letter, ctrlChordLetters[letter-'a']
}

// Per mode: a chord bound only in the ':' box must still reduce to its control
// byte elsewhere.
func ctrlChordBoundIn(mode keyMode, letter byte) bool {
	if letter < 'a' || letter > 'z' {
		return false
	}
	return inputIndex.byCtrl[mode][letter-'a'] != noBinding ||
		pagerIndex.byCtrl[mode][letter-'a'] != noBinding
}

// Alt-b arrives as ESC 'b', indistinguishable from Esc then b in one read. The
// ':' box reads ESC as a prefix wholesale (an unbound M-x is swallowed, not
// "close the box, type x"); everywhere else the prefix is claimed per chord.
func metaBoundIn(mode keyMode, b byte) bool {
	return mode == modeBox || (b < 128 && metaBound[mode][b])
}

// Alt+Shift+B folds to Alt+b; non-letters pass through so M-< and M-> stay addressable.
func metaFold(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b | 0x20
	}
	return b
}

// opensTranscript reports whether a key pressed during incipit yanks the pager up
// first.
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

func metaOpens(b byte) bool { return b < 128 && openerMeta[b] }

func opensTranscriptFor(b byte) bool { return openerByte[b] }

func (c chord) String() string {
	switch c.kind {
	case chordNav:
		return "nav:" + navName(c.nav)
	case chordCtrlLetter:
		return fmt.Sprintf("csi-u ctrl-%c", c.b)
	case chordMeta:
		if c.b == 0x7f {
			return "M-DEL"
		}
		return fmt.Sprintf("M-%c", c.b)
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
	case navLeft:
		return "Left"
	case navRight:
		return "Right"
	}
	return "none"
}
