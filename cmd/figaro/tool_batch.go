package main

import (
	"fmt"
	"io"
	"strings"
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
//	  ⏳ bash · pwd && date
//	  ⏳ bash · ls -la
//	  ⏳ bash · uname -a
//	─── ─────────────────────────────────────
//
// On each tool_end we cursor up to the matching row, rewrite it as
// "✓ bash · pwd && date  (4 lines, 87 ms)" or "✗ ...". On the closing
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
type toolBatchState struct {
	out  io.Writer
	rows []*toolRow
	// rowIndex maps tool_call_id → index into rows.
	rowIndex map[string]int
	// startedAt records when the batch began (for total elapsed
	// reporting if we ever want it).
	startedAt time.Time
	// dirty is true after any in-place row update so Finalize knows
	// it must move the cursor back to the bottom.
	cursorRow int // current row index of the cursor relative to row 0
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
	}
}

// RenderInitial paints the opening rule and one pending row per tool.
// Cursor lands one row below the last pending row (i.e., on what will
// become the footer line) so subsequent in-place updates can navigate
// up by row index.
func (b *toolBatchState) RenderInitial() {
	fmt.Fprintf(b.out, "\n\033[2m─── batch (%d) ───\033[0m\n", len(b.rows))
	for _, r := range b.rows {
		fmt.Fprintln(b.out, formatRow(r))
	}
	// cursor is now on the line *after* the last row.
	b.cursorRow = len(b.rows)
}

// MarkRunning flips a row to running state. Called when the
// corresponding tool_start arrives. Visual is cosmetic only — pending
// and running both show ⏳ — so we just record state for stats.
func (b *toolBatchState) MarkRunning(id string) {
	i, ok := b.rowIndex[id]
	if !ok {
		return
	}
	b.rows[i].state = toolRowRunning
	b.rows[i].startedAt = time.Now()
}

// AppendOutput buffers a tool_output chunk for post-mortem on error.
// In normal (success) batches the buffer is discarded.
func (b *toolBatchState) AppendOutput(id, chunk string) {
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
}

// MarkDone updates a row's status row in place and records the result.
// The output buffer is preserved for Finalize to dump on error.
func (b *toolBatchState) MarkDone(id, result string, isError bool) {
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
	b.rewriteRow(i)
}

// Finalize closes the batch: dumps error tool buffers (if any) and
// repositions the cursor below all the chrome so subsequent output
// (next round, status line, etc.) flows naturally. Idempotent.
func (b *toolBatchState) Finalize() {
	// Move cursor below the rows (it already is, after RenderInitial,
	// unless the last action was a row rewrite — in which case
	// rewriteRow restored it). Print the footer rule.
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

// rewriteRow moves the cursor up to row i's line, clears it, prints
// the new content, then returns the cursor to the row immediately
// below the last row (cursorRow == len(rows)).
func (b *toolBatchState) rewriteRow(i int) {
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
	fmt.Fprintf(b.out, "\r\033[2K%s\n", formatRow(b.rows[i]))
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
// newline. ANSI: dim for pending, default for running, green for ok,
// red for err.
func formatRow(r *toolRow) string {
	var icon, color string
	switch r.state {
	case toolRowPending, toolRowRunning:
		icon = "⏳"
		color = "\033[2m" // dim
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
	if r.state == toolRowOK || r.state == toolRowErr {
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
