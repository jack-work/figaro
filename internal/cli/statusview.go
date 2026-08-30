package cli

// THE STATUS BAR, as a value.
//
// It used to be assembled: statusLine took a lock on a *sessionStatus,
// formatted, shed tokens by rank and returned a string, all in one function
// against the one session a process has. Multiplexing needs N of them — one per
// pane — and a pane's bar must render without owning the session behind it.
//
// So the bar is now a VALUE and a PURE FUNCTION over it. Everything that reads
// a clock, takes a lock or asks a daemon happens where the value is built;
// render() does arithmetic on strings and nothing else, which is what lets it
// be golden-tested at every width. See plans/status-bar-and-modes.md §1.

import (
	"strings"
	"time"

	"github.com/jack-work/figaro/internal/term"
	"github.com/mattn/go-runewidth"
)

// statusView is everything the bar says.
type statusView struct {
	Pit    pitID // what is open (pitid.go); pitNothing in the plain pager
	State  turnStatus
	Tick   uint64 // spinner frame, for the one state that animates
	Aria   string
	Mantra string
	Ctx    string // already formatted: "9.8k/1.0m (1.0%)"
	// Alert is the newest notification, projected into the bar. It is
	// TRANSIENT and its expiry is evaluated by whoever builds this value --
	// never here, or the pure renderer becomes a function of the wall clock
	// and its goldens become flaky.
	Alert      string
	AlertLevel alertLevel
	// LastAt is when the conversation last moved. Verbose only, and absolute:
	// a relative "3m ago" would be true only at the instant it was painted.
	LastAt  time.Time
	Model   string // verbose only
	Hints   string // verbose only: the keys this mode answers
	Verbose bool
}

// lastAtFormat is a full datetime because the question it answers -- is this
// conversation stale? -- outlives a single day, and a bare wall clock is
// ambiguous on a pager left open across midnight.
const lastAtFormat = "01/02/06 15:04:05"

// render draws the bar: ONE row when both halves fit, THREE when they do not.
//
// Three, not two: the narrow form is left group, blank, right group, and the
// blank is in the requirement. It is also the reason this returns rows rather
// than a string — the caller reserves height from the same count, and a bar
// that silently became taller than its reservation would eat a line of the
// conversation.
func (v statusView) render(w int) []string {
	if w <= 0 {
		return nil
	}
	bare, right, at := v.groups()
	r := joinFields(right)
	bareW := displayWidth(joinFields(bare))

	// THE MANTRA TAKES WHATEVER IS LEFT. It used to be cut to 32 runes on a
	// 200-column pane with sixty columns going spare: a fixed cap is a promise
	// about a screen nobody is looking at. It still sheds first, because it is
	// the only field recoverable by looking up.
	l := joinFields(v.withMantra(bare, at, w-bareW-displayWidth(r)-mantraGap))

	if r == "" {
		return []string{clipToWidthEllipsis(l, w)}
	}
	if lw, rw := displayWidth(l), displayWidth(r); lw+rw+2 <= w {
		return []string{l + strings.Repeat(" ", w-lw-rw) + r}
	}
	// Three rows: left, blank, right. The left row no longer shares its width
	// with the right group, so the mantra is measured again against all of it.
	return []string{
		clipToWidthEllipsis(joinFields(v.withMantra(bare, at, w-bareW-mantraGap)), w),
		"",
		clipToWidthEllipsis(r, w),
	}
}

// withMantra puts the mantra back into the left group, clipped to the room it
// was given, and leaves it out when that room says nothing worth having.
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

// mantraGap is what the mantra must leave behind: the " · " that joins it and
// the gutter between the groups. mantraMin is the width below which a mantra
// says nothing worth the room.
const (
	mantraGap = 5
	mantraMin = 8
)

// groups is the split: the mode and the conversation on the left, facts about
// the aria on the right. The MANTRA IS NOT INCLUDED -- render fits it into
// what is left over -- and `at` is where it belongs when it does.
func (v statusView) groups() (left, right []string, at int) {
	// The alert leads, ahead of even the pit: news belongs where the eye lands
	// first, and it is the only field that arrives without being asked for.
	//
	// ONLY TROUBLE IS RED. "sent" is a confirmation and wears the row's own
	// gray; painting every alert red made the bar shout its successes in the
	// colour it reserves for failures, which is how a colour stops meaning
	// anything.
	if v.Alert != "" {
		if v.AlertLevel == alertError {
			left = append(left, term.NoticeInDim(v.Alert))
		} else {
			left = append(left, v.Alert)
		}
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

// stateToken is the state as the bar draws it: the glyph, plus its name under
// verbose. Idle contributes nothing — it is the catch-all, and a row that is
// always visible has nothing to say by saying "nothing is happening".
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

// joinFields is the separator rule, and it is the reason wrapping is done by
// GROUP rather than by column: `·` joins fields ON a row, so a group that
// breaks across rows must not leave one dangling at the break.
func joinFields(fields []string) string { return strings.Join(fields, " · ") }

// ---------------------------------------------------------------------------
// Building the value.
// ---------------------------------------------------------------------------

// viewOf snapshots a session into a statusView. This is where the lock is
// taken and where the wall clock is read; render() does neither.
func (s *sessionStatus) viewOf(pit pitID, verbose bool, now time.Time) statusView {
	if s == nil {
		return statusView{}
	}
	// THE TICKER IS NOT THE ONLY CLOCK. It runs while something animates; an
	// idle pager animates nothing, so an alert posted after the turn ended
	// would have waited for the next keystroke to notice it had expired. Every
	// build of the view checks too -- it is one comparison, it happens exactly
	// where the wall clock is already being read, and it makes "the alert
	// retires" true rather than usually true.
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
	if verbose {
		v.Model = s.model
	}
	return v
}

// barWidthOf reports how tall a bar of this view will be, so a caller that
// reserves rows and a caller that draws them cannot disagree.
func (v statusView) height(w int) int { return len(v.render(w)) }

// ---------------------------------------------------------------------------
// THE FOOTER, AND THERE IS ONLY ONE.
//
// The pager and the incipit each used to build their own: footerRows() for the
// transcript, bookendLines() for the inline stream, both stitching a rule and a
// status row out of the same two helpers in slightly different ways. That is
// how a change lands in one mode and not the other -- and it did: the state
// glyphs, the aria id and the dropped clock reached the pager first and the
// scrollback bookend only because someone remembered to look.
//
// One function now. Both callers pass what differs (the position label, the
// verbosity) and neither owns a single line of layout.
// ---------------------------------------------------------------------------

// footerStanza is the bottom of the screen, in one place: the rule that closes
// the conversation, then the bar. Dimming is applied here too, so "the footer
// is dim" is a fact about the footer rather than a convention every caller
// remembers.
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

// truncRunes is shared with the old renderer.
var _ = runewidth.StringWidth
