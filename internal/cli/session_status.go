package cli

import (
	"fmt"
	"math"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/models"
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

func (st turnStatus) symbol(tick uint64) string {
	if st.moving() {
		frames := livedoc.SpinnerFrames
		return string(frames[int(tick)%len(frames)])
	}
	switch st {
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
		return term.StateInDim(s)
	}
	return s
}

type sessionStatus struct {
	mu        sync.RWMutex
	figaroID  string
	startedAt time.Time
	metrics   aria.Metrics
	limit     int
	plan      *models.PlanUsage
	turn      turnStatus
	tick      uint64
	more      bool
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
	text = noticeSafe(text)
	s.mu.Lock()
	defer s.mu.Unlock()
	if text == "" {
		s.notices = nil
		return
	}
	n := barNotice{text: text, level: level, progress: progress}
	if s.noticeTTL > 0 && !progress {
		n.until = clk.Now().Add(s.noticeTTL)
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

func (s *sessionStatus) withdrawNotice(text string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.notices[:0]
	for _, n := range s.notices {
		if n.text != text {
			kept = append(kept, n)
		}
	}
	s.notices = kept
}

func (s *sessionStatus) retireProgress() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.notices[:0]
	for _, n := range s.notices {
		if !n.progress {
			kept = append(kept, n)
		}
	}
	s.notices = kept
}

func (s *sessionStatus) pinNotice(text string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.notices = []barNotice{{text: noticeSafe(text), level: alertInfo}}
	s.mu.Unlock()
}

func noticeSafe(s string) string { return pitText(strings.Join(strings.Fields(s), " ")) }

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

func (s *sessionStatus) setLimit(limit int) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.limit == limit {
		return false
	}
	s.limit = limit
	return true
}

func (s *sessionStatus) setPlanUsage(u *models.PlanUsage) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if reflect.DeepEqual(s.plan, u) {
		return false
	}
	s.plan = u
	return true
}

func (s *sessionStatus) update(metrics aria.Metrics) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.turn.moving() && !metrics.ContextExact && metrics.ContextTokens < s.metrics.ContextTokens {
		metrics.ContextTokens, metrics.ContextExact = s.metrics.ContextTokens, s.metrics.ContextExact
	}
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
	s.lastAt = sessionNow()
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
	s.lastAt = sessionNow()
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
		if rt.Reason != "" {
			s.post(rt.Reason, alertWarn, false)
		}
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
		s.lastAt = sessionNow()
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

// ruleLine is undecorated; the caller dims it. Resting is single, so a caller
// with no chrome state of its own gets the default weight.
func (s *sessionStatus) ruleLine(width int, label, pos string) string {
	return s.ruleLineOf(width, label, pos, ruleNeverDouble)
}

func ruleAllDouble(int) bool { return true }

func ruleNeverDouble(int) bool { return false }

func ruleCell(double func(int) bool, i int) string {
	if double(i) {
		return ruleDouble
	}
	return ruleSingle
}

func (s *sessionStatus) ruleLineOf(width int, label, pos string, double func(int) bool) string {
	left, right := "", ""
	if label != "" {
		left = ruleCell(double, 0) + ruleCell(double, 1) + " " + label + " "
	}
	if pos != "" {
		right = " " + pos
	}
	fill := width - runewidth.StringWidth(left) - runewidth.StringWidth(right)
	if fill < 3 && left != "" {
		left, fill = "", width-runewidth.StringWidth(right)
	}
	if fill < 3 {
		fill = 3
	}
	var b strings.Builder
	b.WriteString(left)
	from := runewidth.StringWidth(left)
	for i := range fill {
		b.WriteString(ruleCell(double, from+i))
	}
	b.WriteString(right)
	return clipToWidth(b.String(), width)
}

type panelRow struct {
	text string
	tone pitTone
}

func (s *sessionStatus) panelLines() []string {
	rows := s.panelRows()
	if rows == nil {
		return nil
	}
	lines := make([]string, len(rows))
	for i, r := range rows {
		lines[i] = r.text
		if r.tone == toneWarn {
			lines[i] = term.CautionInDim(r.text)
		}
	}
	return lines
}

func (s *sessionStatus) panelRows() []panelRow {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	state := s.turnLabel(true)
	if state == "" {
		state = "idle 𝄐"
	}
	rows := []panelRow{
		{text: "  aria      " + ariaName(s.figaroID) + "  " + term.Dim(s.figaroID)},
		{text: "  status    " + state},
	}
	if mantra := strings.Join(strings.Fields(s.metrics.Mantra), " "); mantra != "" {
		rows = append(rows, panelRow{text: "  mantra    " + mantra})
	}
	if context := formatContextUsage(s.metrics.ContextTokens, s.limit, s.metrics.ContextExact); context != "-" {
		rows = append(rows, panelRow{text: "  context   " + context})
	}
	for i, line := range planUsageLines(s.plan, sessionNow()) {
		label := "            "
		if i == 0 {
			label = "  usage     "
		}
		rows = append(rows, panelRow{text: label + line.text, tone: line.tone})
	}
	rows = append(rows,
		panelRow{text: fmt.Sprintf("  tokens    in %s · out %s", formatTokenCount(s.metrics.TokensIn), formatTokenCount(s.metrics.TokensOut))},
		panelRow{text: fmt.Sprintf("  cache     read %s · write %s", formatTokenCount(s.metrics.CacheReadTokens), formatTokenCount(s.metrics.CacheWriteTokens))},
		panelRow{text: "  started   " + s.startedAt.Format("15:04:05")},
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
		return used + " (capacity unknown)"
	}
	return fmt.Sprintf("%s/%s %.1f%%", used, formatTokenCount(limit), float64(tokens)*100/float64(limit))
}

func planUsageLines(u *models.PlanUsage, now time.Time) []panelRow {
	if u == nil {
		return nil
	}
	var lines []panelRow
	for _, w := range u.Windows {
		if line, ok := planWindowLine(w, now); ok {
			lines = append(lines, line)
		}
	}
	return lines
}

func planWindowLine(w models.PlanWindow, now time.Time) (panelRow, bool) {
	if w.Window == "" {
		return panelRow{}, false
	}
	var b strings.Builder
	b.WriteString(planWindowName(w.Window))
	switch {
	case w.Status == models.PlanRejected:
		b.WriteString(" rejected")
	case w.UsedPercent != nil && w.Status == models.PlanAllowedWarning && *w.UsedPercent >= 99.5 && *w.UsedPercent < 100:
		fmt.Fprintf(&b, " %.1f%%", math.Floor(*w.UsedPercent*10)/10)
	case w.UsedPercent != nil:
		fmt.Fprintf(&b, " %.0f%%", *w.UsedPercent)
	default:
		return panelRow{}, false
	}
	if w.ResetAt.After(now) {
		b.WriteString(" (resets " + planResetClock(w.ResetAt.In(now.Location()), now) + ")")
	}
	if w.Stale {
		b.WriteString(" (stale)")
	}
	row := panelRow{text: b.String()}
	if w.Status == models.PlanAllowedWarning {
		row.tone = toneWarn
	}
	return row, true
}

func planWindowName(w string) string {
	switch w {
	case "five_hour":
		return "5h"
	case "seven_day":
		return "7d"
	}
	return w
}

func planResetClock(at, now time.Time) string {
	if at.Sub(now) < 24*time.Hour {
		return at.Format("15:04")
	}
	return at.Format("Mon 15:04")
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
	return footerStanza(status, termWidth(), "", pitNothing, status.barVerbose(), ruleNeverDouble)
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

func (s *sessionStatus) setMore(v bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.more = v
	s.mu.Unlock()
}

func (s *sessionStatus) barVerbose() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.more
}

func (s *sessionStatus) turnLabelForTest() turnStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.turn
}
