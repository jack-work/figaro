package cli

import (
	"strings"

	"github.com/jack-work/figaro/internal/cmdkit"
)

func focusTranscriptKey(t *transcript) {
	if !t.pit.open() {
		return
	}
	if t.focused == focusPit {
		t.focused = focusTranscript
		return
	}
	t.focused = focusPit
}

// mode is derived from the pit identity (pitid.go); the dispatch order is the
// order here.
func (t *transcript) mode() keyMode {
	if !t.active {
		return modeIncipit
	}
	// A box outranks the selection: the line owns the keyboard while the
	// highlight stays.
	if t.visual.active() && !t.inSearch && !t.boxOpen() {
		return modeVisual
	}
	// The drawer's sub-mode is asked after openPit: a search box or ':' line
	// over it outranks it.
	open := t.openPit()
	if open == pitComposer && t.composerMode == composerNormal {
		return modeComposer
	}
	if k := open.keys(); k == modePanel && t.listMode() {
		return modeList
	}
	return open.keys()
}

func (t *transcript) openPit() pitID {
	switch {
	case t.confirm != nil:
		return pitConfirm
	case t.inSearch:
		return pitSearch
	case t.box == boxCommand:
		return pitCommand
	case t.box == boxComposer:
		return pitComposer
	case t.pit.open():
		return t.pit.id
	default:
		return pitNothing
	}
}

// The pager never self-exits: Ctrl-D / Ctrl-C are handled at the input loop; q and
// Ctrl-T are inert here.
func (t *transcript) key(b byte) { t.dispatch(keyEvent{b: b}) }

// Up and k are one action bound twice; a nav key is never translated back into a byte.
func (t *transcript) navMotion(n navKey) { t.dispatch(keyEvent{nav: n}) }

func (t *transcript) dispatch(ev keyEvent) {
	if t.pending != "" {
		bd := t.prefixed(ev)
		t.pending = ""
		if bd != nil && bd.pager != nil {
			bd.pager(t)
			t.render()
			return
		}
	}
	switch t.mode() {
	case modeConfirm:
		// The question owns the keyboard: a key with no row must not reach
		// the pager underneath.
		if act := pagerAct.pager(modeConfirm, ev); act != nil {
			act(t)
		}
		return
	case modeSearch:
		if ev.nav != navNone {
			return
		}
		if act := pagerAct.pager(modeSearch, ev); act != nil {
			act(t)
		} else {
			t.searchLiteral(ev.b)
		}
		t.render()
		return
	case modeBox:
		// Arrows are live here (history, motions); one with no row is
		// swallowed rather than scrolling the transcript.
		if ev.nav != navNone {
			if act := pagerAct.pager(modeBox, ev); act != nil {
				act(t)
				t.render()
			}
			return
		}
		if act := pagerAct.pager(modeBox, ev); act != nil {
			act(t)
		} else {
			t.boxLiteral(ev.b)
		}
		t.render()
		return
	case modeComposer:
		// The drawer owns the keyboard: a key with no row is inert.
		if act := pagerAct.pager(modeComposer, ev); act != nil {
			act(t)
			t.render()
		}
		return
	case modeVisual:
		// The selection owns the keyboard: a key with no row is inert.
		if act := pagerAct.pager(modeVisual, ev); act != nil {
			act(t)
			t.render()
		}
		return
	case modeList:
		if act := pagerAct.pager(modeList, ev); act != nil {
			act(t)
			t.render()
			return
		}
		if kv, ok := t.pit.live.(cmdkit.KeyView); ok && ev.nav == navNone && ev.b != 0 && kv.Key(ev.b) {
			t.render()
			return
		}
		t.closePanels()
	case modePanel:
		if act := pagerAct.pager(modePanel, ev); act != nil {
			act(t)
			t.render()
			return
		}
		// A hosted verb with a list: the pit owns every motion and the
		// view keeps only its own verbs; without a list the view gets
		// first refusal.
		if t.focused == focusTranscript {
			// The conversation has the keys until `T` hands them back;
			// Esc still closes the pit.
			if act := pagerAct.pager(modeTranscript, ev); act != nil {
				act(t)
				t.render()
				return
			}
		}
		if kv, ok := t.pit.live.(cmdkit.KeyView); ok && ev.nav == navNone && kv.Key(ev.b) {
			t.render()
			return
		}
		t.closePanels()
	}
	if act := pagerAct.pager(modeTranscript, ev); act != nil {
		act(t)
	}
	if !t.boxOpen() {
		t.jumpNote = ""
	}
	t.render()
}

func pagerLineDown(t *transcript) { t.scroll(1) }
func pagerLineUp(t *transcript)   { t.scrollNotch(-1) }
func pagerHalfDown(t *transcript) { t.scroll(t.h / 2) }
func pagerHalfUp(t *transcript)   { t.scroll(-(t.h / 2)) }

func pagerTail(t *transcript) {
	t.follow = true
	t.resetToTail()
}

// note is the bar's notice, not jumpNote, which the dispatcher's epilogue wipes on
// every key.
func (t *transcript) note(s string) { t.setCommandNote(s) }

func pagerTop(t *transcript) {
	t.stopFollowing()
	t.offset = 0
	t.wantTop = !t.atAriaFloor()
}

func pagerSearchPrompt(t *transcript) { t.inSearch, t.query, t.searchDir = true, "", 1 }

func pagerSearchPromptBack(t *transcript) { t.inSearch, t.query, t.searchDir = true, "", -1 }

func pagerHelpPit(t *transcript) { t.openHelpPit() }

func pagerListPit(t *transcript) { t.runListCommand("ls") }

func pagerListHome(t *transcript) { t.runListCommand("ls -H") }

func (t *transcript) runListCommand(line string) {
	if t.command == nil {
		t.note("this session cannot run commands")
		return
	}
	t.command(line)
}
func pagerFindNext(t *transcript)    { t.findRepeat(1) }
func pagerFindPrev(t *transcript)    { t.findRepeat(-1) }
func pagerHelpPanel(t *transcript)   { t.openHelpPit() }
func pagerStatusPanel(t *transcript) { t.openStatusPit() }
func pagerQueuedPanel(t *transcript) { t.openQueueFromKey() }

func pagerFormPit(t *transcript) {
	if strings.HasPrefix(string(t.pit.id), string(pitForm)) {
		t.closePanels()
		return
	}
	if t.openForm != nil {
		t.openForm()
	}
}

func pagerFocusTranscript(t *transcript) { focusTranscriptKey(t) }

// ^N/^P move the pit's selection when one is up, the node selection otherwise.
func pagerSelectNext(t *transcript) { t.selectDown(1) }
func pagerSelectPrev(t *transcript) { t.selectDown(-1) }

func (t *transcript) selectDown(dir int) {
	if p := t.pit.list(); p != nil && p.hasCursor() {
		t.pit.moveSelection(dir)
		return
	}
	t.selectNode(dir, false)
}

func pagerToggleBlocks(t *transcript) { t.toggleSelectedNodes() }

func pagerToggleBodies(t *transcript) { t.toggleSelectedTools() }

func pagerClearSelection(t *transcript) {
	if t.selection.active {
		t.clearSelection()
	}
}

func pagerVisualChar(t *transcript) { t.pressVisual(visualChar) }
func pagerVisualLine(t *transcript) { t.pressVisual(visualLine) }
func pagerVisualLeave(t *transcript) {
	if t.visual.highlighted() {
		t.visual.kind = visualNone
		return
	}
	t.leaveVisual()
}

func pagerVisualDown(t *transcript)     { t.visualMove(1) }
func pagerVisualUp(t *transcript)       { t.visualMove(-1) }
func pagerVisualLeft(t *transcript)     { t.visualMoveCol(-1) }
func pagerVisualRight(t *transcript)    { t.visualMoveCol(1) }
func pagerVisualHalfDown(t *transcript) { t.visualMove(t.h / 2) }
func pagerVisualHalfUp(t *transcript)   { t.visualMove(-(t.h / 2)) }
func pagerVisualEnd(t *transcript)      { t.visualJump(true) }
func pagerVisualTop(t *transcript)      { t.visualJump(false) }

func pagerVisualWordNext(t *transcript)  { t.visualWord(1, false) }
func pagerVisualWordPrev(t *transcript)  { t.visualWord(-1, false) }
func pagerVisualWordEnd(t *transcript)   { t.visualWord(1, true) }
func pagerVisualRowStart(t *transcript)  { t.visualRowStart() }
func pagerVisualFirstText(t *transcript) { t.visualFirstNonBlank() }
func pagerVisualRowEnd(t *transcript)    { t.visualRowEnd() }
func pagerVisualScreenTop(t *transcript) { t.visualScreen(-1) }
func pagerVisualScreenMid(t *transcript) { t.visualScreen(0) }
func pagerVisualScreenBot(t *transcript) { t.visualScreen(1) }
func pagerVisualParaNext(t *transcript)  { t.visualParagraph(1) }
func pagerVisualParaPrev(t *transcript)  { t.visualParagraph(-1) }

// With a highlight up the box opens holding the range placeholder; the highlight stays.
func pagerVisualCommand(t *transcript) {
	pagerCommandBox(t)
	if _, onComment := t.selectedCommentAddr(); t.visual.highlighted() || onComment {
		t.cmdline.insert(visualRangePlaceholder)
	}
}

// vim's '<,'> without the marks; expanded to the qualified coordinate at submit.
const visualRangePlaceholder = "<,>"

func panelToggleHelp(t *transcript) { t.togglePit(pitHelp, t.openHelpPit) }

func panelToggleStatus(t *transcript) { t.togglePit(pitStatus, t.openStatusPit) }

func panelToggleQueued(t *transcript) { t.togglePit(pitQueue, t.openQueueFromKey) }

// Esc's other meaning (clear the selection) is not reached from here.
func panelDismiss(t *transcript) { t.closePanels() }

func pagerNotifications(t *transcript) { t.togglePit(pitNotifications, t.openNotificationsPit) }

func (t *transcript) prefixed(ev keyEvent) *keyBinding {
	if bd := prefixBinding(t.pending, t.mode(), ev); bd != nil {
		return bd
	}
	return prefixBinding(t.pending, modeTranscript, ev)
}

func pagerClearNote(t *transcript) { t.note("") }

// The transcript does not know what dropping means; it hands the id up, as the ':'
// box hands a line up.
func pagerPitDrop(t *transcript) { t.dropSelected(true) }

func pagerPitDropNow(t *transcript) { t.dropSelected(false) }
