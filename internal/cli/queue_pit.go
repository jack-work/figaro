package cli

import (
	"strconv"
	"strings"

	"github.com/jack-work/figaro/internal/render"
)

const (
	queueBodyIndent  = 4
	queueBodyRowsMax = 500
)

func queueRowID(id uint64) string { return strconv.FormatUint(id, 10) }

func queueBodyRowID(id uint64, n int) string { return queueRowID(id) + "\x00" + strconv.Itoa(n) }

func queueMessageOf(rowID string) (uint64, bool) {
	n, err := strconv.ParseUint(valuePath(rowID), 10, 64)
	return n, err == nil
}

func (t *transcript) queuedPitRows() []pitRow {
	if len(t.queued) == 0 {
		return nil
	}
	live := make(map[uint64]bool, len(t.queued))
	rows := make([]pitRow, 0, len(t.queued))
	for _, q := range t.queued {
		live[q.id] = true
		if !t.queueOpen[q.id] {
			rows = append(rows, pitRow{text: q.mark() + " ▸ " + firstLineTrim(q.text), yank: q.text, id: queueRowID(q.id)})
			continue
		}
		lines := queueBodyLines(q.text, max(t.w-queueBodyIndent-1, 8))
		rows = append(rows, pitRow{text: q.mark() + " ▾ " + lines[0], yank: q.text, id: queueRowID(q.id)})
		indent := strings.Repeat(" ", queueBodyIndent)
		for n, line := range lines[1:] {
			rows = append(rows, pitRow{text: indent + line, yank: q.text, id: queueBodyRowID(q.id, n)})
		}
	}
	for id := range t.queueOpen {
		if !live[id] {
			delete(t.queueOpen, id)
		}
	}
	return rows
}

func queueBodyLines(text string, width int) []string {
	text = strings.ReplaceAll(render.StripEscapes(text), "\t", "    ")
	var out []string
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		out = append(out, wrapPlain(line, width)...)
	}
	if len(out) > queueBodyRowsMax {
		out = append(out[:queueBodyRowsMax:queueBodyRowsMax],
			AndMore(len(out)-queueBodyRowsMax, "lines · y yanks all of it"))
	}
	return out
}

func (t *transcript) toggleQueuedMessage() bool {
	row, ok := t.pit.selected()
	if !ok {
		return false
	}
	id, ok := queueMessageOf(row.id)
	if !ok {
		return false
	}
	if t.queueOpen == nil {
		t.queueOpen = map[uint64]bool{}
	}
	if t.queueOpen[id] {
		delete(t.queueOpen, id)
	} else {
		t.queueOpen[id] = true
	}
	t.pit.replaceRows(t.queuedPitRows(), queueRowID(id))
	return true
}

func (t *transcript) dropQueuedSelected() {
	row, ok := t.pit.selected()
	if !ok || t.dropRow == nil {
		return
	}
	id, ok := queueMessageOf(row.id)
	if !ok {
		return
	}
	var keep []pitRow
	var heads []string
	at := -1
	if list := t.pit.list(); list != nil {
		for _, r := range list.rows {
			m, _ := queueMessageOf(r.id)
			if strings.IndexByte(r.id, 0) < 0 {
				if m == id {
					at = len(heads)
				}
				heads = append(heads, r.id)
			}
			if m != id {
				keep = append(keep, r)
			}
		}
	}
	next := ""
	switch {
	case at >= 0 && at+1 < len(heads):
		next = heads[at+1]
	case at > 0:
		next = heads[at-1]
	}
	t.dropRowAt(string(pitQueue), queueRowID(id), next)
	delete(t.queueOpen, id)
	t.pit.replaceRows(keep, next)
}

// Opening defers to a reader who has said what they want on screen; closing does not.
func (t *transcript) showQueuedAuto(on bool) {
	if on {
		if t.showing(pitQueue) {
			t.refreshQueuePit()
			return
		}
		// Dismissed by hand, or a deliberate pit is up. A note is not
		// deliberate.
		if t.queueDismissed || (t.pit.open() && !t.pit.glance()) {
			return
		}
		t.openQueuePit()
		return
	}
	t.queueDismissed = false
	if !t.showing(pitQueue) {
		return
	}
	// A drained queue closes its pit, opened by hand or not, and says so on
	// the bar.
	t.queuedByKey = false
	t.pit.close()
	t.noteQueueEmpty()
}

// The stale snapshot renders immediately; the async refresh replaces it in place.
func (t *transcript) openQueueFromKey() {
	// An empty queue is a sentence on the bar, not a pit; the dismissal is
	// revoked either way.
	t.queueDismissed = false
	if t.queueEmpty() {
		t.noteQueueEmpty()
		if t.queuedFetch != nil {
			// Resync anyway: the seed may still be in flight, and
			// showQueuedAuto opens the pit when a real queue lands.
			t.queuedFetch()
		}
		return
	}
	t.queuedByKey = true
	t.openQueuePit()
	if t.queuedFetch != nil {
		t.queuedFetch()
	}
}

// An unreadable queue is not empty.
func (t *transcript) queueEmpty() bool {
	return len(t.queued) == 0 && t.queuedErr == ""
}

// The pit's absence and its auto-close are the same fact, worded once.
const queueEmptyNote = "message queue is empty"

func (t *transcript) noteQueueEmpty() { t.setCommandNoteAt(queueEmptyNote, alertInfo) }

func (t *transcript) openQueuePit() {
	// No title: the bar already says "queue".
	t.pit.showList(pitQueue, "", t.queuedPitRows())
	t.focused = focusPit
}

// Keeps the selection: a queue that re-sorts under the cursor on every poll is one
// nobody can hit `x` on.
func (t *transcript) refreshQueuePit() {
	sel, had := t.pit.selected()
	keep := ""
	if had {
		keep = sel.id
	}
	t.pit.replaceRows(t.queuedPitRows(), keep)
}
