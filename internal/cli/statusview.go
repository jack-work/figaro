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
	Drawer drawerID // what is open (drawerid.go); drawerNothing in the plain pager
	State  turnStatus
	Tick   uint64 // spinner frame, for the one state that animates
	Aria   string
	Mantra string
	Ctx    string // already formatted: "9.8k/1.0m (1.0%)"
	// Alert is the newest notification, projected into the bar. It is
	// TRANSIENT and its expiry is evaluated by whoever builds this value --
	// never here, or the pure renderer becomes a function of the wall clock
	// and its goldens become flaky.
	Alert string
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
	left, right := v.groups()
	l, r := joinFields(left), joinFields(right)

	if r == "" {
		return []string{clipToWidthEllipsis(l, w)}
	}
	if lw, rw := displayWidth(l), displayWidth(r); lw+rw+2 <= w {
		gap := w - lw - rw
		return []string{l + strings.Repeat(" ", gap) + r}
	}
	// THE MANTRA SHEDS FIRST, and only then does the bar grow a second row.
	// Wrapping is cheaper than losing information, but a three-row bar on a
	// wide-enough pane is worse than a bar without the mantra: the mantra is
	// the one field whose absence costs nothing that is not recoverable by
	// looking at the top of the screen.
	if v.Mantra != "" {
		trimmed := v
		trimmed.Mantra = ""
		if rows := trimmed.render(w); len(rows) == 1 {
			return rows
		}
	}
	return []string{
		clipToWidthEllipsis(l, w),
		"",
		clipToWidthEllipsis(r, w),
	}
}

// groups is the split: what is about the MODE and the conversation on the left,
// what is a fact about the aria on the right.
func (v statusView) groups() (left, right []string) {
	// The alert leads, ahead of even the mode: trouble belongs where the eye
	// lands first, and it is the only field that arrives without being asked
	// for.
	if v.Alert != "" {
		left = append(left, term.NoticeInDim(v.Alert))
	}
	if tok := v.Drawer.token(v.Verbose); tok != "" {
		left = append(left, tok)
	}
	if st := v.stateToken(); st != "" {
		left = append(left, st)
	}
	if v.Aria != "" {
		left = append(left, v.Aria)
	}
	if m := strings.Join(strings.Fields(v.Mantra), " "); m != "" {
		left = append(left, truncRunes(m, 32))
	}
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
	return left, right
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
func (s *sessionStatus) viewOf(drawer drawerID, verbose bool, now time.Time) statusView {
	if s == nil {
		return statusView{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	v := statusView{
		Drawer:  drawer,
		State:   s.turn,
		Tick:    s.tick,
		Aria:    s.figaroID,
		Mantra:  s.metrics.Mantra,
		Alert:   s.notice,
		LastAt:  s.lastAt,
		Verbose: verbose,
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
func footerStanza(s *sessionStatus, w int, pos string, drawer drawerID, verbose bool) []string {
	if s == nil || w <= 0 {
		return nil
	}
	rows := []string{term.Dim(s.ruleLine(w, pos))}
	for _, r := range s.viewOf(drawer, verbose, time.Now()).render(w) {
		rows = append(rows, term.Dim(r))
	}
	return rows
}

// truncRunes is shared with the old renderer.
var _ = runewidth.StringWidth
