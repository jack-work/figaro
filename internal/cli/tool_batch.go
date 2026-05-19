package cli

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/jack-work/figaro/internal/term"
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

	// wrapped means a wrapping toolSoloState owns the header / footer
	// chrome; this batch should only paint per-tool rows. The caller
	// (stream.go) is responsible for finalizing the wrapper (via
	// solo.Done) and printing error dumps after FinalizeRowsOnly.
	wrapped     bool
	wrapperSolo *toolSoloState // when wrapped, repainted on each tick

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

// batchToolEntry describes one tool when constructing a batch state.
// CLI-local; the wire used to carry an equivalent rpc.ToolBatchToolEntry
// but the rendering decision is now driven by per-tool wire events.
type batchToolEntry struct {
	ToolCallID string
	ToolName   string
	Arguments  map[string]interface{}
}

// newToolBatchState pre-builds rows from the batch start payload. No
// rendering happens until RenderInitial.
func newToolBatchState(out io.Writer, entries []batchToolEntry) *toolBatchState {
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

// RenderInitial paints one pending row per tool. In standalone mode
// the rows are bracketed by an opening rule and the cursor lands on
// what will become the closing rule; in wrapped mode the header is
// owned by a toolSoloState above and only rows are emitted (and the
// wrapper is told how many rows were added so its repaint math can
// account for them). Also starts the spinner animation goroutine.
func (b *toolBatchState) RenderInitial() {
	b.mu.Lock()
	if !b.wrapped {
		fmt.Fprintf(b.out, "\n%s\n", term.Dim(fmt.Sprintf("─── batch (%d) ───", len(b.rows))))
	}
	for _, r := range b.rows {
		fmt.Fprintln(b.out, formatRow(r, b.frame))
	}
	b.cursorRow = len(b.rows)
	if b.wrapped && b.wrapperSolo != nil {
		b.wrapperSolo.AddRowsBelow(len(b.rows))
	}
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

// AppendRow adds a new pending row to the batch at runtime — used
// when additional tool_invoke_start events arrive after the batch
// frame has already opened. The new row is printed immediately
// below the existing rows.
func (b *toolBatchState) AppendRow(id, name string, args map[string]interface{}) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, exists := b.rowIndex[id]; exists {
		return
	}
	r := &toolRow{
		id:     id,
		name:   name,
		detail: toolDetailFromArgs(name, args),
		state:  toolRowPending,
	}
	b.rows = append(b.rows, r)
	b.rowIndex[id] = len(b.rows) - 1
	// Cursor is at cursorRow == old len(b.rows). Print the new row
	// on the next line; cursorRow advances by 1.
	fmt.Fprintln(b.out, formatRow(r, b.frame))
	b.cursorRow = len(b.rows)
	if b.wrapped && b.wrapperSolo != nil {
		b.wrapperSolo.AddRowsBelow(1)
	}
}

// UpdateDetail refreshes a row's detail string (e.g. when fully
// decoded args arrive via tool_invoke_ready, replacing the partial
// streamed detail from tool_invoke_delta).
func (b *toolBatchState) UpdateDetail(id, detail string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	i, ok := b.rowIndex[id]
	if !ok {
		return
	}
	b.rows[i].detail = detail
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
// naturally. Idempotent. In wrapped mode this only paints the final
// row state and leaves the closing chrome to the caller — see
// FinalizeRowsOnly + PrintErrorDumps.
func (b *toolBatchState) Finalize() {
	if b.wrapped {
		b.FinalizeRowsOnly()
		return
	}
	b.finalizeRowsLocked()
	b.mu.Lock()
	defer b.mu.Unlock()
	fmt.Fprintln(b.out, term.Dim("───"))
	b.printErrorDumpsLocked()
	fmt.Fprintln(b.out)
}

// FinalizeRowsOnly stops the ticker and paints the final row state.
// Returns whether any tool errored, for the wrapping toolSoloState's
// Done call. Caller must follow with PrintErrorDumps after the
// wrapper is finalized.
func (b *toolBatchState) FinalizeRowsOnly() (anyError bool) {
	b.finalizeRowsLocked()
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, r := range b.rows {
		if r.isError {
			anyError = true
			break
		}
	}
	return anyError
}

// PrintErrorDumps emits the error sub-blocks (if any) and a final
// blank line. Used by stream.go in wrapped mode after the wrapping
// solo header has been finalized in place.
func (b *toolBatchState) PrintErrorDumps() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.printErrorDumpsLocked()
	fmt.Fprintln(b.out)
}

// finalizeRowsLocked stops the spinner ticker and repaints every
// row one final time. Idempotent. Drops b.mu while waiting on the
// ticker to exit so we don't deadlock against tickLoop.
func (b *toolBatchState) finalizeRowsLocked() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	b.mu.Unlock()

	close(b.tickStop)
	<-b.tickDone

	b.mu.Lock()
	defer b.mu.Unlock()
	for i := range b.rows {
		b.rewriteRowLocked(i)
	}
}

// printErrorDumpsLocked emits one error sub-block per errored tool.
// Caller holds b.mu.
func (b *toolBatchState) printErrorDumpsLocked() {
	for _, r := range b.rows {
		if !r.isError {
			continue
		}
		buffered := r.output.String()
		if buffered == "" && r.result == "" {
			continue
		}
		fmt.Fprintf(b.out, "\n%s %s\n", term.Red("✗ "+r.name), r.detail)
		if buffered != "" {
			fmt.Fprintln(b.out, buffered)
		}
		if r.result != "" && !strings.Contains(buffered, r.result) {
			fmt.Fprintln(b.out, r.result)
		}
		fmt.Fprintln(b.out, term.Dim("───"))
	}
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
			// In wrapped mode the wrapping solo owns the header line
			// above all rows. Repaint it with the current frame so
			// the wrapper spinner stays in lockstep with the row
			// spinners — solo's own rowsBelow already counts our
			// rows (bumped by RenderInitial).
			if b.wrapped && b.wrapperSolo != nil {
				b.wrapperSolo.RepaintAtFrame(b.frame)
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
	up := b.cursorRow - i
	if up < 0 {
		up = 0
	}
	if up > 0 {
		fmt.Fprint(b.out, term.CursorUp(up))
	}
	fmt.Fprintf(b.out, "%s%s\n", term.EraseLine, formatRow(b.rows[i], b.frame))
	down := len(b.rows) - (i + 1)
	if down > 0 {
		fmt.Fprint(b.out, term.CursorDown(down))
	}
	fmt.Fprint(b.out, "\r")
	b.cursorRow = len(b.rows)
}

// formatRow returns one display line for a tool row, sans trailing
// newline. ANSI: dim for pending, cyan for running, green for ok,
// red for err. Frame parameter rotates the spinner glyph for
// running rows.
func formatRow(r *toolRow, frame int) string {
	var icon string
	var colorFn func(string) string
	switch r.state {
	case toolRowPending:
		icon = string(spinnerFrames[frame%len(spinnerFrames)])
		colorFn = term.Dim
	case toolRowRunning:
		icon = string(spinnerFrames[frame%len(spinnerFrames)])
		colorFn = term.Cyan
	case toolRowOK:
		icon = "✓"
		colorFn = term.Green
	case toolRowErr:
		icon = "✗"
		colorFn = term.Red
	}

	detail := r.detail
	// Clamp detail so the row never wraps — wrapping breaks cursor math.
	// Row skeleton: "  X name · detail  (stat)"
	// Overhead: 2 (indent) + 1 (icon) + 1 (space) + len(name) + 3 (" · ") + ~20 (stat)
	const rowOverhead = 27
	maxDetail := term.Width() - rowOverhead - len([]rune(r.name))
	if maxDetail < 4 {
		detail = ""
	} else if len([]rune(detail)) > maxDetail {
		detail = string([]rune(detail)[:maxDetail-1]) + "…"
	}

	stat := ""
	switch r.state {
	case toolRowRunning:
		// Live elapsed + buffered byte count so the eye can see
		// real-time progress on each row independently.
		elapsed := time.Since(r.startedAt)
		stat = "  " + term.Dim(fmt.Sprintf("(%s, %s)", formatRowElapsed(elapsed), formatBytes(r.output.Len())))
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
		stat = "  " + term.Dim(fmt.Sprintf("(%s, %s)", formatRowElapsed(elapsed), pluralLines(lines)))
	}

	prefix := fmt.Sprintf("  %s %s", colorFn(icon), r.name)
	if detail != "" {
		prefix += " " + term.Dim("·") + " " + detail
	}
	return prefix + stat
}

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
