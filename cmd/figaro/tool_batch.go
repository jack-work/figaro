package main

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/jack-work/figaro/internal/rpc"
)

// toolBatchState renders a parallel tool batch as a stack of pending
// rows that update in-place as tools complete. Used by mustPromptFigaro
// when the agent emits a stream.tool_batch_start with size > 1.
//
// Layout, top to bottom:
//
//	─── batch (3) ───────────────────────────
//	  ⠹ bash · pwd && date            (1.2 KiB)
//	  ⠼ bash · ls -la                 (340 B)
//	  ✓ bash · uname -a               (1 line, 84 ms)
//	─── ─────────────────────────────────────
//
// Running rows display an animated braille spinner and a live byte
// counter so the user can see parallel execution actually happening.
// On each tool_end we cursor up to the matching row, rewrite it as
// "✓ bash · uname -a (1 line, 84 ms)" or "✗ ...". On the closing
// batch_end we drop down past the footer. If any tool errored, its
// buffered output is dumped under a sub-header before the footer is
// re-anchored.
//
// Coordinate model:
//
//	cursor starts on row 0 (one row below the opening rule).
//	each row index 0..N-1 is one tool's status line.
//	after RenderInitial the cursor is on row N (the footer rule),
//	which is also where Finalize wants it.
//
// We use ANSI CSI codes directly (cursor up = ESC [ n A, erase line
// = ESC [ 2 K, carriage return for column 0). This works on every
// VT100-compatible terminal we care about.
//
// Concurrency:
//
//	Methods are called from two goroutines: the RPC reader (Mark*,
//	AppendOutput) and the spinner ticker (refresh). All state and
//	all writes to out are protected by mu.
type toolBatchState struct {
	out  io.Writer
	rows []*toolRow
	// rowIndex maps tool_call_id → index into rows.
	rowIndex map[string]int
	// startedAt records when the batch began (for total elapsed
	// reporting if we ever want it).
	startedAt time.Time

	mu        sync.Mutex
	cursorRow int // current row index of the cursor relative to row 0
	closed    bool

	// spinner animation
	tickStop chan struct{}
	tickDone chan struct{}
	frame    int
}

// toolRow tracks one tool's display row.
type toolRow struct {
	id        string
	name      string
	detail    string // truncated to 80 visible chars on render
	state     toolRowState
	output    strings.Builder // raw stdout/stderr buffered for error dumps
	startedAt time.Time
	endedAt   time.Time
	result    string // ToolEndParams.Result (final summary text)
	isError   bool
}

type toolRowState int

const (
	toolRowPending toolRowState = iota // before tool_start
	toolRowRunning                     // tool_start seen; still executing
	toolRowOK
	toolRowErr
)

// spinnerFrames is the standard Braille spinner used everywhere from
// npm to spinners on Kubernetes CLIs. 80 ms/frame at 100 ms ticks
// gives a smooth-enough rotation without burning CPU on terminal redraws.
var spinnerFrames = []rune{'⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'}

const spinnerTick = 100 * time.Millisecond

// newToolBatchState pre-builds rows from the batch start payload. No
// rendering happens until RenderInitial.
func newToolBatchState(out io.Writer, entries []rpc.ToolBatchToolEntry) *toolBatchState {
	rows := make([]*toolRow, len(entries))
	idx := make(map[string]int, len(entries))
	for i, e := range entries {
		rows[i] = &toolRow{
			id:     e.ToolCallID,
			name:   e.ToolName,
			detail: toolDetailFromArgs(e.ToolName, e.Arguments),
			state:  toolRowPending,
		}
		idx[e.ToolCallID] = i
	}
	return &toolBatchState{
		out:       out,
		rows:      rows,
		rowIndex:  idx,
		startedAt: time.Now(),
		tickStop:  make(chan struct{}),
		tickDone:  make(chan struct{}),
	}
}

// RenderInitial paints the opening rule and one pending row per tool.
// Cursor lands one row below the last pending row (i.e., on what will
// become the footer line) so subsequent in-place updates can navigate
// up by row index. Also starts the spinner animation goroutine.
func (b *toolBatchState) RenderInitial() {
	b.mu.Lock()
	fmt.Fprintf(b.out, "\n\033[2m─── batch (%d) ───\033[0m\n", len(b.rows))
	for _, r := range b.rows {
		fmt.Fprintln(b.out, formatRow(r, b.frame))
	}
	b.cursorRow = len(b.rows)
	b.mu.Unlock()
	go b.tickLoop()
}

// MarkRunning flips a row to running state. Called when the
// corresponding tool_start arrives.
func (b *toolBatchState) MarkRunning(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	i, ok := b.rowIndex[id]
	if !ok {
		return
	}
	b.rows[i].state = toolRowRunning
	b.rows[i].startedAt = time.Now()
	b.rewriteRowLocked(i)
}

// AppendOutput buffers a tool_output chunk for post-mortem on error.
// In normal (success) batches the buffer is discarded.
func (b *toolBatchState) AppendOutput(id, chunk string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	i, ok := b.rowIndex[id]
	if !ok {
		return
	}
	// Cap per-tool buffer at 64 KiB so a runaway tool can't OOM the
	// CLI. Trailing "[truncated]" marker matches the bash tool's own
	// truncation language.
	const cap = 64 * 1024
	r := b.rows[i]
	if r.output.Len() >= cap {
		return
	}
	if r.output.Len()+len(chunk) > cap {
		chunk = chunk[:cap-r.output.Len()]
	}
	r.output.WriteString(chunk)
	// Live byte counter — let the next spinner tick repaint. We
	// don't repaint here on every chunk because chunks can arrive
	// fast enough to drown out everything else. The 100 ms tick
	// catches up.
}

// MarkDone updates a row's status row in place and records the result.
// The output buffer is preserved for Finalize to dump on error.
func (b *toolBatchState) MarkDone(id, result string, isError bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	i, ok := b.rowIndex[id]
	if !ok {
		return
	}
	r := b.rows[i]
	r.state = toolRowOK
	if isError {
		r.state = toolRowErr
	}
	r.result = result
	r.isError = isError
	r.endedAt = time.Now()
	b.rewriteRowLocked(i)
}

// Finalize closes the batch: stops the spinner, dumps error tool
// buffers (if any) and repositions the cursor below all the chrome
// so subsequent output (next round, status line, etc.) flows
// naturally. Idempotent.
func (b *toolBatchState) Finalize() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	b.mu.Unlock()

	// Stop the ticker and wait for it to exit so we don't race on
	// out writes during the finalize sequence.
	close(b.tickStop)
	<-b.tickDone

	b.mu.Lock()
	defer b.mu.Unlock()

	// One last paint of every row in case any chunk landed between
	// the last tick and stop.
	for i := range b.rows {
		b.rewriteRowLocked(i)
	}

	// Print the footer rule.
	fmt.Fprintln(b.out, "\033[2m───\033[0m")
	// Dump any error tool's buffered output. Each error gets its own
	// sub-header so the user can correlate by tool detail.
	for _, r := range b.rows {
		if !r.isError {
			continue
		}
		buffered := r.output.String()
		if buffered == "" && r.result == "" {
			continue
		}
		fmt.Fprintf(b.out, "\n\033[31m✗ %s\033[0m %s\n", r.name, r.detail)
		if buffered != "" {
			fmt.Fprintln(b.out, buffered)
		}
		if r.result != "" {
			// Only print result if it's distinct from the buffered
			// stream — for the bash tool they often overlap.
			if !strings.Contains(buffered, r.result) {
				fmt.Fprintln(b.out, r.result)
			}
		}
		fmt.Fprintln(b.out, "\033[2m───\033[0m")
	}
	fmt.Fprintln(b.out)
}

// tickLoop advances the spinner frame and repaints any running rows
// every spinnerTick. Exits when tickStop is closed.
func (b *toolBatchState) tickLoop() {
	defer close(b.tickDone)
	t := time.NewTicker(spinnerTick)
	defer t.Stop()
	for {
		select {
		case <-b.tickStop:
			return
		case <-t.C:
			b.mu.Lock()
			if b.closed {
				b.mu.Unlock()
				return
			}
			b.frame++
			anyRunning := false
			for i, r := range b.rows {
				if r.state == toolRowPending || r.state == toolRowRunning {
					b.rewriteRowLocked(i)
					if r.state == toolRowRunning {
						anyRunning = true
					}
				}
			}
			b.mu.Unlock()
			// If nothing's running we can ease up — but cheaper to
			// keep ticking than re-establish the goroutine.
			_ = anyRunning
		}
	}
}

// rewriteRowLocked moves the cursor up to row i's line, clears it,
// prints the new content, then returns the cursor to the row
// immediately below the last row (cursorRow == len(rows)). Caller
// must hold b.mu.
func (b *toolBatchState) rewriteRowLocked(i int) {
	// Up by (cursorRow - i) lines. We're at cursorRow; want to be on
	// row i. After printing one line of content we'll be on row i+1,
	// so we need to come back down by (len(rows) - (i+1)) lines.
	up := b.cursorRow - i
	if up < 0 {
		// Should never happen in current usage but guard anyway.
		up = 0
	}
	if up > 0 {
		fmt.Fprintf(b.out, "\033[%dA", up)
	}
	// Clear current line, write fresh content. Note: the row content
	// itself ends with no newline; we add one explicitly so cursor
	// advances to row i+1.
	fmt.Fprintf(b.out, "\r\033[2K%s\n", formatRow(b.rows[i], b.frame))
	// Restore cursor to bottom (row len(rows)).
	down := len(b.rows) - (i + 1)
	if down > 0 {
		fmt.Fprintf(b.out, "\033[%dB", down)
	}
	// Always reset to column 0 after vertical motion; some terminals
	// preserve column on B but not all.
	fmt.Fprint(b.out, "\r")
	b.cursorRow = len(b.rows)
}

// formatRow returns one display line for a tool row, sans trailing
// newline. ANSI: dim for pending, cyan for running, green for ok,
// red for err. Frame parameter rotates the spinner glyph for
// running rows.
func formatRow(r *toolRow, frame int) string {
	var icon, color string
	switch r.state {
	case toolRowPending:
		icon = string(spinnerFrames[frame%len(spinnerFrames)])
		color = "\033[2m" // dim
	case toolRowRunning:
		icon = string(spinnerFrames[frame%len(spinnerFrames)])
		color = "\033[36m" // cyan — alive and working
	case toolRowOK:
		icon = "✓"
		color = "\033[32m" // green
	case toolRowErr:
		icon = "✗"
		color = "\033[31m" // red
	}
	const reset = "\033[0m"

	detail := r.detail
	// One-line clamp; the row must not wrap or our cursor math breaks.
	const maxDetail = 80
	if len(detail) > maxDetail {
		detail = detail[:maxDetail] + "…"
	}

	stat := ""
	switch r.state {
	case toolRowRunning:
		// Live elapsed + buffered byte count so the eye can see
		// real-time progress on each row independently.
		elapsed := time.Since(r.startedAt)
		stat = "  " + dim(fmt.Sprintf("(%s, %s)", formatRowElapsed(elapsed), formatBytes(r.output.Len())))
	case toolRowOK, toolRowErr:
		elapsed := r.endedAt.Sub(r.startedAt)
		if r.startedAt.IsZero() {
			elapsed = 0
		}
		lines := 0
		if r.output.Len() > 0 {
			lines = strings.Count(r.output.String(), "\n")
			if !strings.HasSuffix(r.output.String(), "\n") {
				lines++
			}
		}
		stat = "  " + dim(fmt.Sprintf("(%s, %s)", formatRowElapsed(elapsed), pluralLines(lines)))
	}

	prefix := fmt.Sprintf("  %s%s%s %s", color, icon, reset, r.name)
	if detail != "" {
		prefix += " " + dim("·") + " " + detail
	}
	return prefix + stat
}

func dim(s string) string { return "\033[2m" + s + "\033[0m" }

func formatRowElapsed(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return "<1ms"
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	default:
		return d.Truncate(100 * time.Millisecond).String()
	}
}

// formatBytes returns a short human-readable byte count: "0 B",
// "847 B", "12.3 KiB". Used in the live row state while the tool
// is running.
func formatBytes(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KiB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1024*1024))
	}
}

func pluralLines(n int) string {
	if n == 1 {
		return "1 line"
	}
	return fmt.Sprintf("%d lines", n)
}

// toolDetailFromArgs is a generalized version of toolDetail that
// works off a raw arguments map (rather than ToolStartParams). The
// batch start carries the same map shape so we reuse the rule set.
func toolDetailFromArgs(toolName string, args map[string]interface{}) string {
	switch toolName {
	case "bash":
		if cmd, ok := args["command"].(string); ok {
			return cmd
		}
	case "read", "write", "edit":
		if path, ok := args["path"].(string); ok {
			return path
		}
	}
	return ""
}
