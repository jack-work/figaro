package figaro

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/compose"
	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/message"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/toolout"
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
	cache   *provider.AssistantCache
	ack     chan error
}

// turnBus is the per-turn provider.Bus. events carries the ordered
// stream the drain loop folds into the open tail; toolsReady feeds the
// speculative dispatcher. events is blocking (no drop) so the open
// message never loses content; toolsReady is best-effort (the post-stream
// reconciliation re-dispatches any dropped call).
type turnBus struct {
	events     chan busEvent
	toolsReady chan message.Content
	ctx        context.Context
}

func newTurnBus(ctx context.Context) *turnBus {
	return &turnBus{
		events:     make(chan busEvent, 256),
		toolsReady: make(chan message.Content, 64),
		ctx:        ctx,
	}
}

func (b *turnBus) PushDelta(c message.Content) { b.events <- busEvent{kind: evDelta, content: c} }

func (b *turnBus) PushFigaro(m message.Message, caches ...provider.AssistantCache) {
	if len(caches) > 1 {
		panic("provider supplied multiple assistant cache payloads")
	}
	var cache *provider.AssistantCache
	if len(caches) == 1 {
		copy := caches[0]
		cache = &copy
	}
	ack := make(chan error, 1)
	select {
	case b.events <- busEvent{kind: evFigaro, msg: m, cache: cache, ack: ack}:
	case <-b.ctx.Done():
		panic(b.ctx.Err())
	}
	select {
	case err := <-ack:
		if err != nil {
			panic(err)
		}
	case <-b.ctx.Done():
		panic(b.ctx.Err())
	}
}

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

// runTurn drives one prompt to completion: user turn, provider.Send,
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

	// Belt-and-suspenders: if a prior turn died after the assistant
	// tool_use was logged but before tool_results were appended, the
	// IR still has a dangling tool_use at the tail. Boot-time repair
	// usually catches this, but cover the case where the boot check
	// missed (e.g. dangling state appeared after boot).
	repairInterruptedTail(a.figLog, a.id)
	if _, err := a.appendUserPrompt(prompt, true); err != nil {
		a.endTurn(fmt.Sprintf("error: append message: %s", err))
		return
	}
	a.startAssistantUnit()

	// Drive: provider -> tools -> repeat.
	allowSteering := false
	for {
		stop := a.driveOneRound(turnCtx, allowSteering)
		if stop {
			return
		}
		allowSteering = true
	}
}

// appendUserPrompt persists one external prompt as its own canonical user
// message and matching committed UI unit.
func (a *Agent) appendUserPrompt(prompt event, allowInlineBoot bool) (store.Entry[message.Message], error) {
	msg := message.Message{
		Role:      message.RoleUser,
		Timestamp: time.Now().UnixMilli(),
	}
	var combined chalkboard.Patch
	if prompt.chalkboard != nil {
		combined = a.combineChalkboardInput(prompt.chalkboard)
	}
	// Seed the mantra from the first user message's opening text, so every
	// conversation has a stable title (the first n chars) without the agent
	// having to set one. Only when unset, so it stays fixed to the opener.
	if prompt.text != "" && a.chalkboardString("mantra") == "" {
		if combined.Set == nil {
			combined.Set = map[string]json.RawMessage{}
		}
		mv, _ := json.Marshal(firstChars(prompt.text, 60))
		combined.Set["mantra"] = mv
	}
	if !combined.IsEmpty() {
		if a.backend != nil {
			if err := a.backend.ApplyChalkboard(a.id, combined); err != nil {
				slog.Error("turn chalkboard append", "aria", a.id, "err", err)
			}
		} else {
			msg.Patches = append(msg.Patches, combined)
		}
		a.chalkboard.Apply(combined)
	}
	// Ephemeral first message: fold the boot patch inline so the loadout
	// reminders render (no channel to hold the transition). State is
	// already seeded by the caller, so this is render-only.
	if allowInlineBoot && a.backend == nil && a.inlineBoot != nil && a.figLog.Len() == 0 {
		if !a.inlineBoot.IsEmpty() {
			msg.Patches = append(msg.Patches, *a.inlineBoot)
		}
		a.inlineBoot = nil
	}
	if prompt.text != "" {
		msg.Content = append(msg.Content, message.TextContent(prompt.text))
	}
	entry, err := a.figLog.Append(store.Entry[message.Message]{Payload: msg})
	if err != nil {
		return store.Entry[message.Message]{}, err
	}
	if a.backend != nil {
		a.backend.Kick()
	}
	a.refreshMetrics()

	if prompt.text != "" {
		a.emitSnapshot("user", []livedoc.Node{{Type: livedoc.NodeProse, Markdown: prompt.text}})
		a.emitCommit()
	}
	return entry, nil
}

func (a *Agent) startAssistantUnit() {
	a.turnStartLT = 0
	if tail, ok := a.figLog.PeekTail(); ok {
		a.turnStartLT = tail.FigaroLT
	}
	a.gov = toolout.New(liveOutputTail)
	a.lastEmit = time.Time{}
	a.argPartials = map[string]string{}
	a.toolTimings = map[string]compose.ToolTiming{}
	a.turn = newTurnState()
	a.emitSnapshot("assistant", nil)
}

// driveOneRound runs one provider.Send + tool dispatch cycle. The
// assistant reply streams as an open message that seals into a log
// entry; if it called tools, their execution streams as an open
// tool_result message that seals in turn. Returns true when the turn
// is complete, false when another round is needed.
func (a *Agent) driveOneRound(turnCtx context.Context, allowSteering bool) (done bool) {
	if allowSteering {
		if err := a.prepareProviderRound(); err != nil {
			a.endTurn("error: append steering prompt: " + err.Error())
			return true
		}
	} else {
		a.serviceForks()
	}
	if a.turn == nil {
		a.turn = newTurnState()
	}
	bus := newTurnBus(turnCtx)
	deferredLog := newDeferredAppendLog(a.figLog)
	in := provider.SendInput{
		AriaID:     a.id,
		FigLog:     deferredLog,
		Snapshot:   a.chalkboard.Snapshot(),
		Chalkboard: a.chalkAccessor(),
		Tools:      a.toolDefs(),
		MaxTokens:  a.chalkboardInt("system.max_tokens"),
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

	// Provisional index for the assistant message. The provider's log facade
	// reserves this LT; the drain loop performs the canonical append.
	assistantIdx := a.nextIndex()

	// Speculative tool dispatcher: PushToolReady kicks each tool off
	// immediately, in parallel with the LLM stream. Tool lifecycle events
	// flow back on toolEvents for IR assembly only — not the wire; the
	// running spinner animates locally on the consumer (zero traffic).
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

	// Phase 1: fold the assistant stream into an in-flight message,
	// recompose the turn blob on each change, and emit a splice. Once the
	// provider seals (evFigaro), checkpoint and append the assistant on the
	// drain loop, then drop the in-flight copy so compose reads it from the log
	// instead — otherwise it would be counted twice.
	asmMsg := newAsm(message.RoleAssistant)
	sealedInline := false
	metricsReady := false
	var roundErr error
	var toolBuf []toolEvent
	events := bus.events
	forkWake := a.inbox.Wake()
	sealedAwaitingProvider := false
	for events != nil {
		select {
		case <-forkWake:
			a.serviceForks()
		case ev, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if !metricsReady {
				a.refreshMetrics()
				metricsReady = true
			}
			// Structural changes (a tool opens, its args decode, the turn
			// seals) emit immediately; high-frequency text/arg streaming is
			// coalesced to ~11fps by emitLive so 1000 token deltas don't
			// trigger 1000 full recompose+socket frames.
			force := false
			switch ev.kind {
			case evDelta:
				asmMsg.addText(ev.content.Type, ev.content.Text)
			case evToolStart:
				asmMsg.toolOpen(ev.id, ev.name)
				force = true
			case evToolArgs:
				a.argPartials[ev.id] += ev.partial
			case evToolReady:
				asmMsg.toolReady(ev.id, ev.name, ev.args)
				force = true
			case evFigaro:
				force = true
			}
			a.noteAssistant(asmMsg.message())
			var ackErr error
			if ev.kind == evFigaro && roundErr == nil && !a.isInterrupted() {
				staged := deferredLog.take(ev.msg)
				a.noteAssistant(&staged.Payload)
				calls := assistantToolInvokes(staged.Payload)
				sealEntry, err := a.figLog.Append(store.Entry[message.Message]{Payload: staged.Payload})
				if err != nil {
					roundErr = fmt.Errorf("append assistant: %w", err)
				} else {
					if a.turn != nil {
						a.turn.committed = true
					}
					if sealEntry.LT != assistantIdx || sealEntry.FigaroLT != assistantIdx {
						roundErr = fmt.Errorf(
							"assistant seal LT mismatch: predicted %d, got lt=%d main_lt=%d",
							assistantIdx, sealEntry.LT, sealEntry.FigaroLT,
						)
					} else if err := a.commitAssistantCache(assistantIdx, ev.cache); err != nil {
						roundErr = err
					} else {
						sealedInline = true
						ev.msg = sealEntry.Payload
						ev.msg.LogicalTime = sealEntry.LT
						if len(calls) == 0 {
							a.turn = nil
						}
					}
				}
				if roundErr != nil {
					a.cancelCurrentTurn()
				} else {
					a.refreshMetrics()
				}
				ackErr = roundErr
			} else if ev.kind == evFigaro {
				if roundErr != nil {
					ackErr = roundErr
				} else {
					ackErr = context.Canceled
				}
			}
			inflight := asmMsg.message()
			if sealedInline {
				inflight = nil
			}
			if roundErr == nil {
				if err := a.emitLive(inflight, force); err != nil {
					roundErr = err
					a.cancelCurrentTurn()
				}
			}
			if ev.ack != nil {
				if ackErr == nil {
					ackErr = roundErr
				}
				ev.ack <- ackErr
				sealedAwaitingProvider = true
				forkWake = nil
			}
		case te := <-toolEvents:
			toolBuf = append(toolBuf, te)
			// Stream speculative tool output live (bounded tail via the
			// governor) under its still-in-flight heading, coalesced.
			switch te.kind {
			case toolBegin:
				a.startToolTiming(te.id, te.at)
				a.noteTool(te.id, te.name, "running", false)
				inflight := asmMsg.message()
				if sealedInline {
					inflight = nil
				}
				if roundErr == nil {
					if err := a.emitLive(inflight, true); err != nil {
						roundErr = err
						a.cancelCurrentTurn()
					}
				}
			case toolChunk:
				a.gov.Feed(te.id, te.chunk)
				a.noteTool(te.id, te.name, "running", false)
				inflight := asmMsg.message()
				if sealedInline {
					inflight = nil
				}
				if roundErr == nil {
					if err := a.emitLive(inflight, false); err != nil {
						roundErr = err
						a.cancelCurrentTurn()
					}
				}
			case toolEnd:
				a.finishToolTiming(te.id, te.at)
				status := "ok"
				if te.outcome.isErr {
					status = "error"
				}
				a.noteTool(te.id, te.name, status, te.outcome.isErr, toolOutcomeText(te.outcome))
				inflight := asmMsg.message()
				if sealedInline {
					inflight = nil
				}
				if roundErr == nil {
					if err := a.emitLive(inflight, true); err != nil {
						roundErr = err
						a.cancelCurrentTurn()
					}
				}
			}
		}
	}
	sendErr := <-sendDone

	if roundErr != nil {
		a.waitWithForks(specDone)
		sealedMessages, sealErr := a.sealTurn()
		if sealErr != nil {
			roundErr = fmt.Errorf("%v; seal interrupted turn: %w", roundErr, sealErr)
		}
		if len(sealedMessages) > 0 {
			a.emitDelta(a.composeTurn(nil))
			a.serviceForks()
			a.endTurn("error: " + roundErr.Error())
			return true
		}
		a.reconcileAriaServer()
		a.serviceForks()
		a.finishTurn("error: " + roundErr.Error())
		return true
	}

	// Seal: the drain loop appended the provider's assistant message.
	// Recompose from the durable tail.
	var lastFig message.Message
	sealEntry, sealed := a.sealedTail(assistantIdx, message.RoleAssistant)
	if sealed {
		lastFig = sealEntry.Payload
		if !a.isInterrupted() {
			a.emitDelta(a.composeTurn(nil))
		}
	}

	if a.isInterrupted() {
		sealedMessages, err := a.sealTurn()
		if err != nil {
			a.reconcileAriaServer()
			a.finishTurn("error: interrupt recovery: " + err.Error())
			return true
		}
		if len(sealedMessages) > 0 {
			a.emitDelta(a.composeTurn(nil))
		}
		a.serviceForks()
		a.endTurn("interrupted")
		return true
	}
	if sendErr != nil {
		if a.turn == nil {
			if sealed {
				a.serviceForks()
				a.endTurn("error: " + sendErr.Error())
			} else {
				a.serviceForks()
				a.endTurnDiscarding("error: " + sendErr.Error())
			}
			return true
		}
		sealedMessages, err := a.sealTurn()
		if err != nil {
			sendErr = fmt.Errorf("%v; seal interrupted turn: %w", sendErr, err)
		}
		if len(sealedMessages) > 0 {
			a.emitDelta(a.composeTurn(nil))
		} else if err != nil {
			a.reconcileAriaServer()
		}
		a.serviceForks()
		if err != nil {
			a.finishTurn("error: " + sendErr.Error())
		} else {
			a.endTurn("error: " + sendErr.Error())
		}
		return true
	}
	if !sealed {
		a.waitWithForks(specDone)
		if a.turn != nil {
			if _, err := a.figLog.Append(store.Entry[message.Message]{Payload: message.Message{
				Role: message.RoleAssistant, StopReason: message.StopEnd, Timestamp: time.Now().UnixMilli(),
			}}); err != nil {
				a.turn = nil
				a.reconcileAriaServer()
				a.finishTurn("error: append empty assistant: " + err.Error())
				return true
			}
			a.turn = nil
		}
		a.serviceForks()
		a.endTurn(string(message.StopEnd))
		return true
	}

	calls := assistantToolInvokes(lastFig)
	if len(calls) == 0 {
		a.turn = nil
		a.waitWithForks(specDone)
		a.serviceForks()
		stopReason := lastFig.StopReason
		if stopReason == "" {
			stopReason = message.StopEnd
		}
		a.endTurn(string(stopReason))
		return true
	}

	if sealedAwaitingProvider {
		a.serviceForks()
	}
	a.waitWithForks(specDone)

	// Phase 2: run the tools (IR assembly), append the tool_result turn,
	// and recompose so completed tools show their clamped output. The
	// spinner animates locally between here and the seal — no wire
	// traffic until the result lands.
	resultTic, collectErr := a.collectToolResults(turnCtx, calls, spec, toolEvents, toolBuf)
	if collectErr != nil {
		sealedMessages, sealErr := a.sealTurn()
		if sealErr != nil {
			collectErr = fmt.Errorf("%v; seal interrupted turn: %w", collectErr, sealErr)
		}
		if len(sealedMessages) > 0 {
			a.emitDelta(a.composeTurn(nil))
			a.endTurn("error: " + collectErr.Error())
		} else {
			a.reconcileAriaServer()
			a.finishTurn("error: " + collectErr.Error())
		}
		return true
	}
	if a.isInterrupted() {
		sealedMessages, err := a.sealTurn()
		if err != nil {
			a.reconcileAriaServer()
			a.finishTurn("error: interrupt recovery: " + err.Error())
			return true
		}
		if len(sealedMessages) > 0 {
			a.emitDelta(a.composeTurn(nil))
		}
		a.endTurn("interrupted")
		return true
	}
	if _, err := a.figLog.Append(store.Entry[message.Message]{Payload: resultTic}); err != nil {
		sealedMessages, sealErr := a.sealTurn()
		if sealErr != nil {
			err = fmt.Errorf("%v; seal interrupted turn: %w", err, sealErr)
		}
		if len(sealedMessages) > 0 {
			a.emitDelta(a.composeTurn(nil))
			a.endTurn(fmt.Sprintf("error: append tool_result: %s", err))
		} else {
			a.reconcileAriaServer()
			a.finishTurn(fmt.Sprintf("error: append tool_result: %s", err))
		}
		return true
	}
	a.turn = nil
	a.refreshMetrics()
	if !a.isInterrupted() {
		a.emitDelta(a.composeTurn(nil))
	}

	if a.isInterrupted() {
		a.endTurn("interrupted")
		return true
	}
	if err := a.appendSteeringPrompts(); err != nil {
		a.endTurn("error: append steering prompt: " + err.Error())
		return true
	}
	return false
}

// appendSteeringPrompts seals the completed tool round, persists each queued
// prompt as its own user message, and opens a fresh assistant unit for the
// next provider round.
func (a *Agent) appendSteeringPrompts() error {
	prompts := a.inbox.TakeReadyUserPrompts()
	if len(prompts) == 0 {
		return nil
	}
	split := hasRenderablePrompt(prompts)
	if split {
		a.emitCommit()
	}
	if err := a.appendPromptEvents(prompts); err != nil {
		return err
	}
	if split {
		a.startAssistantUnit()
	}
	return nil
}

func (a *Agent) prepareProviderRound() error {
	for {
		a.serviceForks()
		prompts := a.inbox.TakeReadyUserPrompts()
		if len(prompts) == 0 {
			return nil
		}
		split := hasRenderablePrompt(prompts)
		if split {
			if len(a.composeTurn(nil)) == 0 {
				a.abandonLive()
			} else {
				a.emitCommit()
			}
		}
		if err := a.appendPromptEvents(prompts); err != nil {
			return err
		}
		if split {
			a.startAssistantUnit()
		}
	}
}

func hasRenderablePrompt(prompts []event) bool {
	for _, prompt := range prompts {
		if prompt.text != "" {
			return true
		}
	}
	return false
}

func (a *Agent) appendPromptEvents(prompts []event) error {
	for i, prompt := range prompts {
		if _, err := a.appendUserPrompt(prompt, false); err != nil {
			// The chalkboard write precedes the IR append. Do not replay it
			// when restoring the still-unpersisted prompt.
			prompts[i].chalkboard = nil
			if !a.inbox.Prepend(prompts[i:]) {
				return fmt.Errorf("%w; inbox closed while restoring prompts", err)
			}
			return err
		}
	}
	return nil
}

// collectToolResults dispatches every call (idempotent), waits for each
// to finish, and assembles the tool_result turn in canonical (calls)
// order. It emits nothing on the wire — the blob is recomposed by the
// caller after the turn is appended. toolBuf holds events that arrived
// during phase 1.
func (a *Agent) collectToolResults(
	turnCtx context.Context,
	calls []message.Content,
	spec *specDispatcher,
	toolEvents chan toolEvent,
	toolBuf []toolEvent,
) (message.Message, error) {
	expect := make(map[string]bool, len(calls))
	for _, tc := range calls {
		if p := spec.dispatch(turnCtx, a, tc); p != nil {
			expect[tc.ToolCallID] = true
		}
	}

	outcomes := make(map[string]toolOutcome, len(calls))
	// Phase-1 events were already checkpointed as they arrived; only their
	// terminal outcomes are needed for canonical result assembly here.
	for _, te := range toolBuf {
		if te.kind == toolEnd {
			outcomes[te.id] = te.outcome
		}
	}
	// Live phase-2 events: stream output under the running tool, collect
	// outcomes.
toolLoop:
	for len(outcomes) < len(expect) {
		var te toolEvent
		var ok bool
		select {
		case <-a.inbox.Wake():
			a.serviceForks()
			continue
		case <-turnCtx.Done():
			break toolLoop
		case te, ok = <-toolEvents:
			if !ok {
				break toolLoop
			}
		}
		switch te.kind {
		case toolBegin:
			a.startToolTiming(te.id, te.at)
			a.noteTool(te.id, te.name, "running", false)
			if err := a.emitLive(nil, true); err != nil {
				a.cancelCurrentTurn()
				return message.Message{}, err
			}
		case toolChunk:
			a.gov.Feed(te.id, te.chunk)
			a.noteTool(te.id, te.name, "running", false)
			if err := a.emitLive(nil, false); err != nil {
				a.cancelCurrentTurn()
				return message.Message{}, err
			}
		case toolEnd:
			a.finishToolTiming(te.id, te.at)
			outcomes[te.id] = te.outcome
			status := "ok"
			if te.outcome.isErr {
				status = "error"
			}
			a.noteTool(te.id, te.name, status, te.outcome.isErr, toolOutcomeText(te.outcome))
			if err := a.emitLive(nil, true); err != nil {
				a.cancelCurrentTurn()
				return message.Message{}, err
			}
		}
	}
	if err := a.emitLive(nil, true); err != nil {
		a.cancelCurrentTurn()
		return message.Message{}, err
	}

	return a.assembleToolResults(calls, expect, outcomes), nil
}

func (a *Agent) waitWithForks(done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		case <-a.inbox.Wake():
			a.serviceForks()
		}
	}
}

func (a *Agent) assembleToolResults(
	calls []message.Content,
	expect map[string]bool,
	outcomes map[string]toolOutcome,
) message.Message {
	results := make([]message.Content, len(calls))
	for i, tc := range calls {
		if !expect[tc.ToolCallID] {
			results[i] = message.ToolResultContent(tc.ToolCallID, tc.ToolName, "Error: missing tool_call_id", true)
			continue
		}
		oc := outcomes[tc.ToolCallID]
		var text string
		for _, c := range oc.content {
			if c.Type == message.ContentProse {
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
	tic := message.Message{
		Role:      message.RoleUser,
		Content:   results,
		Timestamp: time.Now().UnixMilli(),
	}
	return tic
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
	at      int64
	chunk   string
	final   message.Content // toolEnd: the sealed tool_result block
	outcome toolOutcome     // toolEnd: raw content for IR assembly
}

// toolOutcome holds the result of a single dispatched tool execution.
type toolOutcome struct {
	content []message.Content
	isErr   bool
}

func toolOutcomeText(outcome toolOutcome) string {
	var text strings.Builder
	for _, content := range outcome.content {
		if content.Type == message.ContentProse {
			text.WriteString(content.Text)
		}
	}
	return text.String()
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
		s.events <- toolEvent{kind: toolBegin, id: tc.ToolCallID, name: tc.ToolName, at: time.Now().UnixMilli()}

		emitEnd := func(oc toolOutcome) {
			var text string
			for _, c := range oc.content {
				if c.Type == message.ContentProse {
					text += c.Text
				}
			}
			p.outcome = oc
			if a.isInterrupted() {
				return
			}
			s.events <- toolEvent{
				kind:    toolEnd,
				id:      tc.ToolCallID,
				name:    tc.ToolName,
				at:      time.Now().UnixMilli(),
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

// --- live-render node emission ---

// composeTurn builds the current turn's node list: the messages appended
// since the user prompt, plus the in-flight assistant message (nil once
// it has sealed into the log).
func (a *Agent) composeTurn(inflight *message.Message) []livedoc.Node {
	entries := a.figLog.ReadFrom(a.turnStartLT+1, 0)
	var msgs []message.Message
	for _, e := range entries {
		m := e.Payload
		m.LogicalTime = e.LT
		msgs = append(msgs, m)
	}
	if inflight != nil {
		// The provider seals the assistant message into the log concurrently
		// with the drain loop's tail of buffered stream events. Once the sealed
		// copy is durable — the tail entry of this turn's window is an
		// assistant message — composing the in-flight assembly TOO would render
		// the message twice (under a bumped provisional LT, so the aria server
		// folds it as a brand-new node set: the classic duplicated-thinking
		// frame). The durable copy wins.
		if n := len(entries); n > 0 && entries[n-1].Payload.Role == message.RoleAssistant {
			inflight = nil
		}
	}
	if inflight != nil {
		// The in-flight message has no LT until it seals. Stamp its provisional
		// LT — the next main-LT it will seal at — so compose's stable node ids
		// (LT.blockIdx) match what they'll be post-seal and don't jump at the
		// boundary. While the round is still streaming the window is EMPTY
		// (nothing sealed after turnStartLT yet), so the base must be the
		// pre-turn tail LT, not a constant: falling back to 1 re-ids every
		// streamed node at the seal (1.0 → realLT.0), and the aria server folds
		// the re-id as a second copy of the whole message.
		m := *inflight
		m.LogicalTime = a.turnStartLT + 1
		if n := len(entries); n > 0 {
			m.LogicalTime = entries[n-1].LT + 1
		}
		msgs = append(msgs, m)
	}
	nodes := compose.Nodes(msgs, a.gov.Tails(), a.argPartials, a.summarize, a.previewArg, a.toolTimings)
	if dir := os.Getenv("FIGARO_NODE_DEBUG"); dir != "" {
		logComposeFrame(dir, a.id, inflight != nil, nodes)
	}
	return nodes
}

func (a *Agent) startToolTiming(id string, at int64) {
	if id == "" {
		return
	}
	if at == 0 {
		at = time.Now().UnixMilli()
	}
	if a.toolTimings == nil {
		a.toolTimings = map[string]compose.ToolTiming{}
	}
	timing := a.toolTimings[id]
	if timing.StartedAt == 0 {
		timing.StartedAt = at
		a.toolTimings[id] = timing
	}
}

func (a *Agent) finishToolTiming(id string, at int64) {
	if id == "" {
		return
	}
	if at == 0 {
		at = time.Now().UnixMilli()
	}
	a.startToolTiming(id, at)
	timing := a.toolTimings[id]
	if timing.FinishedAt == 0 {
		timing.FinishedAt = at
		a.toolTimings[id] = timing
	}
}

// logComposeFrame (debug, env-gated) appends one line per composed frame so we
// can see whether a node's id churns across frames / seal — the fingerprint of
// the duplication bug.
func logComposeFrame(dir, ariaID string, hasInflight bool, nodes []livedoc.Node) {
	var b strings.Builder
	fmt.Fprintf(&b, "seal=%v n=%d:", !hasInflight, len(nodes))
	for _, n := range nodes {
		content := n.Markdown
		if n.Type == livedoc.NodeTool {
			content = n.Name
		}
		if len(content) > 14 {
			content = content[:14]
		}
		fmt.Fprintf(&b, " [%s]%s(%s)", n.ID, n.Type, content)
	}
	f, err := os.OpenFile(filepath.Join(dir, ariaID+".frames"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(f, b.String())
}

// emitSnapshot opens a new unit at the next figaro LT and sets its initial
// nodes. The aria server diffs subsequent Updates against this internally.
func (a *Agent) emitSnapshot(role string, nodes []livedoc.Node) {
	a.unitLT++
	a.ariaSrv.Open(a.unitLT, role)
	if len(nodes) > 0 {
		a.ariaSrv.Update(nodes)
	}
}

// liveEmitInterval coalesces high-frequency streaming emits (~11fps). Structural
// changes force an immediate emit; token/output streaming is throttled so a busy
// turn doesn't recompose+broadcast on every chunk. liveOutputTail bounds the
// governor's per-tool live tail to the same source-line cap compose renders, so
// the accumulator can't grow unbounded on a huge tool dump.
const (
	liveEmitInterval = 90 * time.Millisecond
	liveOutputTail   = 200 // matches compose's tailBound source-line cap
)

// emitLive recomposes+broadcasts, throttled to liveEmitInterval unless force is
// set (a structural change or a final flush). Interrupted turns emit nothing.
func (a *Agent) emitLive(inflight *message.Message, force bool) error {
	if a.isInterrupted() {
		return nil
	}
	if !force && time.Since(a.lastEmit) < liveEmitInterval {
		return nil
	}
	a.lastEmit = time.Now()
	a.emitDelta(a.composeTurn(inflight))
	return nil
}

// emitDelta hands the current full node list to the aria server, which computes
// the field-level delta vs the prior frame and broadcasts it (versioned).
func (a *Agent) emitDelta(nodes []livedoc.Node) {
	a.ariaSrv.Update(nodes)
}

// emitCommit closes the open unit after it becomes a committed IR message.
func (a *Agent) emitCommit() {
	a.ariaSrv.Close()
}

// abandonLive drops an open unit that never reached the IR.
func (a *Agent) abandonLive() {
	a.ariaSrv.Abandon()
}

// firstChars returns the first n runes of s's opening line (newlines folded
// to spaces), ellipsized when cut — used to seed a conversation's mantra.
func firstChars(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n])) + "…"
}

// asm assembles the in-flight assistant message from provider Bus events
// so the turn blob can be recomposed mid-stream (before the provider
// seals it into the log).
type asm struct {
	msg     message.Message
	toolIdx map[string]int
}

func newAsm(role message.Role) *asm {
	return &asm{msg: message.Message{Role: role}, toolIdx: map[string]int{}}
}

func (s *asm) addText(kind message.ContentType, text string) {
	if text == "" {
		return
	}
	n := len(s.msg.Content)
	if n > 0 && s.msg.Content[n-1].Type == kind {
		s.msg.Content[n-1].Text += text
		return
	}
	s.msg.Content = append(s.msg.Content, message.Content{Type: kind, Text: text})
}

func (s *asm) toolOpen(id, name string) {
	s.toolIdx[id] = len(s.msg.Content)
	s.msg.Content = append(s.msg.Content,
		message.Content{Type: message.ContentToolInvoke, ToolCallID: id, ToolName: name})
}

func (s *asm) toolReady(id, name string, args map[string]interface{}) {
	i, ok := s.toolIdx[id]
	if !ok {
		s.toolOpen(id, name)
		i = s.toolIdx[id]
	}
	if args == nil {
		args = map[string]interface{}{}
	}
	s.msg.Content[i].Arguments = args
	if name != "" {
		s.msg.Content[i].ToolName = name
	}
}

// message returns the in-flight message, or nil when nothing has streamed.
func (s *asm) message() *message.Message {
	if len(s.msg.Content) == 0 {
		return nil
	}
	return &s.msg
}

// isInterrupted reports whether the current turn was interrupted.
func (a *Agent) isInterrupted() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.interrupted
}

func (a *Agent) cancelCurrentTurn() {
	a.mu.RLock()
	cancel := a.turnCancel
	a.mu.RUnlock()
	if cancel != nil {
		cancel()
	}
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
