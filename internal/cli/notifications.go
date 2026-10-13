package cli

// Every alert is posted to the store first; the bar's alert, its unread mark and
// the pit are views of it. The store lives on the transcript, not the session
// status, which a subject switch replaces.

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

const notificationHistory = 500

type notification struct {
	seq    uint64 // order and the pit's row id; bumped when it repeats
	at     time.Time
	last   time.Time
	count  int
	level  alertLevel
	source string // "cli", or the aria it concerns
	text   string // the first line
	detail string // everything, when there was more than a line
	key    string // coalescing key
}

// notificationStore has its own lock: posts arrive from slog on any goroutine and
// must never take the render lock.
type notificationStore struct {
	mu          sync.Mutex
	items       []notification // oldest first
	seq         uint64
	readThrough uint64
}

func newNotificationStore() *notificationStore { return &notificationStore{} }

var digitRun = regexp.MustCompile(`[0-9]+`)

// Digits fold, so "retry 1 of 3" and "retry 2 of 3" are one row.
func coalesceKey(source string, level alertLevel, text string) string {
	return source + "\x00" + strconv.Itoa(int(level)) + "\x00" + digitRun.ReplaceAllString(text, "#")
}

// A repeat moves to the top and is unread again.
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
		n.text, n.detail = line, detail
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

// unread never counts info.
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

func (s *notificationStore) markRead() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.readThrough = s.seq
	s.mu.Unlock()
}

func (s *notificationStore) clear() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.items = nil
	s.readThrough = s.seq
	s.mu.Unlock()
}

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

// Every render marks what is on show as seen.
type notificationsView struct {
	store    *notificationStore
	filter   notificationFilter
	expanded map[string]bool
	closed   bool
}

func (v *notificationsView) Close() { v.closed = true }

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
		// The 2 is the picker's selection column.
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

const notificationIndent = "      "

func (v *notificationsView) Activate(id string) {
	if v.expanded == nil {
		v.expanded = map[string]bool{}
	}
	id = valuePath(id)
	v.expanded[id] = !v.expanded[id]
}

func (v *notificationsView) Key(b byte) bool {
	switch b {
	case 'f':
		v.filter = (v.filter + 1) % 3
		return true
	}
	return false
}

func (v *notificationsView) AriaOf(id string) string {
	id = valuePath(id)
	for _, n := range v.store.newestFirst() {
		if strconv.FormatUint(n.seq, 10) == id {
			if n.source != "" && n.source != "cli" && rpc.ValidateAriaID(n.source) == nil {
				return n.source
			}
			return ""
		}
	}
	return ""
}

// All three glyphs are single-width.
func (l alertLevel) glyph() string {
	switch l {
	case alertError:
		return "✗"
	case alertWarn:
		return "!"
	}
	return "·"
}

func (l alertLevel) tone() pitTone {
	switch l {
	case alertError:
		return toneError
	case alertWarn:
		return toneWarn
	}
	return toneNone
}

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

func (n notification) fullLines(width int) []string {
	body := n.detail
	if body == "" {
		body = n.text
	}
	var out []string
	for _, l := range strings.Split(body, "\n") {
		// Stripped here as well as at the paint: a provider's refusal can
		// carry anything.
		out = append(out, wrapPlain(pitText(strings.TrimRight(l, "\r")), max(width, 20))...)
	}
	if len(out) > notificationLinesMax {
		out = append(out[:notificationLinesMax:notificationLinesMax],
			AndMore(len(out)-notificationLinesMax, "lines · y yanks all of it"))
	}
	return out
}

const notificationLinesMax = 200

func (n notification) yank() string {
	body := n.text
	if n.detail != "" {
		body = n.detail
	}
	return fmt.Sprintf("%s %s %s: %s", n.last.Format(time.RFC3339), n.level.glyph(), n.source, body)
}

// notifyHandler tees WARN and ERROR records into the store, skipping reportAt's
// echo of bar alerts.
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

func teeSlogInto(store *notificationStore) (restore func()) {
	prev := slog.Default()
	slog.SetDefault(slog.New(notifyHandler{inner: prev.Handler(), store: store}))
	return func() { slog.SetDefault(prev) }
}
