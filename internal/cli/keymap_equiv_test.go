package cli

import (
	"fmt"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
)

// The fixture and the starting states the behaviour goldens are swept from
// (keymap_behaviour_test.go).

// oracleTranscript is the fixture the oracle was recorded against: eight
// two-node messages in a 50x8 pane, entered (and so following the tail).
func oracleTranscript() *transcript {
	ft := ldrender.NewFakeTerminal(50, 8)
	client := aria.NewClient()
	for i := 1; i <= 8; i++ {
		client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{
			ID: uint64(i), Sealed: true,
			Nodes: []livedoc.Node{
				{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("msg%02d body", i)},
				{Type: livedoc.NodeTool, Name: "bash", Output: fmt.Sprintf("out%02d", i)},
			},
		}}}}, aria.Notify)
	}
	tr := newTranscript(ft, 50, 8, ldrender.NodeText{}, client, "aria1234", time.Unix(0, 0))
	tr.enter()
	return tr
}

// offToken reports what a key did to the SCROLL POSITION, relative to where an
// inert key in the same state leaves it.
//
// It is relative because the absolute offset is not what this oracle is for.
// Following the tail pins the offset at `total_lines - body`, so any change to
// how a message is drawn moves every recorded number while changing no
// binding at all, which has now happened often enough to be the main cost of
// owning the table. A delta is a statement about the KEY ("u scrolls a page
// up"), and a page is a function of the pane, not of the conversation.
//
// `top` is absolute on purpose: Home goes to line 0 whatever the content, and
// recording that as a delta would put the content length back in the table.
func offToken(tr *transcript, base int) string {
	_, maxOff := tr.layout(len(tr.footLines()))
	off := tr.offset
	switch {
	case off == base:
		return "off:same"
	case off == 0:
		return "off:top"
	case tr.follow && off == maxOff:
		// FOLLOWING: the offset is not a position the key chose, it is a
		// consequence of being pinned to the tail, entering the transcript,
		// toggling verbose and opening a panel all re-pin it. Recorded by name
		// so the number, which is content length, stays out of the table.
		//
		// Not following is the other case: there the offset IS a position, and
		// the delta is the assertion worth keeping.
		return "off:bottom"
	case tr.selection.active:
		// SELECTION-DRIVEN: the offset is wherever the selected node had to be
		// scrolled to, so it is a fact about the node's position in the
		// conversation, not about the key. `sel=true` next door already says
		// the key selected something; the distance it travelled is content.
		return "off:sel"
	case tr.visual.active():
		// The same argument for a visual selection: entering one detaches from
		// the tail and seeds on what is on screen, and where that is is the
		// content's length, not the key's meaning.
		return "off:vis"
	}
	return fmt.Sprintf("off:%+d", off-base)
}

// oracleSignature is every piece of pager state a keystroke can touch. base is
// the offset an inert key leaves in this state; see offToken.
func oracleSignature(tr *transcript, base int) string {
	return fmt.Sprintf("%s fol=%v srch=%v q=%q mq=%q h=%v s=%v Q=%v g=%v sel=%v exp=%d jmp=%v jq=%q vis=%s pit=%s",
		offToken(tr, base), tr.follow, tr.inSearch, tr.query, tr.matchQuery,
		tr.showing("help"), tr.showing("status"), tr.showing("queue"), tr.pendG, tr.selection.active,
		len(tr.expanded), tr.boxOpen(), tr.cmdline.String(), visToken(tr), pitToken(tr))
}

// pitToken is what is open below the transcript and how a reader stands in
// it: the pit, where its cursor and window are, whether it holds the pane,
// which side has the keys, and a hosted view's own state where it has one.
func pitToken(tr *transcript) string {
	if !tr.pit.open() {
		return "none"
	}
	tok := string(tr.pit.id)
	if p := tr.pit.list(); p != nil {
		tok += fmt.Sprintf("@%d/%d", p.cursor, p.top)
	}
	if tr.full {
		tok += "+full"
	}
	if tr.focused == focusTranscript {
		tok += "+T"
	}
	if v, ok := tr.pit.live.(*notificationsView); ok {
		tok += fmt.Sprintf("+f%d", v.filter)
	}
	return tok
}

// visToken is the visual mode as a statement about the KEY: the highlight's
// kind (or cur, cursor only), how far the cursor stands from the anchor (from
// the top of the viewport, when there is no anchor), and its column. The
// anchor's absolute line is content and stays out of the table, exactly as
// offToken keeps the offset out.
func visToken(tr *transcript) string {
	if !tr.visual.active() {
		return "none"
	}
	f, _ := tr.visualLineOf(tr.visual.cursor)
	kind, a := "cur", 0
	switch tr.visual.kind {
	case visualChar:
		kind = "char"
		a, _ = tr.visualLineOf(tr.visual.anchor)
	case visualLine:
		kind = "line"
		a, _ = tr.visualLineOf(tr.visual.anchor)
	default:
		a = tr.offset
	}
	return fmt.Sprintf("%s%+d:%d", kind, f-a, tr.visual.cursor.col)
}

// inertOffset is where the offset sits after a key this state does not bind,
// the reference every cell in the row is measured against. The probe key is
// chosen from the row itself, so a state that later binds 0x01 cannot silently
// start measuring against something that moves.
// inertOffset is where 0x00, which no table has ever bound, leaves the
// viewport: the baseline offToken measures every other key against.
func inertOffset(setup func(*transcript)) int {
	probe := byte(0)
	tr := oracleTranscript()
	setup(tr)
	tr.key(probe)
	return tr.offset
}

// The starting states, by the name the oracle records them under.
var oracleStates = map[string]func(*transcript){
	"transcript":       func(tr *transcript) {},
	"transcript+sel":   func(tr *transcript) { tr.selectNode(1, false); tr.render() },
	"transcript+pendG": func(tr *transcript) { tr.key('g') },
	"help":             func(tr *transcript) { tr.key('h') },
	"status":           func(tr *transcript) { tr.key('!') },
	// THE QUEUE PIT NEEDS A QUEUE, as of 2026-10-10: `Q` on an empty one is a
	// bar notice and no pit, so this state used to describe a panel that can
	// no longer exist. One queued message, and the row describes the pit a
	// reader actually sees.
	"queued": func(tr *transcript) {
		tr.queued = []queuedItem{{id: 1, text: "a queued prompt"}}
		tr.key('Q')
	},
	"panel+sel":  func(tr *transcript) { tr.selectNode(1, false); tr.key('!') },
	"search":     func(tr *transcript) { tr.key('/'); tr.key('m'); tr.key('s') },
	"search+sel": func(tr *transcript) { tr.selectNode(1, false); tr.key('/'); tr.key('m') },
	"jump":       func(tr *transcript) { tr.key(':'); tr.key('1'); tr.key('2') },
	// v puts the cursor up; a second v/V anchors the highlight.
	"visual":        func(tr *transcript) { tr.key('V'); tr.key('V'); tr.key('j') },
	"visual+char":   func(tr *transcript) { tr.key('v'); tr.key('v'); tr.key('l'); tr.key('l') },
	"visual+cursor": func(tr *transcript) { tr.key('v'); tr.key('j') },
	// 'f' armed: the fork jump waiting for its direction (modeFork). The
	// only state here with no pre-refactor twin -- the mode did not exist --
	// so its row was recorded from the behaviour it was built to have.
	"fork": func(tr *transcript) { tr.key('f') },
	// 'c' armed: the comment jump waiting for its direction (modeComment),
	// built to behave as the fork jump does.
	"comment": func(tr *transcript) { tr.key('c') },
	// A y/n question up: the mode a destructive verb opens, where every key
	// that is not an answer is swallowed.
	"confirm": func(tr *transcript) { tr.askConfirm("kill aria1234?", func() {}) },
	// A one-row note is a glance: any key it does not own wipes it.
	"note": func(tr *transcript) { tr.noteOrClear("a passing remark"); tr.render() },
	// A list with the conversation focused behind it: T hands the keys over.
	"help+focus": func(tr *transcript) { tr.key('h'); tr.key('T') },
	// A hosted view with letters of its own (the notifications pit's f).
	"notifications": func(tr *transcript) { tr.openNotificationsPit(); tr.render() },
	// The composer in NORMAL mode: '>' opens it, two words are typed,
	// Esc drops into the motions.
	"compose": func(tr *transcript) {
		tr.key('>')
		for _, b := range []byte("alpha bravo") {
			tr.key(b)
		}
		tr.key(0x1b)
	},
}

// sweepPager presses every byte and every navigation key in one state and
// returns what each left behind. Recording and verifying both come through
// here, so the golden can never describe keys the check does not press.
func sweepPager(setup func(*transcript)) (cells map[string]string, base int) {
	base = inertOffset(setup)
	cells = make(map[string]string, 128+int(navEnd-navUp)+1)
	record := func(name string, press func(*transcript)) {
		tr := oracleTranscript()
		setup(tr)
		press(tr)
		cells[name] = oracleSignature(tr, base)
	}
	for b := 0; b < 128; b++ {
		b := byte(b)
		record(fmt.Sprintf("0x%02x", b), func(tr *transcript) { tr.key(b) })
	}
	for n := navUp; n <= navEnd; n++ {
		n := n
		record(navName(n), func(tr *transcript) { tr.navMotion(n) })
	}
	return cells, base
}
