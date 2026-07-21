package cli

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
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
)

type sessionStatus struct {
	mu        sync.RWMutex
	figaroID  string
	startedAt time.Time
	metrics   aria.Metrics
	turn      turnStatus
	tick      uint64
}

func newSessionStatus(figaroID string, startedAt time.Time) *sessionStatus {
	return &sessionStatus{figaroID: figaroID, startedAt: startedAt}
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

func (s *sessionStatus) finishTurn(reason string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	reason = strings.ToLower(reason)
	switch {
	case strings.Contains(reason, "interrupt"):
		s.turn = turnStatusInterrupted
	case strings.HasPrefix(reason, "error:"), strings.Contains(reason, "disconnect"):
		s.turn = turnStatusError
	default:
		s.turn = turnStatusCompleted
	}
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
	}
	return ""
}

// ruleLine is the upper of the two footer rows: a full-width rule with the
// identity right-aligned — "─────…── aria <id>[ · <pos>] ───". Undecorated
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

// statusLine is the lower footer row: plain left-aligned text —
// "<mantra> · <turn state> · ctx … · cost … · <time>[ · ? help · ! status]".
// hints adds the key hooks (live pager only; sealed scrollback omits them).
// Narrow panes shed the mantra first, then cost, then ctx, then the time —
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
		tokens = append(tokens, tok{"^/ help", 5})
	}
	s.mu.RUnlock()

	join := func() string {
		parts := make([]string, 0, len(tokens))
		for _, t := range tokens {
			parts = append(parts, t.text)
		}
		return strings.Join(parts, " · ")
	}
	for rank := 0; rank < 4 && runewidth.StringWidth(join()) > width; rank++ {
		kept := tokens[:0]
		for _, t := range tokens {
			if t.rank != rank {
				kept = append(kept, t)
			}
		}
		tokens = kept
	}
	return clipToWidth(join(), width)
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

// bookendLines is the incipit seal / live-region bookend: the same two-row
// footer the transcript pins (rule + status text), minus the key hints —
// they'd be dead text once sealed into scrollback.
func bookendLines(status *sessionStatus) []string {
	w := termWidth()
	return []string{
		term.Dim(status.ruleLine(w, "")),
		term.Dim(status.statusLine(w, false)),
	}
}
