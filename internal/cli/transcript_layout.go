package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"
)

func (t *transcript) footLines() []string {
	rows := t.drawerLines()
	if rows == nil {
		return nil
	}
	// Fullscreen is the pit and nothing else: no rule, no page position.
	if t.fullPit() {
		return rows
	}
	return append([]string{t.transcriptRule()}, rows...)
}

func (t *transcript) transcriptRule() string {
	total := t.index.total
	body, _ := t.layoutNow()
	pos := ""
	if total > body {
		end := min(t.offset+body, total)
		mark := ""
		if !t.whole() {
			mark = "+"
		}
		pos = fmt.Sprintf("%d–%d/%d%s", t.offset+1, end, total, mark)
	}
	if t.follow {
		pos = strings.TrimSpace(pos + " live")
	}
	return "\x1b[2m" + t.status.ruleLine(t.w, t.composerHints(), pos) + "\x1b[0m"
}

type pitFocus uint8

const (
	focusPit        pitFocus = iota // a pit you opened is a pit you are in
	focusTranscript                 // `T`: read the conversation, keep the pit
)

// A fullscreen pit recedes when the transcript takes the keys.
func (t *transcript) fullPit() bool {
	return t.full && (t.pit.open() || t.box == boxComposer) && t.focused == focusPit
}

// The only place this arithmetic is done: the pane, minus the bar (one row, or
// three when it wraps), the blank above it, the rule, and a row of conversation.
func (t *transcript) pitRoom() int {
	if t.fullPit() {
		// Fullscreen: everything but the status bar and the blank above
		// it; no rule is drawn.
		return max(t.h-t.barRows()-1, 1)
	}
	return max(t.h-t.barRows()-5, 1)
}

// Without re-entering footLines, which calls transcriptRule, which would call this.
func (t *transcript) layoutNow() (body, maxOff int) {
	return t.layout(len(t.drawerLines()) + 1)
}

func (t *transcript) drawerLines() []string {
	rows := t.inputDrawerLines()
	if rows == nil {
		if !t.pit.open() {
			return nil
		}
		t.pit.full = t.fullPit()
		return t.pit.lines(t.w, t.pitRoom())
	}
	if t.inSearch {
		return append(t.searchedLines(len(rows)), rows...)
	}
	return rows
}

func (t *transcript) searchedLines(reserve int) []string {
	switch {
	case t.box == boxComposer:
		return t.boxLines(reserve)
	case t.pit.open():
		t.pit.full = t.fullPit()
		return t.pit.lines(t.w, max(t.pitRoom()-reserve, 1))
	}
	return nil
}

func padTo(s string, n int) string {
	if w := runewidth.StringWidth(s); w < n {
		return s + strings.Repeat(" ", n-w)
	}
	return s
}

// The bar is not always one row: on a narrow pane it is three, and reserving one
// while painting three pushes the conversation off the top.
func (t *transcript) layout(foot int) (body, maxOff int) {
	body = t.h - 1 - t.barRows() - foot
	if t.follow {
		body--
	}
	if body < 1 {
		body = 1
	}
	return body, max(t.index.total-body, 0)
}

// barRows is asked before the bar is drawn so the reservation and the painting
// cannot disagree. The status bar is inviolable: search, command line and errors
// live in the pit.
func (t *transcript) barRows() int {
	if t.status == nil {
		return 1
	}
	return max(t.status.viewOf(t.openPit(), t.status.barVerbose(), time.Now()).height(t.w), 1)
}

func (t *transcript) footerRows(total, body int) (rule string, bar []string) {
	pos := ""
	if total > body {
		end := t.offset + body
		if end > total {
			end = total
		}
		mark := ""
		if !t.whole() {
			mark = "+"
		}
		pos = fmt.Sprintf("%d–%d/%d%s", t.offset+1, end, total, mark)
	}
	// The live marker does not ride on the range: a transcript that fits the
	// pane is still either following or pinned.
	if t.follow {
		pos = strings.TrimSpace(pos + " live")
	}
	// One footer, shared with the incipit's bookend (footerStanza).
	stanza := footerStanza(t.status, t.w, pos, t.openPit(), t.status.barVerbose())
	if len(stanza) == 0 {
		return "", []string{""}
	}
	return stanza[0], stanza[1:]
}

// The prompt is the editor's: under ^R it reads `(reverse-i-search)`. The box
// wraps on the pit's page; the drawer may take the pane (F), the command line
// may not.
func (t *transcript) boxLines(reserve int) []string {
	e := t.editor()
	rows := t.composerTitle()
	room := max(min(t.boxRows(), t.pitRoom()-reserve)-len(rows), 1)
	if t.fullPit() {
		room = max(t.pitRoom()-reserve-len(rows), 1)
	}
	lo, hi := -1, -1
	if from, to, ok := t.composerSpan(); ok {
		lo, hi = from, to
	}
	rows = append(rows, e.wrapWash(e.prompt(t.boxSigil()), t.w, room, lo, hi)...)
	if t.box == boxComposer && t.fullPit() {
		for len(rows) < room {
			rows = append(rows, "")
		}
	}
	return rows
}

func (t *transcript) inputDrawerLines() []string {
	var rows []string
	switch {
	case t.inSearch:
		sigil := "/"
		if t.searchStep() < 0 {
			sigil = "?"
		}
		// A search aimed at a drawer or list says so with one glyph.
		if g := t.searchTarget(); g != "" {
			sigil = g + sigil
		}
		rows = []string{pitGray(clipToWidth(sigil+t.query, t.w))}
	case t.box != boxNone:
		rows = t.boxLines(0)
	default:
		return nil
	}
	footer, own := t.jumpFooter()
	if t.boxOpen() && t.menu != nil {
		room := t.pitRoom() - len(rows)
		if own {
			room--
		}
		rows = append(rows, t.menu.lines(t.w, room)...)
	}
	if own {
		rows = append(rows, pitGray(clipToWidth("  "+footer, t.w)))
	}
	return rows
}
