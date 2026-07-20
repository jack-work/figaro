package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldmouse "github.com/jack-work/figaro/internal/livelog/render/mouse"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/term"
	"github.com/jack-work/figaro/internal/transport"
	"github.com/mattn/go-runewidth"
)

const spinnerFPS = 11 // spinner frames per second (~90ms/frame)

// recentCursor is a beyond-the-end LT: ReadBefore(recentCursor, N) returns the
// newest N committed messages — the pager's initial (lazy) window.
const recentCursor = 1 << 60

// Terminal control: disable auto-margin (so a full-width row never wraps) and
// hide the cursor while the renderer owns the screen.
const (
	autowrapOff = "\x1b[?7l"
	autowrapOn  = "\x1b[?7h"
	cursorHide  = "\x1b[?25l"
	cursorShow  = "\x1b[?25h"
)

// mustPromptFigaro is the interactive (TTY) prompt path. It renders the
// aria-read wire through the incipit-seal renderer: closed messages seal to
// native scrollback once and are never redrawn; only the open message is a live
// region, so a terminal resize repaints just that bounded part. The renderer
// folds each aria frame and animates spinners locally (no extra wire traffic).
func mustPromptFigaro(ctx context.Context, ep transport.Endpoint, figaroID, prompt string, loaded *config.Loaded, set renderSettings) {
	ctx, span := figOtel.Start(ctx, "cli.prompt")
	defer span.End()

	startedAt := time.Now()
	listen := set.listen // Ctrl-L / --listen: stay open past turn-done
	status := newSessionStatus(figaroID, startedAt)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	width := term.Width()
	if width <= 0 {
		width = 80
	}
	height := term.Height()

	// Bookend: a status rule (aria id + start time) pinned just below the
	// agent's reply. Gated on the status-line config.
	var bookendFn func() []string
	if loaded.StatusLine() {
		bookendFn = func() []string { return bookendLines(status) }
	}

	lt := newLivelogTurn(os.Stdout, width, height, &set, figaroID, startedAt, status, bookendFn, dimRule)
	tc := term.NewClient() // platform terminal boundary: raw mode, resize, clipboard

	// The renderer owns the cursor and assumes one row per line: disable the
	// terminal's auto-margin so a full-width row never wraps, and hide the
	// cursor. It draws in incipit (no alternate screen) — sealed output lands in
	// the normal scrollback.
	fmt.Fprint(os.Stdout, autowrapOff+cursorHide)
	defer fmt.Fprint(os.Stdout, cursorShow+autowrapOn)
	defer lt.leaveTranscript() // restore the screen if we exit while in the pager

	// Static opening rule: a single dim horizontal line separating the user's
	// shell prompt from the response stream. Printed once, lives in scrollback.
	fmt.Fprintln(os.Stdout, dimRule())

	// The renderer is single-threaded; the notify pump, the spinner ticker, the
	// SIGWINCH handler, and keybindings all serialize on mu.
	var mu sync.Mutex
	doneCh := make(chan struct{}, 1)
	disconnectCh := make(chan struct{}, 1) // Ctrl-D: leave the turn running
	running := true                        // a turn is in flight until turn.done; gates Ctrl-C
	sendCursor := -1                       // cursor from Qua; stop only once committed past it and idle

	onNotify := func(method string, params json.RawMessage) {
		mu.Lock()
		defer mu.Unlock()
		switch method {
		case rpc.MethodAriaFrame:
			var r aria.AriaRead
			if json.Unmarshal(params, &r) == nil {
				lt.apply(r)
			}
		case rpc.MethodTurnDone:
			var d rpc.DoneEntry
			_ = json.Unmarshal(params, &d)
			isErr := strings.HasPrefix(d.Reason, "error:")
			if isErr {
				if strings.Contains(d.Reason, "no credential") || strings.Contains(d.Reason, "resolve token") {
					fmt.Fprint(os.Stderr, "\n"+providerSetupHint())
				} else {
					fmt.Fprintln(os.Stderr, "\n"+d.Reason)
				}
			}
			// Settle when the agent reports idle (inbox empty, no turn running):
			// a turn that ended with our steer still queued reports idle=false,
			// so we correctly wait for our own turn. A daemon predating the idle
			// field sends nil — treat that as settled (the pre-steering behavior),
			// so an old running daemon doesn't strand the command. We only act
			// once our prompt has been submitted (sendCursor set after Qua
			// returns), so a turn.done that predates our send can't end us early.
			// Do NOT gate on lt.cursor() advancing — the final commit can arrive
			// via async desync recovery AFTER this one-shot turn.done, which
			// would strand us and hang the command.
			idle := d.Idle == nil || *d.Idle
			lt.finishTurn(d.Reason)
			settled := sendCursor >= 0 && idle
			if !settled {
				break
			}
			running = false
			// Close on turn-done — in incipit OR transcript — UNLESS listening
			// (Ctrl-L / --listen), which keeps the session open until Ctrl-D/C.
			if !listen {
				select {
				case doneCh <- struct{}{}:
				default:
				}
			}
		}
	}

	fcli, err := figaro.DialClient(ep, onNotify)
	if err != nil {
		die("connect figaro: %s", err)
	}
	defer fcli.Close()

	// On a version desync, re-read from the last fully-committed LT and re-apply
	// the full snapshot (off the notify path so the pump isn't blocked).
	lt.setDesync(func(sinceLT int) {
		go func() {
			rctx, rcancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer rcancel()
			r, rerr := fcli.Read(rctx, sinceLT)
			if rerr != nil {
				return
			}
			mu.Lock()
			lt.apply(r)
			mu.Unlock()
		}()
	})

	// Local spinner animation: ticks the open message's running tool; zero extra
	// wire traffic (output streams via aria frames).
	stopTick := make(chan struct{})
	go func() {
		t := time.NewTicker(time.Second / spinnerFPS)
		defer t.Stop()
		for {
			select {
			case <-stopTick:
				return
			case <-t.C:
				mu.Lock()
				lt.tick()
				mu.Unlock()
			}
		}
	}()
	defer close(stopTick)

	// Repaint on resize (platform-abstracted: SIGWINCH on unix, a console event
	// on Windows — all behind the term.Client boundary).
	defer tc.OnResize(func(w, h int) {
		mu.Lock()
		lt.resize(w, h)
		mu.Unlock()
	})()

	// Live keybindings. MakeRaw disables signal generation, so Ctrl-C (0x03) and
	// Ctrl-D (0x04) arrive as input BYTES (portable, and identical in incipit and
	// transcript) — the input loop owns them, not a SIGINT handler.
	if tc.IsTTY() {
		if restore, err := tc.MakeRaw(); err == nil {
			defer restore()
			fmt.Fprint(os.Stdout, enableModifiedKeyReporting)
			defer fmt.Fprint(os.Stdout, disableModifiedKeyReporting)
			// Belt-and-braces: always disable mouse reporting on exit so a crash
			// mid-pager can't leave the shell spewing raw \x1b[<…M.
			defer os.Stdout.WriteString(ldmouse.Disable)
			in := &interactiveInput{
				tc: tc, lt: lt, fcli: fcli, mu: &mu, set: &set,
				figaroID: figaroID, listen: &listen, cancel: cancel, disconnectCh: disconnectCh,
			}
			if listen {
				in.enterTranscript() // --listen: open the pager immediately
			}
			go in.run()
		}
	}

	cursor, qerr := fcli.Qua(ctx, prompt, buildPromptChalkboard())
	if qerr != nil {
		die("prompt: %s", qerr)
	}
	mu.Lock()
	sendCursor = cursor
	lt.status.beginTurn()
	mu.Unlock()

	select {
	case <-doneCh:
		// The committed bookend is the final line; nothing more to print.
	case <-disconnectCh:
		lt.abandon("disconnected — turn continues")
		fmt.Fprintln(os.Stderr, "follow: figaro listen "+figaroID)
	case <-fcli.Done():
		lt.abandon("agent disconnected before turn completed")
		os.Exit(1)
	case <-ctx.Done():
		// Ctrl-C: interrupt the in-flight turn; if nothing's running (e.g.
		// listening after turn-done), it's just a clean close.
		mu.Lock()
		wasRunning := running
		mu.Unlock()
		if wasRunning {
			fmt.Fprintln(os.Stderr, "\ninterrupting...")
			intCtx, intCancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = fcli.Interrupt(intCtx)
			intCancel()
			select {
			case <-doneCh:
			case <-fcli.Done():
			case <-time.After(3 * time.Second):
				lt.abandon("interrupted (agent did not respond)")
			}
			fmt.Fprintln(os.Stderr, "interrupted")
		}
	}
}

// interactiveInput is the shared control-key + pager input loop for the live
// TTY commands — send's mustPromptFigaro and listen's tailFigaro. It owns
// Ctrl-C/D/L/T/O and 'y' (copy id), plus the pager's scroll + mouse, so both
// commands behave identically in incipit and transcript.
type interactiveInput struct {
	tc           term.Client
	lt           *livelogTurn
	fcli         transcriptReadClient
	mu           *sync.Mutex
	set          *renderSettings
	figaroID     string
	listen       *bool // Ctrl-L flips it on (stay open past turn-done)
	cancel       context.CancelFunc
	disconnectCh chan struct{}
	copyCancel   context.CancelFunc
	copyGen      uint64
	copyPlan     selectionCopyPlan
	copyFailed   bool
	copyFailedLo selectionPoint
	copyFailedHi selectionPoint
	searchCancel context.CancelFunc
	searchGen    uint64
	searchQuery  string
	searchDone   chan struct{}
}

type transcriptReadClient interface {
	Read(context.Context, int) (aria.AriaRead, error)
	ReadBefore(context.Context, int, int) (aria.AriaRead, error)
}

// enterTranscript opens the pager on the recent window (older history pages in
// on scroll-up); shared by Ctrl-T, Ctrl-L, and listen's auto-enter. No-op when
// already in the pager.
//
// Two reads are needed for a viewer joining mid-turn. ReadBefore pulls the
// recent COMMITTED window (lazy pagination), but it omits the open, in-flight
// message — so Read(recentCursor) fetches just that (it skips all committed and
// returns only the open Live frame as a full-create). Without it, a listener
// that connects while a message is streaming never gets the message's base
// version, so the field-delta frames that follow can't be applied and the live
// message doesn't render until the next turn opens a fresh message. That is the
// "fanout looked broken until I sent again" bug.
func (in *interactiveInput) enterTranscript() {
	in.mu.Lock()
	already := in.lt.transcriptActive()
	in.mu.Unlock()
	if already {
		return
	}
	rctx, rcancel := context.WithTimeout(context.Background(), 5*time.Second)
	r, rerr := in.fcli.ReadBefore(rctx, recentCursor, transcriptPageSize)
	live, lerr := in.fcli.Read(rctx, recentCursor) // just the open in-flight message
	rcancel()
	in.mu.Lock()
	in.lt.enterTranscript()
	if rerr == nil {
		in.lt.apply(r)
	}
	if lerr == nil {
		in.lt.apply(live)
	}
	in.mu.Unlock()
}

func (in *interactiveInput) pageTranscript() {
	in.mu.Lock()
	if query, searching := in.lt.transcriptHistorySearch(); searching {
		if in.searchCancel == nil {
			ctx, cancel := context.WithCancel(context.Background())
			in.searchGen++
			gen := in.searchGen
			done := make(chan struct{})
			in.searchCancel = cancel
			in.searchQuery = query
			in.searchDone = done
			go in.pageTranscriptSearch(ctx, cancel, done, gen, query)
		}
		in.mu.Unlock()
		return
	}
	req, need := in.lt.transcriptPageCursor()
	in.mu.Unlock()
	if !need {
		return
	}

	rctx, rcancel := context.WithTimeout(context.Background(), 5*time.Second)
	messages, err := in.readTranscriptPage(rctx, req)
	rcancel()
	in.mu.Lock()
	defer in.mu.Unlock()
	if err != nil {
		in.lt.transcriptPageFailed()
		return
	}
	in.lt.transcriptApplyPage(req, messages)
}

func (in *interactiveInput) pageTranscriptSearch(ctx context.Context, cancel context.CancelFunc, done chan struct{}, gen uint64, query string) {
	defer close(done)
	defer cancel()
	for {
		in.mu.Lock()
		if !in.searchMatchesLocked(gen, query) {
			in.mu.Unlock()
			return
		}
		req, need := in.lt.transcriptPageCursor()
		if !need {
			in.finishSearchWorkerLocked(gen)
			in.mu.Unlock()
			return
		}
		in.mu.Unlock()

		rctx, rcancel := context.WithTimeout(ctx, 5*time.Second)
		messages, err := in.readTranscriptPage(rctx, req)
		rcancel()

		in.mu.Lock()
		if !in.searchMatchesLocked(gen, query) {
			in.mu.Unlock()
			return
		}
		if err != nil {
			if ctx.Err() == nil {
				in.lt.transcriptPageFailed()
			}
			in.finishSearchWorkerLocked(gen)
			in.mu.Unlock()
			return
		}
		in.lt.transcriptApplyPage(req, messages)
		if _, searching := in.lt.transcriptHistorySearch(); !searching {
			in.finishSearchWorkerLocked(gen)
			in.mu.Unlock()
			return
		}
		in.mu.Unlock()
	}
}

func (in *interactiveInput) readTranscriptPage(ctx context.Context, req transcriptPageRequest) ([]aria.Message, error) {
	if len(req.cached) > 0 {
		return req.cached, nil
	}
	read := func(before, limit int) (aria.AriaRead, error) {
		return in.fcli.ReadBefore(ctx, before, limit)
	}
	var (
		r   aria.AriaRead
		err error
	)
	if req.after != 0 {
		r, err = readNextPage(req.after, req.watermark, transcriptPageSize, read)
	} else {
		limit := transcriptPageSize
		if req.expected.Count != 0 {
			limit = req.expected.Count
		}
		r, err = read(req.before, limit)
	}
	if err != nil {
		return nil, err
	}
	return committedMessages(r.Committed), nil
}

func (in *interactiveInput) searchMatchesLocked(gen uint64, query string) bool {
	current, searching := in.lt.transcriptHistorySearch()
	return gen == in.searchGen && in.searchCancel != nil &&
		in.lt.transcriptActive() && searching &&
		query == in.searchQuery && query == current
}

func (in *interactiveInput) finishSearchWorkerLocked(gen uint64) {
	if gen != in.searchGen {
		return
	}
	in.searchCancel = nil
	in.searchQuery = ""
	in.searchDone = nil
}

func (in *interactiveInput) cancelTranscriptSearchLocked() {
	if in.searchCancel != nil {
		in.searchCancel()
		in.searchCancel = nil
		in.searchQuery = ""
		in.searchDone = nil
		in.searchGen++
	}
	if in.lt.transcriptSearchingHistory() {
		in.lt.transcriptPageFailed()
	}
}

func (in *interactiveInput) cancelTranscriptSearch() {
	in.mu.Lock()
	in.cancelTranscriptSearchLocked()
	in.mu.Unlock()
}

func readNextPage(after, watermark, limit int, read func(int, int) (aria.AriaRead, error)) (aria.AriaRead, error) {
	if after >= watermark || limit <= 0 {
		return aria.AriaRead{}, nil
	}
	high := watermark + 1
	best, err := read(high, limit)
	if err != nil {
		return aria.AriaRead{}, err
	}
	if committedAfter(best.Committed, after) < limit {
		return filterCommittedAfter(best, after), nil
	}
	low := after + 1
	for range 64 {
		if low >= high {
			return filterCommittedAfter(best, after), nil
		}
		mid := low + (high-low)/2
		r, err := read(mid, limit)
		if err != nil {
			return aria.AriaRead{}, err
		}
		if committedAfter(r.Committed, after) >= limit {
			high, best = mid, r
		} else {
			low = mid + 1
		}
	}
	return filterCommittedAfter(best, after), nil
}

func committedAfter(committed []aria.Committed, after int) int {
	n := 0
	for _, m := range committed {
		if m.LT > after {
			n++
		}
	}
	return n
}

func filterCommittedAfter(r aria.AriaRead, after int) aria.AriaRead {
	out := r
	out.Committed = nil
	for _, m := range r.Committed {
		if m.LT > after {
			out.Committed = append(out.Committed, m)
		}
	}
	return out
}

// run reads input until stdin errors, Ctrl-C (cancel), or Ctrl-D (disconnect).
// Call under a MakeRaw session so Ctrl-C/Ctrl-D arrive as bytes.
func (in *interactiveInput) run() {
	defer in.cancelSelectionCopy()
	defer in.cancelTranscriptSearch()
	buf := make([]byte, 64)
	var pending []byte // a mouse/escape sequence split across reads
	for {
		n, err := in.tc.Read(buf)
		if err != nil {
			in.cancel()
			return
		}
		data := append(pending, buf[:n]...)
		pending = nil
		i := 0
		for i < len(data) {
			in.mu.Lock()
			active := in.lt.transcriptActive()
			in.mu.Unlock()
			// A read chunk that ends on a bare ESC is the Esc KEY, not the start
			// of a split escape sequence: terminals write sequences atomically,
			// only the key arrives alone. Without this, Esc stalls in pending
			// until the NEXT keypress flushes it.
			bareEsc := len(data)-i == 1 && data[i] == 0x1b
			if active && !bareEsc {
				if ev, consumed, ok, need := ldmouse.Parse(data[i:]); need {
					pending = append(pending, data[i:]...)
					break
				} else if ok {
					i += consumed
					delta := 0
					switch ev.Button {
					case ldmouse.WheelUp:
						delta = -3
					case ldmouse.WheelDown:
						delta = 3
					}
					if delta != 0 {
						in.mu.Lock()
						in.cancelTranscriptSearchLocked()
						in.lt.transcriptScroll(delta)
						in.mu.Unlock()
						in.pageTranscript()
					}
					continue
				}
			}
			var b byte
			if key, consumed, ok, need := parseModifiedKey(data[i:]); need && !bareEsc {
				pending = append(pending, data[i:]...)
				break
			} else if ok {
				i += consumed
				if key.ctrl && (key.code == 'n' || key.code == 'N' || key.code == 'p' || key.code == 'P') {
					in.enterTranscript()
					in.mu.Lock()
					in.cancelTranscriptSearchLocked()
					delta := 1
					if key.code == 'p' || key.code == 'P' {
						delta = -1
					}
					in.lt.transcriptSelect(delta, key.shift || key.alt)
					in.mu.Unlock()
					in.pageTranscript()
					continue
				}
				var representable bool
				b, representable = key.asByte()
				if !representable {
					continue
				}
			} else {
				b = data[i]
				i++
			}
			if !active && opensTranscriptFor(b) {
				in.enterTranscript()
				in.mu.Lock()
				active = in.lt.transcriptActive()
				in.mu.Unlock()
			}
			// Universal control keys — identical in incipit and transcript.
			switch b {
			case 0x03: // Ctrl-C: interrupt (if running) + close
				if active {
					in.mu.Lock()
					in.cancelTranscriptSearchLocked()
					plan, selected := in.lt.transcriptSelectionPlan()
					if selected && in.copyFailed &&
						in.copyFailedLo == plan.lo && in.copyFailedHi == plan.hi {
						in.copyFailed = false
						in.lt.clearTranscriptSelection()
						in.mu.Unlock()
						in.cancelSelectionCopy()
						in.cancel()
						return
					}
					if selected && in.copyCancel != nil {
						in.copyCancel()
						in.copyCancel = nil
						in.copyGen++
						in.copyFailed = true
						in.copyFailedLo, in.copyFailedHi = in.copyPlan.lo, in.copyPlan.hi
						in.copyPlan = selectionCopyPlan{}
						in.mu.Unlock()
						continue
					}
					if selected {
						copyCtx, copyCancel := context.WithTimeout(context.Background(), 30*time.Second)
						in.copyGen++
						gen := in.copyGen
						in.copyCancel = copyCancel
						in.copyPlan = plan
						in.copyFailed = false
						in.mu.Unlock()
						go in.copySelection(copyCtx, copyCancel, gen, plan)
						continue
					}
					in.mu.Unlock()
				}
				in.cancelTranscriptSearch()
				in.cancelSelectionCopy()
				in.cancel()
				return
			case 0x04: // Ctrl-D: disconnect; the turn keeps running
				in.cancelTranscriptSearch()
				in.cancelSelectionCopy()
				select {
				case in.disconnectCh <- struct{}{}:
				default:
				}
				return
			case 0x0c: // Ctrl-L: listen (stay open past turn-done) + transcript
				in.mu.Lock()
				in.cancelTranscriptSearchLocked()
				*in.listen = true
				in.mu.Unlock()
				in.enterTranscript()
				continue
			case 0x14: // Ctrl-T: enter transcript (no-op if already there)
				in.cancelTranscriptSearch()
				in.enterTranscript()
				continue
			case 0x0f: // Ctrl-O: toggle verbosity
				in.mu.Lock()
				in.cancelTranscriptSearchLocked()
				in.set.verbose = !in.set.verbose
				in.lt.invalidateTranscriptRows()
				in.lt.render()
				in.mu.Unlock()
				continue
			case 'y': // copy the aria id to the clipboard (OSC 52)
				if active && in.lt.transcriptSearching() {
					break // typing into the search box — let it fall to the pager
				}
				in.tc.SetClipboard(in.figaroID)
				continue
			}
			// Remaining keys drive the pager (scroll/search) when active.
			if active {
				in.mu.Lock()
				in.cancelTranscriptSearchLocked()
				in.lt.transcriptKey(b)
				in.mu.Unlock()
				in.pageTranscript()
			}
		}
	}
}

func (in *interactiveInput) copySelection(ctx context.Context, cancel context.CancelFunc, gen uint64, plan selectionCopyPlan) {
	text, err := selectionText(plan, transcriptPageSize, func(before, limit int) (aria.AriaRead, error) {
		return in.fcli.ReadBefore(ctx, before, limit)
	})
	cancel()
	in.mu.Lock()
	if gen != in.copyGen {
		in.mu.Unlock()
		return
	}
	in.copyCancel = nil
	in.copyPlan = selectionCopyPlan{}
	current, active := in.lt.transcriptSelectionPlan()
	if err == nil && active && current.lo == plan.lo && current.hi == plan.hi {
		in.lt.clearTranscriptSelection()
	}
	if err == nil {
		in.copyFailed = false
		in.tc.SetClipboard(text)
	} else {
		in.copyFailed = true
		in.copyFailedLo, in.copyFailedHi = plan.lo, plan.hi
	}
	in.mu.Unlock()
}

func (in *interactiveInput) cancelSelectionCopy() {
	in.mu.Lock()
	if in.copyCancel != nil {
		in.copyCancel()
		in.copyCancel = nil
		in.copyPlan = selectionCopyPlan{}
		in.copyGen++
	}
	in.mu.Unlock()
}

func opensTranscriptFor(b byte) bool {
	switch b {
	case 'j', 'k', 'u', 'd', 'g', 'G', '/', '?', 0x0f, 0x0e, 0x10, 0x0d, 0x0a:
		return true
	default:
		return false
	}
}

// dimRule returns a plain dim full-width horizontal rule — the opening rule and
// the seal after a non-assistant (user/steering) message.
func dimRule() string { return term.Dim(strings.Repeat("─", termWidth())) }

// abandonRule returns a labeled dim rule used when a live region ends without
// a normal seal (crash, disconnect, interrupt-timeout). Shape: "─── [reason] ───..."
func abandonRule(reason string) string {
	return labeledRule("[" + reason + "]")
}

// labeledRule builds "─── <label> ───…" filled with box-drawing dashes to the
// exact viewport width. Widths are DISPLAY columns (runewidth): the dashes and
// "·" are multi-byte, and byte-length math is what made these rules render
// shorter than the plain dimRule.
func labeledRule(label string) string {
	prefix := "─── " + label + " "
	fill := termWidth() - runewidth.StringWidth(prefix)
	if fill < 3 {
		fill = 3
	}
	return term.Dim(prefix + strings.Repeat("─", fill))
}

// termWidth returns the terminal width, defaulting to 80.
func termWidth() int {
	if w := term.Width(); w > 0 {
		return w
	}
	return 80
}
