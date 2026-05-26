package figaro

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime/debug"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/message"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/store"
)

// turnBus is the per-turn provider.Bus. Buffered channels, never-block
// pushes. Closed after provider.Send returns.
type turnBus struct {
	deltas     chan message.Content
	figs       chan message.Message
	notifs     chan rpc.Notification
	toolsReady chan message.Content
}

func newTurnBus() *turnBus {
	return &turnBus{
		deltas:     make(chan message.Content, 64),
		figs:       make(chan message.Message, 4),
		notifs:     make(chan rpc.Notification, 256),
		toolsReady: make(chan message.Content, 64),
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

func (b *turnBus) PushToolInvokeStart(toolCallID, toolName string) {
	b.pushNotif(rpc.Notification{
		JSONRPC: "2.0",
		Method:  rpc.MethodToolInvokeStart,
		Params:  rpc.ToolInvokeStartParams{ToolCallID: toolCallID, ToolName: toolName},
	})
}

func (b *turnBus) PushToolInvokeDelta(toolCallID, partialJSON string) {
	b.pushNotif(rpc.Notification{
		JSONRPC: "2.0",
		Method:  rpc.MethodToolInvokeDelta,
		Params:  rpc.ToolInvokeDeltaParams{ToolCallID: toolCallID, PartialJSON: partialJSON},
	})
}

// PushMessageEnd fans out the wire-level message_end notification
// announcing the stop reason. Fires before PushFigaro so the CLI has
// the metadata it needs to settle rendering decisions (solo vs
// batch) before the full payload arrives.
func (b *turnBus) PushMessageEnd(stopReason string) {
	b.pushNotif(rpc.Notification{
		JSONRPC: "2.0",
		Method:  rpc.MethodMessageEnd,
		Params:  rpc.MessageEndParams{StopReason: stopReason},
	})
}

func (b *turnBus) pushNotif(n rpc.Notification) {
	select {
	case b.notifs <- n:
	default:
		// Drop if full.
	}
}

// PushToolReady is the speculative-dispatch hook: providers call this
// at content_block_stop on a tool_use block so the harness can start
// executing the tool before the stream finishes. Best-effort; harness
// also reconciles against the final assembled message in PushFigaro.
//
// Also fans out a MethodToolInvokeReady wire notification so the CLI
// learns that the model has finished authoring this invocation — the
// signal it uses to settle solo-vs-batch rendering decisions.
func (b *turnBus) PushToolReady(call message.Content) {
	b.pushNotif(rpc.Notification{
		JSONRPC: "2.0",
		Method:  rpc.MethodToolInvokeReady,
		Params: rpc.ToolInvokeReadyParams{
			ToolCallID: call.ToolCallID,
			ToolName:   call.ToolName,
			Arguments:  call.Arguments,
		},
	})
	select {
	case b.toolsReady <- call:
	default:
		// Buffer full — drop. The post-stream reconciliation pass
		// in driveOneRound will dispatch any tool_calls in lastFig
		// that we didn't dispatch speculatively.
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
	var combined chalkboard.Patch
	if prompt.chalkboard != nil {
		combined = a.combineChalkboardInput(prompt.chalkboard)
	}
	if len(a.figLog.Read()) == 0 && a.chalkboard != nil {
		// First-turn bootstrap: fold the boot patch (loadout values +
		// runtime fill-ins), the env-var capture, and any client input
		// into a single patch that rides on this tic. The chalkboard
		// renderer then projects each key as a <system-reminder> block
		// on the wire — that's how the model learns about skills,
		// credo, cwd, etc.
		boot := chalkboard.Patch{Set: map[string]json.RawMessage{}}
		if a.bootPatch != nil && !a.bootPatch.IsEmpty() {
			for k, v := range a.bootPatch.Set {
				boot.Set[k] = v
			}
			boot.Remove = append(boot.Remove, a.bootPatch.Remove...)
		}
		// Runtime fill-ins. The loadout may not have these; the agent
		// stamps them at first-turn from its own process state.
		cwd, _ := os.Getwd()
		if _, ok := boot.Set["system.cwd"]; !ok {
			if b, err := json.Marshal(cwd); err == nil {
				boot.Set["system.cwd"] = b
			}
		}
		if _, ok := boot.Set["system.root"]; !ok {
			if b, err := json.Marshal(cwd); err == nil {
				boot.Set["system.root"] = b
			}
		}
		if _, ok := boot.Set["system.max_tokens"]; !ok {
			boot.Set["system.max_tokens"] = json.RawMessage(`8192`)
		}
		// Allowlisted env vars -> system.environment.*.
		if envPatch := chalkboard.EnvironmentPatch(); !envPatch.IsEmpty() {
			for k, v := range envPatch.Set {
				boot.Set[k] = v
			}
		}
		// Client input wins on conflict — they explicitly named the key.
		if !combined.IsEmpty() {
			for k, v := range combined.Set {
				boot.Set[k] = v
			}
			boot.Remove = append(boot.Remove, combined.Remove...)
		}
		a.bootPatch = nil // consumed; never re-applied
		if !boot.IsEmpty() {
			tic.Patches = append(tic.Patches, boot)
			a.chalkboard.Apply(boot)
		}
		_ = a.chalkboard.Save()
	} else if !combined.IsEmpty() {
		tic.Patches = append(tic.Patches, combined)
		a.chalkboard.Apply(combined)
	}
	if prompt.text != "" {
		tic.Content = append(tic.Content, message.TextContent(prompt.text))
	}
	// Belt-and-suspenders: if a prior turn died after the assistant
	// tool_use was logged but before tool_results were appended, the
	// IR still has a dangling tool_use at the tail. Boot-time repair
	// usually catches this, but cover the case where the boot check
	// missed (e.g. dangling state appeared after boot).
	appendInterruptSentinelIfDangling(a.figLog, a.id)
	if _, err := a.figLog.Append(store.Entry[message.Message]{Payload: tic}); err != nil {
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
		FigLog: a.figLog,
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
			close(bus.toolsReady)
		}()
		started := time.Now()
		err := a.prov.Send(turnCtx, in, bus)
		figOtel.RecordRequestDuration(turnCtx, time.Since(started),
			attribute.String("provider", a.prov.Name()),
			attribute.String("model", a.currentModel()),
			attribute.String("status", statusOf(err)))
		sendDone <- err
	}()

	// Speculative tool dispatcher: every PushToolReady kicks off the
	// tool's goroutine immediately, in parallel with the still-running
	// LLM stream. Results land in `spec` keyed by tool_call_id; the
	// post-stream collector in runTools reconciles against lastFig.
	spec := newSpecDispatcher()
	specDone := make(chan struct{})
	go func() {
		defer close(specDone)
		for tc := range bus.toolsReady {
			if a.isInterrupted() {
				continue
			}
			spec.dispatch(turnCtx, a, tc)
		}
	}()

	// Drain deltas + notifs + figaro. Wire-order: MethodMessage must
	// arrive after all MethodDelta/MethodToolInvoke*/MethodMessageEnd
	// for the turn. PushFigaro is the producer's last act, so when we
	// see a fig we drain remaining deltas/notifs before fanning out
	// MethodMessage.
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


	calls := assistantToolInvokes(lastFig)
	if len(calls) == 0 {
		// No tools to run; drain the speculative dispatcher (it shouldn't
		// have anything, since no tool_use blocks landed) and finish.
		<-specDone
		stopReason := lastFig.StopReason
		if stopReason == "" {
			stopReason = message.StopEnd
		}
		a.endTurn(string(stopReason))
		return true
	}

	// Wait for the speculative dispatcher to consume the closed
	// toolsReady channel (it's only closed once Send returns, which is
	// already true at this point). After this point spec.results is
	// safe to read for any ID we know about.
	<-specDone

	results := a.runTools(turnCtx, calls, spec)

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
	if _, err := a.figLog.Append(store.Entry[message.Message]{Payload: resultTic}); err != nil {
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

// toolOutcome holds the result of a single dispatched tool execution.
type toolOutcome struct {
	content []message.Content
	isErr   bool
}

// toolPending tracks one in-flight (or completed) speculative tool.
type toolPending struct {
	call    message.Content
	done    chan struct{}
	outcome toolOutcome // valid after done is closed
}

// specDispatcher kicks off tool executions as soon as a provider
// signals PushToolReady, well before the LLM stream completes. It is
// the producer half of Slice A's parallelism story; runTools is the
// consumer that emits the wire notifications in canonical order.
//
// Dispatch is idempotent per tool_call_id: a second call with the same
// ID is a no-op. This lets runTools' reconciliation pass call
// dispatch() for every call in lastFig without worrying about whether
// the provider already emitted PushToolReady for it.
type specDispatcher struct {
	mu      sync.Mutex
	pending map[string]*toolPending
}

func newSpecDispatcher() *specDispatcher {
	return &specDispatcher{pending: make(map[string]*toolPending)}
}

// dispatch launches a goroutine for tc unless one is already in
// flight for that tool_call_id. Returns the toolPending so callers
// can wait on completion. The goroutine owns the full wire-level
// execution lifecycle for this tool: it fans out MethodToolStart,
// streams MethodToolOutput chunks from the tool's onChunk, then
// fans out MethodToolEnd before signaling completion via p.done.
// runTools only consumes the resulting toolPending for IR assembly
// — it does not emit wire events for execution.
func (s *specDispatcher) dispatch(turnCtx context.Context, a *Agent, tc message.Content) *toolPending {
	if tc.Type != message.ContentToolInvoke || tc.ToolCallID == "" {
		return nil
	}
	s.mu.Lock()
	if p, ok := s.pending[tc.ToolCallID]; ok {
		s.mu.Unlock()
		return p
	}
	p := &toolPending{call: tc, done: make(chan struct{})}
	s.pending[tc.ToolCallID] = p
	s.mu.Unlock()

	go func() {
		defer close(p.done)
		figOtel.Event(turnCtx, "agent.tool.goroutine_enter",
			attribute.String("tool", tc.ToolName),
			attribute.String("tool_call_id", tc.ToolCallID),
			attribute.Bool("speculative", true),
		)

		// Execution-lifecycle wire: tool_start fires the moment we
		// begin running. Output streams from onChunk. tool_end fires
		// just before we signal completion. This is what makes the
		// CLI see "tool 1 started, tool 1 finished" interleaved with
		// other tools whose invokes are still arriving — the wire
		// reflects execution truth.
		a.fanOut(rpc.Notification{
			JSONRPC: "2.0",
			Method:  rpc.MethodToolStart,
			Params: rpc.ToolStartParams{
				ToolCallID: tc.ToolCallID,
				ToolName:   tc.ToolName,
				Arguments:  tc.Arguments,
			},
		})

		t, ok := a.tools.Get(tc.ToolName)
		if !ok {
			p.outcome = toolOutcome{
				content: []message.Content{message.TextContent(fmt.Sprintf("Unknown tool: %s", tc.ToolName))},
				isErr:   true,
			}
			a.fanOut(rpc.Notification{
				JSONRPC: "2.0",
				Method:  rpc.MethodToolEnd,
				Params: rpc.ToolEndParams{
					ToolCallID: tc.ToolCallID, ToolName: tc.ToolName,
					Result: fmt.Sprintf("Unknown tool: %s", tc.ToolName), IsError: true,
				},
			})
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
		var resultText string
		var isErr bool
		if err != nil {
			resultText = fmt.Sprintf("Error: %s", err)
			isErr = true
			p.outcome = toolOutcome{
				content: []message.Content{message.TextContent(resultText)},
				isErr:   true,
			}
		} else {
			for _, c := range content {
				if c.Type == message.ContentText {
					resultText += c.Text
				}
			}
			p.outcome = toolOutcome{content: content, isErr: false}
		}

		a.fanOut(rpc.Notification{
			JSONRPC: "2.0",
			Method:  rpc.MethodToolEnd,
			Params: rpc.ToolEndParams{
				ToolCallID: tc.ToolCallID,
				ToolName:   tc.ToolName,
				Result:     resultText,
				IsError:    isErr,
			},
		})
	}()
	return p
}

// runTools assembles tool_result content blocks in canonical order
// for the next user-role tic. It does not emit wire events — the
// speculative dispatcher owns the entire execution-lifecycle wire
// (MethodToolStart, MethodToolOutput, MethodToolEnd) from inside
// each tool's goroutine. Any call that wasn't speculatively
// dispatched (provider didn't emit PushToolReady, or the buffer
// dropped it) is dispatched here as a fallback.
func (a *Agent) runTools(turnCtx context.Context, calls []message.Content, spec *specDispatcher) []message.Content {
	pendings := make([]*toolPending, len(calls))
	for i, tc := range calls {
		pendings[i] = spec.dispatch(turnCtx, a, tc)
	}

	results := make([]message.Content, len(calls))
	for i, tc := range calls {
		p := pendings[i]
		if p == nil {
			// Malformed call (no ID); synthesize an error result. The
			// dispatcher refused this call, so no wire events were
			// emitted for it — leave it that way; the IR still gets
			// a well-formed pair.
			results[i] = message.ToolResultContent(tc.ToolCallID, tc.ToolName,
				"Error: missing tool_call_id", true)
			continue
		}
		<-p.done
		var resultText string
		for _, c := range p.outcome.content {
			if c.Type == message.ContentText {
				resultText += c.Text
			}
		}
		results[i] = message.ToolResultContent(tc.ToolCallID, tc.ToolName, resultText, p.outcome.isErr)
	}
	return results
}


func (a *Agent) fanOutFigaro(m message.Message) {
	tail, ok := a.figLog.PeekTail()
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

func assistantToolInvokes(m message.Message) []message.Content {
	if m.Role != message.RoleAssistant {
		return nil
	}
	var out []message.Content
	for _, c := range m.Content {
		if c.Type == message.ContentToolInvoke {
			out = append(out, c)
		}
	}
	return out
}

// combineChalkboardInput merges client-supplied chalkboard input
// with the persisted snapshot.
//
// Two shapes, two contracts:
//
//   - Context is *purely additive*. It carries the client's view of
//     state-at-send-time; the agent sets keys whose values differ
//     from the snapshot but never derives removals from absence.
//     This lets clients ship a full chalkboard copy without racing
//     concurrent set/unset from another shell.
//   - Patch is explicit set + remove; mutations the client really
//     means. `figaro set`/`unset`/`loadout` land here.
//
// system.* on Context is dropped: the harness owns that namespace,
// and a stale client view must not clobber it. Patch is left intact
// (it's the user explicitly mutating; trust them).
func (a *Agent) combineChalkboardInput(input *rpc.ChalkboardInput) chalkboard.Patch {
	if a.chalkboard == nil || input == nil {
		return chalkboard.Patch{}
	}
	var clientPatch chalkboard.Patch
	if input.Patch != nil {
		clientPatch = chalkboard.Patch{Set: input.Patch.Set, Remove: input.Patch.Remove}
	}
	var ctxPatch chalkboard.Patch
	if input.Context != nil {
		ctx := withoutSystemNS(chalkboard.Snapshot(input.Context))
		snap := a.chalkboard.Snapshot()
		ctxPatch = additivePatch(ctx, snap)
	}
	switch {
	case !ctxPatch.IsEmpty() && !clientPatch.IsEmpty():
		return chalkboard.Merge(ctxPatch, clientPatch)
	case !ctxPatch.IsEmpty():
		return ctxPatch
	case !clientPatch.IsEmpty():
		return clientPatch
	}
	return chalkboard.Patch{}
}

// additivePatch returns a Set-only patch with ctx entries whose
// values differ from snap. Keys present in snap but absent from ctx
// are NOT removed — Context is purely additive by contract.
func additivePatch(ctx, snap chalkboard.Snapshot) chalkboard.Patch {
	var p chalkboard.Patch
	for k, v := range ctx {
		if old, ok := snap[k]; ok && bytes.Equal(old, v) {
			continue
		}
		if p.Set == nil {
			p.Set = map[string]json.RawMessage{}
		}
		p.Set[k] = v
	}
	return p
}

func statusOf(err error) string {
	if err != nil {
		return "failure"
	}
	return "success"
}
