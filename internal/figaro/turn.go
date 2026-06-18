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

// busEventKind tags one ordered event from the provider Bus.
type busEventKind int

const (
	evDelta     busEventKind = iota // text/thinking content delta
	evToolStart                     // tool_invoke block opened
	evToolArgs                      // tool_invoke partial argument JSON
	evToolReady                     // tool_invoke arguments decoded
	evFigaro                        // assembled message (provider appended it)
)

// busEvent is one provider Bus call, carried in order to the drain
// loop so it can fold the open tail message single-threaded.
type busEvent struct {
	kind    busEventKind
	content message.Content
	id      string
	name    string
	partial string
	args    map[string]interface{}
	msg     message.Message
}

// turnBus is the per-turn provider.Bus. events carries the ordered
// stream the drain loop folds into the open tail; toolsReady feeds the
// speculative dispatcher. events is blocking (no drop) so the open
// message never loses content; toolsReady is best-effort (the post-stream
// reconciliation re-dispatches any dropped call).
type turnBus struct {
	events     chan busEvent
	toolsReady chan message.Content
}

func newTurnBus() *turnBus {
	return &turnBus{
		events:     make(chan busEvent, 256),
		toolsReady: make(chan message.Content, 64),
	}
}

func (b *turnBus) PushDelta(c message.Content) { b.events <- busEvent{kind: evDelta, content: c} }

func (b *turnBus) PushFigaro(m message.Message) { b.events <- busEvent{kind: evFigaro, msg: m} }

func (b *turnBus) PushToolInvokeStart(toolCallID, toolName string) {
	b.events <- busEvent{kind: evToolStart, id: toolCallID, name: toolName}
}

func (b *turnBus) PushToolInvokeDelta(toolCallID, partialJSON string) {
	b.events <- busEvent{kind: evToolArgs, id: toolCallID, partial: partialJSON}
}

// PushMessageEnd is a no-op under the log.* model: the stop reason rides
// the sealed message.Message (log.entry), so there is no separate
// pre-seal metadata frame.
func (b *turnBus) PushMessageEnd(string) {}

// PushToolReady records the decoded invocation in the open message and
// arms speculative dispatch.
func (b *turnBus) PushToolReady(call message.Content) {
	b.events <- busEvent{kind: evToolReady, id: call.ToolCallID, name: call.ToolName, args: call.Arguments}
	select {
	case b.toolsReady <- call:
	default:
		// Buffer full — drop. driveOneRound re-dispatches any call in
		// the sealed assistant message that wasn't dispatched here.
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
	if sentinel, ok := appendInterruptSentinelIfDangling(a.figLog, a.id); ok {
		a.fanLogEntry(sentinel)
	}
	stamped, err := a.figLog.Append(store.Entry[message.Message]{Payload: tic})
	if err != nil {
		a.endTurn(fmt.Sprintf("error: append tic: %s", err))
		return
	}
	a.fanLogEntry(stamped)

	// Drive: provider -> tools -> repeat.
	for {
		stop := a.driveOneRound(turnCtx)
		if stop {
			return
		}
	}
}

// driveOneRound runs one provider.Send + tool dispatch cycle. The
// assistant reply streams as an open message that seals into a log
// entry; if it called tools, their execution streams as an open
// tool_result message that seals in turn. Returns true when the turn
// is complete, false when another round is needed.
func (a *Agent) driveOneRound(turnCtx context.Context) (done bool) {
	bus := newTurnBus()
	in := provider.SendInput{
		AriaID:    a.id,
		FigLog:    a.figLog,
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
			close(bus.events)
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

	// Provisional index for the assistant message. Gapless single-writer:
	// the provider appends at exactly tail+1.
	assistantIdx := a.nextIndex()

	// Speculative tool dispatcher: every PushToolReady kicks off the
	// tool's goroutine immediately, in parallel with the still-running
	// LLM stream. Tool lifecycle events flow back through toolEvents so
	// the drain loop folds them into one open tool_result message.
	toolEvents := make(chan toolEvent, 64)
	spec := newSpecDispatcher(toolEvents)
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

	// Phase 1: fold the assistant stream into an open message. Tool
	// lifecycle events that arrive speculatively (before the assistant
	// seals) are buffered — at most one message is open at a time.
	ab := newOpenBuilder(assistantIdx, message.RoleAssistant)
	var toolBuf []toolEvent
	events := bus.events
	for events != nil {
		select {
		case ev, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			switch ev.kind {
			case evDelta:
				ab.addText(ev.content.Type, ev.content.Text)
			case evToolStart:
				ab.toolOpen(ev.id, ev.name)
			case evToolArgs:
				ab.toolArgs(ev.id, ev.partial)
			case evToolReady:
				ab.toolReady(ev.id, ev.name, ev.args)
			case evFigaro:
				// Provider has appended; the seal is handled after the
				// loop from the durable tail.
			}
			if !a.isInterrupted() && ab.dirty() {
				a.fanOpen(ab)
			}
		case te := <-toolEvents:
			toolBuf = append(toolBuf, te)
		}
	}
	sendErr := <-sendDone

	// Seal the assistant message from the durable tail.
	var lastFig message.Message
	sealEntry, sealed := a.sealedTail(assistantIdx, message.RoleAssistant)
	if sealed {
		lastFig = sealEntry.Payload
		a.fanLogEntry(sealEntry)
	}

	if a.isInterrupted() {
		if !sealed {
			a.fanAbort(assistantIdx, string(message.InterruptUserInterrupt))
		}
		a.endTurn("interrupted")
		return true
	}
	if sendErr != nil {
		if !sealed {
			a.fanAbort(assistantIdx, string(message.InterruptFault))
		}
		a.endTurn("error: " + sendErr.Error())
		return true
	}
	if !sealed {
		// Empty response: the provider returned without appending.
		<-specDone
		a.endTurn(string(message.StopEnd))
		return true
	}

	calls := assistantToolInvokes(lastFig)
	if len(calls) == 0 {
		<-specDone
		stopReason := lastFig.StopReason
		if stopReason == "" {
			stopReason = message.StopEnd
		}
		a.endTurn(string(stopReason))
		return true
	}

	// Wait for the speculative dispatcher to drain toolsReady (closed
	// once Send returned, already true here).
	<-specDone

	// Phase 2: tool execution as an open tool_result message at the
	// next index.
	resultTic := a.streamToolResults(turnCtx, assistantIdx+1, calls, spec, toolEvents, toolBuf)

	stamped, err := a.figLog.Append(store.Entry[message.Message]{Payload: resultTic})
	if err != nil {
		a.fanAbort(assistantIdx+1, string(message.InterruptFault))
		a.endTurn(fmt.Sprintf("error: append tool_result: %s", err))
		return true
	}
	a.fanLogEntry(stamped)

	if a.isInterrupted() {
		a.endTurn("interrupted")
		return true
	}
	return false
}

// streamToolResults dispatches every call (idempotent), folds the
// tools' execution lifecycle into one open tool_result message at
// resultIdx, and returns the sealed tool_result tic in canonical
// (calls) order. toolBuf holds events that arrived during phase 1.
func (a *Agent) streamToolResults(
	turnCtx context.Context,
	resultIdx uint64,
	calls []message.Content,
	spec *specDispatcher,
	toolEvents chan toolEvent,
	toolBuf []toolEvent,
) message.Message {
	expect := make(map[string]bool, len(calls))
	for _, tc := range calls {
		if p := spec.dispatch(turnCtx, a, tc); p != nil {
			expect[tc.ToolCallID] = true
		}
	}

	rb := newOpenBuilder(resultIdx, message.RoleUser)
	for _, tc := range calls {
		if expect[tc.ToolCallID] {
			rb.resultOpen(tc.ToolCallID, tc.ToolName)
		}
	}
	if !a.isInterrupted() && rb.dirty() {
		a.fanOpen(rb)
	}

	outcomes := make(map[string]toolOutcome, len(calls))
	process := func(te toolEvent) {
		switch te.kind {
		case toolBegin:
			// Block already opened above.
		case toolChunk:
			rb.resultChunk(te.id, te.chunk)
		case toolEnd:
			rb.resultFinal(te.id, te.final)
			outcomes[te.id] = te.outcome
		}
		if !a.isInterrupted() && rb.dirty() {
			a.fanOpen(rb)
		}
	}
	for _, te := range toolBuf {
		process(te)
	}
	for len(outcomes) < len(expect) {
		te, ok := <-toolEvents
		if !ok {
			break
		}
		process(te)
	}

	results := make([]message.Content, len(calls))
	for i, tc := range calls {
		if !expect[tc.ToolCallID] {
			results[i] = message.ToolResultContent(tc.ToolCallID, tc.ToolName, "Error: missing tool_call_id", true)
			continue
		}
		oc := outcomes[tc.ToolCallID]
		var text string
		for _, c := range oc.content {
			if c.Type == message.ContentText {
				text += c.Text
			}
		}
		results[i] = message.ToolResultContent(tc.ToolCallID, tc.ToolName, text, oc.isErr)
	}
	// On interrupt, any tool that never produced a result gets a
	// synthetic error so the tool_use/tool_result pairing stays intact.
	if a.isInterrupted() {
		for i, tc := range calls {
			if results[i].Type == "" {
				results[i] = message.ToolResultContent(
					tc.ToolCallID, tc.ToolName,
					"interrupted: tool execution was cancelled", true)
			}
		}
	}
	return message.Message{
		Role:      message.RoleUser,
		Content:   results,
		Timestamp: time.Now().UnixMilli(),
	}
}

// nextIndex returns the LT the next appended message will occupy.
func (a *Agent) nextIndex() uint64 {
	if e, ok := a.figLog.PeekTail(); ok {
		return e.LT + 1
	}
	return 1
}

// sealedTail returns the durable tail entry iff it sits at expectIdx
// with the expected role — i.e. the provider actually appended it.
func (a *Agent) sealedTail(expectIdx uint64, role message.Role) (store.Entry[message.Message], bool) {
	e, ok := a.figLog.PeekTail()
	if !ok || e.LT != expectIdx || e.Payload.Role != role {
		return store.Entry[message.Message]{}, false
	}
	return e, true
}

// toolEventKind tags one tool execution lifecycle event.
type toolEventKind int

const (
	toolBegin toolEventKind = iota
	toolChunk
	toolEnd
)

// toolEvent carries one tool's execution lifecycle back to the drain
// loop, which folds it into the open tool_result message.
type toolEvent struct {
	kind    toolEventKind
	id      string
	name    string
	chunk   string
	final   message.Content // toolEnd: the sealed tool_result block
	outcome toolOutcome     // toolEnd: raw content for IR assembly
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
// signals PushToolReady, well before the LLM stream completes, and
// reports each tool's lifecycle on events. Dispatch is idempotent per
// tool_call_id, so the post-stream reconciliation pass can call
// dispatch() for every call without double-launching.
type specDispatcher struct {
	mu      sync.Mutex
	pending map[string]*toolPending
	events  chan toolEvent
}

func newSpecDispatcher(events chan toolEvent) *specDispatcher {
	return &specDispatcher{pending: make(map[string]*toolPending), events: events}
}

// dispatch launches a goroutine for tc unless one is already in flight
// for that tool_call_id. The goroutine runs the tool and reports
// toolBegin / toolChunk / toolEnd on s.events; the drain loop folds
// those into the open tool_result message.
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
		s.events <- toolEvent{kind: toolBegin, id: tc.ToolCallID, name: tc.ToolName}

		emitEnd := func(oc toolOutcome) {
			var text string
			for _, c := range oc.content {
				if c.Type == message.ContentText {
					text += c.Text
				}
			}
			p.outcome = oc
			s.events <- toolEvent{
				kind:    toolEnd,
				id:      tc.ToolCallID,
				name:    tc.ToolName,
				final:   message.ToolResultContent(tc.ToolCallID, tc.ToolName, text, oc.isErr),
				outcome: oc,
			}
		}

		t, ok := a.tools.Get(tc.ToolName)
		if !ok {
			emitEnd(toolOutcome{
				content: []message.Content{message.TextContent(fmt.Sprintf("Unknown tool: %s", tc.ToolName))},
				isErr:   true,
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
			s.events <- toolEvent{kind: toolChunk, id: tc.ToolCallID, name: tc.ToolName, chunk: string(chunk)}
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
			emitEnd(toolOutcome{
				content: []message.Content{message.TextContent(fmt.Sprintf("Error: %s", err))},
				isErr:   true,
			})
			return
		}
		emitEnd(toolOutcome{content: content, isErr: false})
	}()
	return p
}

// --- fan-out helpers (log.* frames) ---

// fanOpen publishes the current open-tail state as a full-mode
// OpenEntry and records it on the agent for follow-reads and recovery.
func (a *Agent) fanOpen(b *openBuilder) {
	e := b.snapshot()
	a.mu.Lock()
	a.openIdx = e.Index
	a.openVer = e.Version
	a.openMsg = e.Message
	a.openLive = true
	a.mu.Unlock()
	a.fanOut(rpc.Notification{JSONRPC: "2.0", Method: rpc.MethodLogOpen, Params: e})
}

// fanLogEntry publishes a sealed message and clears the open slot if
// this entry seals it.
func (a *Agent) fanLogEntry(e store.Entry[message.Message]) {
	m := e.Payload
	m.LogicalTime = e.LT
	a.mu.Lock()
	if a.openIdx == e.LT {
		a.clearOpenLocked()
	}
	a.mu.Unlock()
	a.fanOut(rpc.Notification{
		JSONRPC: "2.0",
		Method:  rpc.MethodLogEntry,
		Params:  rpc.LogEntry{Index: e.LT, Message: m},
	})
}

// fanAbort burns a never-sealed open tail.
func (a *Agent) fanAbort(index uint64, reason string) {
	a.mu.Lock()
	if a.openIdx == index {
		a.clearOpenLocked()
	}
	a.mu.Unlock()
	a.fanOut(rpc.Notification{
		JSONRPC: "2.0",
		Method:  rpc.MethodLogAbort,
		Params:  rpc.AbortEntry{Index: index, Reason: reason},
	})
}

// clearOpenLocked resets the open-tail snapshot. Caller holds a.mu.
func (a *Agent) clearOpenLocked() {
	a.openLive = false
	a.openIdx = 0
	a.openVer = 0
	a.openMsg = message.Message{}
}

// isInterrupted reports whether the current turn was interrupted.
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
