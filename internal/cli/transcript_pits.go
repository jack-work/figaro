package cli

import (
	"fmt"
	"strings"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/cmdkit"
)

// A row is selectable because its verb said what it names, not because its text
// looks aria-shaped.
func (t *transcript) setCmdOut(title string, rows []pitRow) {
	t.queuedByKey = false
	t.pit.showList(pitOutput, ":"+title, rows)
	t.pit.focus(t.status.figaroID)
	t.focused = focusPit
	t.render()
}

func (t *transcript) statusPanelLines() []string {
	rows := t.status.panelLines()
	if max := t.h - 4; len(rows) > max && max > 0 {
		rows = rows[:max]
	}
	for i, r := range rows {
		rows[i] = "\x1b[2m" + clipToWidth(r, t.w) + "\x1b[0m"
	}
	return rows
}

func firstLineTrim(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

// A bottom panel, not a floating overlay: the terminal has one alternate buffer.
func (t *transcript) helpLines() []string {
	body := helpBody()
	rows := make([]string, 0, len(body)+5)
	rows = append(rows, "")
	rows = append(rows, body...)
	// The mouse is documented here, not in the keymap: the table is keyed by
	// keystroke and its invariants are stated in keys.
	rows = append(rows, mouseHelpRows()...)
	if v := helpVersionLine(); v != "" {
		rows = append(rows, "", "  "+v)
	}
	if max := t.h - 4; len(rows) > max && max > 0 { // tiny pane: keep the top of the list
		rows = rows[:max]
	}
	for i, r := range rows {
		rows[i] = "\x1b[2m" + clipToWidth(r, t.w) + "\x1b[0m"
	}
	return rows
}

func (t *transcript) closePanels() {
	if t.showing(pitQueue) {
		t.queueDismissed = true
	}
	t.pit.close()
	t.queuedByKey = false
	t.focused = focusPit // the next pit opens focused
}

func (t *transcript) showing(id pitID) bool {
	return t.pit.open() && t.pit.id == id
}

func (t *transcript) togglePit(id pitID, open func()) {
	was := t.showing(id)
	t.closePanels()
	if !was {
		open()
	}
}

func (t *transcript) openHelpPit() {
	rows := make([]pitRow, 0, 24)
	for _, l := range helpBody() {
		rows = append(rows, staticRow(l))
	}
	rows = append(rows, staticRow(""))
	for _, l := range mouseHelpRows() {
		rows = append(rows, staticRow(l))
	}
	if v := helpVersionLine(); v != "" {
		rows = append(rows, staticRow(""), staticRow("  "+v))
	}
	t.pit.showList(pitHelp, "", rows)
	t.focused = focusPit
}

func (t *transcript) openStatusPit() {
	rows := make([]pitRow, 0, 12)
	for _, l := range t.status.panelLines() {
		rows = append(rows, staticRow(l))
	}
	t.pit.showList(pitStatus, "", rows)
	t.focused = focusPit
}

// Opening it is reading it, so the unread mark goes.
func (t *transcript) openNotificationsPit() {
	t.pit.showLive(pitNotifications, &notificationsView{store: t.notes})
	t.focused = focusPit
	t.notes.markRead()
}

func (t *transcript) showLivePit(name string, v cmdkit.LiveView, full bool) {
	t.queuedByKey = false
	t.pit.showLive(pitID(name), v)
	if full {
		t.full = true
	}
	t.focused = focusPit
	t.render()
}

// A one-line result is an alert on the bar, retiring after cli.notice_ttl;
// anything longer is a pit. A note must never replace an open list.
func (t *transcript) setCommandNote(note string) { t.setCommandNoteAt(note, alertInfo) }

// The bar alone, not posted to the history: the result follows and replaces it.
func (t *transcript) setProgressNote(note string) {
	if note == "" || strings.Contains(note, "\n") {
		return
	}
	t.status.setProgress(note)
	t.render()
}

func (t *transcript) setCommandNoteAt(note string, level alertLevel) {
	// Every note is posted to the history, whatever the bar does with it.
	t.notes.post(level, "cli", note)
	if note == "" {
		t.status.setNotice("")
		if t.showing(pitNote) {
			t.pit.close()
		}
		t.render()
		return
	}
	if !strings.Contains(note, "\n") {
		t.status.setNoticeAt(note, level)
		t.render()
		return
	}
	t.pit.showNote(note)
	t.focused = focusPit
	t.render()
}

// In the bar, never a pit: a confirmation must not replace the list it was yanking
// from.
func (t *transcript) noteYank(text string) {
	one := firstLineTrim(text)
	if displayWidth(one) > 24 {
		one = fmt.Sprintf("%d bytes", len(text))
	}
	t.setCommandNote("yanked " + one)
}

func (t *transcript) dropRowAt(pit, id, next string) {
	if t.dropRow != nil {
		t.dropRow(pit, id, next)
	}
}

func (t *transcript) dropSelected(ask bool) {
	if t.pit.id == pitQueue {
		t.dropQueuedSelected()
		return
	}
	row, ok := t.pit.selected()
	if !ok || row.id == "" || t.dropRow == nil {
		return
	}
	pit, id := string(t.pit.id), row.id
	// Where the reader stands afterwards is a fact about this list, not the daemon.
	next := ""
	if n, ok := t.pit.neighbour(); ok {
		next = n.id
	}
	drop := func() {
		t.dropRowAt(pit, id, next)
		t.pit.removeSelected() // optimistic: the refresh confirms it
	}
	// Only an aria is worth a question: a queued message is reprinted on the
	// way out, an aria is gone.
	if ask && rpc.ValidateAriaID(id) == nil {
		t.askConfirm(killPrompt(id), drop)
		return
	}
	drop()
}
