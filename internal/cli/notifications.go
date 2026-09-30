package cli

// NOTIFICATIONS: what figaro told you, still there after it stops saying it.
// See plans/notifications.md.
//
// Everything the pager says went through one slot, the first cell of the
// status bar, which retires after notice_ttl. After that the only trace was
// one string (sessionStatus.lastError) and the log file. Now every alert is
// POSTED to this store first, and the bar and the pit are two views of it:
//
//	the ALERT   the newest one, in the bar's first slot, retiring as before
//	the MARK    𝄞 N in the bar while warnings or errors are unread
//	the PIT     `space n` / `:notifications`: the history, as a picker
//
// The store lives on the TRANSCRIPT, not the session status: a subject switch
// mints a new sessionStatus, and a history that forgot everything on a hop
// would be the vanishing all over again. It is in the pager process's memory
// and dies with it; see the plan for why nothing here is written to disk.

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jack-work/figaro/api/rpc"
)

// notificationHistory is how many the store keeps: Neovim's message history
// default (messagesopt=history:500).
const notificationHistory = 500

// notification is one thing figaro said.
type notification struct {
	seq    uint64    // order, and the pit's row id; bumped when it repeats
	at     time.Time // the first time it was said
	last   time.Time // the latest time, when it has repeated
	count  int       // how many times: drawn as ×N
	level  alertLevel
	source string // "cli", or the aria it concerns
	text   string // one line, what the bar showed
	detail string // everything, when there was more than a line
	key    string // coalescing key: source, level, text with digits folded
}

// notificationStore is the history. Its own lock: posts arrive from the render
// path and from slog, which may be called from any goroutine, and must never
// take the render lock to get here.
type notificationStore struct {
	mu          sync.Mutex
	items       []notification // oldest first
	seq         uint64
	readThrough uint64 // everything at or below this seq has been seen
}

func newNotificationStore() *notificationStore { return &notificationStore{} }

var digitRun = regexp.MustCompile(`[0-9]+`)

// coalesceKey is what makes two notifications the same one said again, as
// Gluck's Neovim msg_router coalesces: the same source, level and text,
// counting numbers as equal, so "retry 1 of 3" and "retry 2 of 3" are one row
// that says ×2 and a poller failing every tick is one row, not three hundred.
func coalesceKey(source string, level alertLevel, text string) string {
	return source + "\x00" + strconv.Itoa(int(level)) + "\x00" + digitRun.ReplaceAllString(text, "#")
}

// post adds a notification, or bumps the one it repeats. A repeat is NEW: it
// moves to the top and is unread again, because it happened again.
func (s *notificationStore) post(level alertLevel, source, text string) {
	if s == nil {
		return
	}
	text = strings.TrimRight(text, "\n")
	if strings.TrimSpace(text) == "" {
		return
	}
	line, detail := text, ""
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		line, detail = text[:i], text
	}
	line = strings.Join(strings.Fields(line), " ")
	now := time.Now()
	key := coalesceKey(source, level, line)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	for i := range s.items {
		if s.items[i].key != key {
			continue
		}
		n := s.items[i]
		n.seq, n.last, n.count = s.seq, now, n.count+1
		n.text, n.detail = line, detail // the newest wording of it
		s.items = append(append(s.items[:i:i], s.items[i+1:]...), n)
		return
	}
	s.items = append(s.items, notification{
		seq: s.seq, at: now, last: now, count: 1,
		level: level, source: source, text: line, detail: detail, key: key,
	})
	if over := len(s.items) - notificationHistory; over > 0 {
		s.items = append(s.items[:0:0], s.items[over:]...)
	}
}

// newestFirst is a copy of the history, newest first.
func (s *notificationStore) newestFirst() []notification {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]notification, len(s.items))
	for i, n := range s.items {
		out[len(s.items)-1-i] = n
	}
	return out
}

// unread counts the warnings and errors not yet seen, and the worst of them.
// Info never counts: "sent" is not something to come back for.
func (s *notificationStore) unread() (n int, worst alertLevel) {
	if s == nil {
		return 0, alertInfo
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, it := range s.items {
		if it.seq > s.readThrough && it.level > alertInfo {
			n++
			if it.level > worst {
				worst = it.level
			}
		}
	}
	return n, worst
}

// markRead: everything posted so far has been seen.
func (s *notificationStore) markRead() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.readThrough = s.seq
	s.mu.Unlock()
}

// clear empties the history, as Vim's `:messages clear` does.
func (s *notificationStore) clear() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.items = nil
	s.readThrough = s.seq
	s.mu.Unlock()
}

// ---------------------------------------------------------------------------
// The pit.
// ---------------------------------------------------------------------------

// notificationFilter is `f`'s cycle: everything, then warnings and up, then
// errors alone.
type notificationFilter uint8

const (
	filterAll notificationFilter = iota
	filterWarn
	filterError
)

func (f notificationFilter) keeps(l alertLevel) bool {
	switch f {
	case filterWarn:
		return l >= alertWarn
	case filterError:
		return l >= alertError
	}
	return true
}

func (f notificationFilter) String() string {
	switch f {
	case filterWarn:
		return "warnings and errors"
	case filterError:
		return "errors"
	}
	return "all"
}

// notificationsView is the pit's live view: the pit drives the rows (picker),
// the view owns what they say and its own verbs. It is live because the
// history grows while it is open, and reading it while it grows is reading
// it: every render marks what is on show as seen.
type notificationsView struct {
	store    *notificationStore
	filter   notificationFilter
	expanded map[string]bool // row id -> showing its detail
	closed   bool
}

func (v *notificationsView) Close() { v.closed = true }

// Items is one row per notification, newest first, with an opened row's whole
// text beneath it.
func (v *notificationsView) Items(width int) []pitRow {
	v.store.markRead()
	items := v.store.newestFirst()
	if width <= 0 {
		width = 80
	}
	var rows []pitRow
	for _, n := range items {
		if !v.filter.keeps(n.level) {
			continue
		}
		id := strconv.FormatUint(n.seq, 10)
		yank := n.yank()
		rows = append(rows, pitRow{
			id:   id,
			yank: yank,
			text: n.row(),
			tone: n.level.tone(),
		})
		if !v.expanded[id] {
			continue
		}
		// AS THE STATE PIT OPENS A VALUE: the whole thing, wrapped to the
		// pane, each line selectable so a reader can walk a long one, and
		// every one of them yanks the whole. The indent is the row's
		// continuation, and the 2 is the picker's own selection column.
		for i, l := range n.fullLines(width - len(notificationIndent) - 2) {
			rows = append(rows, pitRow{
				text: notificationIndent + l,
				yank: yank,
				id:   fmt.Sprintf("%s\x00%d", id, i),
				tone: n.level.tone(),
			})
		}
	}
	if len(rows) == 0 {
		msg := "  nothing yet"
		if v.filter != filterAll {
			msg = "  no " + v.filter.String() + " (f shows more)"
		}
		return []pitRow{staticRow(msg)}
	}
	return rows
}

// notificationIndent is what an opened line is set in by, so it reads as the
// row's continuation and not as another notification.
const notificationIndent = "      "

// Activate is Enter, the pit's own action: spell the notification out, or fold
// it back. A line of an opened one addresses the notification it belongs to,
// so Enter there folds it, as it does over a form value.
func (v *notificationsView) Activate(id string) {
	if v.expanded == nil {
		v.expanded = map[string]bool{}
	}
	id = valuePath(id)
	v.expanded[id] = !v.expanded[id]
}

// Key takes the view's own letters: f cycles the filter. (e expands, as it
// does in every itemised pit: see transcript.pitVerb.) Everything else is
// the pit's.
func (v *notificationsView) Key(b byte) bool {
	switch b {
	case 'f':
		v.filter = (v.filter + 1) % 3
		return true
	}
	return false
}

// AriaOf is what `a` attends from a row: the aria it concerns, when it
// concerns one. A line of an opened notification answers for the whole.
func (v *notificationsView) AriaOf(id string) string {
	id = valuePath(id)
	for _, n := range v.store.newestFirst() {
		if strconv.FormatUint(n.seq, 10) == id {
			// A source is "cli" or an aria id, by construction; the id check
			// is for the wire, and it accepts "cli" as readily as an id.
			if n.source != "" && n.source != "cli" && rpc.ValidateAriaID(n.source) == nil {
				return n.source
			}
			return ""
		}
	}
	return ""
}

// levelGlyph is the level at a glance: the bar's own ✗ for trouble, ! for a
// warning, · for news. Single-width, all three.
func (l alertLevel) glyph() string {
	switch l {
	case alertError:
		return "✗"
	case alertWarn:
		return "!"
	}
	return "·"
}

// tone is the level as a COLOUR: the pit paints a row red for trouble and
// yellow for a warning, the same two the bar's unread mark wears. A glyph
// alone did not find an error in a list of forty.
func (l alertLevel) tone() pitTone {
	switch l {
	case alertError:
		return toneError
	case alertWarn:
		return toneWarn
	}
	return toneNone
}

// row is the line the pit draws: when, how bad, about what, what, how often.
func (n notification) row() string {
	src := n.source
	if src == "" {
		src = "cli"
	}
	s := fmt.Sprintf("%s  %s  %-8s  %s", n.last.Format("15:04:05"), n.level.glyph(), src, n.text)
	if n.detail != "" {
		s += " …"
	}
	if n.count > 1 {
		s += fmt.Sprintf("  ×%d", n.count)
	}
	return s
}

// fullLines is what Enter shows: the whole of it, wrapped to the pane, as the
// state pit's Enter spells out a value. The detail when the message had more
// than a line, else the row's own single line, which the pane clipped.
//
// It used to show the full date and the source instead, which is furniture:
// the row already carries the time and the aria, and what a reader wants from
// a clipped error is the rest of the error.
func (n notification) fullLines(width int) []string {
	body := n.detail
	if body == "" {
		body = n.text
	}
	var out []string
	for _, l := range strings.Split(body, "\n") {
		// Wrapped, never clipped, and stripped at the source as well as at the
		// paint: a provider's refusal can carry anything. The yank is
		// untouched.
		out = append(out, wrapPlain(pitText(strings.TrimRight(l, "\r")), max(width, 20))...)
	}
	if len(out) > notificationLinesMax {
		out = append(out[:notificationLinesMax:notificationLinesMax],
			AndMore(len(out)-notificationLinesMax, "lines · y yanks all of it"))
	}
	return out
}

// notificationLinesMax bounds an opened one. A message is a line or a
// paragraph; anything past this is a log, and the yank is the way to read it.
const notificationLinesMax = 200

// yank is what y copies: the whole of it, with where it came from.
func (n notification) yank() string {
	body := n.text
	if n.detail != "" {
		body = n.detail
	}
	return fmt.Sprintf("%s %s %s: %s", n.last.Format(time.RFC3339), n.level.glyph(), n.source, body)
}

// ---------------------------------------------------------------------------
// The CLI's own slog.
// ---------------------------------------------------------------------------

// notifyHandler tees the pager process's WARN and ERROR records into the
// store. They reached a log file and not the person at the CLI: a freeze dump
// written, templates that failed to load, messages dropped on the way out.
//
// It skips what the store already has. reportAt logs every bar alert as
// "figaro session report=…", and teeing those would say everything twice.
type notifyHandler struct {
	inner slog.Handler
	store *notificationStore
}

func (h notifyHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= slog.LevelWarn || h.inner.Enabled(ctx, l)
}

func (h notifyHandler) Handle(ctx context.Context, rec slog.Record) error {
	if rec.Level >= slog.LevelWarn && !isReportEcho(rec) {
		level := alertWarn
		if rec.Level >= slog.LevelError {
			level = alertError
		}
		text := rec.Message
		rec.Attrs(func(a slog.Attr) bool {
			text += " " + a.Key + "=" + a.Value.String()
			return true
		})
		h.store.post(level, "cli", text)
	}
	if h.inner.Enabled(ctx, rec.Level) {
		return h.inner.Handle(ctx, rec)
	}
	return nil
}

func (h notifyHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return notifyHandler{inner: h.inner.WithAttrs(as), store: h.store}
}

func (h notifyHandler) WithGroup(name string) slog.Handler {
	return notifyHandler{inner: h.inner.WithGroup(name), store: h.store}
}

// isReportEcho is reportAt's own log line, which the store has already.
func isReportEcho(rec slog.Record) bool {
	if rec.Message != "figaro session" {
		return false
	}
	echo := false
	rec.Attrs(func(a slog.Attr) bool {
		if a.Key == "report" {
			echo = true
			return false
		}
		return true
	})
	return echo
}

// teeSlogInto installs the tee for the life of a pager session and returns
// what puts the old handler back.
func teeSlogInto(store *notificationStore) (restore func()) {
	prev := slog.Default()
	slog.SetDefault(slog.New(notifyHandler{inner: prev.Handler(), store: store}))
	return func() { slog.SetDefault(prev) }
}
