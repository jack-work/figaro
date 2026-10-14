package cli

// The status bar is a value and a pure function over it: clocks, locks and the
// daemon are read where viewOf builds it, never in render.

import (
	"strconv"
	"strings"
	"time"

	"github.com/jack-work/figaro/internal/term"
	"github.com/mattn/go-runewidth"
)

type statusView struct {
	Pit         pitID
	State       turnStatus
	Tick        uint64
	Aria        string
	Mantra      string
	Ctx         string // already formatted
	Alerts      []barAlert
	Unread      int
	UnreadLevel alertLevel
	LastAt      time.Time // verbose only; absolute, since a relative string is true only when painted
	Model       string    // verbose only
	Hints       string    // verbose only
	Verbose     bool
}

type barAlert struct {
	Text  string
	Level alertLevel
}

func (a barAlert) paint(text string) string {
	switch a.Level {
	case alertError:
		return term.NoticeInDim(text)
	case alertWarn:
		return term.CautionInDim(text)
	}
	return text
}

// A full date: a pager left open across midnight makes a bare clock ambiguous.
const lastAtFormat = "01/02/06 15:04:05"

// render returns one row when both groups fit, else left, blank, right; the caller
// reserves height from the same count.
func (v statusView) render(w int) []string {
	if w <= 0 {
		return nil
	}
	rest, right, at := v.groups()
	r := joinFields(right)

	alerts := v.fitAlerts(rest, w, displayWidth(r))
	bare := append(alerts, rest...)
	at += len(alerts)
	bareW := displayWidth(joinFields(bare))

	l := joinFields(v.withMantra(bare, at, w-bareW-displayWidth(r)-mantraGap))

	if r == "" {
		return []string{clipToWidthEllipsis(l, w)}
	}
	if lw, rw := displayWidth(l), displayWidth(r); lw+rw+2 <= w {
		return []string{l + strings.Repeat(" ", w-lw-rw) + r}
	}
	return []string{
		clipToWidthEllipsis(joinFields(v.withMantra(bare, at, w-bareW-mantraGap)), w),
		"",
		clipToWidthEllipsis(r, w),
	}
}

const (
	alertMin      = 24
	olderAlertMin = 12
)

func (v statusView) fitAlerts(rest []string, w, rightW int) []string {
	if len(v.Alerts) == 0 {
		return nil
	}
	sep := displayWidth(" · ")
	room := w - rightW
	if rightW > 0 {
		room -= 2 // the gutter between the groups
	}
	for _, f := range rest {
		room -= displayWidth(f) + sep
	}
	out := make([]string, 0, len(v.Alerts))
	for i, a := range v.Alerts {
		cut := room
		if i > 0 {
			room -= sep
			if room < olderAlertMin {
				break
			}
			cut = room
		} else if cut < alertMin {
			cut = alertMin
		}
		text := a.Text
		if displayWidth(text) > cut {
			text = clipToWidthEllipsis(text, cut)
		}
		room -= displayWidth(text)
		out = append(out, a.paint(text))
	}
	return out
}

func (v statusView) withMantra(left []string, at, room int) []string {
	m := strings.Join(strings.Fields(v.Mantra), " ")
	if m == "" || room < mantraMin {
		return left
	}
	if displayWidth(m) > room {
		m = clipToWidthEllipsis(m, room)
	}
	return append(left[:at:at], append([]string{m}, left[at:]...)...)
}

// mantraGap is the " · " joiner plus the gutter between the groups.
const (
	mantraGap = 5
	mantraMin = 8
)

// groups leaves the mantra out; at is where render puts it.
func (v statusView) groups() (left, right []string, at int) {
	if v.Unread > 0 {
		mark := pitNotifications.face().glyph + " " + strconv.Itoa(v.Unread)
		switch v.UnreadLevel {
		case alertError:
			mark = term.NoticeInDim(mark)
		case alertWarn:
			mark = term.CautionInDim(mark)
		}
		left = append(left, mark)
	}
	if tok := v.Pit.token(v.Verbose); tok != "" {
		left = append(left, tok)
	}
	if st := v.stateToken(); st != "" {
		left = append(left, st)
	}
	if v.Aria != "" {
		left = append(left, v.Aria)
	}
	at = len(left)
	if v.Verbose && v.Model != "" {
		left = append(left, v.Model)
	}
	if v.Verbose && v.Hints != "" {
		left = append(left, v.Hints)
	}

	if v.Ctx != "" {
		right = append(right, v.Ctx)
	}
	if v.Verbose && !v.LastAt.IsZero() {
		right = append(right, v.LastAt.Format(lastAtFormat))
	}
	return left, right, at
}

func (v statusView) stateToken() string {
	if v.State == turnStatusIdle {
		return ""
	}
	sym := v.State.symbol(v.Tick)
	if v.Verbose {
		if name := v.State.name(); name != "" {
			return v.State.paint(name + " " + sym)
		}
	}
	return v.State.paint(sym)
}

// `·` joins fields on a row, which is why wrapping is by group: no joiner may
// dangle at a break.
func joinFields(fields []string) string { return strings.Join(fields, " · ") }

// viewOf takes the lock and reads the clock; render does neither.
func (s *sessionStatus) viewOf(pit pitID, verbose bool, now time.Time) statusView {
	if s == nil {
		return statusView{}
	}
	// The ticker runs only while something animates: an idle pager retires
	// alerts here.
	s.retireAlert(now)
	s.mu.RLock()
	defer s.mu.RUnlock()
	v := statusView{
		Pit:     pit,
		State:   s.turn,
		Tick:    s.tick,
		Aria:    s.figaroID,
		Mantra:  s.metrics.Mantra,
		LastAt:  s.lastAt,
		Verbose: verbose,
	}
	for _, n := range s.notices {
		v.Alerts = append(v.Alerts, barAlert{Text: n.text, Level: n.level})
	}
	if ctx := formatContextUsage(s.metrics.ContextTokens, s.metrics.ContextLimit, s.metrics.ContextExact); ctx != "-" {
		v.Ctx = ctx
	}
	// Lock order is status, then store, everywhere; the store never takes the
	// status's.
	v.Unread, v.UnreadLevel = s.notes.unread()
	if verbose {
		v.Model = s.model
	}
	return v
}

func (v statusView) height(w int) int { return len(v.render(w)) }

// footerStanza is the one footer the pager and the incipit share: the rule, then
// the bar, dimmed here.
func footerStanza(s *sessionStatus, w int, pos string, pit pitID, verbose bool) []string {
	if s == nil || w <= 0 {
		return nil
	}
	rows := []string{term.Dim(s.ruleLine(w, pos))}
	for _, r := range s.viewOf(pit, verbose, time.Now()).render(w) {
		rows = append(rows, term.Dim(r))
	}
	return rows
}

var _ = runewidth.StringWidth
