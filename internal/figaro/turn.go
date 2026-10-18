package figaro

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/jack-work/figaro/internal/mark"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
	"github.com/jack-work/figaro/internal/toolout"
	"github.com/jack-work/figaro/internal/turns"
)

type busEventKind int

const (
	evDelta busEventKind = iota
	evToolStart
	evToolArgs
	evToolReady
	evFigaro
)

// busEvent is one provider Bus call, carried in order so the drain loop folds the open tail single-threaded.
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

// events blocks (no drop) so the open message never loses content; toolsReady is
// best-effort, the post-stream reconciliation re-dispatches any dropped call.
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
	// Cannot hang: the drain loop reads until events is closed, which happens after Send returns.
	b.events <- busEvent{kind: evFigaro, msg: m, cache: cache, ack: ack}
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

// PushMessageEnd is a no-op: the stop reason rides the appended message.
func (b *turnBus) PushMessageEnd(string) {}

// PushToolReady arms speculative dispatch.
func (b *turnBus) PushToolReady(call message.Content) {
	b.events <- busEvent{kind: evToolReady, id: call.ToolCallID, name: call.ToolName, args: call.Arguments}
	select {
	case b.toolsReady <- call:
	default:
		// Buffer full: driveOneRound re-dispatches any call not dispatched here.
	}
}

// runTurn drives one prompt to completion: provider.Send, tool dispatch, repeat.
func (a *Agent) runTurn(ctx context.Context, prompt event) {
	a.mu.Lock()
	a.lastActive = time.Now()
	a.mu.Unlock()

	// Turn start is the one moment no round is in flight, so an orphaned invoke
	// (a fork over a dead tool call) is closed here, before history is assembled.
	if a.backend != nil {
		if n, err := a.backend.CloseOpenToolCalls(a.id); err != nil {
			slog.Error("close orphaned tool calls at turn start", "aria", a.id, "err", err)
		} else if n > 0 {
			slog.Info("closed orphaned tool calls at turn start", "aria", a.id, "calls", n)
		}
	}

	turnCtx, span := figOtel.Start(ctx, "figaro.qua",
		figOtel.WithAttributes(
			attribute.String("figaro.id", a.id),
			attribute.String("figaro.model", a.currentModel()),
			attribute.String("figaro.provider", a.providerName()),
		),
	)
	defer span.End()
	started, rounds := time.Now(), 0
	defer func() {
		figOtel.RecordTurn(turnCtx, time.Since(started), rounds,
			attribute.String("provider", a.providerName()),
			attribute.String("model", a.currentModel()),
			attribute.String("status", turnStatus(a.turnReason)))
	}()
	turnCtx, cancel := context.WithCancel(turnCtx)
	a.mu.Lock()
	a.turnCtx = turnCtx
	a.turnCancel = cancel
	a.interrupted = false
	a.mu.Unlock()
	a.turnRunning.Store(true)
	a.publishRuntime(rpc.RuntimeCommitting, "")
	defer func() {
		a.mu.Lock()
		a.turnCtx = nil
		a.turnCancel = nil
		a.mu.Unlock()
		a.turnRunning.Store(false)
		cancel()
	}()

	if prompt.text != "" {
		a.runInquiryMethods(turnCtx, prompt.text)
	}
	if _, err := a.appendUserPrompt(prompt, false); err != nil {
		a.endTurn(fmt.Sprintf("error: append message: %s", err))
		return
	}
	// From here a delete aimed at this prompt is refused as committed.
	a.inbox.MarkCommitted([]event{prompt})
	a.inbox.MarkTurn(promptIDs(prompt), a.turnID)
	a.startAssistantUnit()

	allowSteering := false
	for {
		rounds++
		stop := a.driveOneRound(turnCtx, allowSteering)
		if stop {
			return
		}
		allowSteering = true
	}
}

// appendUserPrompt persists one prompt as its own user message. steering is
// persisted so a replayed log classifies the same way it did live.
func (a *Agent) appendUserPrompt(prompt event, steering bool) (store.Entry[message.Message], error) {
	msg := message.Message{
		Role:      message.RoleInput,
		Steering:  steering && prompt.text != "",
		Timestamp: time.Now().UnixMilli(),
	}
	// Seed the mantra from the first prompt, only when unset.
	seed := map[string]json.RawMessage{}
	if prompt.text != "" && a.formString("mantra") == "" {
		mv, _ := json.Marshal(firstChars(prompt.text, 60))
		seed["mantra"] = mv
	}
	combined := form.Build(form.Snapshot{}, seed, nil)
	if !combined.IsIdentity() {
		// Durability precedes visibility: a failed append does not advance the in-memory form.
		if _, err := a.backend.ApplyForm(a.id, combined); err != nil {
			slog.Error("turn form append", "aria", a.id, "err", err)
			combined = form.Patch{}
		}
		if !combined.IsIdentity() {
			a.form.Apply(combined)
		}
	}
	if blocks := senderRuns(prompt.segments); len(blocks) > 0 {
		msg.Content = append(msg.Content, blocks...)
	} else if prompt.text != "" {
		msg.Content = append(msg.Content, message.TextContent(prompt.text))
	}
	if len(msg.Content) > 0 {
		// Only an inquiry opens a turn; a steer joins the one in flight, or the live
		// turn id runs ahead of the projection's and the client abandons the steered turn.
		if !steering {
			a.openTurn()
		}
	}
	entry, err := a.appendMsg(msg)
	if err != nil {
		return store.Entry[message.Message]{}, err
	}
	// refreshMetrics precedes OpenInquiry so the frame carrying the question also
	// carries the context count and the mantra this prompt seeded.
	a.refreshMetrics()

	if prompt.text != "" {
		// Commit directly: an Open+Update+Close for the user message flickers on the client.
		if a.proj != nil {
			if steering {
				// A steer records no inquiry and builds no node; the projection emits its steering node.
				return entry, nil
			}
			// Segments come off the message, not the event, so live and re-read agree on who asked.
			a.turnFirstLT = entry.LT
			a.ariaSrv.OpenInquiry(a.turnID, prompt.text, a.openingFormDeltas(), a.projInquirySegments(msg)...)
		}
	}
	return entry, nil
}

func (a *Agent) startAssistantUnit() {
	// The region boundary is fixed for the turn: the producer recomposes the whole
	// region every frame, so a reopen replaces rather than appends.
	if a.turnStartTurn != a.turnID || a.turnStartLT == 0 {
		a.turnStartTurn = a.turnID
		a.turnStartLT = 0
		if tail, ok := a.figLog.PeekTail(); ok {
			a.turnStartLT = tail.FigaroLT
			// Back up past the trailing run of steers so they sit inside the recomposed window.
			for a.turnStartLT > 0 {
				prev := a.figLog.ReadFrom(a.turnStartLT, 1)
				if len(prev) == 0 || !turns.IsSteering(prev[0].Payload) {
					break
				}
				a.turnStartLT--
			}
		}
	}
	a.gov = toolout.New(liveOutputTail)
	a.lastEmit = time.Time{}
	a.argPartials = map[string]string{}
	if a.proj != nil {
		a.proj.ResetTools()
	}
	a.turn = newTurnState()
	a.emitSnapshot(livedoc.RoleOutput, nil)
}

// driveOneRound runs one provider.Send + tool dispatch cycle; true when the turn is complete.
func (a *Agent) driveOneRound(turnCtx context.Context, allowSteering bool) (done bool) {
	// An interrupt between rounds ends as "interrupted", not as a dead-context provider error.
	if a.isInterrupted() {
		if repaired, err := a.repairTurnTail(); err != nil {
			a.reconcileAriaServer()
			a.finishTurn("error: interrupt recovery: " + err.Error())
			return true
		} else if len(repaired) > 0 {
			a.emitDelta(a.composeTurn(nil))
		}
		a.endTurn("interrupted")
		return true
	}
	if allowSteering {
		if err := a.prepareProviderRound(); err != nil {
			a.endTurn("error: append steering prompt: " + err.Error())
			return true
		}
	} else {
		a.serviceSets()
	}
	if a.turn == nil {
		a.turn = newTurnState()
	}
	// Re-resolve the provider after this round's sets, so a switch lands on the next round.
	if err := a.syncProvider(); err != nil {
		a.endTurn("error: " + err.Error())
		return true
	}
	prov := a.provider()
	if prov == nil {
		a.endTurn("error: no provider configured (set system.provider)")
		return true
	}
	bus := newTurnBus(turnCtx)
	// Thinking is the only state the client's spinner animates on.
	a.publishRuntime(rpc.RuntimeThinking, "")
	in := provider.SendInput{
		AriaID:    a.id,
		FigLog:    a.figLog,
		Snapshot:  a.form.Snapshot(),
		Form:      a.formAccessor(),
		Studies:   a.studyAccessors(),
		Tools:     a.toolDefs(),
		MaxTokens: a.formInt("system.max_tokens"),
	}
	sendDone := make(chan error, 1)
	started := time.Now()
	go func() {
		defer func() {
			if r := recover(); r != nil {
				sendDone <- fmt.Errorf("provider send panic: %v\n%s", r, debug.Stack())
			}
			close(bus.events)
			close(bus.toolsReady)
		}()
		err := prov.Send(turnCtx, in, bus)
		figOtel.RecordRequestDuration(turnCtx, time.Since(started),
			attribute.String("provider", prov.Name()),
			attribute.String("model", a.currentModel()),
			attribute.String("status", statusOf(err)))
		sendDone <- err
	}()

	// The provider's log facade reserves this LT; the drain loop performs the append.
	assistantIdx := a.nextIndex()

	// Speculative dispatch: tools start in parallel with the stream. Lifecycle
	// events feed IR assembly only, not the wire.
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

	// Fold the stream into an in-flight message; once the provider completes,
	// append it and drop the in-flight copy so compose does not count it twice.
	asmMsg := newAsm(message.RoleOutput)
	if a.turn != nil {
		a.turn.asm = asmMsg
	}
	appendedInline := false
	metricsReady := false
	tokenSeen := false
	var roundErr error
	var toolBuf []toolEvent
	events := bus.events
	forkWake := a.inbox.Wake()
	appendedAwaitingProvider := false
	for events != nil {
		select {
		case <-forkWake:
		case ev, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if !metricsReady {
				a.refreshMetrics()
				metricsReady = true
			}
			// Structural changes emit immediately; text/arg deltas are coalesced by emitLive.
			force := false
			switch ev.kind {
			case evDelta:
				if !tokenSeen {
					tokenSeen = true
					mark.Mark("token.first", "aria", a.id, "turn", a.turnID)
					figOtel.RecordTTFT(turnCtx, time.Since(started),
						attribute.String("provider", prov.Name()),
						attribute.String("model", a.currentModel()))
				}
				asmMsg.addText(ev.content.Type, ev.content.Text)
			case evToolStart:
				asmMsg.toolOpen(ev.id, ev.name)
				a.openToolTiming(ev.id, time.Now().UnixMilli())
				force = true
			case evToolArgs:
				a.argPartials[ev.id] += ev.partial
			case evToolReady:
				asmMsg.toolReady(ev.id, ev.name, ev.args)
				force = true
			case evFigaro:
				force = true
			}
			var ackErr error
			// An interrupted turn still takes the provider's message: it carries native
			// blocks (thinking signatures) that repair can only synthesise as text.
			if ev.kind == evFigaro && roundErr == nil {
				staged := store.Entry[message.Message]{Payload: ev.msg}
				a.stageAssistant(&staged)
				calls := assistantToolInvokes(staged.Payload)
				appendedEntry, err := a.appendMsg(staged.Payload)
				if err != nil {
					roundErr = fmt.Errorf("append assistant: %w", err)
				} else {
					if a.turn != nil {
						a.turn.committed = true
					}
					assistantIdx = appendedEntry.LT
					if err := a.commitAssistantCache(assistantIdx, ev.cache); err != nil {
						roundErr = err
					} else {
						appendedInline = true
						ev.msg = appendedEntry.Payload
						ev.msg.LogicalTime = appendedEntry.LT
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
			if appendedInline {
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
				appendedAwaitingProvider = true
				forkWake = nil
			}
		case te := <-toolEvents:
			toolBuf = append(toolBuf, te)
			switch te.kind {
			case toolBegin:
				a.startToolTiming(te.id, te.at)
				a.noteTool(te.id, te.name, "running", false)
				inflight := asmMsg.message()
				if appendedInline {
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
				if appendedInline {
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
				if appendedInline {
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
		repairedMessages, repairErr := a.repairTurnTail()
		if repairErr != nil {
			roundErr = fmt.Errorf("%v; repair interrupted turn: %w", roundErr, repairErr)
		}
		if len(repairedMessages) > 0 {
			a.emitDelta(a.composeTurn(nil))
			a.endTurn("error: " + roundErr.Error())
			return true
		}
		a.reconcileAriaServer()
		a.finishTurn("error: " + roundErr.Error())
		return true
	}

	// Recompose from the durable tail.
	var lastFig message.Message
	appendedEntry, appended := a.appendedTail(assistantIdx, message.RoleOutput)
	if appended {
		lastFig = appendedEntry.Payload
		if !a.isInterrupted() {
			a.emitDelta(a.composeTurn(nil))
		}
	}

	if a.isInterrupted() {
		repairedMessages, err := a.repairTurnTail()
		if err != nil {
			a.reconcileAriaServer()
			a.finishTurn("error: interrupt recovery: " + err.Error())
			return true
		}
		if len(repairedMessages) > 0 {
			a.emitDelta(a.composeTurn(nil))
		}
		a.endTurn("interrupted")
		return true
	}
	if sendErr != nil {
		if a.turn == nil {
			if appended {
				a.endTurn("error: " + sendErr.Error())
			} else {
				a.endTurnDiscarding("error: " + sendErr.Error())
			}
			return true
		}
		repairedMessages, err := a.repairTurnTail()
		if err != nil {
			sendErr = fmt.Errorf("%v; repair interrupted turn: %w", sendErr, err)
		}
		if len(repairedMessages) > 0 {
			a.emitDelta(a.composeTurn(nil))
		} else if err != nil {
			a.reconcileAriaServer()
		}
		if err != nil {
			a.finishTurn("error: " + sendErr.Error())
		} else {
			a.endTurn("error: " + sendErr.Error())
		}
		return true
	}
	if !appended {
		a.waitWithForks(specDone)
		if a.turn != nil {
			if _, err := a.appendMsg(message.Message{
				Role: message.RoleOutput, StopReason: message.StopEnd, Timestamp: time.Now().UnixMilli(),
			}); err != nil {
				a.turn = nil
				a.reconcileAriaServer()
				a.finishTurn("error: append empty assistant: " + err.Error())
				return true
			}
			a.turn = nil
		}
		a.endTurn(string(message.StopEnd))
		return true
	}

	calls := assistantToolInvokes(lastFig)
	if len(calls) == 0 {
		a.turn = nil
		a.waitWithForks(specDone)
		stopReason := lastFig.StopReason
		if stopReason == "" {
			stopReason = message.StopEnd
		}
		a.endTurn(string(stopReason))
		return true
	}

	if appendedAwaitingProvider {
	}
	a.waitWithForks(specDone)

	// Run the tools, append the tool_result turn, recompose.
	a.publishRuntime(rpc.RuntimeTooling, "")
	resultTic, collectErr := a.collectToolResults(turnCtx, calls, spec, toolEvents, toolBuf)
	if collectErr != nil {
		repairedMessages, repairErr := a.repairTurnTail()
		if repairErr != nil {
			collectErr = fmt.Errorf("%v; repair interrupted turn: %w", collectErr, repairErr)
		}
		if len(repairedMessages) > 0 {
			a.emitDelta(a.composeTurn(nil))
			a.endTurn("error: " + collectErr.Error())
		} else {
			a.reconcileAriaServer()
			a.finishTurn("error: " + collectErr.Error())
		}
		return true
	}
	if a.isInterrupted() {
		repairedMessages, err := a.repairTurnTail()
		if err != nil {
			a.reconcileAriaServer()
			a.finishTurn("error: interrupt recovery: " + err.Error())
			return true
		}
		if len(repairedMessages) > 0 {
			a.emitDelta(a.composeTurn(nil))
		}
		a.endTurn("interrupted")
		return true
	}
	if _, err := a.appendMsg(resultTic); err != nil {
		repairedMessages, repairErr := a.repairTurnTail()
		if repairErr != nil {
			err = fmt.Errorf("%v; repair interrupted turn: %w", err, repairErr)
		}
		if len(repairedMessages) > 0 {
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

// appendSteeringPrompts persists queued prompts as one user message and opens a fresh assistant unit.
func (a *Agent) appendSteeringPrompts() error {
	// An interrupted turn does not take the queue with it: a round opened on a
	// cancelled context dies and the prompts go unanswered. The next turn asks them.
	if a.isInterrupted() {
		return nil
	}
	a.serviceSets()
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
		// A steer is structural: emit now, or the node waits for the next round's first chunk.
		a.emitDelta(a.composeTurn(nil))
	}
	return nil
}

func (a *Agent) prepareProviderRound() error {
	// Same rule as appendSteeringPrompts: a cancelled turn must not lift prompts.
	if a.isInterrupted() {
		return nil
	}
	for {
		progressed := a.serviceSets()
		prompts := a.inbox.TakeReadyUserPrompts()
		if len(prompts) == 0 {
			// A serviced set may have uncovered another prompt; loop before concluding.
			if progressed {
				continue
			}
			return nil
		}
		split := hasRenderablePrompt(prompts)
		if split {
			if pre, suf, _ := a.composeTurn(nil); len(pre)+len(suf) == 0 {
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
			a.emitDelta(a.composeTurn(nil))
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

// appendPromptEvents drains queued prompts into a running turn as one message.
func (a *Agent) appendPromptEvents(prompts []event) error {
	merged, ok := mergePromptEvents(prompts)
	if !ok {
		return nil
	}
	if _, err := a.appendUserPrompt(merged, true); err != nil {
		// The form write precedes the IR append; do not replay it when restoring.
		for i := range prompts {
			prompts[i].form = nil
		}
		if !a.inbox.Prepend(prompts) {
			return fmt.Errorf("%w; inbox closed while restoring prompts", err)
		}
		return err
	}
	a.inbox.MarkCommitted(prompts)
	return nil
}

// mergePromptEvents folds a batch into one prompt; a later prompt's form value wins.
func mergePromptEvents(prompts []event) (event, bool) {
	if len(prompts) == 0 {
		return event{}, false
	}
	if len(prompts) == 1 {
		return prompts[0], true
	}
	texts := make([]string, 0, len(prompts))
	out := event{typ: eventUserPrompt, id: prompts[0].id, at: prompts[0].at}
	for i, p := range prompts {
		if p.text != "" {
			texts = append(texts, p.text)
		}
		out.form = mergeFormInput(out.form, p.form)
		out.segments = append(out.segments, p.segments...)
		out.merged = append(out.merged, p.merged...)
		if i > 0 && p.id != 0 {
			out.merged = append(out.merged, p.id)
		}
	}
	// A blank line, not a newline: markdown rejoins a soft break.
	out.text = strings.Join(texts, "\n\n")
	return out, true
}

// senderRuns folds consecutive segments sharing a sender into one Content.
func senderRuns(segments []promptSegment) []message.Content {
	var out []message.Content
	for _, seg := range segments {
		if seg.text == "" {
			continue
		}
		if n := len(out); n > 0 && out[n-1].Sender == seg.sender && out[n-1].Principal == seg.principal {
			out[n-1].Text += "\n\n" + seg.text
			continue
		}
		c := message.SenderText(seg.sender, seg.text)
		c.Principal = seg.principal
		out = append(out, c)
	}
	return out
}

// mergeFormInput merges b over a, in queue order, without mutating either.
func mergeFormInput(a, b *rpc.FormInput) *rpc.FormInput {
	if b == nil {
		return a
	}
	if a == nil {
		return b
	}
	out := &rpc.FormInput{}
	if len(a.Context) > 0 || len(b.Context) > 0 {
		out.Context = map[string]json.RawMessage{}
		for k, v := range a.Context {
			out.Context[k] = v
		}
		for k, v := range b.Context {
			out.Context[k] = v
		}
	}
	if a.Patch != nil || b.Patch != nil {
		var merged form.Patch
		for _, src := range []*rpc.FormPatch{a.Patch, b.Patch} {
			if src == nil {
				continue
			}
			merged = form.Merge(merged, *src)
		}
		out.Patch = &merged
	}
	return out
}

// collectToolResults waits for every call and assembles the tool_result turn in
// call order; it emits nothing on the wire. toolBuf holds phase-1 events.
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
	for _, te := range toolBuf {
		if te.kind == toolEnd {
			outcomes[te.id] = te.outcome
		}
	}
toolLoop:
	for len(outcomes) < len(expect) {
		var te toolEvent
		var ok bool
		select {
		case <-a.inbox.Wake():
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
		}
	}
}

func (a *Agent) assembleToolResults(
	calls []message.Content,
	expect map[string]bool,
	outcomes map[string]toolOutcome,
) message.Message {
	results := make([]message.Content, len(calls))
	var images []message.Content
	total := a.toolImageBudget()
	budget := total
	for i, tc := range calls {
		// Malformed arguments are answered in the API's documented shape so the model resends the call.
		if raw, bad := message.MalformedArgsOf(tc); bad {
			results[i] = message.ToolResultContent(tc.ToolCallID, tc.ToolName,
				message.InvalidJSONResult(raw), true)
			continue
		}
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
		kept, notes, spent := harvestToolImages(tc, oc, budget, total)
		budget -= spent
		images = append(images, kept...)
		for _, note := range notes {
			text += note
		}
		results[i] = message.ToolResultContent(tc.ToolCallID, tc.ToolName, text, oc.isErr)
	}
	// Unfinished tools get a synthetic error so tool_use/tool_result pairing holds.
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
		Role:      message.RoleInput,
		Content:   append(results, images...),
		Timestamp: time.Now().UnixMilli(),
	}
	return tic
}

// toolImageBudget is the base64 ceiling for all tool imagery on one tool_result tic.
func (a *Agent) toolImageBudget() int { return a.settings.InlineImageBudget() }

// harvestToolImages keeps images even from an errored tool and refits any that
// exceed the message budget or a provider's per-request limit.
func harvestToolImages(tc message.Content, oc toolOutcome, remaining, total int) (kept []message.Content, notes []string, spent int) {
	for _, c := range oc.content {
		if c.Type != message.ContentImage || c.Data == "" {
			continue
		}
		left := remaining - spent
		if _, needed, _ := tool.FitSendable(c.Data, c.MimeType, tool.DefaultImageLimits()); len(c.Data) <= left && !needed {
			spent += len(c.Data)
			kept = append(kept, message.ToolImageContent(tc.ToolCallID, tc.ToolName, c.MimeType, c.Data))
			continue
		}
		if left > total {
			left = total
		}
		fitted, note, ok := refitToolImage(c, left)
		if !ok {
			// Say which limit was hit; the model reasons from the note.
			notes = append(notes, fmt.Sprintf(
				"\n[image omitted: %s of base64 does not fit the %s still free in this message's %s image budget]",
				tool.FormatSize(len(c.Data)), tool.FormatSize(maxInt(left, 0)), tool.FormatSize(total)))
			continue
		}
		spent += len(fitted.Data)
		kept = append(kept, message.ToolImageContent(tc.ToolCallID, tc.ToolName, fitted.MimeType, fitted.Data))
		notes = append(notes, note)
	}
	return kept, notes, spent
}

// refitToolImage re-encodes one image to fit limit and returns the coordinate note.
func refitToolImage(c message.Content, limit int) (tool.FittedImage, string, bool) {
	raw, err := base64.StdEncoding.DecodeString(c.Data)
	if err != nil {
		return tool.FittedImage{}, "", false
	}
	fitted, err := tool.FitImage(raw, c.MimeType, tool.ImageLimits{MaxBase64: limit})
	if err != nil {
		return tool.FittedImage{}, "", false
	}
	if fitted.Resized {
		return fitted, fmt.Sprintf(
			"\n[image rescaled to %dx%d to share this message's image budget with the other tools in this round; "+
				"multiply coordinates by a FURTHER %.2f, on top of any factor noted above]",
			fitted.W, fitted.H, fitted.Scale()), true
	}
	return fitted, fmt.Sprintf(
		"\n[image re-encoded as %s to share this message's image budget with the other tools in this round; "+
			"coordinates are unchanged]", fitted.MimeType), true
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (a *Agent) nextIndex() uint64 {
	if e, ok := a.figLog.PeekTail(); ok {
		return e.LT + 1
	}
	return 1
}

// appendedTail reports the tail entry iff the provider appended it at expectIdx.
func (a *Agent) appendedTail(expectIdx uint64, role message.Role) (store.Entry[message.Message], bool) {
	e, ok := a.figLog.PeekTail()
	if !ok || e.LT != expectIdx || e.Payload.Role != role {
		return store.Entry[message.Message]{}, false
	}
	return e, true
}

type toolEventKind int

const (
	toolBegin toolEventKind = iota
	toolChunk
	toolEnd
)

// toolEvent carries one tool's lifecycle back to the drain loop.
type toolEvent struct {
	kind    toolEventKind
	id      string
	name    string
	at      int64
	chunk   string
	outcome toolOutcome
}

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

type toolPending struct {
	call    message.Content
	done    chan struct{}
	outcome toolOutcome // valid after done is closed
}

// specDispatcher starts tools as PushToolReady arrives; dispatch is idempotent
// per tool_call_id, so the post-stream pass may call it for every call.
type specDispatcher struct {
	mu      sync.Mutex
	pending map[string]*toolPending
	events  chan toolEvent
}

func newSpecDispatcher(events chan toolEvent) *specDispatcher {
	return &specDispatcher{pending: make(map[string]*toolPending), events: events}
}

// dispatch launches tc unless already in flight for that tool_call_id.
func (s *specDispatcher) dispatch(turnCtx context.Context, a *Agent, tc message.Content) *toolPending {
	if tc.Type != message.ContentToolInvoke || tc.ToolCallID == "" {
		return nil
	}
	// A call with malformed arguments never runs; no outcome becomes an error result.
	if _, bad := message.MalformedArgsOf(tc); bad {
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
			p.outcome = oc
			if a.isInterrupted() {
				return
			}
			s.events <- toolEvent{
				kind:    toolEnd,
				id:      tc.ToolCallID,
				name:    tc.ToolName,
				at:      time.Now().UnixMilli(),
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
		started := time.Now()
		content, err := t.Execute(turnCtx, tc.Arguments, onChunk)
		figOtel.Event(turnCtx, "agent.tool.execute_post",
			attribute.String("tool", tc.ToolName),
			attribute.String("tool_call_id", tc.ToolCallID),
			attribute.Bool("err", err != nil),
		)
		figOtel.RecordTool(turnCtx, time.Since(started),
			attribute.String("tool", tc.ToolName),
			attribute.String("sandbox", a.tools.Sandbox()),
			attribute.Bool("err", err != nil))
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

// composeTurn builds the turn's node list: messages since the prompt plus the in-flight assistant.
func (a *Agent) composeTurn(inflight *message.Message) (prefix, suffix []livedoc.Node, stable int) {
	msgs := a.regionMessages()
	if inflight != nil {
		// Once the durable copy is the window's tail, composing the in-flight copy too
		// renders the message twice under a bumped provisional LT.
		if n := len(msgs); n > 0 && msgs[n-1].Role == message.RoleOutput {
			inflight = nil
		}
	}
	if inflight != nil {
		// Stamp the provisional LT so node ids do not jump at the append. The base is
		// the pre-turn tail, not a constant, or the aria server folds a re-id as a copy.
		m := *inflight
		m.LogicalTime = a.turnStartLT + 1
		if n := len(msgs); n > 0 {
			m.LogicalTime = msgs[n-1].LogicalTime + 1
		}
		msgs = append(msgs, m)
	}
	pre, suf, stab := a.projNodes(msgs, a.gov.Tails(), a.argPartials)
	if dir := os.Getenv("FIGARO_NODE_DEBUG"); dir != "" {
		logComposeFrame(dir, a.id, inflight != nil, append(append([]livedoc.Node(nil), pre...), suf...))
	}
	return pre, suf, stab
}

// openToolTiming stamps the start of generation, before the arguments are written.
func (a *Agent) openToolTiming(id string, at int64) {
	if a.proj == nil {
		return
	}
	a.proj.ToolOpened(id, at)
}

func (a *Agent) startToolTiming(id string, at int64) {
	if a.proj == nil {
		return
	}
	if at == 0 {
		at = time.Now().UnixMilli()
	}
	a.proj.ToolStarted(id, at)
}

func (a *Agent) finishToolTiming(id string, at int64) {
	if a.proj == nil {
		return
	}
	if at == 0 {
		at = time.Now().UnixMilli()
	}
	a.proj.ToolStarted(id, at)
	a.proj.ToolFinished(id, at)
}

// logComposeFrame is an env-gated debug trace of node ids across frames.
func logComposeFrame(dir, ariaID string, hasInflight bool, nodes []livedoc.Node) {
	var b strings.Builder
	fmt.Fprintf(&b, "appended=%v n=%d:", !hasInflight, len(nodes))
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

// emitSnapshot opens a streaming suffix where the turn's committed nodes end.
func (a *Agent) emitSnapshot(role string, nodes []livedoc.Node) {
	a.ariaSrv.OpenTurn(a.turnID)
	if len(nodes) > 0 {
		a.ariaSrv.Update(nil, nodes, 0)
	}
}

// liveOutputTail bounds the governor's per-tool live tail to compose's source-line cap.
const liveOutputTail = 200

func (a *Agent) liveEmitInterval() time.Duration {
	return time.Duration(a.settings.StreamEmitIntervalMs()) * time.Millisecond
}

// emitLive is throttled to the emit interval unless force is set. Interrupted turns emit nothing.
func (a *Agent) emitLive(inflight *message.Message, force bool) error {
	if a.isInterrupted() {
		return nil
	}
	if !force && time.Since(a.lastEmit) < a.liveEmitInterval() {
		return nil
	}
	a.lastEmit = time.Now()
	a.emitDelta(a.composeTurn(inflight))
	return nil
}

// emitDelta hands the full node list to the aria server, which diffs and broadcasts.
func (a *Agent) emitDelta(prefix, suffix []livedoc.Node, stable int) {
	a.ariaSrv.Update(prefix, suffix, stable)
}

func (a *Agent) emitCommit() {
	a.ariaSrv.Close()
}

func (a *Agent) abandonLive() {
	a.ariaSrv.Abandon()
}

func firstChars(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n])) + "…"
}

// asm assembles the in-flight assistant message; per-block Builders keep
// delta appends from going quadratic.
type asm struct {
	msg     message.Message
	text    []*strings.Builder // nil for non-text blocks
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
	if n > 0 && s.msg.Content[n-1].Type == kind && s.text[n-1] != nil {
		s.text[n-1].WriteString(text)
		return
	}
	b := &strings.Builder{}
	b.WriteString(text)
	s.msg.Content = append(s.msg.Content, message.Content{Type: kind})
	s.text = append(s.text, b)
}

func (s *asm) toolOpen(id, name string) {
	s.toolIdx[id] = len(s.msg.Content)
	s.msg.Content = append(s.msg.Content,
		message.Content{Type: message.ContentToolInvoke, ToolCallID: id, ToolName: name})
	s.text = append(s.text, nil)
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

// message returns nil when nothing has streamed. Builder.String() does not copy.
func (s *asm) message() *message.Message {
	if len(s.msg.Content) == 0 {
		return nil
	}
	for i, b := range s.text {
		if b != nil {
			s.msg.Content[i].Text = b.String()
		}
	}
	return &s.msg
}

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
	if m.Role != message.RoleOutput {
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

// combineFormInput merges client-supplied form input with the persisted snapshot.
func (a *Agent) combineFormInput(input *rpc.FormInput) form.Patch {
	if a.form == nil || input == nil {
		return form.Patch{}
	}
	snap := a.form.Snapshot()
	var clientPatch form.Patch
	if input.Patch != nil {
		clientPatch = *input.Patch
	}
	var ctxPatch form.Patch
	if input.Context != nil {
		ctxPatch = additivePatch(withoutSystemNS(form.FromMap(input.Context)), snap)
	}
	out := form.Patch{}
	for _, p := range []form.Patch{ctxPatch, clientPatch} {
		switch {
		case p.IsIdentity():
		case out.IsIdentity():
			out = p
		default:
			out = form.Merge(out, p)
		}
	}
	return out
}

// additivePatch never removes keys absent from ctx: Context is additive by contract.
func additivePatch(ctx, snap form.Snapshot) form.Patch {
	return snap.Apply(ctx.AsPatch()).Diff(snap)
}

func turnStatus(reason string) string {
	switch {
	case strings.HasPrefix(reason, "error:"):
		return "failure"
	case reason == "interrupted":
		return "interrupted"
	}
	return "success"
}

func statusOf(err error) string {
	if err != nil {
		return "failure"
	}
	return "success"
}

// regionMessages is the open turn's messages, held across frames.
func (a *Agent) regionMessages() []message.Message {
	if a.regionStart != a.turnStartLT {
		// A new turn is a new region; a memo carried across would serve the previous turn's messages.
		a.regionStart, a.regionMsgs, a.regionLast = a.turnStartLT, nil, 0
	}
	from := a.turnStartLT + 1
	if a.regionLast >= from {
		from = a.regionLast + 1
	}
	for _, e := range a.figLog.ReadFrom(from, 0) {
		m := e.Payload
		m.LogicalTime = e.LT
		a.regionMsgs = append(a.regionMsgs, m)
		a.regionLast = e.LT
	}
	n := len(a.regionMsgs)
	return a.regionMsgs[:n:n]
}
