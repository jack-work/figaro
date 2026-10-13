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
	Pit    pitID
	State  turnStatus
	Tick   uint64
	Aria   string
	Mantra string
	Ctx    string // already formatted
	// Alert's expiry is evaluated by whoever builds the value, never here.
	Alert       string
	AlertLevel  alertLevel
	Unread      int
	UnreadLevel alertLevel
	LastAt      time.Time // verbose only; absolute, since a relative string is true only when painted
	Model       string    // verbose only
	Hints       string    // verbose only
	Verbose     bool
}

// A full date: a pager left open across midnight makes a bare clock ambiguous.
const lastAtFormat = "01/02/06 15:04:05"

// render returns one row when both groups fit, else left, blank, right; the caller
// reserves height from the same count.
func (v statusView) render(w int) []string {
	if w <= 0 {
		return nil
	}
	bare, right, at := v.groups()
	r := joinFields(right)

	// The alert clips itself, never its neighbours; the mantra takes what is
	// left and sheds first.
	v.clipAlert(bare, w, displayWidth(r))
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

// Below alertMin the alert is not cut further; the row splits in three instead.
const alertMin = 24

// clipAlert cuts bare[0] in place; the paint is reapplied after the cut.
func (v statusView) clipAlert(bare []string, w, rightW int) {
	if v.Alert == "" || len(bare) == 0 {
		return
	}
	others := 0
	for _, f := range bare[1:] {
		others += displayWidth(f) + displayWidth(" · ")
	}
	room := w - others - rightW
	if rightW > 0 {
		room -= 2 // the gutter between the groups
	}
	if room < alertMin {
		room = alertMin
	}
	if displayWidth(v.Alert) <= room {
		return
	}
	cut := clipToWidthEllipsis(v.Alert, room)
	switch v.AlertLevel {
	case alertError:
		cut = term.NoticeInDim(cut)
	case alertWarn:
		cut = term.CautionInDim(cut)
	}
	bare[0] = cut
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
	// Only trouble is painted; a confirmation wears the row's own gray.
	if v.Alert != "" {
		switch v.AlertLevel {
		case alertError:
			left = append(left, term.NoticeInDim(v.Alert))
		case alertWarn:
			left = append(left, term.CautionInDim(v.Alert))
		default:
			left = append(left, v.Alert)
		}
	}
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
		Pit:        pit,
		State:      s.turn,
		Tick:       s.tick,
		Aria:       s.figaroID,
		Mantra:     s.metrics.Mantra,
		Alert:      s.notice,
		AlertLevel: s.noticeLevel,
		LastAt:     s.lastAt,
		Verbose:    verbose,
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
