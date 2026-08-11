package figaro

import (
	"context"
	"encoding/base64"
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

	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/message"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
	"github.com/jack-work/figaro/internal/toolout"
	"github.com/jack-work/figaro/internal/turns"
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
// the appended message.Message (log.entry), so there is no separate
// pre-append metadata frame.
func (b *turnBus) PushMessageEnd(string) {}

// PushToolReady records the decoded invocation in the open message and
// arms speculative dispatch.
func (b *turnBus) PushToolReady(call message.Content) {
	b.events <- busEvent{kind: evToolReady, id: call.ToolCallID, name: call.ToolName, args: call.Arguments}
	select {
	case b.toolsReady <- call:
	default:
		// Buffer full: drop. driveOneRound re-dispatches any call in
		// the appended assistant message that wasn't dispatched here.
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
			attribute.String("figaro.provider", a.providerName()),
		),
	)
	defer span.End()
	turnCtx, cancel := context.WithCancel(turnCtx)
	a.mu.Lock()
	a.turnCtx = turnCtx
	a.turnCancel = cancel
	a.interrupted = false
	a.mu.Unlock()
	a.turnRunning.Store(true)
	defer func() {
		a.mu.Lock()
		a.turnCtx = nil
		a.turnCancel = nil
		a.mu.Unlock()
		a.turnRunning.Store(false)
		cancel()
	}()

	// Belt-and-suspenders: if a prior turn died after the assistant
	// tool_use was logged but before tool_results were appended, the
	// IR still has a dangling tool_use at the tail. Boot-time repair
	// usually catches this, but cover the case where the boot check
	// missed (e.g. dangling state appeared after boot).
	repairInterruptedTail(a.figLog, a.id)
	if _, err := a.appendUserPrompt(prompt, true, false); err != nil {
		a.endTurn(fmt.Sprintf("error: append message: %s", err))
		return
	}
	// It is a message now, not a queued one. A delete aimed at it from here on
	// is refused as committed rather than silently missing its target.
	a.inbox.MarkCommitted([]event{prompt})
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
//
// steering distinguishes the two kinds of input, and the DRAIN is the only place
// that can decide it: it alone knows whether a turn was already in flight when
// this prompt came off the queue. An inquiry opens a turn; a steer joins the one
// already running. The field is persisted so a replayed log classifies the same
// way it did live: but nothing outside this package ever supplies it.
func (a *Agent) appendUserPrompt(prompt event, allowInlineBoot, steering bool) (store.Entry[message.Message], error) {
	msg := message.Message{
		Role:      message.RoleInput,
		Steering:  steering && prompt.text != "",
		Timestamp: time.Now().UnixMilli(),
	}
	var combined form.Patch
	if prompt.form != nil {
		combined = a.combineFormInput(prompt.form)
	}
	// Seed the mantra from the first user message's opening text, so every
	// conversation has a stable title (the first n chars) without the agent
	// having to set one. Only when unset, so it stays fixed to the opener.
	if prompt.text != "" && a.formString("mantra") == "" {
		if combined.Set == nil {
			combined.Set = map[string]json.RawMessage{}
		}
		mv, _ := json.Marshal(firstChars(prompt.text, 60))
		combined.Set["mantra"] = mv
	}
	if !combined.IsEmpty() {
		if a.backend != nil {
			// DURABILITY PRECEDES VISIBILITY. On a failed append the
			// in-memory form is NOT advanced, so the published board and
			// the log agree and a restart replays cleanly.
			//
			// The reverse: which this did: is not a lost write but a
			// hallucinated one: the patch is projected to the model as a
			// <system-reminder> on the next tic, so the agent acts on state
			// that will not exist after a restart. applyControlPatch has always
			// bailed here; this path did not, and the asymmetry was the bug.
			//
			// The turn CONTINUES rather than aborting. The patch is a
			// transition riding the turn, not the turn's content, and killing a
			// live exchange over a form write is a worse failure than
			// proceeding without it: the error is logged and the message still
			// reaches the model.
			if _, err := a.backend.ApplyForm(a.id, combined); err != nil {
				slog.Error("turn form append", "aria", a.id, "err", err)
				combined = form.Patch{}
			}
		} else {
			msg.Patches = append(msg.Patches, combined)
		}
		if !combined.IsEmpty() {
			a.form.Apply(combined)
		}
	}
	// Ephemeral first message: fold the boot patch inline so the outfit
	// reminders render (no channel to hold the transition). State is
	// already seeded by the caller, so this is render-only.
	if allowInlineBoot && a.backend == nil && a.inlineBoot != nil && a.figLog.Len() == 0 {
		if !a.inlineBoot.IsEmpty() {
			msg.Patches = append(msg.Patches, *a.inlineBoot)
		}
		a.inlineBoot = nil
	}
	if blocks := senderRuns(prompt.segments); len(blocks) > 0 {
		msg.Content = append(msg.Content, blocks...)
	} else if prompt.text != "" {
		// No segments: an event built directly (tests, internal submissions).
		msg.Content = append(msg.Content, message.TextContent(prompt.text))
	}
	if len(msg.Content) > 0 {
		// Only an inquiry opens a turn. A steer joins the exchange already in
		// flight, so the counter must not move: otherwise the live turn id
		// runs ahead of the one the projection derives, the client sees a new
		// turn, and the turn being steered is abandoned mid-stream with its
		// closing prose never rendered.
		//
		// Keyed on the CONTENT, not on which branch produced it. Attaching this
		// to one of the two paths above is exactly the bug that shipped for a
		// moment here: the attributed path skipped openTurn, so the second turn
		// recomposed the first and the reply arrived twice.
		if !steering {
			a.openTurn()
		}
	}
	entry, err := a.appendMsg(msg)
	if err != nil {
		return store.Entry[message.Message]{}, err
	}
	// Kick expedites the store's background flush. It is a hint, not a
	// barrier, a non-blocking channel send, and disk follows with bounded
	// lag whether or not it is called: so its position buys a watching
	// client no durability it would not otherwise have. It stays ahead of the
	// broadcast because it costs ~50ns to have the flush already in flight
	// when someone is told, and because the steering branch below RETURNS:
	// anything moved past it is silently skipped for every steer.
	if a.backend != nil {
		a.backend.Kick()
	}
	// refreshMetrics stays here, AHEAD of OpenInquiry, deliberately. Every
	// aria-server broadcast is stamped with sessionMetrics() by the
	// subscription in NewAgent, so the frame that first carries the user's
	// question is also the frame that carries the footer's context count and
	// the mantra this very prompt just seeded. Refreshing first is what makes
	// that frame describe a world which already contains the question.
	//
	// It is not a latency cost worth reclaiming. Measured
	// (BenchmarkPromptBroadcastGap, Ryzen 7 5800X): the steady-state
	// incremental refresh is 0.6-0.8µs and FLAT from 100 to 5,000 messages,
	// because it folds exactly the row just appended. The O(n) fallback
	// (refreshMetricsFrom: 282µs at 5k messages) is unreachable from here:
	// it is guarded on tail.LT < metricsLT, i.e. the log rewound under the
	// agent, and a successful appendMsg has just put the tail strictly ahead.
	// Broadcasting first would save ~0.6µs and cost a footer with no mantra
	// and no ctx for as long as the model takes to send its first frame -
	// ~1.4s in a pty A/B. Pinned by TestInquiryFrameCarriesFreshMetrics.
	a.refreshMetrics()

	if prompt.text != "" {
		// Commit the user message directly: no Open+Update+Close ping-pong.
		// The old path briefly opened a live region for the user, which the
		// client rendered as an in-flight message and then immediately appended,
		// producing a visible flicker between send and durable commit. A direct
		// Commit makes the message appear only when the transcript truly holds
		// it: the aria frame carries {Role, Nodes} on the first hop and the
		// client short-circuits to OnClosed with no OnLive event.
		//
		// Gated on the projector: the inquiry is UI IR, and a build without the
		// projection must render nothing at all rather than a stream of bare
		// prompts with no replies.
		if a.proj != nil {
			if steering {
				// A steer joins the turn already in flight: no inquiry to record
				// (that would overwrite the question being steered), and no node
				// built here: startAssistantUnit keeps the steer inside the
				// recomposed window so the PROJECTION emits its steering node.
				// One producer of UI IR, not two: hand-building it here is what
				// silently lost the steer when the region was recomposed.
				return entry, nil
			}
			// The inquiry is TEXT ON THE TURN, not a node: recording it is
			// the whole of the prompt's UI IR. It broadcasts, so a watching
			// client shows the question the instant it commits.
			// Segments come off the MESSAGE, not the event: the message is what
			// the projection will re-derive them from on a re-read, so live and
			// re-read cannot tell two different stories about who asked.
			a.ariaSrv.OpenInquiry(a.turnID, prompt.text, a.projInquirySegments(msg)...)
		}
	}
	return entry, nil
}

func (a *Agent) startAssistantUnit() {
	// The streaming region's boundary is fixed for the LIFETIME OF THE TURN -
	// aria.Server.OpenTurn recomputes its base only when the turn id changes,
	// because the producer is contracted to recompose the WHOLE region every
	// frame so that a reopen replaces rather than appends.
	//
	// So the compose window must be pinned the same way. Recomputing it from the
	// tail on every round silently narrowed it: after a steer drained, the
	// backup stopped at the preceding tool_result, so the window became
	// [steer, reply] while the server's region still began at the TOOL's index.
	// The shorter region then overwrote the tool in place: it was never frozen,
	// appeared nowhere on screen despite being in the IR, and left its voice-run
	// header stranded with nothing under it.
	if a.turnStartTurn != a.turnID || a.turnStartLT == 0 {
		a.turnStartTurn = a.turnID
		a.turnStartLT = 0
		if tail, ok := a.figLog.PeekTail(); ok {
			a.turnStartLT = tail.FigaroLT
			// Steers are the tail when this unit opens right after a drain, and a
			// single drain can yield several. Back up past the whole trailing run
			// so every one of them sits INSIDE the recomposed window (which is
			// turnStartLT+1..) and the PROJECTION emits their steering nodes. The
			// drain used to hand-build one node instead, which both lost steers
			// beyond the first and vanished entirely on the next recompose.
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

// driveOneRound runs one provider.Send + tool dispatch cycle. The
// assistant reply streams as an open message that appends into a log
// entry; if it called tools, their execution streams as an open
// tool_result message that appends in turn. Returns true when the turn
// is complete, false when another round is needed.
func (a *Agent) driveOneRound(turnCtx context.Context, allowSteering bool) (done bool) {
	// The interrupt may have landed between rounds. Ending here says
	// "interrupted", which is true; falling through would call the provider
	// with a dead context and end the turn as "error: context canceled",
	// which is the same event wearing a fault's clothes.
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
	// The form is authoritative: re-resolve the provider now, after
	// this round's queued `set`s have been serviced, so a provider switch
	// lands on the very next round instead of waiting for a restart.
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
	deferredLog := newDeferredAppendLog(a.figLog)
	in := provider.SendInput{
		AriaID:    a.id,
		FigLog:    deferredLog,
		Snapshot:  a.form.Snapshot(),
		Form:      a.formAccessor(),
		Studies:   a.studyAccessors(),
		Tools:     a.toolDefs(),
		MaxTokens: a.formInt("system.max_tokens"),
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
		err := prov.Send(turnCtx, in, bus)
		figOtel.RecordRequestDuration(turnCtx, time.Since(started),
			attribute.String("provider", prov.Name()),
			attribute.String("model", a.currentModel()),
			attribute.String("status", statusOf(err)))
		sendDone <- err
	}()

	// Provisional index for the assistant message. The provider's log facade
	// reserves this LT; the drain loop performs the canonical append.
	assistantIdx := a.nextIndex()

	// Speculative tool dispatcher: PushToolReady kicks each tool off
	// immediately, in parallel with the LLM stream. Tool lifecycle events
	// flow back on toolEvents for IR assembly only: not the wire; the
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
	// provider completes (evFigaro), checkpoint and append the assistant on the
	// drain loop, then drop the in-flight copy so compose reads it from the log
	// instead: otherwise it would be counted twice.
	asmMsg := newAsm(message.RoleOutput)
	appendedInline := false
	metricsReady := false
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
			// Structural changes (a tool opens, its args decode, the turn
			// completes) emit immediately; high-frequency text/arg streaming is
			// coalesced to ~11fps by emitLive so 1000 token deltas don't
			// trigger 1000 full recompose+socket frames.
			force := false
			switch ev.kind {
			case evDelta:
				asmMsg.addText(ev.content.Type, ev.content.Text)
			case evToolStart:
				asmMsg.toolOpen(ev.id, ev.name)
				a.openToolTiming(ev.id, time.Now().UnixMilli())
				force = true
			case evToolArgs:
				// Streamed unconditionally, though a collapsed tool block never
				// draws them. If the bytes ever matter, drop them here for
				// unexpanded nodes and catch up on demand when one is opened.
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
				appendedEntry, err := a.appendMsg(staged.Payload)
				if err != nil {
					roundErr = fmt.Errorf("append assistant: %w", err)
				} else {
					if a.turn != nil {
						a.turn.committed = true
					}
					if appendedEntry.LT != assistantIdx || appendedEntry.FigaroLT != assistantIdx {
						roundErr = fmt.Errorf(
							"assistant append LT mismatch: predicted %d, got lt=%d main_lt=%d",
							assistantIdx, appendedEntry.LT, appendedEntry.FigaroLT,
						)
					} else if err := a.commitAssistantCache(assistantIdx, ev.cache); err != nil {
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
			// Stream speculative tool output live (bounded tail via the
			// governor) under its still-in-flight heading, coalesced.
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

	// Durable tail: the drain loop appended the provider's assistant message.
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

	// Phase 2: run the tools (IR assembly), append the tool_result turn,
	// and recompose so completed tools show their clamped output. The
	// spinner animates locally between here and the append: no wire
	// traffic until the result lands.
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

// appendSteeringPrompts closes the completed tool round, persists each queued
// prompt as its own user message, and opens a fresh assistant unit for the
// next provider round.
func (a *Agent) appendSteeringPrompts() error {
	// AN INTERRUPTED TURN DOES NOT TAKE THE QUEUE WITH IT. Draining here after
	// the cancel is how queued messages got "received", appended to the log,
	// visible on screen, and then never answered: the round that absorbed
	// them opened with an already-cancelled context, so it died immediately
	// and took them down with it. They stay queued instead, and the next turn
	// (a fresh one, opened by the drain loop) asks them properly.
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
	}
	return nil
}

func (a *Agent) prepareProviderRound() error {
	// Same rule as appendSteeringPrompts, at the other drain site: a cancelled
	// turn must not lift prompts it cannot answer.
	if a.isInterrupted() {
		return nil
	}
	for {
		progressed := a.serviceSets()
		prompts := a.inbox.TakeReadyUserPrompts()
		if len(prompts) == 0 {
			// A serviced set may have uncovered another behind it; loop again
			// before concluding the queue is drained.
			if progressed {
				continue
			}
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

// appendPromptEvents drains queued prompts INTO A RUNNING TURN, as ONE message.
//
// The batch is the contiguous run of user prompts Inbox.TakeReadyUserPrompts
// lifted in a single locked pass: it stops at the first fork or form set,
// because those need ordering. So the batch boundary is already exactly the set
// that belongs together, and we join it rather than splitting it: three nudges
// typed during one tool round are ONE message of three lines at ONE LT, not
// three messages the model must reconcile.
//
// Both callers run inside the turn loop, so by construction everything reaching
// here was drawn from the queue while a turn was in flight. That is the whole
// classification rule and this is the only place it is made: a prompt drained
// mid-turn IS a steer; a prompt that opens a turn goes through appendUserPrompt
// directly with steering=false. Nothing upstream declares it, a prompt
// pipelined by a script and one typed by someone watching are identical on the
// wire, and the drain is the only point that knows the turn boundary as the
// agent itself sees it rather than as a client call returning. One message means
// one steering decision, taken once.
//
// Backcompat is read-only: logs written before this carry N separate user
// messages and keep reading exactly as they did. Nothing on disk is migrated.
func (a *Agent) appendPromptEvents(prompts []event) error {
	merged, ok := mergePromptEvents(prompts)
	if !ok {
		return nil
	}
	if _, err := a.appendUserPrompt(merged, false, true); err != nil {
		// All-or-nothing: the form write precedes the IR append, so do not
		// replay it when restoring. One message means one failure unit: there is
		// no partial tail to prepend.
		for i := range prompts {
			prompts[i].form = nil
		}
		if !a.inbox.Prepend(prompts) {
			return fmt.Errorf("%w; inbox closed while restoring prompts", err)
		}
		return err
	}
	// Every id in the batch is now part of one durable message.
	a.inbox.MarkCommitted(prompts)
	return nil
}

// mergePromptEvents folds a drained batch into one prompt: texts joined by a
// newline in queue order, form input merged in the same order so a later
// prompt's value wins. Reports false when the batch is empty.
//
// Identity folds with the content: the result keeps the FIRST id (it is the
// same message, continued) and records every other id in merged, so a client
// that read the queue a moment ago can still find where its id went.
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
		// Concatenated, never flattened: the fold is exactly where attribution
		// used to be lost. Three nudges from three senders become one message
		// of three attributed segments, not one anonymous paragraph.
		out.segments = append(out.segments, p.segments...)
		out.merged = append(out.merged, p.merged...)
		if i > 0 && p.id != 0 {
			out.merged = append(out.merged, p.id)
		}
	}
	// A BLANK line between them, not a single newline. Two reasons, and they
	// point the same way. On screen, prose is rendered as markdown, where a
	// lone newline is a SOFT break: glamour rejoins the lines and three
	// messages arrive as "test2 test3 test4", which is what made this look
	// like one garbled sentence. And for the model, a blank line is the
	// unambiguous mark of "these were separate messages", which is exactly
	// what they were. What the user sees and what the agent reads agree.
	out.text = strings.Join(texts, "\n\n")
	return out, true
}

// senderRuns folds attributed segments into one Content per RUN of consecutive
// segments sharing a sender.
//
// NOT one Content per segment. Within a run the texts are joined by a BLANK
// line, which is the separator mergePromptEvents chose and for the reason it
// chose it: prose renders as markdown, where a lone newline is a soft break, so
// glamour would rejoin three messages into one garbled sentence. Splitting a
// run into separate blocks would hand that same problem to the provider, which
// concatenates a user message's text blocks.
//
// So the common case: several nudges from ONE sender, or from none at all -
// is exactly one block, byte-identical to what the fold produced before
// attribution existed. A block appears only where the sender actually CHANGES,
// which is the only place a reader needs telling.
func senderRuns(segments []promptSegment) []message.Content {
	var out []message.Content
	for _, seg := range segments {
		if seg.text == "" {
			continue
		}
		if n := len(out); n > 0 && out[n-1].Sender == seg.sender {
			out[n-1].Text += "\n\n" + seg.text
			continue
		}
		out = append(out, message.SenderText(seg.sender, seg.text))
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
		out.Patch = &rpc.FormPatch{}
		for _, src := range []*rpc.FormPatch{a.Patch, b.Patch} {
			if src == nil {
				continue
			}
			if len(src.Set) > 0 && out.Patch.Set == nil {
				out.Patch.Set = map[string]json.RawMessage{}
			}
			for k, v := range src.Set {
				out.Patch.Set[k] = v
			}
			out.Patch.Remove = append(out.Patch.Remove, src.Remove...)
		}
	}
	return out
}

// collectToolResults dispatches every call (idempotent), waits for each
// to finish, and assembles the tool_result turn in canonical (calls)
// order. It emits nothing on the wire: the blob is recomposed by the
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
		// The arguments never arrived intact, so nothing was executed. Report
		// it in the shape the API documents for exactly this case, an
		// is_error result whose content is {"INVALID_JSON": "<what arrived>"}
		//, and hand back the bytes rather than a description of them. That is
		// the whole repair path: one round trip, and the model resends the
		// call it owed us.
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
		Role:      message.RoleInput,
		Content:   append(results, images...),
		Timestamp: time.Now().UnixMilli(),
	}
	return tic
}

// toolImageBudget is the base64 ceiling for ALL tool imagery on one
// tool_result tic, derived from the configured WAL segment size.
//
// It is a hard safety limit, not taste: the tic is appended to the IR as ONE
// figwal record, and a record that does not fit inside a single WAL segment
// fails the append outright and takes the turn with it. config owns the
// derivation (segment size, its floor, and the provider ceiling above which
// bigger buys nothing) so the number moves with the store geometry instead of
// being pinned to the smallest legal configuration, a user who raises
// store.segment_size gets the headroom they paid for. Nil-safe: an agent
// constructed without settings gets the same default the store would use.
func (a *Agent) toolImageBudget() int { return a.settings.InlineImageBudget() }

// harvestToolImages pulls the image blocks out of one tool's outcome, tags
// them with the call that produced them, and reports the budget it spent plus
// a note for any image it had to alter or could not carry. Images ride along
// even when the tool errored: a screenshot attached to a failure is usually
// the whole point.
//
// An image over the remaining budget is RESCALED, not discarded. The read tool
// already fits each image to the whole-message budget at ingest, so the only
// way to arrive here is several images in one parallel round competing for one
// record, and dropping the losers would reproduce, at a different threshold,
// the silent blindness this path exists to end. Dropping is reserved for an
// image that cannot be encoded legibly in the space that is left, and it is
// announced in that tool's own result text.
func harvestToolImages(tc message.Content, oc toolOutcome, remaining, total int) (kept []message.Content, notes []string, spent int) {
	for _, c := range oc.content {
		if c.Type != message.ContentImage || c.Data == "" {
			continue
		}
		left := remaining - spent
		if len(c.Data) <= left {
			spent += len(c.Data)
			kept = append(kept, message.ToolImageContent(tc.ToolCallID, tc.ToolName, c.MimeType, c.Data))
			continue
		}
		fitted, note, ok := refitToolImage(c, left)
		if !ok {
			// Say WHY, precisely. "Exceeds the budget" would be a lie when the
			// budget was spent by an earlier call in the same round, and a model
			// reasons from what it is told.
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

// refitToolImage re-encodes one already-inlined image to fit the bytes still
// free on this record, and returns the note that keeps the model's coordinates
// honest.
//
// The factor is reported as a FURTHER multiplier rather than an absolute one:
// the ingest note already told the model how this image relates to the file on
// disk, and this pass only knows how the new pixels relate to the ones it was
// handed. Two composable factors are correct; one absolute-looking factor that
// silently measures from the wrong baseline is not.
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

// nextIndex returns the LT the next appended message will occupy.
func (a *Agent) nextIndex() uint64 {
	if e, ok := a.figLog.PeekTail(); ok {
		return e.LT + 1
	}
	return 1
}

// appendedTail returns the durable tail entry iff it sits at expectIdx
// with the expected role: i.e. the provider actually appended it.
func (a *Agent) appendedTail(expectIdx uint64, role message.Role) (store.Entry[message.Message], bool) {
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
	outcome toolOutcome // toolEnd: raw content for IR assembly
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
	// A QUARANTINED CALL NEVER RUNS. Its arguments did not arrive as valid
	// JSON, so there is nothing to run it WITH: executing on a guess is how a
	// half-parsed `edit` writes the wrong bytes into a source file. Refusing
	// here: the one chokepoint every tool passes through: leaves the call
	// with no outcome, and assembleToolResults turns that into an error result
	// the model can act on.
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
// it has appended into the log).
func (a *Agent) composeTurn(inflight *message.Message) []livedoc.Node {
	entries := a.figLog.ReadFrom(a.turnStartLT+1, 0)
	var msgs []message.Message
	for _, e := range entries {
		m := e.Payload
		m.LogicalTime = e.LT
		msgs = append(msgs, m)
	}
	if inflight != nil {
		// The provider appends the assistant message into the log concurrently
		// with the drain loop's tail of buffered stream events. Once the appended
		// copy is durable: the tail entry of this turn's window is an
		// assistant message: composing the in-flight assembly TOO would render
		// the message twice (under a bumped provisional LT, so the aria server
		// folds it as a brand-new node set: the classic duplicated-thinking
		// frame). The durable copy wins.
		if n := len(entries); n > 0 && entries[n-1].Payload.Role == message.RoleOutput {
			inflight = nil
		}
	}
	if inflight != nil {
		// The in-flight message has no LT until it appends. Stamp its provisional
		// LT: the next main-LT it will append at: so compose's stable node ids
		// (LT.blockIdx) match what they@ be post-append and don't jump at the
		// boundary. While the round is still streaming the window is EMPTY
		// (nothing appended after turnStartLT yet), so the base must be the
		// pre-turn tail LT, not a constant: falling back to 1 re-ids every
		// streamed node at the append (1.0 → realLT.0), and the aria server folds
		// the re-id as a second copy of the whole message.
		m := *inflight
		m.LogicalTime = a.turnStartLT + 1
		if n := len(entries); n > 0 {
			m.LogicalTime = entries[n-1].LT + 1
		}
		msgs = append(msgs, m)
	}
	nodes := a.projNodes(msgs, a.gov.Tails(), a.argPartials)
	if dir := os.Getenv("FIGARO_NODE_DEBUG"); dir != "" {
		logComposeFrame(dir, a.id, inflight != nil, nodes)
	}
	return nodes
}

// openToolTiming stamps the start of GENERATION: the provider has opened a
// tool block and the model is about to write its arguments. Nothing else knows
// this moment: by the time the tool is dispatched the writing is over, which
// is why a thirty-second write used to render [0ms].
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

// logComposeFrame (debug, env-gated) appends one line per composed frame so we
// can see whether a node's id churns across frames / append: the fingerprint of
// the duplication bug.
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

// emitSnapshot opens a streaming suffix on the current turn and sets its
// initial nodes. The suffix begins wherever the turn's committed nodes end, so
// a multi-round turn extends rather than restarting. The aria server diffs
// subsequent Updates against this internally.
func (a *Agent) emitSnapshot(role string, nodes []livedoc.Node) {
	a.ariaSrv.OpenTurn(a.turnID)
	if len(nodes) > 0 {
		a.ariaSrv.Update(nodes)
	}
}

// The live-emit interval coalesces high-frequency streaming emits (~11fps by
// default, `stream_emit_interval_ms`). Structural changes force an immediate
// emit; token/output streaming is throttled so a busy turn doesn't
// recompose+broadcast on every chunk: smoothness against CPU, which is the
// user's call, not ours. liveOutputTail bounds the governor's per-tool live
// tail to the same source-line cap compose renders, so the accumulator can't
// grow unbounded on a huge tool dump; that one is NOT a knob (see the survey:
// three paging invariants cite it).
const liveOutputTail = 200 // matches compose's tailBound source-line cap

func (a *Agent) liveEmitInterval() time.Duration {
	return time.Duration(a.settings.StreamEmitIntervalMs()) * time.Millisecond
}

// emitLive recomposes+broadcasts, throttled to the emit interval unless force is
// set (a structural change or a final flush). Interrupted turns emit nothing.
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
// to spaces), ellipsized when cut: used to seed a conversation's mantra.
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
// appends it into the log).
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

// combineFormInput merges client-supplied form input
// with the persisted snapshot.
//
// Two shapes, two contracts:
//
//   - Context is *purely additive*. It carries the client's view of
//     state-at-send-time; the agent sets keys whose values differ
//     from the snapshot but never derives removals from absence.
//     This lets clients ship a full form copy without racing
//     concurrent set/unset from another shell.
//   - Patch is explicit set + remove; mutations the client really
//     means. `figaro set`/`unset` land here.
//
// system.* on Context is dropped: the harness owns that namespace,
// and a stale client view must not clobber it. Patch is left intact
// (it's the user explicitly mutating; trust them).
func (a *Agent) combineFormInput(input *rpc.FormInput) form.Patch {
	if a.form == nil || input == nil {
		return form.Patch{}
	}
	snap := a.form.Snapshot()
	var clientPatch form.Patch
	if input.Patch != nil {
		clientPatch = form.Patch{Set: input.Patch.Set, Remove: input.Patch.Remove}
	}
	var ctxPatch form.Patch
	if input.Context != nil {
		ctxPatch = additivePatch(withoutSystemNS(form.FromMap(input.Context)), snap)
	}
	// Precedence: the client's passive view, then what it explicitly set.
	out := form.Patch{}
	for _, p := range []form.Patch{ctxPatch, clientPatch} {
		switch {
		case p.IsEmpty():
		case out.IsEmpty():
			out = p
		default:
			out = form.Merge(out, p)
		}
	}
	return out
}

// additivePatch returns a Set-only patch with ctx entries whose values
// differ from snap. Keys present in snap but absent from ctx are NOT
// removed: Context is purely additive by contract.
//
// Equality is the form's own (semantic JSON equality via the tree),
// not bytes.Equal: a semantically equal Set keeps the board's existing
// bytes, so a byte comparison would re-report the same key every turn and
// fire a redundant <system-reminder> each time.
func additivePatch(ctx, snap form.Snapshot) form.Patch {
	return snap.Apply(ctx.AsPatch()).Diff(snap)
}

func statusOf(err error) string {
	if err != nil {
		return "failure"
	}
	return "success"
}
