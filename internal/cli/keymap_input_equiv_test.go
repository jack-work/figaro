package cli

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Behavioural equivalence at the input level: what the read loop does with a
// key, in every mode, must be what it did before the keymap.
//
// Generated the same way as the pager oracle in keymap_equiv_test.go — by
// running 45bee38 over every byte, every navigation encoding and a handful of
// CSI-u chords in five starting states, and recording everything a keystroke
// can move: the pager, the verbosity and listen flags, the clipboard, the
// copy state machine, the disconnect channel, whether the context was
// cancelled, whether the loop stopped, and what bytes were held back for the
// next read.
//
// The off= column was rebased once, when the stray blank row above a thinking
// block was removed (internal/render, trimBlankEdges): a message lost a row,
// so the ROW-budgeted pager window holds 18 of the fixture's messages instead
// of 17 and the bottom of the content moved. Nothing else in the signature
// moved — every other column is byte-identical to 45bee38.
//
// The 0x1b row was rebased a second time, and this one is a deliberate
// BEHAVIOUR change rather than a re-measurement. 45bee38 held a bare Esc back
// (rest="\x1b") in every pager mode, because mouse reporting is enabled there
// and ldmouse.Parse claimed the byte as a possible split `\x1b[<…M`. So the
// oracle was certifying a DEAD ESCAPE KEY: it did not clear a selection, did
// not close a panel (h stayed true), and did not cancel a search (q stayed
// "ms") — none of which is what keymap.go's table says Esc does. Esc now
// dispatches on its own read, so all four rows lose rest="\x1b" and gain the
// effect the table promised. See mouse.Parse for the rule and its cost.
//
// The off= column was rebased a THIRD time, by sepRows: the separator between
// two messages lost its trailing blank (transcript_index.go), so every message
// boundary is one row shorter. This is a re-measurement, not a behaviour
// change — only off= moved, in every row, and fol=, the selection identity and
// every other column are untouched.
//
// It did not move UNIFORMLY, and the exception is worth stating because it
// looks like a bug. Most rows fell by 17 (the fixture's 17 message boundaries,
// one row each). The incipit 0x0f row ROSE, 742 -> 771, because that key opens
// the pager AND toggles verbosity in one press, and the retained window is
// budgeted in ROWS, not messages: verbose tool inputs used to push the window
// down to 17 messages, and with a shorter separator the same budget now holds
// 18. So the window gained a whole message and the line space grew. Measured,
// not assumed — heldWindow() reports 17 messages before and 18 after, for the
// same fixture and the same budget.
//
// The off= column was rebased a FIFTH time, by exactly ONE row and only in the
// 19 states where the HELP PANEL IS SHOWING (h=true). The '?' panel grew a
// single generated line documenting the mouse gestures (click to select, click
// again to expand, wheel to scroll — see transcript_mouse.go's mouseHelpRows),
// and the panel is drawn inside the frame: one more panel row is one fewer body
// row, and a FOLLOWING viewport's offset is correspondingly one higher. This is
// the same mechanism as the FOURTH-rebase note about a keymap-generated panel
// row, and it is a re-measurement, not a behaviour change — h=true is the only
// predicate that selects the moved rows, no other column moved anywhere, and the
// 19 were patched individually from the oracle's own report rather than by a
// blanket rule over every h=true literal (a blanket bump moved rows the panel
// height does not reach, and broke the search case).
//
// The mouse gestures themselves are deliberately NOT in this oracle: it is keyed
// by keystroke, and a pointer is not a chord. They are covered by
// transcript_mouse_test.go, which asserts them against the painted frame.
//
// The off=/fol=/sel= columns were rebased a FOURTH time, for the eight
// selection chords in the INCIPIT state (0x0e, 0x10 and their CSI-u and alt
// encodings), and this one is a behaviour change, deliberately.
//
// Those keys open the pager AND move the node selection in one press. The
// pager used to open on a COPY of the client's closed tail taken at enter()
// — and in this fixture the catch-up read that fills the client lands after
// that copy is taken, so ^N found an empty window, did nothing, and left the
// pager following. It took a frame for the window to catch up, and only the
// SECOND ^N selected anything. The oracle was certifying that dead first
// press.
//
// The window is now an interval into the store rather than a copy of it
// (docs/range-store.md phase 2), so there is no stale snapshot to be caught
// behind: the first ^N selects, which detaches (fol=false) and gives the live
// padding row back to content (off 753 -> 752). Everything else in those rows
// is untouched, and no other state's rows moved at all.
// ---------------------------------------------------------------------------

type inputProbe struct {
	in        *interactiveInput
	lt        *livelogTurn
	tc        *recordingTerminal
	cancelled *atomic.Bool
}

func newInputProbe(tb testing.TB, open bool) *inputProbe {
	in, lt := navInput(tb, &countingWriter{}, open)
	tc := newRecordingTerminal()
	var cancelled atomic.Bool
	in.tc = tc
	in.cancel = func() { cancelled.Store(true) }
	return &inputProbe{in: in, lt: lt, tc: tc, cancelled: &cancelled}
}

var inputStates = map[string]func(testing.TB) *inputProbe{
	"incipit": func(tb testing.TB) *inputProbe { return newInputProbe(tb, false) },
	"transcript": func(tb testing.TB) *inputProbe {
		return newInputProbe(tb, true)
	},
	"transcript+sel": func(tb testing.TB) *inputProbe {
		p := newInputProbe(tb, true)
		p.lt.tr.selectNode(1, false)
		p.lt.tr.render()
		return p
	},
	"search": func(tb testing.TB) *inputProbe {
		p := newInputProbe(tb, true)
		p.in.consume([]byte("/ms"))
		return p
	},
	"panel": func(tb testing.TB) *inputProbe {
		p := newInputProbe(tb, true)
		p.in.consume([]byte("?"))
		return p
	},
	"jump": func(tb testing.TB) *inputProbe {
		p := newInputProbe(tb, true)
		p.in.consume([]byte(":12"))
		return p
	},
}

// inputSignature snapshots everything a keystroke can move. It reads under the
// render lock: a key can leave background work behind (the queued-panel fetch,
// the history-search worker), and those write the same fields.
func inputSignature(p *inputProbe, stop bool, rest []byte) string {
	p.in.mu.Lock()
	defer p.in.mu.Unlock()
	tr := p.lt.tr
	copyFailed, copying := p.in.copyFailed, p.in.copyCancel != nil
	clip, _ := p.tc.clipboard.Load().(string)
	if len(clip) > 12 {
		clip = fmt.Sprintf("%d bytes", len(clip))
	}
	return fmt.Sprintf("stop=%v rest=%q act=%v off=%d fol=%v srch=%v q=%q h=%v s=%v Q=%v g=%v sel=%v verb=%v disc=%d canc=%v clip=%q cpfail=%v cping=%v jmp=%v jq=%q",
		stop, string(rest), tr.active, tr.offset, tr.follow, tr.inSearch, tr.query,
		tr.showHelp, tr.showStatus, tr.showQueued, tr.pendG, tr.selection.active,
		p.in.set.verbose, len(p.in.disconnectCh), p.cancelled.Load(), clip, copyFailed, copying,
		tr.inJump, tr.jumpQuery)
}

// settleProbe waits out the background paging and any selection copy the
// keystroke kicked off, so the signature is deterministic.
func settleProbe(tb testing.TB, p *inputProbe) {
	for {
		p.in.mu.Lock()
		done := p.in.pageDone
		p.in.mu.Unlock()
		if done == nil {
			break
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			tb.Fatal("history prefetch never finished")
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		p.in.mu.Lock()
		busy := p.in.copyCancel != nil || p.in.searchCancel != nil
		p.in.mu.Unlock()
		if !busy {
			return
		}
		if time.Now().After(deadline) {
			tb.Fatal("a background worker (copy or search) never finished")
		}
		time.Sleep(time.Millisecond)
	}
}

// The keys swept, by the name the oracle records them under.
func inputSweepKeys() []struct {
	name string
	data string
} {
	var keys []struct {
		name string
		data string
	}
	add := func(name, data string) {
		keys = append(keys, struct {
			name string
			data string
		}{name, data})
	}
	for b := 0; b < 128; b++ {
		add(fmt.Sprintf("0x%02x", b), string([]byte{byte(b)}))
	}
	for _, n := range []navKey{navUp, navDown, navPageUp, navPageDown, navHome, navEnd} {
		add("nav:"+inputOracleNavName(n), inputNavSeq(n))
	}
	add("csiu ^n", "\x1b[110;5u")
	add("csiu ^N+shift", "\x1b[110;6u")
	add("csiu ^p", "\x1b[112;5u")
	add("csiu ^p+alt", "\x1b[112;7u")
	add("csiu ^d", "\x1b[100;5u")
	add("csiu ^l", "\x1b[108;5u")
	add("csiu ^t", "\x1b[116;5u")
	add("csiu ^o", "\x1b[111;5u")
	add("alt ^n fallback", "\x1b\x0e")
	add("alt ^p fallback", "\x1b\x10")
	return keys
}

func inputOracleNavName(n navKey) string {
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
	return "?"
}

func inputNavSeq(n navKey) string {
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

// REBASED A SECOND TIME, for the cold-selection seed (Ctrl-N/Ctrl-P). Two
// causes have moved this oracle in quick succession and they must not be
// confused:
//
//   - sepRows (c1ef47f) shrank the separator by one row, so every off= fell by
//     a CONSTANT one-per-message-boundary. fol= did not move; which block was
//     selected did not move. That regeneration is already in this file.
//   - THIS one is behavioural. A cold selection used to seed from the ends of
//     the RETAINED WINDOW (len(refs)-1 for ^P, 0 for ^N) and then let
//     ensureSelectionVisible drag the page to it. It now seeds from the
//     VIEWPORT and does not scroll. Every fixture whose state is built by a
//     cold selectNode therefore STARTS somewhere else, and every key measured
//     from it reports a different off=.
//
// Three columns other than off= moved, all in the +sel states, and both
// patterns are consequences of that new starting position rather than of the
// keys themselves:
//
//   - old= true->false on the upward scroll. The old fixture started at the top
//     of the window (off=2), so scrolling up hit the edge and armed the
//     older-history prefetch. From where the reader actually is, it does not.
//   - fol= false->true with new= true->false on the four downward scrolls.
//     From off=59 a half-page down runs past the last row, which re-attaches to
//     the tail (pagerTail), and following does not arm the newer prefetch. That
//     is the documented live-padding behaviour, reached now and not before.
//
// The off= column was rebased a FOURTH time, and only on the ^O rows — the
// eight cells (0x0f and csiu ^o, in each of the four states that have them)
// where the signature is taken with verbosity ON. Ctrl-O now also draws one
// dim coordinate row above every rendered node (transcript_coords.go), so a
// verbose window is taller. Nothing else in the signature moved, and no other
// key moved at all: verbose is the only state in which the row exists.
//
// The two directions are the same mechanism seen from two sides, and both were
// MEASURED (heldWindow() before and after, same fixture):
//
//   - already in the pager (transcript, search, panel): 771 -> 843, +72.
//     Ctrl-O drops the row cache; it does NOT rebuild the window. So the
//     window keeps its 18 messages and each simply grows: 18 messages x 4
//     nodes = 72 coordinate rows, exactly the delta. (panel starts 20 rows
//     lower for its open panel, hence 791 -> 863; the delta is the same 72.)
//   - from incipit: 771 -> 745, -26. There Ctrl-O OPENS the pager, so the
//     window is built cold by resetToTail — and that window is budgeted in
//     ROWS, not messages. Taller messages buy fewer of them: 18 messages /
//     808 rows before, 16 / 782 after. This is the same budget effect the
//     sepRows note above records, running the other way: there the separator
//     shrank and this very row ROSE, 742 -> 771.
//
// Regenerated a FIFTH time, for the ':' coordinate jump. Three classes,
// diffed cell by cell against the previous literal before installing:
//
//   - EVERY expectation gained the suffix ` jmp=false jq=""` — the signature
//     grew the jump box's two columns.
//   - the three `0x3a` cells (transcript, transcript+sel, panel) report
//     `jmp=true` and NOTHING else moved. ':' inside the search box is still
//     literal text, which is why the search state is not in this list at all.
//   - EVERY cell whose signature is taken with the HELP PANEL showing rose by
//     exactly ONE row of off=. The panel is generated from the keymap, the
//     keymap gained a ':' row, so the panel is one line taller, layout() gives
//     the body one line less, and a FOLLOWING viewport's offset is one higher.
//     Nineteen cells in the panel state plus the two '?' cells that open it.
//     No other column moved in any of them; verb=, sel=, clip= and rest= are
//     byte-identical.
//
// REGENERATED A SIXTH TIME, for phase 2a-part-2 (the pager's window became an
// interval into the range store, and fetched history stopped living in a second
// copy). SIX cells moved, all of them for one reason: the window can now grow
// DOWNWARD over history THE STORE ALREADY HOLDS, for free, and this fixture
// applies the whole aria to the client.
//
//   - nav:Home, in the four states that have it: off= 0 -> 790. Home still goes
//     to the top of what is held; the prefetch then pulls the rest of the
//     retained history into the window and the viewport ANCHOR is restored, so
//     the reader keeps looking at the same rows and their line number moves.
//     That is exactly what a landed page always did (applyPage ->
//     restoreViewportAnchor); it is new only in that no round trip is needed.
//   - 0x0a / 0x0d in the "jump" state: `:12` now LANDS (off=306, sel=true)
//     where it used to give up and restore the reader (off=753, fol=true). The
//     probe's client holds all 120 turns, so turn 12 was never missing — the
//     old pager just could not see it, because its window was three fetched
//     pages wide and the store was not the window.
//
// And one new state, "jump".
//
// Regenerated mechanically, not hand-edited.
// REGENERATED A SEVENTH TIME, for the pager/incipit width invariant: node rows
// are now rendered at the pane's full width (they used to be rendered at w-2
// and prefixed with a blank column for the selection bar), so every message is
// a few rows SHORTER and the row addresses move.
//
// ONE COLUMN MOVED, off=, in 345 of 350 cells; every other column is
// byte-identical, including sel=, fol=, verb=, clip= and rest=. Diffed field by
// field before installing. The deltas group, and each group is the same fact
// seen from a different anchor:
//
//   - -13, in 325 cells: the tail-anchored offset. The retained window holds
//     the same messages and each is 13 rows shorter in total.
//   - -8 / -15 / -2: the same shrink measured in the verbose (^O) and
//     cold-selection states, where the window is built from a different seed.
//   - +28 (nav:Home) and +102 (the ':12' jump landing): these are budgeted in
//     ROWS, so shorter messages buy MORE of them — the window reaches further
//     back and the row a landed anchor sits on has more history above it. Same
//     mechanism as the sepRows note above, running the other way.
//
// Regenerated mechanically, not hand-edited.
var inputOracle = []struct {
	state string
	inert string
	keys  map[string]string
}{
	{"incipit", "stop=false rest=\"\" act=false off=0 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"", map[string]string{
		"0x03":            "stop=true rest=\"\" act=false off=0 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=true clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x04":            "stop=true rest=\"\" act=false off=0 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=1 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x0a":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x0c":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x0d":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x0e":            "stop=false rest=\"\" act=true off=742 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x0f":            "stop=false rest=\"\" act=true off=761 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=true disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x10":            "stop=false rest=\"\" act=true off=742 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x14":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x21":            "stop=false rest=\"\" act=true off=749 fol=true srch=false q=\"\" h=false s=true Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x2f":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x3f":            "stop=false rest=\"\" act=true off=767 fol=true srch=false q=\"\" h=true s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x47":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x51":            "stop=false rest=\"\" act=true off=746 fol=true srch=false q=\"\" h=false s=false Q=true g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x64":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x67":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=true sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x6a":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x6b":            "stop=false rest=\"\" act=true off=742 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x75":            "stop=false rest=\"\" act=true off=722 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x79":            "stop=false rest=\"\" act=false off=0 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"aria0001\" cpfail=false cping=false jmp=false jq=\"\"",
		"nav:Up":          "stop=false rest=\"\" act=true off=742 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"nav:Down":        "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"nav:PgUp":        "stop=false rest=\"\" act=true off=722 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"nav:PgDn":        "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"nav:Home":        "stop=false rest=\"\" act=true off=780 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"nav:End":         "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"csiu ^n":         "stop=false rest=\"\" act=true off=742 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"csiu ^N+shift":   "stop=false rest=\"\" act=true off=742 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"csiu ^p":         "stop=false rest=\"\" act=true off=742 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"csiu ^p+alt":     "stop=false rest=\"\" act=true off=742 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"csiu ^d":         "stop=true rest=\"\" act=false off=0 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=1 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"csiu ^l":         "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"csiu ^t":         "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"csiu ^o":         "stop=false rest=\"\" act=true off=761 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=true disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"alt ^n fallback": "stop=false rest=\"\" act=true off=742 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"alt ^p fallback": "stop=false rest=\"\" act=true off=742 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
	}},
	{"jump", "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12\"", map[string]string{
		"0x03":            "stop=true rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=true clip=\"\" cpfail=false cping=false jmp=true jq=\"12\"",
		"0x04":            "stop=true rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=1 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12\"",
		"0x08":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"1\"",
		"0x0a":            "stop=false rest=\"\" act=true off=228 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x0d":            "stop=false rest=\"\" act=true off=228 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x0f":            "stop=false rest=\"\" act=true off=811 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=true disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12\"",
		"0x1b":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x20":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12 \"",
		"0x21":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12!\"",
		"0x22":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12\\\"\"",
		"0x23":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12#\"",
		"0x24":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12$\"",
		"0x25":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12%\"",
		"0x26":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12&\"",
		"0x27":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12'\"",
		"0x28":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12(\"",
		"0x29":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12)\"",
		"0x2a":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12*\"",
		"0x2b":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12+\"",
		"0x2c":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12,\"",
		"0x2d":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12-\"",
		"0x2e":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12.\"",
		"0x2f":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12/\"",
		"0x30":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"120\"",
		"0x31":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"121\"",
		"0x32":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"122\"",
		"0x33":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"123\"",
		"0x34":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"124\"",
		"0x35":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"125\"",
		"0x36":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"126\"",
		"0x37":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"127\"",
		"0x38":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"128\"",
		"0x39":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"129\"",
		"0x3a":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12:\"",
		"0x3b":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12;\"",
		"0x3c":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12<\"",
		"0x3d":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12=\"",
		"0x3e":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12>\"",
		"0x3f":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12?\"",
		"0x40":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12@\"",
		"0x41":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12A\"",
		"0x42":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12B\"",
		"0x43":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12C\"",
		"0x44":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12D\"",
		"0x45":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12E\"",
		"0x46":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12F\"",
		"0x47":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12G\"",
		"0x48":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12H\"",
		"0x49":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12I\"",
		"0x4a":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12J\"",
		"0x4b":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12K\"",
		"0x4c":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12L\"",
		"0x4d":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12M\"",
		"0x4e":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12N\"",
		"0x4f":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12O\"",
		"0x50":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12P\"",
		"0x51":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12Q\"",
		"0x52":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12R\"",
		"0x53":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12S\"",
		"0x54":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12T\"",
		"0x55":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12U\"",
		"0x56":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12V\"",
		"0x57":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12W\"",
		"0x58":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12X\"",
		"0x59":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12Y\"",
		"0x5a":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12Z\"",
		"0x5b":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12[\"",
		"0x5c":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12\\\\\"",
		"0x5d":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12]\"",
		"0x5e":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12^\"",
		"0x5f":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12_\"",
		"0x60":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12`\"",
		"0x61":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12a\"",
		"0x62":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12b\"",
		"0x63":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12c\"",
		"0x64":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12d\"",
		"0x65":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12e\"",
		"0x66":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12f\"",
		"0x67":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12g\"",
		"0x68":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12h\"",
		"0x69":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12i\"",
		"0x6a":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12j\"",
		"0x6b":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12k\"",
		"0x6c":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12l\"",
		"0x6d":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12m\"",
		"0x6e":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12n\"",
		"0x6f":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12o\"",
		"0x70":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12p\"",
		"0x71":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12q\"",
		"0x72":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12r\"",
		"0x73":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12s\"",
		"0x74":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12t\"",
		"0x75":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12u\"",
		"0x76":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12v\"",
		"0x77":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12w\"",
		"0x78":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12x\"",
		"0x79":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12y\"",
		"0x7a":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12z\"",
		"0x7b":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12{\"",
		"0x7c":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12|\"",
		"0x7d":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12}\"",
		"0x7e":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12~\"",
		"0x7f":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"1\"",
		"csiu ^n":         "stop=false rest=\"\" act=true off=742 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12\"",
		"csiu ^N+shift":   "stop=false rest=\"\" act=true off=742 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12\"",
		"csiu ^p":         "stop=false rest=\"\" act=true off=742 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12\"",
		"csiu ^p+alt":     "stop=false rest=\"\" act=true off=742 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12\"",
		"csiu ^d":         "stop=true rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=1 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12\"",
		"csiu ^o":         "stop=false rest=\"\" act=true off=811 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=true disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12\"",
		"alt ^n fallback": "stop=false rest=\"\" act=true off=742 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12\"",
		"alt ^p fallback": "stop=false rest=\"\" act=true off=742 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"12\"",
	}},
	{"panel", "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"", map[string]string{
		"0x03":            "stop=true rest=\"\" act=true off=767 fol=true srch=false q=\"\" h=true s=false Q=false g=false sel=false verb=false disc=0 canc=true clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x04":            "stop=true rest=\"\" act=true off=767 fol=true srch=false q=\"\" h=true s=false Q=false g=false sel=false verb=false disc=1 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x0c":            "stop=false rest=\"\" act=true off=767 fol=true srch=false q=\"\" h=true s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x0e":            "stop=false rest=\"\" act=true off=742 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x0f":            "stop=false rest=\"\" act=true off=835 fol=true srch=false q=\"\" h=true s=false Q=false g=false sel=false verb=true disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x10":            "stop=false rest=\"\" act=true off=742 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x14":            "stop=false rest=\"\" act=true off=767 fol=true srch=false q=\"\" h=true s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x21":            "stop=false rest=\"\" act=true off=749 fol=true srch=false q=\"\" h=false s=true Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x2f":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x3a":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"\"",
		"0x48":            "stop=false rest=\"\" act=true off=767 fol=true srch=false q=\"\" h=true s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x51":            "stop=false rest=\"\" act=true off=746 fol=true srch=false q=\"\" h=false s=false Q=true g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x58":            "stop=false rest=\"\" act=true off=767 fol=true srch=false q=\"\" h=true s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x67":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=true sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x6b":            "stop=false rest=\"\" act=true off=742 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x71":            "stop=true rest=\"\" act=true off=767 fol=true srch=false q=\"\" h=true s=false Q=false g=false sel=false verb=false disc=1 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x75":            "stop=false rest=\"\" act=true off=722 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x79":            "stop=false rest=\"\" act=true off=767 fol=true srch=false q=\"\" h=true s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"aria0001\" cpfail=false cping=false jmp=false jq=\"\"",
		"nav:Up":          "stop=false rest=\"\" act=true off=742 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"nav:PgUp":        "stop=false rest=\"\" act=true off=722 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"nav:Home":        "stop=false rest=\"\" act=true off=780 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"csiu ^n":         "stop=false rest=\"\" act=true off=766 fol=false srch=false q=\"\" h=true s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"csiu ^N+shift":   "stop=false rest=\"\" act=true off=766 fol=false srch=false q=\"\" h=true s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"csiu ^p":         "stop=false rest=\"\" act=true off=766 fol=false srch=false q=\"\" h=true s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"csiu ^p+alt":     "stop=false rest=\"\" act=true off=766 fol=false srch=false q=\"\" h=true s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"csiu ^d":         "stop=true rest=\"\" act=true off=767 fol=true srch=false q=\"\" h=true s=false Q=false g=false sel=false verb=false disc=1 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"csiu ^l":         "stop=false rest=\"\" act=true off=767 fol=true srch=false q=\"\" h=true s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"csiu ^t":         "stop=false rest=\"\" act=true off=767 fol=true srch=false q=\"\" h=true s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"csiu ^o":         "stop=false rest=\"\" act=true off=835 fol=true srch=false q=\"\" h=true s=false Q=false g=false sel=false verb=true disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"alt ^n fallback": "stop=false rest=\"\" act=true off=766 fol=false srch=false q=\"\" h=true s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"alt ^p fallback": "stop=false rest=\"\" act=true off=766 fol=false srch=false q=\"\" h=true s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
	}},
	{"search", "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"ms\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"", map[string]string{
		"0x03":            "stop=true rest=\"\" act=true off=743 fol=true srch=true q=\"ms\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=true clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x04":            "stop=true rest=\"\" act=true off=743 fol=true srch=true q=\"ms\" h=false s=false Q=false g=false sel=false verb=false disc=1 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x08":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"m\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x0a":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"ms\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x0d":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"ms\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x0f":            "stop=false rest=\"\" act=true off=811 fol=true srch=true q=\"ms\" h=false s=false Q=false g=false sel=false verb=true disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x1b":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x20":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"ms \" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x21":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"ms!\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x22":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"ms\\\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x23":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"ms#\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x24":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"ms$\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x25":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"ms%\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x26":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"ms&\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x27":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"ms'\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x28":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"ms(\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x29":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"ms)\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x2a":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"ms*\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x2b":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"ms+\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x2c":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"ms,\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x2d":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"ms-\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x2e":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"ms.\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x2f":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"ms/\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x30":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"ms0\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x31":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"ms1\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x32":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"ms2\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x33":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"ms3\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x34":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"ms4\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x35":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"ms5\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x36":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"ms6\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x37":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"ms7\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x38":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"ms8\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x39":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"ms9\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x3a":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"ms:\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x3b":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"ms;\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x3c":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"ms<\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x3d":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"ms=\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x3e":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"ms>\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x3f":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"ms?\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x40":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"ms@\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x41":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"msA\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x42":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"msB\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x43":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"msC\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x44":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"msD\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x45":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"msE\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x46":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"msF\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x47":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"msG\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x48":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"msH\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x49":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"msI\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x4a":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"msJ\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x4b":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"msK\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x4c":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"msL\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x4d":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"msM\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x4e":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"msN\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x4f":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"msO\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x50":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"msP\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x51":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"msQ\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x52":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"msR\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x53":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"msS\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x54":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"msT\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x55":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"msU\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x56":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"msV\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x57":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"msW\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x58":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"msX\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x59":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"msY\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x5a":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"msZ\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x5b":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"ms[\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x5c":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"ms\\\\\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x5d":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"ms]\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x5e":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"ms^\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x5f":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"ms_\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x60":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"ms`\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x61":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"msa\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x62":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"msb\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x63":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"msc\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x64":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"msd\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x65":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"mse\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x66":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"msf\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x67":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"msg\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x68":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"msh\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x69":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"msi\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x6a":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"msj\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x6b":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"msk\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x6c":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"msl\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x6d":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"msm\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x6e":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"msn\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x6f":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"mso\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x70":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"msp\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x71":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"msq\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x72":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"msr\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x73":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"mss\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x74":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"mst\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x75":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"msu\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x76":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"msv\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x77":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"msw\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x78":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"msx\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x79":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"msy\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x7a":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"msz\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x7b":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"ms{\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x7c":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"ms|\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x7d":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"ms}\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x7e":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"ms~\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x7f":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"m\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"csiu ^n":         "stop=false rest=\"\" act=true off=742 fol=false srch=true q=\"ms\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"csiu ^N+shift":   "stop=false rest=\"\" act=true off=742 fol=false srch=true q=\"ms\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"csiu ^p":         "stop=false rest=\"\" act=true off=742 fol=false srch=true q=\"ms\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"csiu ^p+alt":     "stop=false rest=\"\" act=true off=742 fol=false srch=true q=\"ms\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"csiu ^d":         "stop=true rest=\"\" act=true off=743 fol=true srch=true q=\"ms\" h=false s=false Q=false g=false sel=false verb=false disc=1 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"csiu ^o":         "stop=false rest=\"\" act=true off=811 fol=true srch=true q=\"ms\" h=false s=false Q=false g=false sel=false verb=true disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"alt ^n fallback": "stop=false rest=\"\" act=true off=742 fol=false srch=true q=\"ms\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"alt ^p fallback": "stop=false rest=\"\" act=true off=742 fol=false srch=true q=\"ms\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
	}},
	{"transcript", "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"", map[string]string{
		"0x03":            "stop=true rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=true clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x04":            "stop=true rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=1 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x0e":            "stop=false rest=\"\" act=true off=742 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x0f":            "stop=false rest=\"\" act=true off=811 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=true disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x10":            "stop=false rest=\"\" act=true off=742 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x21":            "stop=false rest=\"\" act=true off=749 fol=true srch=false q=\"\" h=false s=true Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x2f":            "stop=false rest=\"\" act=true off=743 fol=true srch=true q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x3a":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"\"",
		"0x3f":            "stop=false rest=\"\" act=true off=767 fol=true srch=false q=\"\" h=true s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x51":            "stop=false rest=\"\" act=true off=746 fol=true srch=false q=\"\" h=false s=false Q=true g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x67":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=true sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x6b":            "stop=false rest=\"\" act=true off=742 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x71":            "stop=true rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=1 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x75":            "stop=false rest=\"\" act=true off=722 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x79":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"aria0001\" cpfail=false cping=false jmp=false jq=\"\"",
		"nav:Up":          "stop=false rest=\"\" act=true off=742 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"nav:PgUp":        "stop=false rest=\"\" act=true off=722 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"nav:Home":        "stop=false rest=\"\" act=true off=780 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"csiu ^n":         "stop=false rest=\"\" act=true off=742 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"csiu ^N+shift":   "stop=false rest=\"\" act=true off=742 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"csiu ^p":         "stop=false rest=\"\" act=true off=742 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"csiu ^p+alt":     "stop=false rest=\"\" act=true off=742 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"csiu ^d":         "stop=true rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=1 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"csiu ^o":         "stop=false rest=\"\" act=true off=811 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=true disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"alt ^n fallback": "stop=false rest=\"\" act=true off=742 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"alt ^p fallback": "stop=false rest=\"\" act=true off=742 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
	}},
	{"transcript+sel", "stop=false rest=\"\" act=true off=742 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"", map[string]string{
		// Enter and Return toggle the SELECTED node's expansion. They were
		// inert until nodeExpandable learned to answer for arguments: a tool
		// whose arguments are still streaming has no output yet, and it is
		// exactly the node a reader most wants to open.
		"0x0a":            "stop=false rest=\"\" act=true off=751 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x0d":            "stop=false rest=\"\" act=true off=751 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x03":            "stop=false rest=\"\" act=true off=742 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=true cping=false jmp=false jq=\"\"",
		"0x04":            "stop=true rest=\"\" act=true off=742 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=1 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x0f":            "stop=false rest=\"\" act=true off=742 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=true disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x10":            "stop=false rest=\"\" act=true off=740 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x1b":            "stop=false rest=\"\" act=true off=742 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=false verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x21":            "stop=false rest=\"\" act=true off=742 fol=false srch=false q=\"\" h=false s=true Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x2f":            "stop=false rest=\"\" act=true off=742 fol=false srch=true q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x3a":            "stop=false rest=\"\" act=true off=742 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=true jq=\"\"",
		"0x3f":            "stop=false rest=\"\" act=true off=742 fol=false srch=false q=\"\" h=true s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x47":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x51":            "stop=false rest=\"\" act=true off=742 fol=false srch=false q=\"\" h=false s=false Q=true g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x64":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x67":            "stop=false rest=\"\" act=true off=742 fol=false srch=false q=\"\" h=false s=false Q=false g=true sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x6a":            "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x6b":            "stop=false rest=\"\" act=true off=741 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x71":            "stop=true rest=\"\" act=true off=742 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=1 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x75":            "stop=false rest=\"\" act=true off=722 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"0x79":            "stop=false rest=\"\" act=true off=742 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=true cping=false jmp=false jq=\"\"",
		"nav:Up":          "stop=false rest=\"\" act=true off=741 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"nav:Down":        "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"nav:PgUp":        "stop=false rest=\"\" act=true off=722 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"nav:PgDn":        "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"nav:Home":        "stop=false rest=\"\" act=true off=780 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"nav:End":         "stop=false rest=\"\" act=true off=743 fol=true srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"csiu ^p":         "stop=false rest=\"\" act=true off=740 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"csiu ^p+alt":     "stop=false rest=\"\" act=true off=740 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"csiu ^d":         "stop=true rest=\"\" act=true off=742 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=1 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"csiu ^o":         "stop=false rest=\"\" act=true off=742 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=true disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
		"alt ^p fallback": "stop=false rest=\"\" act=true off=740 fol=false srch=false q=\"\" h=false s=false Q=false g=false sel=true verb=false disc=0 canc=false clip=\"\" cpfail=false cping=false jmp=false jq=\"\"",
	}},
}

// TestKeymap_InputBehaviourIsUnchanged sweeps every key through the real input
// loop in every mode and compares against the frozen oracle. A key missing
// from a row is an assertion too: it must leave the state an unbound control
// byte would have left.
func TestKeymap_InputBehaviourIsUnchanged(t *testing.T) {
	for _, row := range inputOracle {
		build, ok := inputStates[row.state]
		if !ok {
			t.Fatalf("oracle names state %q, which the harness does not build", row.state)
		}
		t.Run(row.state, func(t *testing.T) {
			for _, k := range inputSweepKeys() {
				p := build(t)
				rest, stop := p.in.consume([]byte(k.data))
				settleProbe(t, p)
				want, special := row.keys[k.name]
				if !special {
					want = row.inert
				}
				if got := inputSignature(p, stop, rest); got != want {
					verdict := "differs from the pre-refactor input loop"
					if !special {
						verdict = "was inert before the refactor and is not now"
					}
					t.Errorf("%s in %s mode %s:\n got %s\nwant %s", k.name, row.state, verdict, got, want)
				}
			}
		})
	}
}
