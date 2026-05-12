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

// turnBus is the per-turn provider.Bus. Buffered channels, never-block
// pushes. Closed after provider.Send returns.
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
		// Drop if full.
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
		// Drop if full.
	}
}

// runTurn drives one prompt to completion: user tic, provider.Send,
// tool dispatch, repeat until done/interrupt/error.
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

	// Build user tic.
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
		// Bootstrap: system.prompt, system.skills.
		if a.outfitter != nil {
			if patch, err := a.outfitter.Bootstrap(a.chalkboard.Snapshot(),
				outfit.CurrentBootCtx(a.prov.Name(), a.id)); err == nil && !patch.IsEmpty() {
				tic.Patches = append(tic.Patches, patch)
				a.chalkboard.Apply(patch)
			}
		}
		// Allowlisted env vars -> system.environment.*.
		if envPatch := chalkboard.EnvironmentPatch(); !envPatch.IsEmpty() {
			tic.Patches = append(tic.Patches, envPatch)
			a.chalkboard.Apply(envPatch)
		}
		_ = a.chalkboard.Save()
	}
	if prompt.text != "" {
		tic.Content = append(tic.Content, message.TextContent(prompt.text))
	}
	// Belt-and-suspenders: if a prior turn died after the assistant
	// tool_use was logged but before tool_results were appended, the
	// IR still has a dangling tool_use at the tail. Boot-time repair
	// usually catches this, but cover the case where the boot check
	// missed (e.g. dangling state appeared after boot).
	appendInterruptSentinelIfDangling(a.figStream, a.id)
	if _, err := a.figStream.Append(store.Entry[message.Message]{Payload: tic}); err != nil {
		a.fanOutError(fmt.Sprintf("append user tic: %s", err))
		a.endTurn("error: append tic")
		return
	}

	// Drive: provider -> tools -> repeat.
	for {
		stop := a.driveOneRound(turnCtx)
		if stop {
			return
		}
	}
}

// driveOneRound runs one provider.Send + tool dispatch cycle.
// Returns true when the turn is complete, false when more rounds needed.
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

	// Drain deltas + notifs + figaro. Wire-order: MethodMessage must
	// arrive after all MethodDelta/MethodToolUse* for the turn.
	// PushFigaro is the producer's last act, so when we see a fig
	// we drain remaining deltas/notifs before fanning out MethodMessage.
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
			// Drain remaining deltas before fanning out MethodMessage.
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

	// Always append tool_results to match tool_use blocks, even on
	// interrupt. Fill missing slots with synthetic error results.
	if a.isInterrupted() {
		for i, tc := range calls {
			if results[i].Type == "" {
				results[i] = message.ToolResultContent(
					tc.ToolCallID, tc.ToolName,
					"interrupted: tool execution was cancelled",
					true,
				)
			}
		}
	}


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

	if a.isInterrupted() {
		a.fanOutError("interrupted")
		a.endTurn("interrupted")
		return true
	}
	return false
}

// runTools executes tool_use blocks in parallel and returns matching
// tool_result blocks. Multi-tool rounds are bracketed with batch
// start/end notifications.
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

// TODO: could interruption be an event with guaranteed completion?
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

// combineChalkboardInput merges client-supplied chalkboard input
// with the persisted snapshot.
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
