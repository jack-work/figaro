package cli

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/term"
	"github.com/jack-work/figaro/sdk/aria"
	"github.com/mattn/go-runewidth"
)

type turnStatus uint8

const (
	turnStatusIdle     turnStatus = iota // the catch-all: none of the others is known to hold
	turnStatusSending                    // the only state the client asserts about itself; every other arrives from the daemon
	turnStatusAccepted                   // the daemon has the message and is not yet answering it
	turnStatusThinking
	turnStatusTooling
	turnStatusCompleted
	turnStatusInterrupted // a hup is not an error: gray, not red
	turnStatusError
	turnStatusDisconnected // the turn continues on the daemon; the glyph is static since nothing repaints after a detach
)

// The moving states use disjoint glyph families: a reader tells families apart,
// speeds never.
var (
	sendingFrames  = []rune{'→', '⇢', '⇉', '⇶'}
	acceptedFrames = []rune{'⠁', '⠂', '⠄', '⡀', '⢀', '⠠', '⠐', '⠈'}
)

func (st turnStatus) symbol(tick uint64) string {
	frame := func(fs []rune) string { return string(fs[int(tick)%len(fs)]) }
	switch st {
	case turnStatusSending:
		return frame(sendingFrames)
	case turnStatusAccepted:
		return frame(acceptedFrames)
	case turnStatusThinking, turnStatusTooling:
		frames := livedoc.SpinnerFrames
		return string(frames[int(tick)%len(frames)])
	case turnStatusCompleted:
		return "✓"
	case turnStatusInterrupted:
		return "!"
	case turnStatusError:
		return "✗"
	case turnStatusDisconnected:
		return "⠸"
	}
	return "𝄐"
}

// Thinking has no name: the animation says it.
func (st turnStatus) name() string {
	switch st {
	case turnStatusThinking:
		return ""
	case turnStatusSending:
		return "sending"
	case turnStatusAccepted:
		return "queued"
	case turnStatusTooling:
		return "tools"
	case turnStatusCompleted:
		return "done"
	case turnStatusInterrupted:
		return "hup"
	case turnStatusError:
		return "error"
	case turnStatusDisconnected:
		return "detached"
	}
	return "idle"
}

func (st turnStatus) paint(s string) string {
	switch st {
	case turnStatusError:
		return term.NoticeInDim(s)
	case turnStatusInterrupted:
		return term.StateDim(s)
	}
	return s
}

type sessionStatus struct {
	mu        sync.RWMutex
	figaroID  string
	startedAt time.Time
	metrics   aria.Metrics
	turn      turnStatus
	tick      uint64
	verbose   bool      // the bar's verbosity (^V), not the tool-output toggle (^O)
	lastAt    time.Time // when the conversation last moved, in either direction
	model     string
	noticeTTL time.Duration      // zero: the alert holds the slot until displaced
	lastError string             // does not retire with the alert: a failed turn's closer carries it
	notes     *notificationStore // the transcript's; outlives this status
	notices   []barNotice
}

type barNotice struct {
	text     string
	level    alertLevel
	until    time.Time
	progress bool
}

const maxBarNotices = 2

// A constructor default: one a caller must install is not a default.
const defaultNoticeTTL = 10 * time.Second

func newSessionStatus(figaroID string, startedAt time.Time) *sessionStatus {
	return &sessionStatus{figaroID: figaroID, startedAt: startedAt, noticeTTL: defaultNoticeTTL}
}

type alertLevel uint8

const (
	alertInfo  alertLevel = iota // gray, never unread
	alertWarn                    // yellow
	alertError                   // red
)

func (s *sessionStatus) setNoticeAt(text string, level alertLevel) {
	s.post(text, level, false)
}

func (s *sessionStatus) setProgress(text string) {
	s.post(text, alertInfo, true)
}

func (s *sessionStatus) post(text string, level alertLevel, progress bool) {
	if s == nil {
		return
	}
	text = strings.Join(strings.Fields(text), " ")
	s.mu.Lock()
	defer s.mu.Unlock()
	if text == "" {
		s.notices = nil
		return
	}
	n := barNotice{text: text, level: level, progress: progress}
	if s.noticeTTL > 0 {
		n.until = time.Now().Add(s.noticeTTL)
	}
	if level == alertError {
		s.lastError = text
	}
	kept := []barNotice{n}
	for _, old := range s.notices {
		if old.progress || old.text == text || len(kept) == maxBarNotices {
			continue
		}
		kept = append(kept, old)
	}
	s.notices = kept
}

func (s *sessionStatus) lastTurnFailed() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.turn == turnStatusError
}

func (s *sessionStatus) failure() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastError
}

func (s *sessionStatus) setNotice(text string) { s.post(text, alertInfo, false) }

func (s *sessionStatus) pinNotice(text string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.notices = []barNotice{{text: strings.Join(strings.Fields(text), " "), level: alertInfo}}
	s.mu.Unlock()
}

func (s *sessionStatus) noticeText() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.notices) == 0 {
		return ""
	}
	return s.notices[0].text
}

func (s *sessionStatus) setNoticeTTL(d time.Duration) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.noticeTTL = d
	s.mu.Unlock()
}

func (s *sessionStatus) update(metrics aria.Metrics) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.metrics == metrics {
		return false
	}
	s.metrics = metrics
	return true
}

func (s *sessionStatus) beginTurn() {
	if s == nil {
		return
	}
	s.mu.Lock()
	// beginTurn runs after the submit, so the daemon's state may already have
	// landed and outranks the guess.
	if !s.turn.authoritative() {
		s.turn = turnStatusSending
	}
	s.lastAt = time.Now()
	s.mu.Unlock()
}

func (s *sessionStatus) touch(at time.Time) {
	if s == nil || at.IsZero() {
		return
	}
	s.mu.Lock()
	if at.After(s.lastAt) {
		s.lastAt = at
	}
	s.mu.Unlock()
}

func (s *sessionStatus) setModel(m string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.model = m
	s.mu.Unlock()
}

// finishTurn classifies the daemon's fixed reason vocabulary; client-side outcomes
// go through setTurn.
func (s *sessionStatus) finishTurn(reason string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.lastAt = time.Now()
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

// setRuntime folds a `<aria>/runtime` patch onto the bar and reports whether
// anything shown changed.
func (s *sessionStatus) setRuntime(rt runtimeView) bool {
	if s == nil || !rt.Known {
		return false
	}
	var st turnStatus
	switch rt.State {
	case rpc.RuntimeAccepted, rpc.RuntimeCommitting:
		st = turnStatusAccepted
	case rpc.RuntimeThinking:
		st = turnStatusThinking
	case rpc.RuntimeTooling:
		st = turnStatusTooling
	case rpc.RuntimeIdle:
		// Idle carries the verdict of the turn that ended; the bar shows
		// the verdict.
		if rt.Reason != "" {
			s.finishTurn(rt.Reason)
			s.setModelIfKnown(rt.Model)
			return true
		}
		st = turnStatusIdle
	default:
		return false
	}

	s.mu.Lock()
	changed := s.turn != st
	s.turn = st
	if changed {
		s.lastAt = time.Now()
	}
	if rt.Model != "" && s.model != rt.Model {
		s.model, changed = rt.Model, true
	}
	s.mu.Unlock()
	return changed
}

func (s *sessionStatus) setModelIfKnown(m string) {
	if m != "" {
		s.setModel(m)
	}
}

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
	if !s.turn.moving() {
		return false
	}
	s.tick++
	return true
}

func (st turnStatus) moving() bool {
	switch st {
	case turnStatusSending, turnStatusAccepted, turnStatusThinking, turnStatusTooling:
		return true
	}
	return false
}

// authoritative: the daemon put us here, and a client guess may not overwrite it.
func (st turnStatus) authoritative() bool {
	switch st {
	case turnStatusAccepted, turnStatusThinking, turnStatusTooling:
		return true
	}
	return false
}

// turnRunning counts sending and accepted: Ctrl-C before the first token must
// interrupt, not exit.
func (s *sessionStatus) turnRunning() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.turn.moving()
}

// Caller holds s.mu.
func (s *sessionStatus) turnLabel(verbose bool) string {
	if s.turn == turnStatusIdle {
		return ""
	}
	sym := s.turn.symbol(s.tick)
	if !verbose {
		return s.turn.paint(sym)
	}
	if name := s.turn.name(); name != "" {
		return s.turn.paint(name + " " + sym)
	}
	return s.turn.paint(sym)
}

// ruleLine is undecorated; the caller dims it.
func (s *sessionStatus) ruleLine(width int, pos string) string {
	if pos == "" {
		return clipToWidth(strings.Repeat("─", max(width, 0)), width)
	}
	right := " " + pos
	fill := width - runewidth.StringWidth(right)
	if fill < 3 {
		fill = 3
	}
	return clipToWidth(strings.Repeat("─", fill)+right, width)
}

func (s *sessionStatus) panelLines() []string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	state := s.turnLabel(true)
	if state == "" {
		state = "idle 𝄐"
	}
	rows := []string{
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

// The limit is known before the first turn; the tilde marks an estimate and is
// spent only on a non-zero count.
func formatContextUsage(tokens, limit int, exact bool) string {
	if tokens < 0 {
		tokens = 0
	}
	if tokens == 0 && limit <= 0 {
		return "-"
	}
	used := formatTokenCount(tokens)
	if !exact && tokens > 0 {
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

// bookendLines is the pager's footer with no position label, at the same verbosity.
func bookendLines(status *sessionStatus) []string {
	return footerStanza(status, termWidth(), "", pitNothing, status.barVerbose())
}

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

// retireAlert is the only part of the bar that reads the wall clock; render stays pure.
func (s *sessionStatus) retireAlert(now time.Time) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.notices[:0]
	for _, n := range s.notices {
		if n.until.IsZero() || now.Before(n.until) {
			kept = append(kept, n)
		}
	}
	retired := len(kept) != len(s.notices)
	s.notices = kept
	return retired
}

func (s *sessionStatus) toggleVerbose() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.verbose = !s.verbose
	return s.verbose
}

func (s *sessionStatus) setVerbose(v bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.verbose = v
	s.mu.Unlock()
}

func (s *sessionStatus) barVerbose() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.verbose
}

func (s *sessionStatus) turnLabelForTest() turnStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.turn
}
