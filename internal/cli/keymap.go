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
	modeConfirm                   // a y/n question owns the keyboard until it is answered
	modeComposer                  // the composer's NORMAL mode: motions over the draft
	modeList
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
	inConfirm    keyModeSet = 1 << modeConfirm
	inComposer   keyModeSet = 1 << modeComposer
	inList       keyModeSet = 1 << modeList

	// A transcript-mode row is also reachable with a panel up: the panel
	// swallows only its own keys.
	inPager  = inTranscript | inSearchBox | inBox | inPanel | inVisual | inConfirm | inComposer | inList
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

	help helpID

	action string
	layer  string
	prefix string

	// Exactly one of pager/input is set. pager rows run inside the transcript
	// under the render lock; input rows run on the read loop and may stop it.
	pager pagerFunc
	input inputFunc
}

func (b *keyBinding) hidden() bool { return b.help == helpNone }

var keymap = mustCompile(defaultLayers)

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
	case inPanel, inPit, inList:
		return "(in a list) "
	case inSearchBox:
		return "(in /) "
	case inConfirm:
		return "(asked) "
	case inComposer:
		return "(in ✎) "
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
	prefixRows = map[prefixSlot]*keyBinding{}
	for i := range liveRows {
		bd := &liveRows[i]
		if bd.prefix != "" {
			for m := keyMode(0); m < numKeyModes; m++ {
				if bd.modes&(1<<m) != 0 {
					prefixRows[prefixSlot{bd.prefix, m, bd.chord}] = bd
				}
			}
			continue
		}
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

type prefixSlot struct {
	prefix string
	mode   keyMode
	chord  chord
}

var prefixRows map[prefixSlot]*keyBinding

func prefixBinding(prefix string, mode keyMode, ev keyEvent) *keyBinding {
	if prefix == "" {
		return nil
	}
	return prefixRows[prefixSlot{prefix, mode, ev.chord()}]
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
