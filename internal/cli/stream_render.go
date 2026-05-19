package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/jack-work/largo"
	"go.opentelemetry.io/otel/attribute"

	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/pacer"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/term"
)

// streamRenderer is the per-prompt CLI state machine. One instance
// handles every wire notification for the lifetime of a prompt; it
// owns the rendering for solo and batch tool layouts and decides
// which mode to commit to based on observation of tool_invoke_start
// events.
//
// Threading: Handle is called from a single goroutine (the JSON-RPC
// notify pump). All state lives on the struct; no internal locks
// needed.
type streamRenderer struct {
	ctx context.Context

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
}

// invokedTool records one tool_invoke_start for the current round.
type invokedTool struct {
	id   string
	name string
	args map[string]interface{} // populated by tool_invoke_ready
}

func newStreamRenderer(ctx context.Context, sw *largo.Writer, pace *pacer.Pacer) *streamRenderer {
	return &streamRenderer{
		ctx:            ctx,
		sw:             sw,
		pace:           pace,
		pendingOutput:  map[string][]string{},
		streamedDetail: map[string]*pendingToolArg{},
		done:           make(chan struct{}, 1),
	}
}

// Done returns a channel that closes when MethodDone has fired.
func (r *streamRenderer) Done() <-chan struct{} { return r.done }

// suspendIfNeeded transitions stdout from largo/markdown mode to raw
// mode, returning a raw io.Writer the tool renderers can use.
func (r *streamRenderer) suspendIfNeeded() io.Writer {
	if r.rawOut != nil {
		return r.rawOut
	}
	r.pace.Flush()
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
		_ = r.sw.Resume()
		r.rawOut = nil
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
			r.sw.Write([]byte("\n> *🤔 " + largo.EscapeInline(p.Text) + "*\n\n"))
		}

	case rpc.MethodMessage:
		figOtel.Event(r.ctx, "cli.recv.message")
		r.pace.Flush()
		r.sw.Flush()
		r.resetRound()

	case rpc.MethodMessageEnd:
		var p rpc.MessageEndParams
		if json.Unmarshal(params, &p) == nil {
			figOtel.Event(r.ctx, "cli.recv.message_end",
				attribute.String("stop_reason", p.StopReason))
			r.roundCommitted = true
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
		// Solo path: this is the first tool of the round and we're
		// still in solo. Refresh its detail with the fully-decoded
		// args (in case invoke_ready hadn't landed yet).
		if r.solo != nil && r.solo.callID == p.ToolCallID {
			if d := toolDetail(p); d != "" {
				r.solo.UpdateDetail(d)
			}
			r.flushPendingOutputTo(p.ToolCallID, func(c string) {
				r.solo.Write([]byte(c))
			})
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
		// Route by tool_call_id. If we have a container ready for
		// this id, send the chunk there. Otherwise buffer until
		// tool_start opens it (or upgradeToBatch flushes it).
		switch {
		case r.batch != nil:
			r.batch.AppendOutput(p.ToolCallID, p.Chunk)
		case r.solo != nil && r.solo.callID == p.ToolCallID:
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
		} else if r.solo != nil && r.solo.callID == p.ToolCallID {
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
			r.sw.Write([]byte("\n**❌ error:** " + largo.EscapeInline(p.Message) + "\n\n"))
		}

	case rpc.MethodDone:
		r.openInvokes = 0
		r.roundCommitted = true
		r.finalizeRendering()
		r.pace.Flush()
		r.sw.Flush()
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
