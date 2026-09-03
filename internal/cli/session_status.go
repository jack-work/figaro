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

// turnStatus is what the last turn DID, and it carries three things a status
// row needs separately: a symbol, a name, and a colour.
//
// They used to be one fused string ("completed ✓", "interrupted !"), which is
// why the bar could not be succinct: there was no way to ask for the symbol
// without the word. The default bar shows symbols alone and verbose adds the
// names, so the two are now different questions with different answers.
// See plans/status-bar-and-modes.md §3.
type turnStatus uint8

const (
	// turnStatusIdle is THE CATCH-ALL, and it is defined as such rather than
	// as a condition: it is what the bar says when none of the others is
	// known to hold. Everything with a claim of its own has a state below.
	turnStatusIdle turnStatus = iota
	turnStatusThinking
	turnStatusCompleted
	// turnStatusInterrupted is a HUP, and a hup is not an error: the user
	// stopped the turn on purpose. Hence gray, not red -- the palette is the
	// only thing on the row that says "this was meant".
	turnStatusInterrupted
	turnStatusError
	// turnStatusDisconnected: this CLI stopped watching while the turn was
	// still running. It is NOT a failure: the user chose to detach and the
	// turn continues on the daemon, which is exactly when the
	// "follow: figaro listen" hint applies.
	//
	// ITS ANIMATION WAS THE DEFECT, NOT THE FACT. This state used to draw a
	// spinner frame, and nothing repaints after a detach -- a still picture of
	// a turn that is still moving. A draft of the plan proposed deleting the
	// state; the fact is worth keeping and the movement is not, so the glyph
	// is static and the state survives.
	turnStatusDisconnected
)

// symbol is the state in one glyph. tick animates the only state that moves.
func (st turnStatus) symbol(tick uint64) string {
	switch st {
	case turnStatusThinking:
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

// name is the word beside the symbol under verbose. THINKING HAS NONE, by
// requirement: the animation says it, and a word beside a moving glyph reads
// as a label on a machine that is already talking.
func (st turnStatus) name() string {
	switch st {
	case turnStatusThinking:
		return ""
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

// paint colours a rendered state. Only two states have a colour: trouble is
// red and a deliberate stop is gray, and everything else inherits the row.
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
	// verbose is the BAR's verbosity (^V), not the tool-output toggle (^O).
	// Seeded from config; see plans/status-bar-and-modes.md §5.
	verbose bool
	// lastAt is when this conversation last MOVED, in either direction --
	// not when the session started, and not when the user last typed. An
	// agent working alone for an hour is a conversation that is moving, and a
	// bar that called that stale would be wrong in the case you are checking.
	lastAt time.Time
	// model is shown under verbose only.
	model string
	// noticeLevel is what KIND of news the alert is. "sent" and "showing
	// abc12345" are confirmations and wear the row's own gray; only trouble is
	// red. Painting every alert red made the bar cry wolf on its own success
	// messages -- and it is the same severity the notification ring will sort
	// by, so it is one field rather than two ideas.
	noticeLevel alertLevel
	// noticeUntil is when the bar's alert retires; see retireAlert.
	noticeUntil time.Time
	// noticeTTL is how long a posted alert holds the slot. Zero: forever.
	noticeTTL time.Duration
	// notice is trouble the user must see, an error reason, an interrupt
	// notice: carried IN the frame buffer instead of being written straight
	// to the terminal. While the pager is up there is no scrollback to write
	// to, the cursor sits on the status row, and a raw write scrolls the grid
	// out from under the painter (see transcript.screenMoved). So it lives
	// here, is painted red at the LEFT of the status row, and sheds last.
	notice string
}

// defaultNoticeTTL is how long an alert holds the bar's first slot. It is a
// CONSTRUCTOR DEFAULT, not a config-only value, and that distinction is the
// whole of a bug I shipped: setNoticeTTL existed, config.NoticeTTL existed,
// and nothing called either -- so every alert was minted with a zero TTL,
// which means "hold the slot until something displaces you", which means an
// error sat in the bar until the next one arrived. Forever, in practice.
//
// A default that has to be installed by a caller is not a default.
const defaultNoticeTTL = 10 * time.Second

func newSessionStatus(figaroID string, startedAt time.Time) *sessionStatus {
	return &sessionStatus{figaroID: figaroID, startedAt: startedAt, noticeTTL: defaultNoticeTTL}
}

// setNotice publishes (or clears, with "") the bar's alert: the newest thing
// that happened, in the first left-hand slot. Newlines are folded to spaces --
// the slot is one field on one row, and the full text is reprinted to the
// shell by leaveTranscript, so nothing is lost by flattening it here.
//
// IT RETIRES ON ITS OWN. An alert holds the slot for noticeTTL and then goes,
// or a newer one displaces it, whichever is first. Nothing waits to be
// dismissed: a bar item that must be acknowledged is a bar item that is still
// there tomorrow. Retirement happens on the ticker (retireAlert), because a
// clock cannot fire inside a pure renderer.
// alertLevel sorts news by whether it is trouble.
type alertLevel uint8

const (
	alertInfo  alertLevel = iota // a confirmation: gray, like the rest of the row
	alertError                   // trouble: red, and eventually a notification
)

// setNoticeAt posts an alert with its level. setNotice is the confirmation
// case, which is the common one.
func (s *sessionStatus) setNoticeAt(text string, level alertLevel) {
	s.setNotice(text)
	if s == nil {
		return
	}
	s.mu.Lock()
	s.noticeLevel = level
	s.mu.Unlock()
}

func (s *sessionStatus) setNotice(text string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.notice = strings.Join(strings.Fields(text), " ")
	s.noticeLevel = alertInfo
	switch {
	case s.notice == "":
		s.noticeUntil = time.Time{}
	case s.noticeTTL > 0:
		s.noticeUntil = time.Now().Add(s.noticeTTL)
	default:
		s.noticeUntil = time.Time{}
	}
	s.mu.Unlock()
}

// setNoticeTTL configures how long an alert holds the slot.
func (s *sessionStatus) setNoticeTTL(d time.Duration) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.noticeTTL = d
	s.mu.Unlock()
}

// update takes a metrics snapshot and reports whether it CHANGED anything the
// bar shows. The pager polls for these, so "no news" must cost no paint.
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
	s.turn = turnStatusThinking
	s.lastAt = time.Now()
	s.mu.Unlock()
}

// touch records that the conversation MOVED. Called on every frame that
// carries content, in either direction: what the bar answers with it is "is
// this stale", and a turn that is producing output is not stale even though
// nobody has typed for an hour.
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

// setModel records the model for the verbose bar.
func (s *sessionStatus) setModel(m string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.model = m
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

// turnRunning is whether a turn is in flight: what Ctrl-C needs to know, and
// the difference between exit 130 and a clean close.
func (s *sessionStatus) turnRunning() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.turn == turnStatusThinking
}

// turnLabel is the current turn state as it goes on the status row: the symbol
// alone, or "name symbol" under verbose. "" when idle, because the catch-all
// has nothing to announce on a row that is always visible.
//
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

// ruleLine is the upper of the two footer rows: a full-width rule with the
// identity right-aligned: "─────…── aria <id>[ · <pos>] ───". Undecorated
// (the caller dims it).
// ruleLine closes the conversation, and carries the one thing the bar does
// not: where in it this window sits. The position ENDS the row -- a decorative
// cap left it three cells short of the figure below it.
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

// THE OLD ASSEMBLED ROW IS GONE. statusLine()/statusLineVerbose() built the
// bar by hand -- token list, rank ladder, ellipsis -- and after statusview.go
// landed they were dead in production and alive only in tests. Two renderers,
// one of them asserted against: that is how the pager and the incipit came to
// disagree in the first place. The bar is statusView.render and nothing else.

// panelLines is the '!' status panel: the figaro-status detail rendered from
// the live metrics snapshot, shown above the footer while output streams.
func (s *sessionStatus) panelLines() []string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	// THE PANEL IS ALWAYS VERBOSE: it is the place a reader goes to read words,
	// and "✓" in a labelled column would be the bar's answer to a question the
	// panel was opened to ask properly.
	state := s.turnLabel(true)
	if state == "" {
		state = "idle 𝄐"
	}
	// NO LEADING BLANK. The panel used to open with an empty row, which read as
	// an extra newline under the rule above it.
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

// formatContextUsage is the capacity figure: what is in the window, out of
// what the window holds, and the quotient.
//
// THE LIMIT DOES NOT WAIT FOR A TRANSCRIPT. It is a provider+model lookup --
// the daemon reports it on an aria that has never taken a turn -- so a session
// that has said nothing yet still knows it has a megabyte, and saying "0/1.0m
// 0.0%" is both true and the thing a reader opened the bar to see. Hiding it
// until the first turn made the figure look like it came and went.
//
// The tilde marks an ESTIMATE, so it is spent only on a number there is
// something to estimate: at zero tokens there is not.
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

// bookendLines is the incipit closer / live-region bookend. It is THE SAME
// footer the pager draws -- footerStanza -- with no position label, because a
// stream that is not paging has no window to report.
//
// THE INCIPIT'S BAR IS THE PAGER'S BAR. It reads the same verbosity from the
// same session, because "one canonical implementation" is not satisfied by one
// FUNCTION with two callers that pass different arguments -- this caller used
// to force verbose on the theory that scrollback cannot be asked, and the
// result was an inline stream showing detail the pager had just been told to
// stop showing. If `m` is off, it is off everywhere.
func bookendLines(status *sessionStatus) []string {
	return footerStanza(status, termWidth(), "", pitNothing, status.barVerbose())
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

// setNotice is gone; post() is how trouble reaches the bar. See notify().
//
// noticeUntil is when the current alert retires. Zero means it stays until
// something displaces it, which is what a TTL of 0 configures.
//
// retireAlert clears an expired alert and reports whether it did, so the
// ticker knows whether this frame needs a repaint. It is the ONLY thing in the
// bar that is a function of the wall clock, and it deliberately lives here
// rather than in statusView.render, which must stay pure.
func (s *sessionStatus) retireAlert(now time.Time) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.notice == "" || s.noticeUntil.IsZero() || now.Before(s.noticeUntil) {
		return false
	}
	s.notice, s.noticeUntil = "", time.Time{}
	return true
}

// toggleVerbose flips the bar's verbosity and reports the new value.
func (s *sessionStatus) toggleVerbose() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.verbose = !s.verbose
	return s.verbose
}

// setVerbose seeds it from config.
func (s *sessionStatus) setVerbose(v bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.verbose = v
	s.mu.Unlock()
}

// barVerbose reports the BAR's verbosity under the lock. It is a method rather
// than a field read because the render path asks for it on every frame, and a
// racy read of a bool is still a race.
func (s *sessionStatus) barVerbose() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.verbose
}
