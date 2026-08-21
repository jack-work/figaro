package cli

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/term"
	"github.com/mattn/go-runewidth"
)

type turnStatus uint8

const (
	turnStatusIdle turnStatus = iota
	turnStatusThinking
	turnStatusCompleted
	turnStatusInterrupted
	turnStatusError
	// turnStatusDisconnected: this CLI stopped watching while the turn was
	// still running. It is NOT a failure: the user chose to detach and the
	// turn continues on the daemon, which is exactly when the
	// "follow: figaro listen" hint applies.
	turnStatusDisconnected
)

type sessionStatus struct {
	mu        sync.RWMutex
	figaroID  string
	startedAt time.Time
	metrics   aria.Metrics
	turn      turnStatus
	tick      uint64
	// notice is trouble the user must see, an error reason, an interrupt
	// notice: carried IN the frame buffer instead of being written straight
	// to the terminal. While the pager is up there is no scrollback to write
	// to, the cursor sits on the status row, and a raw write scrolls the grid
	// out from under the painter (see transcript.screenMoved). So it lives
	// here, is painted red at the LEFT of the status row, and sheds last.
	notice string
}

func newSessionStatus(figaroID string, startedAt time.Time) *sessionStatus {
	return &sessionStatus{figaroID: figaroID, startedAt: startedAt}
}

// setNotice publishes (or clears, with "") the red left-hand notice. Newlines
// are folded to spaces: the status row is one physical line, and the full text
// is reprinted to the shell by leaveTranscript, so nothing is lost by
// flattening it here.
func (s *sessionStatus) setNotice(text string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.notice = strings.Join(strings.Fields(text), " ")
	s.mu.Unlock()
}

func (s *sessionStatus) update(metrics aria.Metrics) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.metrics = metrics
	s.mu.Unlock()
}

func (s *sessionStatus) beginTurn() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.turn = turnStatusThinking
	s.mu.Unlock()
}

// finishTurn classifies a reason reported BY THE SERVER on turn.done, whose
// vocabulary is fixed. Client-side outcomes (detach, agent loss) are set
// explicitly with setTurn instead of being inferred from English, a status is
// a fact about the turn, not a substring of a sentence.
func (s *sessionStatus) finishTurn(reason string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	reason = strings.ToLower(reason)
	switch {
	case strings.Contains(reason, "interrupt"):
		s.turn = turnStatusInterrupted
	case strings.HasPrefix(reason, "error:"):
		s.turn = turnStatusError
	default:
		s.turn = turnStatusCompleted
	}
	s.mu.Unlock()
}

// setTurn records an outcome the caller already knows.
func (s *sessionStatus) setTurn(st turnStatus) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.turn = st
	s.mu.Unlock()
}

func (s *sessionStatus) advance() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.turn != turnStatusThinking {
		return false
	}
	s.tick++
	return true
}

// turnLabel is the current turn state as a short token ("thinking ⠧",
// "completed ✓", …), "" when idle. Caller holds s.mu.
func (s *sessionStatus) turnLabel() string {
	switch s.turn {
	case turnStatusThinking:
		frames := livedoc.SpinnerFrames
		return "thinking " + string(frames[int(s.tick)%len(frames)])
	case turnStatusCompleted:
		return "completed ✓"
	case turnStatusInterrupted:
		return "interrupted !"
	case turnStatusError:
		return "error ✗"
	case turnStatusDisconnected:
		// Static, not animated: nothing repaints after we detach, so a
		// spinner frame here is a still picture of a turn that is still
		// moving elsewhere.
		return "disconnected ⠸"
	}
	return ""
}

// ruleLine is the upper of the two footer rows: a full-width rule with the
// identity right-aligned: "─────…── aria <id>[ · <pos>] ───". Undecorated
// (the caller dims it).
func (s *sessionStatus) ruleLine(width int, pos string) string {
	label := "aria " + s.figaroID
	if pos != "" {
		label += " · " + pos
	}
	right := " " + label + " ───"
	fill := width - runewidth.StringWidth(right)
	if fill < 3 {
		fill = 3
	}
	return clipToWidth(strings.Repeat("─", fill)+right, width)
}

// statusLine is the lower footer row: plain left-aligned text -
// "<mantra> · <turn state> · ctx … · cost … · <time>[ · ? help · ! status]".
// hints adds the key hooks (live pager only; frozen scrollback omits them).
// Narrow panes shed the mantra first, then cost, then ctx, then the time -
// the turn state and the hints survive last.
func (s *sessionStatus) statusLine(width int, hints bool) string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	type tok struct {
		text string
		rank int // shed order: lower sheds first (0 = mantra)
	}
	var tokens []tok
	// THE NOTICE IS THE ONE TOKEN THAT MUST NOT BE SHED, and it goes first:
	// trouble belongs where the eye lands, not after the token cost. Rank 6 is
	// above every shed pass, so only the ellipsis can ever shorten it. Red is
	// re-lit against the caller's dim wrapper (22 = not-dim) and handed back
	// dim + default-foreground, so the rest of the row is unchanged.
	if s.notice != "" {
		tokens = append(tokens, tok{"\x1b[22;31m" + s.notice + "\x1b[39;2m", 6})
	}
	if mantra := strings.Join(strings.Fields(s.metrics.Mantra), " "); mantra != "" {
		tokens = append(tokens, tok{truncRunes(mantra, 32), 0})
	}
	if label := s.turnLabel(); label != "" {
		tokens = append(tokens, tok{label, 4})
	}
	if context := formatContextUsage(s.metrics.ContextTokens, s.metrics.ContextLimit, s.metrics.ContextExact); context != "-" {
		tokens = append(tokens, tok{"ctx " + context, 2})
	}
	if cost := formatSessionTokenCost(s.metrics.TokensIn, s.metrics.TokensOut); cost != "-" {
		tokens = append(tokens, tok{"cost " + cost, 1})
	}
	tokens = append(tokens, tok{s.startedAt.Format("15:04:05"), 3})
	if hints {
		tokens = append(tokens, tok{"? help", 5}, tok{"! status", 5})
	}
	s.mu.RUnlock()

	join := func() string {
		parts := make([]string, 0, len(tokens))
		for _, t := range tokens {
			parts = append(parts, t.text)
		}
		return strings.Join(parts, " · ")
	}
	for rank := 0; rank < 4 && displayWidth(join()) > width; rank++ {
		kept := tokens[:0]
		for _, t := range tokens {
			if t.rank != rank {
				kept = append(kept, t)
			}
		}
		tokens = kept
	}
	// Ellipsis, not a hard clip: the status row is read as a sentence, and a
	// bare cut ends mid-word with nothing to say it was cut. One column buys
	// the difference between "cost 4.5k to" and "cost 4.5k…".
	return clipToWidthEllipsis(join(), width)
}

// panelLines is the '!' status panel: the figaro-status detail rendered from
// the live metrics snapshot, shown above the footer while output streams.
func (s *sessionStatus) panelLines() []string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	state := s.turnLabel()
	if state == "" {
		state = "idle"
	}
	rows := []string{
		"",
		"  aria      " + s.figaroID,
		"  status    " + state,
	}
	if mantra := strings.Join(strings.Fields(s.metrics.Mantra), " "); mantra != "" {
		rows = append(rows, "  mantra    "+mantra)
	}
	if context := formatContextUsage(s.metrics.ContextTokens, s.metrics.ContextLimit, s.metrics.ContextExact); context != "-" {
		rows = append(rows, "  context   "+context)
	}
	rows = append(rows,
		fmt.Sprintf("  tokens    in %s · out %s", formatTokenCount(s.metrics.TokensIn), formatTokenCount(s.metrics.TokensOut)),
		fmt.Sprintf("  cache     read %s · write %s", formatTokenCount(s.metrics.CacheReadTokens), formatTokenCount(s.metrics.CacheWriteTokens)),
		"  started   "+s.startedAt.Format("15:04:05"),
	)
	return rows
}

func formatContextUsage(tokens, limit int, exact bool) string {
	if tokens <= 0 {
		return "-"
	}
	used := formatTokenCount(tokens)
	if !exact {
		used = "~" + used
	}
	if limit <= 0 {
		return used
	}
	return fmt.Sprintf("%s/%s %.1f%%", used, formatTokenCount(limit), float64(tokens)*100/float64(limit))
}

func formatSessionTokenCost(tokensIn, tokensOut int) string {
	total := tokensIn + tokensOut
	if total <= 0 {
		return "-"
	}
	return formatTokenCount(total) + " tok"
}

func formatTokenCount(tokens int) string {
	switch {
	case tokens >= 1_000_000:
		return fmt.Sprintf("%.1fm", float64(tokens)/1_000_000)
	case tokens >= 1_000:
		return fmt.Sprintf("%.1fk", float64(tokens)/1_000)
	default:
		return fmt.Sprintf("%d", tokens)
	}
}

// bookendLines is the incipit closer / live-region bookend: the same two-row
// footer the transcript pins (rule + status text), minus the key hints -
// they'd be dead text once frozen into scrollback. A blank row sits between
// the rule and the status text so the status line breathes.
func bookendLines(status *sessionStatus) []string {
	w := termWidth()
	// Two rows only: rule + status. The blank that gives the footer breathing
	// room is painted ABOVE it by compose()'s pre-closer blank, not between the
	// rule and the status text.
	return []string{
		term.Dim(status.ruleLine(w, "")),
		term.Dim(status.statusLine(w, false)),
	}
}

// formatCtxCell renders a context size for the narrow CTX column in `list`:
// "820", "135k", "1.0m". Whole thousands below a million keep the cell short;
// million-scale windows would otherwise read as "1000k".
func formatCtxCell(tokens int) string {
	switch {
	case tokens >= 1_000_000:
		return fmt.Sprintf("%.1fm", float64(tokens)/1_000_000)
	case tokens >= 1_000:
		return fmt.Sprintf("%dk", tokens/1_000)
	default:
		return fmt.Sprintf("%d", tokens)
	}
}
