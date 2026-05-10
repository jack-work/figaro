package figaro

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/message"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/outfit"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/store"
)

// turnBus is the per-turn provider.Bus. It feeds buffered channels
// the runTurn loop drains; pushes never block. Closed by runTurn
// after provider.Send returns. notifs is a catch-all channel for
// non-text/non-figaro events (tool_use_start, tool_use_delta) so new
// event types can be added without growing the channel set.
type turnBus struct {
	deltas chan message.Content
	figs   chan message.Message
	notifs chan rpc.Notification
}

func newTurnBus() *turnBus {
	return &turnBus{
		deltas: make(chan message.Content, 64),
		figs:   make(chan message.Message, 4),
		notifs: make(chan rpc.Notification, 256),
	}
}

func (b *turnBus) PushDelta(c message.Content) {
	select {
	case b.deltas <- c:
	default:
		// Drop if full — UX streaming is best-effort.
	}
}

func (b *turnBus) PushFigaro(m message.Message) {
	b.figs <- m
}

func (b *turnBus) PushToolUseStart(toolCallID, toolName string) {
	b.pushNotif(rpc.Notification{
		JSONRPC: "2.0",
		Method:  rpc.MethodToolUseStart,
		Params:  rpc.ToolUseStartParams{ToolCallID: toolCallID, ToolName: toolName},
	})
}

func (b *turnBus) PushToolUseDelta(toolCallID, partialJSON string) {
	b.pushNotif(rpc.Notification{
		JSONRPC: "2.0",
		Method:  rpc.MethodToolUseDelta,
		Params:  rpc.ToolUseDeltaParams{ToolCallID: toolCallID, PartialJSON: partialJSON},
	})
}

func (b *turnBus) pushNotif(n rpc.Notification) {
	select {
	case b.notifs <- n:
	default:
		// Drop if full — UX progress signals are best-effort.
	}
}

// runTurn drives one user prompt to completion. Builds the user tic,
// loops provider.Send → tool dispatch until the assistant message
// has no tool_use blocks (or interrupt / error). Tool execution
// happens synchronously inside this function; concurrency comes from
// goroutines fanning into a channel.
func (a *Agent) runTurn(ctx context.Context, prompt event) {
	a.mu.Lock()
	a.lastActive = time.Now()
	a.mu.Unlock()

	turnCtx, span := figOtel.Start(ctx, "figaro.qua",
		figOtel.WithAttributes(
			attribute.String("figaro.id", a.id),
			attribute.String("figaro.model", a.currentModel()),
			attribute.String("figaro.provider", a.prov.Name()),
		),
	)
	defer span.End()
	turnCtx, cancel := context.WithCancel(turnCtx)
	a.mu.Lock()
	a.turnCtx = turnCtx
	a.turnCancel = cancel
	a.interrupted = false
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.turnCtx = nil
		a.turnCancel = nil
		a.mu.Unlock()
		cancel()
	}()

	// User tic = prompt text + any chalkboard input + bootstrap patch
	// on a fresh aria.
	tic := message.Message{
		Role:      message.RoleUser,
		Timestamp: time.Now().UnixMilli(),
	}
	if prompt.chalkboard != nil {
		if combined := a.combineChalkboardInput(prompt.chalkboard); !combined.IsEmpty() {
			tic.Patches = append(tic.Patches, combined)
			a.chalkboard.Apply(combined)
		}
	}
	if len(a.figStream.Read()) == 0 && a.chalkboard != nil {
		// Outfitter bootstrap: system.prompt, system.skills.
		if a.outfitter != nil {
			if patch, err := a.outfitter.Bootstrap(a.chalkboard.Snapshot(),
				outfit.CurrentBootCtx(a.prov.Name(), a.id)); err == nil && !patch.IsEmpty() {
				tic.Patches = append(tic.Patches, patch)
				a.chalkboard.Apply(patch)
			}
		}
		// Allowlisted env vars → system.environment.* one-shot.
		if envPatch := chalkboard.EnvironmentPatch(); !envPatch.IsEmpty() {
			tic.Patches = append(tic.Patches, envPatch)
			a.chalkboard.Apply(envPatch)
		}
		_ = a.chalkboard.Save()
	}
	if prompt.text != "" {
		tic.Content = append(tic.Content, message.TextContent(prompt.text))
	}
	if _, err := a.figStream.Append(store.Entry[message.Message]{Payload: tic}); err != nil {
		a.fanOutError(fmt.Sprintf("append user tic: %s", err))
		a.endTurn("error: append tic")
		return
	}

	// Drive: provider → tools → repeat or done.
	for {
		stop := a.driveOneRound(turnCtx)
		if stop {
			return
		}
	}
}

// driveOneRound runs one provider.Send + (if needed) tool dispatch
// cycle. Returns true when the turn is complete (done, error, or
// interrupted) — the caller exits. Returns false when more rounds
// are needed (assistant emitted tool_use; tools ran; user-role
// tool_result tic appended; loop again).
func (a *Agent) driveOneRound(turnCtx context.Context) (done bool) {
	bus := newTurnBus()
	in := provider.SendInput{
		AriaID:    a.id,
		FigStream: a.figStream,
		Snapshot:  a.chalkboard.Snapshot(),
		Tools:     a.toolDefs(),
		MaxTokens: a.chalkboardInt("system.max_tokens"),
	}
	sendDone := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				sendDone <- fmt.Errorf("provider send panic: %v\n%s", r, debug.Stack())
			}
			close(bus.deltas)
			close(bus.figs)
			close(bus.notifs)
		}()
		started := time.Now()
		err := a.prov.Send(turnCtx, in, bus)
		figOtel.RecordRequestDuration(turnCtx, time.Since(started),
			attribute.String("provider", a.prov.Name()),
			attribute.String("model", a.currentModel()),
			attribute.String("status", statusOf(err)))
		sendDone <- err
	}()

	// Drain deltas + notifs + figaro. All channels close when
	// provider.Send returns (its goroutine closes them via the defer).
	//
	// Wire-order matters: MethodMessage *must* arrive after every
	// MethodDelta and MethodToolUse* for that turn. The CLI uses
	// MethodMessage as a "round complete, flush largo now" trigger;
	// a stray delta after the flush would sit in largo's buffer
	// un-rendered until the next flush trigger.
	//
	// Provider contract: PushFigaro is the last thing the producer
	// does for the turn. So when we observe a fig on the channel,
	// every delta and notif has already been pushed — we just need
	// to drain those buffers fully before fanning out MethodMessage.
	var lastFig message.Message
	drainPreFigOnce := func() {
		for {
			select {
			case d, ok := <-bus.deltas:
				if !ok {
					bus.deltas = nil
					continue
				}
				if !a.isInterrupted() {
					a.fanOut(rpc.Notification{
						JSONRPC: "2.0",
						Method:  rpc.MethodDelta,
						Params:  rpc.DeltaParams{Text: d.Text, ContentType: d.Type},
					})
				}
			case n, ok := <-bus.notifs:
				if !ok {
					bus.notifs = nil
					continue
				}
				if !a.isInterrupted() {
					a.fanOut(n)
				}
			default:
				return
			}
		}
	}
	for bus.deltas != nil || bus.figs != nil || bus.notifs != nil {
		select {
		case d, ok := <-bus.deltas:
			if !ok {
				bus.deltas = nil
				continue
			}
			if !a.isInterrupted() {
				a.fanOut(rpc.Notification{
					JSONRPC: "2.0",
					Method:  rpc.MethodDelta,
					Params:  rpc.DeltaParams{Text: d.Text, ContentType: d.Type},
				})
			}
		case n, ok := <-bus.notifs:
			if !ok {
				bus.notifs = nil
				continue
			}
			if !a.isInterrupted() {
				a.fanOut(n)
			}
		case m, ok := <-bus.figs:
			if !ok {
				bus.figs = nil
				continue
			}
			// Provider is done producing deltas (PushFigaro is the
			// last act). Drain anything still buffered before fanning
			// out MethodMessage so the CLI receives all deltas first.
			if bus.deltas != nil || bus.notifs != nil {
				drainPreFigOnce()
			}
			lastFig = m
			a.fanOutFigaro(m)
		}
	}
	sendErr := <-sendDone

	if a.isInterrupted() {
		a.fanOutError("interrupted")
		a.endTurn("interrupted")
		return true
	}
	if sendErr != nil {
		a.fanOutError(sendErr.Error())
		a.endTurn("error: " + sendErr.Error())
		return true
	}

	// Tool dispatch?
	calls := assistantToolCalls(lastFig)
	if len(calls) == 0 {
		stopReason := lastFig.StopReason
		if stopReason == "" {
			stopReason = message.StopEnd
		}
		a.endTurn(string(stopReason))
		return true
	}

	results := a.runTools(turnCtx, calls)
	if a.isInterrupted() {
		a.fanOutError("interrupted")
		a.endTurn("interrupted")
		return true
	}

	// Tool-result tic feeds the next round.
	resultTic := message.Message{
		Role:      message.RoleUser,
		Content:   results,
		Timestamp: time.Now().UnixMilli(),
	}
	if _, err := a.figStream.Append(store.Entry[message.Message]{Payload: resultTic}); err != nil {
		a.fanOutError(fmt.Sprintf("append tool_result tic: %s", err))
		a.endTurn("error: append tool_result")
		return true
	}
	return false
}

// runTools executes the assistant's tool_use blocks in parallel and
// returns the matching tool_result content blocks in input order.
// Tool output chunks fan out as MethodToolOutput; tool ends as
// MethodToolEnd. Cancellation via turnCtx propagates to each tool.
//
// For rounds with more than one tool call we bracket the run with
// MethodToolBatchStart / MethodToolBatchEnd notifications so the CLI
// can switch into a summary render mode. Single-tool rounds skip the
// batch notifications and keep their live-streaming UX.
func (a *Agent) runTools(turnCtx context.Context, calls []message.Content) []message.Content {
	type res struct {
		idx     int
		content []message.Content
		isErr   bool
	}
	ch := make(chan res, len(calls))

	isBatch := len(calls) > 1
	if isBatch {
		entries := make([]rpc.ToolBatchToolEntry, len(calls))
		for i, tc := range calls {
			entries[i] = rpc.ToolBatchToolEntry{
				ToolCallID: tc.ToolCallID,
				ToolName:   tc.ToolName,
				Arguments:  tc.Arguments,
			}
		}
		a.fanOut(rpc.Notification{
			JSONRPC: "2.0",
			Method:  rpc.MethodToolBatchStart,
			Params:  rpc.ToolBatchStartParams{Size: len(calls), Tools: entries},
		})
	}

	// Fan-out start notifications + spawn workers.
	for i, tc := range calls {
		figOtel.Event(turnCtx, "agent.tool_start.fanout_pre",
			attribute.String("tool", tc.ToolName),
			attribute.String("tool_call_id", tc.ToolCallID),
		)
		a.fanOut(rpc.Notification{
			JSONRPC: "2.0",
			Method:  rpc.MethodToolStart,
			Params: rpc.ToolStartParams{
				ToolCallID: tc.ToolCallID, ToolName: tc.ToolName,
				Arguments: tc.Arguments,
			},
		})
		figOtel.Event(turnCtx, "agent.tool_start.fanout_post",
			attribute.String("tool", tc.ToolName),
			attribute.String("tool_call_id", tc.ToolCallID),
		)
		go func(i int, tc message.Content) {
			figOtel.Event(turnCtx, "agent.tool.goroutine_enter",
				attribute.String("tool", tc.ToolName),
				attribute.String("tool_call_id", tc.ToolCallID),
			)
			t, ok := a.tools.Get(tc.ToolName)
			if !ok {
				ch <- res{i, []message.Content{message.TextContent(fmt.Sprintf("Unknown tool: %s", tc.ToolName))}, true}
				return
			}
			var firstChunk bool
			onChunk := func(chunk []byte) {
				if a.isInterrupted() {
					return
				}
				if !firstChunk {
					firstChunk = true
					figOtel.Event(turnCtx, "agent.tool.first_chunk",
						attribute.String("tool", tc.ToolName),
						attribute.String("tool_call_id", tc.ToolCallID),
						attribute.Int("bytes", len(chunk)),
					)
				}
				a.fanOut(rpc.Notification{
					JSONRPC: "2.0",
					Method:  rpc.MethodToolOutput,
					Params: rpc.ToolOutputParams{
						ToolCallID: tc.ToolCallID,
						ToolName:   tc.ToolName,
						Chunk:      string(chunk),
					},
				})
			}
			figOtel.Event(turnCtx, "agent.tool.execute_pre",
				attribute.String("tool", tc.ToolName),
				attribute.String("tool_call_id", tc.ToolCallID),
			)
			content, err := t.Execute(turnCtx, tc.Arguments, onChunk)
			figOtel.Event(turnCtx, "agent.tool.execute_post",
				attribute.String("tool", tc.ToolName),
				attribute.String("tool_call_id", tc.ToolCallID),
				attribute.Bool("err", err != nil),
			)
			if err != nil {
				ch <- res{i, []message.Content{message.TextContent(fmt.Sprintf("Error: %s", err))}, true}
				return
			}
			ch <- res{i, content, false}
		}(i, tc)
	}

	// Fan-in.
	results := make([]message.Content, len(calls))
	for n := 0; n < len(calls); n++ {
		r := <-ch
		tc := calls[r.idx]
		var resultText string
		for _, c := range r.content {
			if c.Type == message.ContentText {
				resultText += c.Text
			}
		}
		a.fanOut(rpc.Notification{
			JSONRPC: "2.0",
			Method:  rpc.MethodToolEnd,
			Params: rpc.ToolEndParams{
				ToolCallID: tc.ToolCallID, ToolName: tc.ToolName,
				Result: resultText, IsError: r.isErr,
			},
		})
		figOtel.Event(turnCtx, "agent.tool_end.fanout_post",
			attribute.String("tool", tc.ToolName),
			attribute.String("tool_call_id", tc.ToolCallID),
			attribute.Bool("err", r.isErr),
		)
		results[r.idx] = message.ToolResultContent(tc.ToolCallID, tc.ToolName, resultText, r.isErr)
	}
	if isBatch {
		a.fanOut(rpc.Notification{
			JSONRPC: "2.0",
			Method:  rpc.MethodToolBatchEnd,
			Params:  rpc.ToolBatchEndParams{Size: len(calls)},
		})
	}
	return results
}

// agent: what listens to these messages?
func (a *Agent) fanOutFigaro(m message.Message) {
	tail, ok := a.figStream.PeekTail()
	if !ok {
		return
	}
	stamped := tail.Payload
	stamped.LogicalTime = tail.LT
	ctx := a.turnCtx
	if ctx == nil {
		ctx = context.Background()
	}
	figOtel.Event(ctx, "agent.message.fanout_pre",
		attribute.Int64("logical_time", int64(tail.LT)),
	)
	a.fanOut(rpc.Notification{
		JSONRPC: "2.0",
		Method:  rpc.MethodMessage,
		Params:  rpc.MessageParams{LogicalTime: tail.LT, Message: stamped},
	})
	figOtel.Event(ctx, "agent.message.fanout_post",
		attribute.Int64("logical_time", int64(tail.LT)),
	)
}

func (a *Agent) fanOutError(msg string) {
	a.fanOut(rpc.Notification{
		JSONRPC: "2.0",
		Method:  rpc.MethodError,
		Params:  rpc.ErrorParams{Message: msg},
	})
}

// Is there a way to break apart our events such that interruption could be sent in as an
// event and we could guarantee all events run to completion and that our tasks are small
// and parallelizable and can't change global state?
func (a *Agent) isInterrupted() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.interrupted
}

func assistantToolCalls(m message.Message) []message.Content {
	if m.Role != message.RoleAssistant {
		return nil
	}
	var out []message.Content
	for _, c := range m.Content {
		if c.Type == message.ContentToolCall {
			out = append(out, c)
		}
	}
	return out
}

// combineChalkboardInput merges client-supplied chalkboard input with
// the persisted snapshot — see chalkboard.go's applyChalkboardInput
// for the original logic. Returns the patch to attach.
func (a *Agent) combineChalkboardInput(input *rpc.ChalkboardInput) chalkboard.Patch {
	if a.chalkboard == nil || input == nil {
		return chalkboard.Patch{}
	}
	var clientPatch chalkboard.Patch
	if input.Patch != nil {
		clientPatch = chalkboard.Patch{Set: input.Patch.Set, Remove: input.Patch.Remove}
	}
	snap := withoutSystemNS(a.chalkboard.Snapshot())
	switch {
	case input.Context != nil && input.Patch != nil:
		ctxSnap := withoutSystemNS(chalkboard.Snapshot(input.Context))
		return chalkboard.Merge(ctxSnap.Diff(snap), clientPatch)
	case input.Context != nil:
		ctxSnap := withoutSystemNS(chalkboard.Snapshot(input.Context))
		return ctxSnap.Diff(snap)
	case input.Patch != nil:
		return clientPatch
	}
	return chalkboard.Patch{}
}

func statusOf(err error) string {
	if err != nil {
		return "failure"
	}
	return "success"
}
