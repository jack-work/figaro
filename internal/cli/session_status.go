package cli

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/mattn/go-runewidth"
)

type sessionStatus struct {
	mu        sync.RWMutex
	figaroID  string
	startedAt time.Time
	metrics   aria.Metrics
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

func (s *sessionStatus) footerTokens() []string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	tokens := []string{s.figaroID}
	if mantra := strings.Join(strings.Fields(s.metrics.Mantra), " "); mantra != "" {
		tokens = append(tokens, truncRunes(mantra, 32))
	}
	if context := formatContextUsage(s.metrics.ContextTokens, s.metrics.ContextLimit, s.metrics.ContextExact); context != "-" {
		tokens = append(tokens, "ctx "+context)
	}
	if cost := formatSessionTokenCost(s.metrics.TokensIn, s.metrics.TokensOut); cost != "-" {
		tokens = append(tokens, "cost "+cost)
	}
	tokens = append(tokens, s.startedAt.Format("15:04:05"), "? help")
	return tokens
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

func sessionStatusRule(status *sessionStatus, width int, right string) string {
	tokens := status.footerTokens()
	for len(tokens) > 1 {
		left := "─── " + strings.Join(tokens, " · ") + " "
		if runewidth.StringWidth(left)+runewidth.StringWidth(right)+3 <= width {
			break
		}
		tokens = tokens[:len(tokens)-1]
	}
	left := "─── " + strings.Join(tokens, " · ") + " "
	fill := width - runewidth.StringWidth(left) - runewidth.StringWidth(right)
	if fill < 0 {
		fill = 0
	}
	return clipToWidth(left+strings.Repeat("─", fill)+right, width)
}
