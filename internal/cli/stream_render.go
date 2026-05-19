package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jack-work/largo"
	"go.opentelemetry.io/otel/attribute"

	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/pacer"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/term"
)

// recordWireTrace appends one NDJSON line documenting one wire event.
// Best-effort; failures are silent because tracing must not interfere
// with the run.
func recordWireTrace(w io.Writer, method string, params json.RawMessage) {
	rec := map[string]interface{}{
		"t":      time.Now().UnixMicro(),
		"method": method,
	}
	var peek struct {
		ToolCallID string `json:"tool_call_id"`
	}
	if json.Unmarshal(params, &peek) == nil && peek.ToolCallID != "" {
		rec["id"] = peek.ToolCallID
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	b = append(b, '\n')
	_, _ = w.Write(b)
}

// streamRenderer is the per-prompt CLI state machine. One instance
// handles every wire notification for the lifetime of a prompt; it
// owns the rendering for solo and batch tool layouts and decides
// which mode to commit to based on observation of tool_invoke_start
// events.
//
// Threading: Handle is called from a single goroutine (the JSON-RPC
// notify pump). However, the pacer drainer is a separate goroutine
// that writes to sw on a ticker. Any Suspend/Resume cycle on sw
// must be serialized against that drainer or largo panics on a
// write to a suspended writer. The writeMu mutex guards every write
// path that targets sw — pacer goes through a pacedWriter shim
// which acquires writeMu; the renderer's direct sw.Write and
// Suspend/Resume calls acquire it inline.
type streamRenderer struct {
	ctx context.Context

	// writeMu serializes all writes through sw against
	// Suspend/Resume transitions. Held briefly for each Write and
	// across the (Flush, Suspend) and Resume transitions.
	writeMu sync.Mutex

	// Output sinks. `sw` is the largo (markdown) writer wrapped
	// around stdout; `pace` rate-limits character delivery; `rawOut`
	// is the un-paced sink used while tool rendering owns the
	// terminal (suspended sw).
	sw     *largo.Writer
	pace   *pacer.Pacer
	rawOut io.Writer

	// Tool-rendering state.
	batch *toolBatchState
	solo  *toolSoloState

	// roundInvokes records every tool_invoke_start in the current
	// round, in arrival order. resetRound() clears it on MethodMessage.
	roundInvokes []invokedTool

	// roundCommitted flips true on MessageEnd. Until then the CLI
	// can't tell if more invocations are still coming. Combined with
	// openInvokes == 0, it's the round-finalize trigger.
	roundCommitted bool

	// openInvokes counts tool_invoke_starts that haven't yet seen a
	// matching tool_end. Used (with roundCommitted) to detect
	// "round's tools are all done."
	openInvokes int

	// soloCommitted flips true on MessageEnd when we've committed to
	// solo rendering for the round (one invocation only). After this
	// point tool_output for the solo's id can stream directly to
	// solo.Write — no risk of upgrade-to-batch repositioning it.
	soloCommitted bool

	// pendingOutput buffers tool_output chunks for tools whose
	// rendering container hasn't been opened yet. Keyed by
	// tool_call_id. Flushed into the row/solo on tool_start (or
	// upgradeToBatch). This is what stops output from leaking onto
	// rawOut when a chunk arrives before its container exists.
	pendingOutput map[string][]string

	// streamedDetail tracks the partial input JSON streamed via
	// tool_invoke_delta, so the CLI can update detail strings
	// progressively before tool_invoke_ready arrives.
	streamedDetail map[string]*pendingToolArg

	// done becomes non-nil when MethodDone has fired.
	done chan struct{}

	// wireTrace, when non-nil, receives one JSON line per Handle()
	// call. Opt-in via FIGARO_WIRE_TRACE=/path/to/file.
	wireTrace io.Writer
}

// invokedTool records one tool_invoke_start for the current round.
type invokedTool struct {
	id   string
	name string
	args map[string]interface{} // populated by tool_invoke_ready
}

func newStreamRenderer(ctx context.Context, sw *largo.Writer) *streamRenderer {
	r := &streamRenderer{
		ctx:            ctx,
		sw:             sw,
		pendingOutput:  map[string][]string{},
		streamedDetail: map[string]*pendingToolArg{},
		done:           make(chan struct{}, 1),
	}
	if path := os.Getenv("FIGARO_WIRE_TRACE"); path != "" {
		if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			r.wireTrace = f
		} else {
			slog.Warn("FIGARO_WIRE_TRACE open failed", "path", path, "err", err)
		}
	}
	return r
}

// SetPacer wires the pacer into the renderer. The pacer must be
// constructed against r.PacedOut() so its drainer writes go through
// the renderer's writeMu.
func (r *streamRenderer) SetPacer(p *pacer.Pacer) {
	r.pace = p
}

// pacedWriter wraps writes from the pacer drainer goroutine through
// the renderer's writeMu. The pacer drains text from the LLM stream
// on a ticker; without this serialization a Write can race a
// Suspend() call and trigger largo's panic.
type pacedWriter struct{ r *streamRenderer }

func (pw *pacedWriter) Write(p []byte) (int, error) {
	pw.r.writeMu.Lock()
	defer pw.r.writeMu.Unlock()
	if pw.r.rawOut != nil {
		// Tool rendering owns the terminal; drop the pacer write.
		// (Should not happen in practice — the pacer is Flushed
		// before Suspend — but be defensive.)
		return len(p), nil
	}
	return pw.r.sw.Write(p)
}

// PacedOut returns the io.Writer the pacer should be constructed
// against. Calling this between newStreamRenderer and the pacer's
// New() is how we hook the serialization in.
func (r *streamRenderer) PacedOut() io.Writer { return &pacedWriter{r: r} }

// lockedFlush calls sw.Flush() under writeMu. External callers in
// shutdown paths (mustPromptFigaro's error / interrupt branches)
// use this to avoid racing the pacer drainer.
func (r *streamRenderer) lockedFlush() {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	r.sw.Flush()
}

// Done returns a channel that closes when MethodDone has fired.
func (r *streamRenderer) Done() <-chan struct{} { return r.done }

// suspendIfNeeded transitions stdout from largo/markdown mode to raw
// mode, returning a raw io.Writer the tool renderers can use. The
// writeMu lock is held across pace.Flush(), sw.Flush(), and
// sw.Suspend() so the pacer drainer can't sneak a Write between
// Flush and Suspend.
func (r *streamRenderer) suspendIfNeeded() io.Writer {
	if r.rawOut != nil {
		return r.rawOut
	}
	// Flush the pacer queue *before* acquiring writeMu — Flush has
	// its own internal spin and will eventually re-enter the pacer
	// drainer via pacedWriter.Write, which would deadlock if we held
	// writeMu here. After Flush returns, the queue is empty; any
	// concurrent drainer wakeup would then see no work.
	r.pace.Flush()

	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	r.sw.Flush()
	r.rawOut = r.sw.Suspend()
	return r.rawOut
}

// resumeIfSuspended hands stdout back to largo and clears tool state.
func (r *streamRenderer) resumeIfSuspended() {
	if r.batch != nil {
		if r.batch.wrapped && r.solo != nil {
			anyErr := r.batch.FinalizeRowsOnly()
			r.solo.Done(anyErr)
			r.solo = nil
			r.batch.PrintErrorDumps()
		} else {
			r.batch.Finalize()
		}
		r.batch = nil
	}
	if r.solo != nil {
		r.solo.Freeze()
		r.solo = nil
	}
	if r.rawOut == nil {
		return
	}
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	_ = r.sw.Resume()
	r.rawOut = nil
}

// recordInvokeStart appends a new invokedTool to the round.
func (r *streamRenderer) recordInvokeStart(id, name string) {
	r.roundInvokes = append(r.roundInvokes, invokedTool{id: id, name: name})
}

// recordInvokeReady backfills decoded args for an earlier invokeStart.
func (r *streamRenderer) recordInvokeReady(id string, args map[string]interface{}) {
	for i := range r.roundInvokes {
		if r.roundInvokes[i].id == id {
			r.roundInvokes[i].args = args
			return
		}
	}
}

// resetRound clears per-round bookkeeping. Called on MethodMessage,
// which marks the end of one Send cycle within a turn.
func (r *streamRenderer) resetRound() {
	r.roundInvokes = r.roundInvokes[:0]
	r.roundCommitted = false
	r.soloCommitted = false
}

// commitRoundIfReady fires the rendering-finalize path when the round
// has both: seen MessageEnd (so we know the invoke set is closed)
// AND all open invokes have closed (every tool_invoke_start has a
// matching tool_end).
func (r *streamRenderer) commitRoundIfReady() {
	if !r.roundCommitted || r.openInvokes > 0 {
		return
	}
	r.finalizeRendering()
}

// finalizeRendering wraps up whatever mode (solo or batch) is open.
func (r *streamRenderer) finalizeRendering() {
	if r.batch != nil {
		if r.batch.wrapped && r.solo != nil {
			anyErr := r.batch.FinalizeRowsOnly()
			r.solo.Done(anyErr)
			r.solo = nil
			fmt.Fprintln(r.rawOut, term.Dim("───"))
			r.batch.PrintErrorDumps()
		} else {
			r.batch.Finalize()
		}
		r.batch = nil
	} else if r.solo != nil {
		// Solo without batch upgrade: it's already painted its
		// header/output via Write; Done writes the closing rule.
		r.solo.Done(false)
		r.solo = nil
	}
	if r.rawOut != nil {
		r.writeMu.Lock()
		_ = r.sw.Resume()
		r.rawOut = nil
		r.writeMu.Unlock()
	}
}

// upgradeToBatch is called the moment a round's second tool_invoke_start
// arrives. The first invoke had opened a solo placeholder; now we
// repurpose it as the batch header and add per-tool rows for every
// invoke seen so far. Any output that had streamed for tool 1 (and
// queued for tool 2) gets flushed into the right row.
func (r *streamRenderer) upgradeToBatch() {
	if r.batch != nil {
		return
	}
	entries := make([]batchToolEntry, 0, len(r.roundInvokes))
	for _, t := range r.roundInvokes {
		entries = append(entries, batchToolEntry{
			ToolCallID: t.id,
			ToolName:   t.name,
			Arguments:  t.args,
		})
	}
	wrapped := false
	if r.solo != nil {
		r.solo.UpdateHeader("batch", fmt.Sprintf("(%d)", len(entries)))
		r.solo.StopTicker()
		wrapped = true
	} else {
		r.suspendIfNeeded()
	}
	r.batch = newToolBatchState(r.rawOut, entries)
	r.batch.wrapped = wrapped
	if wrapped {
		r.batch.wrapperSolo = r.solo
	}
	r.batch.RenderInitial()
	// Flush any buffered output into the matching rows.
	for id, chunks := range r.pendingOutput {
		for _, c := range chunks {
			r.batch.AppendOutput(id, c)
		}
		delete(r.pendingOutput, id)
	}
}

// updateBatchSize re-renders the batch header to reflect the current
// row count. Called when AppendRow grows the table.
func (r *streamRenderer) updateBatchSize() {
	if r.batch == nil || !r.batch.wrapped || r.batch.wrapperSolo == nil {
		return
	}
	r.batch.wrapperSolo.UpdateHeader("batch", fmt.Sprintf("(%d)", len(r.roundInvokes)))
}

// flushPendingOutputTo flushes any buffered chunks for id into dst
// (a func that consumes one chunk at a time).
func (r *streamRenderer) flushPendingOutputTo(id string, dst func(string)) {
	chunks, ok := r.pendingOutput[id]
	if !ok {
		return
	}
	for _, c := range chunks {
		dst(c)
	}
	delete(r.pendingOutput, id)
}

// Handle dispatches one wire notification. This is the entry point
// the JSON-RPC notify pump calls for each incoming event.
func (r *streamRenderer) Handle(method string, params json.RawMessage) {
	slog.Debug("rpc recv", "method", method, "params", json.RawMessage(params))

	// Per-event wire trace, opt-in via FIGARO_WIRE_TRACE=/path/to/file.
	// Appends one JSON object per event with timestamp, method, and
	// (when present) tool_call_id. Cheap escape hatch when the UI
	// looks wrong and we need to confirm the wire was sane.
	if r.wireTrace != nil {
		recordWireTrace(r.wireTrace, method, params)
	}

	switch method {
	case rpc.MethodDelta:
		var p rpc.DeltaParams
		if json.Unmarshal(params, &p) == nil {
			figOtel.Event(r.ctx, "cli.recv.delta",
				attribute.String("text", p.Text),
			)
			r.pace.Push(p.Text)
		}

	case rpc.MethodThinking:
		var p rpc.ThinkingParams
		if json.Unmarshal(params, &p) == nil {
			r.writeMu.Lock()
			r.sw.Write([]byte("\n> *🤔 " + largo.EscapeInline(p.Text) + "*\n\n"))
			r.writeMu.Unlock()
		}

	case rpc.MethodMessage:
		figOtel.Event(r.ctx, "cli.recv.message")
		r.pace.Flush()
		r.writeMu.Lock()
		r.sw.Flush()
		r.writeMu.Unlock()
		r.resetRound()

	case rpc.MethodMessageEnd:
		var p rpc.MessageEndParams
		if json.Unmarshal(params, &p) == nil {
			figOtel.Event(r.ctx, "cli.recv.message_end",
				attribute.String("stop_reason", p.StopReason))
			r.roundCommitted = true
			// If we committed to solo (one invocation this round),
			// flush any buffered output for it into the solo now. The
			// solo placeholder is now a committed surface; subsequent
			// chunks (and the eventual tool_end) can write straight to it.
			if r.batch == nil && r.solo != nil {
				r.flushPendingOutputTo(r.solo.callID, func(c string) {
					r.solo.Write([]byte(c))
				})
				r.soloCommitted = true
			}
			r.commitRoundIfReady()
		}

	case rpc.MethodToolInvokeStart:
		var p rpc.ToolInvokeStartParams
		if json.Unmarshal(params, &p) != nil {
			return
		}
		figOtel.Event(r.ctx, "cli.recv.tool_invoke_start",
			attribute.String("tool", p.ToolName),
			attribute.String("tool_call_id", p.ToolCallID),
		)
		r.streamedDetail[p.ToolCallID] = &pendingToolArg{toolName: p.ToolName}
		r.recordInvokeStart(p.ToolCallID, p.ToolName)
		r.openInvokes++

		switch {
		case len(r.roundInvokes) == 1 && r.batch == nil && r.solo == nil:
			// First tool: open a solo placeholder. If only one tool
			// arrives this round it stays solo.
			r.suspendIfNeeded()
			r.solo = newToolSoloState(r.rawOut, p.ToolName, "")
			r.solo.Start()
			r.solo.callID = p.ToolCallID
		case len(r.roundInvokes) == 2 && r.batch == nil:
			r.upgradeToBatch()
		case r.batch != nil:
			r.batch.AppendRow(p.ToolCallID, p.ToolName, nil)
			r.updateBatchSize()
		}

	case rpc.MethodToolInvokeDelta:
		var p rpc.ToolInvokeDeltaParams
		if json.Unmarshal(params, &p) != nil {
			return
		}
		figOtel.Event(r.ctx, "cli.recv.tool_invoke_delta",
			attribute.String("tool_call_id", p.ToolCallID),
			attribute.Int("bytes", len(p.PartialJSON)),
		)
		if pt, ok := r.streamedDetail[p.ToolCallID]; ok {
			pt.json += p.PartialJSON
			if detail := extractPartialDetail(pt.toolName, pt.json); detail != "" {
				r.applyDetail(p.ToolCallID, detail)
			}
		}

	case rpc.MethodToolInvokeReady:
		var p rpc.ToolInvokeReadyParams
		if json.Unmarshal(params, &p) != nil {
			return
		}
		figOtel.Event(r.ctx, "cli.recv.tool_invoke_ready",
			attribute.String("tool_call_id", p.ToolCallID),
		)
		r.recordInvokeReady(p.ToolCallID, p.Arguments)
		if detail := toolDetailFromArgs(p.ToolName, p.Arguments); detail != "" {
			r.applyDetail(p.ToolCallID, detail)
		}

	case rpc.MethodToolStart:
		var p rpc.ToolStartParams
		if json.Unmarshal(params, &p) != nil {
			return
		}
		figOtel.Event(r.ctx, "cli.recv.tool_start",
			attribute.String("tool", p.ToolName),
			attribute.String("tool_call_id", p.ToolCallID),
		)
		if r.batch != nil {
			r.batch.MarkRunning(p.ToolCallID)
			r.flushPendingOutputTo(p.ToolCallID, func(c string) {
				r.batch.AppendOutput(p.ToolCallID, c)
			})
			return
		}
		// Solo path: refresh detail with decoded args. Don't flush
		// pending output yet — MessageEnd commits the solo, and only
		// then is it safe to write to the terminal.
		if r.solo != nil && r.solo.callID == p.ToolCallID {
			if d := toolDetail(p); d != "" {
				r.solo.UpdateDetail(d)
			}
		}

	case rpc.MethodToolOutput:
		var p rpc.ToolOutputParams
		if json.Unmarshal(params, &p) != nil {
			return
		}
		figOtel.Event(r.ctx, "cli.recv.tool_output",
			attribute.Int("bytes", len(p.Chunk)),
			attribute.String("tool_call_id", p.ToolCallID),
		)
		// Route by tool_call_id. The batch frame is a committed
		// rendering surface — once it's open we can stream chunks
		// directly into the row by id. The solo placeholder is
		// committed only once MessageEnd confirms only one
		// invocation arrived (soloCommitted = true). Until then,
		// writing to the solo immediately would leak the chunk to
		// terminal scrollback above any subsequent batch frame.
		switch {
		case r.batch != nil:
			r.batch.AppendOutput(p.ToolCallID, p.Chunk)
		case r.soloCommitted && r.solo != nil && r.solo.callID == p.ToolCallID:
			r.solo.Write([]byte(p.Chunk))
		default:
			r.pendingOutput[p.ToolCallID] = append(r.pendingOutput[p.ToolCallID], p.Chunk)
		}

	case rpc.MethodToolEnd:
		var p rpc.ToolEndParams
		if json.Unmarshal(params, &p) != nil {
			return
		}
		figOtel.Event(r.ctx, "cli.recv.tool_end",
			attribute.String("tool", p.ToolName),
			attribute.String("tool_call_id", p.ToolCallID),
			attribute.Bool("error", p.IsError))
		if r.batch != nil {
			// Flush any buffered output (in case tool_start was missed).
			r.flushPendingOutputTo(p.ToolCallID, func(c string) {
				r.batch.AppendOutput(p.ToolCallID, c)
			})
			r.batch.MarkDone(p.ToolCallID, p.Result, p.IsError)
		} else if r.soloCommitted && r.solo != nil && r.solo.callID == p.ToolCallID {
			// Only flush to solo if it's already committed. Pre-commit,
			// the buffer stays in pendingOutput — MessageEnd will
			// commit and flush, or upgradeToBatch will transplant.
			r.flushPendingOutputTo(p.ToolCallID, func(c string) {
				r.solo.Write([]byte(c))
			})
			if p.IsError && r.rawOut != nil {
				r.rawOut.Write([]byte("\n" + term.Red("⚠ error:") + " " + p.Result + "\n"))
			}
		}
		if r.openInvokes > 0 {
			r.openInvokes--
		}
		r.commitRoundIfReady()

	case rpc.MethodError:
		var p rpc.ErrorParams
		if json.Unmarshal(params, &p) == nil {
			r.pace.Flush()
			r.resumeIfSuspended()
			r.writeMu.Lock()
			r.sw.Write([]byte("\n**❌ error:** " + largo.EscapeInline(p.Message) + "\n\n"))
			r.writeMu.Unlock()
		}

	case rpc.MethodDone:
		r.openInvokes = 0
		r.roundCommitted = true
		r.finalizeRendering()
		r.pace.Flush()
		r.writeMu.Lock()
		r.sw.Flush()
		r.writeMu.Unlock()
		select {
		case r.done <- struct{}{}:
		default:
		}
	}
}

// applyDetail updates the detail string on whichever container owns
// the given id.
func (r *streamRenderer) applyDetail(id, detail string) {
	if r.batch != nil {
		r.batch.UpdateDetail(id, detail)
		return
	}
	if r.solo != nil && r.solo.callID == id {
		r.solo.UpdateDetail(detail)
	}
}

// stripANSI is exposed for tests that need to assert on rendered
// content without color/cursor noise.
func stripANSIRendered(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) {
				c := s[j]
				if c >= 0x40 && c <= 0x7e {
					j++
					break
				}
				j++
			}
			i = j - 1
			continue
		}
		out.WriteByte(s[i])
	}
	return out.String()
}
