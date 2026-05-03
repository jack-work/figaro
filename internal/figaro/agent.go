package figaro

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"text/template"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/credo"
	"github.com/jack-work/figaro/internal/message"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tokens"
	"github.com/jack-work/figaro/internal/tool"
)

type eventType int

const (
	eventUserPrompt eventType = iota
	eventToolOutput
	eventToolResult
	eventInterrupt
	eventRehydrate
	eventFigaro      // durable assistant Message landed in figStream
	eventFigaroDelta // partial assistant text — translated from a translog live entry
	eventTransLive   // routed to translog live on Recv (translated by synchronize)
	eventSendComplete
)

type event struct {
	typ eventType

	// eventUserPrompt
	text       string
	chalkboard *rpc.ChalkboardInput

	// eventToolOutput, eventToolResult
	toolCallID string
	toolName   string

	// eventToolOutput
	chunk string

	// eventToolResult
	content []message.Content
	isErr   bool
	err     error

	// eventRehydrate
	rehydratePatch message.Patch

	// eventFigaro
	figMsg message.Message

	// eventFigaroDelta
	deltaText string
	deltaCT   message.ContentType

	// eventTransLive
	transPayload []json.RawMessage

	// eventSendComplete
	sendSummary provider.ProjectionSummary
}

// Config holds everything needed to construct an Agent.
type Config struct {
	ID         string
	Label      string // optional human-readable label (persisted in aria meta)
	SocketPath string
	Provider   provider.Provider
	Model      string // model ID (e.g. "claude-sonnet-4-20250514"), for metadata
	Scribe     credo.Scribe
	Cwd        string // working directory
	Root       string // project root
	MaxTokens  int
	Tools      *tool.Registry // tools available to the agent
	LogDir     string         // directory for per-figaro JSONL event log (empty = no logging)
	Backend    store.Backend  // aria persistence (nil = ephemeral)

	// Chalkboard plumbing. Optional — nil = no chalkboard processing.
	// Caller (angelus for persistent arias; nil for ephemeral) owns
	// construction by calling chalkboard.Open at the right path; the
	// Agent calls Close on Kill.
	Chalkboard          *chalkboard.State
	ChalkboardTemplates *template.Template

	// TranslationStream is the per-aria, per-provider translation
	// stream cache. Optional — nil = no translation caching, the
	// provider re-renders from IR on every Send. Caller opens the
	// stream at arias/{id}/translations/{provider}.jsonl via
	// Backend.OpenTranslation. The Agent calls Close on Kill.
	TranslationStream store.Stream[[]json.RawMessage]
}

// Agent is the goroutine-based implementation of Figaro.
// All events flow through an Inbox (selfish/patient priority mailbox).
// One goroutine drains the inbox — no concurrent dispatch, no races.
// TODO: convert to child process via --figaro flag for full isolation.
type Agent struct {
	id         string
	label      string
	socketPath string
	prov       provider.Provider
	model      string
	scribe     credo.Scribe
	cwd        string
	root       string
	maxTokens  int
	tools      *tool.Registry
	figStream  store.Stream[message.Message]
	backend    store.Backend // nil = ephemeral

	// Chalkboard. Optional. *chalkboard.State owns the in-memory
	// snapshot AND the on-disk cache file.
	chalkboard *chalkboard.State
	cbTmpls    *template.Template

	// translog persists per-figaro-message wire projections for one
	// provider. Read on Send to populate the cache; written on each
	// StreamEvent.Done that carries a Translation. Closed on Kill.
	// Optional — ephemeral arias run without a translation cache.
	translog store.Stream[[]json.RawMessage]

	// inProgressTic is a mutable user-role Message accumulated
	// between LLM sends. Built up by eventUserPrompt and
	// eventToolResult; finalized (figStream.Append + send) when
	// ready. Nil between turns.
	inProgressTic *message.Message

	// Actor inbox — priority mailbox with selfish/patient queues.
	inbox *Inbox

	// Turn state (only accessed by the drain loop goroutine).
	pendingTools     int
	pendingToolCalls []message.Content
	systemPrompt     string
	turnCtx          context.Context // carries otel span for current turn; cancelled on interrupt / turn end
	turnCancel       context.CancelFunc
	endTurnSpan      func()
	interrupted      bool // true if current turn was interrupted; suppress error noise

	// Subscriber fan-out.
	mu sync.RWMutex
	// Channel subscribers: used in tests only. Could be extended later
	// for in-process logging or other observers.
	subscribers map[chan rpc.Notification]struct{}
	// Socket subscribers: jsonrpc server connections (CLI, frontends).
	// All notifications (deltas, tool output, done, etc.) flow through here.
	serverSubs map[*serverSubscriber]struct{}

	// Metrics.
	createdAt  time.Time
	lastActive time.Time
	tokensIn   int
	tokensOut  int
	cacheRead  int
	cacheWrite int

	// Event log.
	logEncoder *json.Encoder // nil if no log dir configured
	logFile    *os.File

	// Lifecycle.
	cancel context.CancelFunc
	done   chan struct{}
}

// NewAgent creates and starts a figaro agent.
// The agent begins draining its inbox immediately.
func NewAgent(cfg Config) *Agent {
	ctx, cancel := context.WithCancel(context.Background())

	a := &Agent{
		id:          cfg.ID,
		label:       cfg.Label,
		socketPath:  cfg.SocketPath,
		prov:        cfg.Provider,
		model:       cfg.Model,
		scribe:      cfg.Scribe,
		cwd:         cfg.Cwd,
		root:        cfg.Root,
		maxTokens:   cfg.MaxTokens,
		tools:       cfg.Tools,
		backend:     cfg.Backend,
		chalkboard:  cfg.Chalkboard,
		cbTmpls:     cfg.ChalkboardTemplates,
		translog:    cfg.TranslationStream,
		subscribers: make(map[chan rpc.Notification]struct{}),
		createdAt:   time.Now(),
		lastActive:  time.Now(),
		cancel:      cancel,
		done:        make(chan struct{}),
	}

	a.figStream = a.newStream()
	if a.translog == nil {
		a.translog = store.NewMemStream[[]json.RawMessage]()
	}
	a.inbox = NewInbox(ctx, a.figStream, a.translog)

	a.tokensIn, a.tokensOut, a.cacheRead, a.cacheWrite = sumUsage(unwrapMessages(a.figStream.Durable()))

	// Open per-figaro event log.
	if cfg.LogDir != "" {
		os.MkdirAll(cfg.LogDir, 0700)
		logPath := filepath.Join(cfg.LogDir, cfg.ID+".jsonl")
		if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600); err == nil {
			a.logFile = f
			a.logEncoder = json.NewEncoder(f)
		}
	}

	// Translation log staleness check. If any persisted entry's
	// fingerprint differs from the provider's current Fingerprint(),
	// throw out the whole log — translations are derivable from the
	// figaro timeline + the current encoder config, so regeneration
	// is the safe move on any encoder-config change. Subsequent
	// assistant turns repopulate naturally as the SSE accumulator
	// fires.
	a.invalidateTranslogIfStale()

	// Bootstrap: on a fresh aria, run the Scribe once, snapshot the
	// system prompt + a few related keys into chalkboard.system.*, and
	// emit a state-only tic carrying that patch. Subsequent turns read
	// the system prompt from the chalkboard snapshot — no re-templating
	// per turn.
	a.bootstrapIfNeeded()

	go a.runWithRecovery(ctx)
	return a
}

// newStore creates the appropriate store for this agent.

// newStream opens the canonical figaro IR Stream for this agent.
// With a Backend: opens the per-aria FileStream and seeds AriaMeta
// via the backend. Without: a MemStream (ephemeral).
func (a *Agent) newStream() store.Stream[message.Message] {
	if a.backend == nil {
		return store.NewMemStream[message.Message]()
	}
	stream, err := a.backend.Open(a.id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "figaro %s: backend open error: %v (falling back to ephemeral)\n", a.id, err)
		return store.NewMemStream[message.Message]()
	}
	// Persist metadata for aria restoration on angelus restart.
	// Preserve any existing label from disk if Config didn't supply one.
	label := a.label
	if label == "" {
		if existing, _ := a.backend.Meta(a.id); existing != nil {
			label = existing.Label
		}
	}
	if err := a.backend.SetMeta(a.id, &store.AriaMeta{
		Provider: a.prov.Name(),
		Model:    a.model,
		Cwd:      a.cwd,
		Root:     a.root,
		Label:    label,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "figaro %s: backend set meta: %v\n", a.id, err)
	}
	a.label = label
	return stream
}

func (a *Agent) ID() string         { return a.id }
func (a *Agent) SocketPath() string { return a.socketPath }

func (a *Agent) SetModel(model string) {
	a.mu.Lock()
	a.model = model
	a.mu.Unlock()
	a.prov.SetModel(model)
}

// SetLabel updates the aria's label and persists it to disk. Empty
// string clears the label. No-op for ephemeral arias (no backend to
// write meta to).
func (a *Agent) SetLabel(label string) error {
	a.mu.Lock()
	a.label = label
	a.mu.Unlock()

	if a.backend == nil {
		return nil
	}
	meta, _ := a.backend.Meta(a.id)
	if meta == nil {
		meta = &store.AriaMeta{
			Provider: a.prov.Name(),
			Model:    a.model,
			Cwd:      a.cwd,
			Root:     a.root,
		}
	}
	meta.Label = label
	return a.backend.SetMeta(a.id, meta)
}

func (a *Agent) Prompt(text string) {
	a.inbox.SendPatient(event{typ: eventUserPrompt, text: text})
}

// SubmitPrompt enqueues a prompt with the full request shape, including
// any optional chalkboard input. The agent applies chalkboard logic
// (diff against persisted snapshot, persist patch, render reminders) on
// the drain-loop goroutine.
func (a *Agent) SubmitPrompt(req rpc.PromptRequest) {
	a.inbox.SendPatient(event{
		typ:        eventUserPrompt,
		text:       req.Text,
		chalkboard: req.Chalkboard,
	})
}

// Interrupt signals the agent to abort the current turn. Selfish event,
// so it cuts in front of any pending LLM/tool work. Safe to call at any
// time — if the agent is idle, the event is harmlessly absorbed.
func (a *Agent) Interrupt() {
	a.inbox.SendSelfish(event{typ: eventInterrupt})
}

// TODO: Do we really need to do a full pass over this just to unwrap it?
func (a *Agent) Context() []message.Message {
	return unwrapMessages(a.figStream.Durable())
}

// unwrapMessages extracts the .Payload field from each Entry, returning
// the conversation as a flat []message.Message. Caller must treat the
// result as read-only — it shares storage with the stream's mirror.
func unwrapMessages(entries []store.Entry[message.Message]) []message.Message {
	if len(entries) == 0 {
		return nil
	}
	out := make([]message.Message, len(entries))
	for i, e := range entries {
		out[i] = e.Payload
		out[i].LogicalTime = e.LT
	}
	return out
}

func (a *Agent) Subscribe() <-chan rpc.Notification {
	ch := make(chan rpc.Notification, 128)
	a.mu.Lock()
	a.subscribers[ch] = struct{}{}
	a.mu.Unlock()
	return ch
}

func (a *Agent) Unsubscribe(ch <-chan rpc.Notification) {
	// We need the send-side channel to remove from the map.
	// The caller passes the receive-only channel they got from Subscribe.
	// We iterate to find the matching one.
	a.mu.Lock()
	for sch := range a.subscribers {
		if sch == ch {
			delete(a.subscribers, sch)
			close(sch)
			break
		}
	}
	a.mu.Unlock()
}

func (a *Agent) Info() FigaroInfo {
	a.mu.RLock()
	defer a.mu.RUnlock()

	msgs := a.Context()
	state := "idle"
	if !a.inbox.IsIdle() {
		state = "active"
	}

	ctxTokens, ctxExact := tokens.ContextSize(msgs)

	return FigaroInfo{
		ID:               a.id,
		Label:            a.label,
		State:            state,
		Provider:         a.prov.Name(),
		Model:            a.model,
		MessageCount:     len(msgs),
		TokensIn:         a.tokensIn,
		TokensOut:        a.tokensOut,
		CacheReadTokens:  a.cacheRead,
		CacheWriteTokens: a.cacheWrite,
		ContextTokens:    ctxTokens,
		ContextExact:     ctxExact,
		CreatedAt:        a.createdAt,
		LastActive:       a.lastActive,
	}
}

func (a *Agent) Kill() {
	a.cancel()
	<-a.done // wait for drain loop to exit

	a.mu.Lock()
	for ch := range a.subscribers {
		close(ch)
	}
	a.subscribers = nil
	a.mu.Unlock()

	// Close the figaro IR stream (per-line append already handles
	// durability — Close is a flush hook for future buffered modes).
	if err := a.figStream.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "figaro %s: figaro stream close error: %v\n", a.id, err)
	}

	// Flush the chalkboard snapshot to its on-disk cache.
	if a.chalkboard != nil {
		if err := a.chalkboard.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "figaro %s: chalkboard close error: %v\n", a.id, err)
		}
	}

	// Close the translation log.
	if a.translog != nil {
		if err := a.translog.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "figaro %s: translation log close error: %v\n", a.id, err)
		}
	}

	if a.logFile != nil {
		a.logFile.Close()
	}
}

// runWithRecovery runs the drain loop and restarts it on panic.
// On panic: logs the stack, resets the store, swaps the inbox,
// notifies subscribers of the error, and restarts the loop. The
// figaro's registry entry, pid bindings, socket, and credo all
// survive — recovery is invisible to the model. The stderr log
// line is the only artifact.
func (a *Agent) runWithRecovery(ctx context.Context) {
	defer close(a.done)

	for {
		panicked := a.actProtected(ctx)
		if !panicked {
			return // clean exit (context cancelled)
		}

		// Check if we should stop entirely.
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Reset the figaro IR stream — re-open the persistent backing
		// (or a fresh MemStream for ephemeral). For backed streams,
		// the on-disk NDJSON has the last persisted state and
		// FileStream replays it at Open.
		a.mu.Lock()
		a.figStream = a.newStream()
		a.tokensIn, a.tokensOut, a.cacheRead, a.cacheWrite = sumUsage(unwrapMessages(a.figStream.Durable()))
		a.mu.Unlock()

		// End any active otel span.
		if a.endTurnSpan != nil {
			a.endTurnSpan()
			a.endTurnSpan = nil
		}
		// Cancel any in-flight turn so provider/tool goroutines unwind.
		if a.turnCancel != nil {
			a.turnCancel()
			a.turnCancel = nil
		}

		// Swap the inbox. Old goroutines captured the old inbox —
		// their SendSelfish calls will return false (closed).
		a.inbox.Close()
		a.inbox = NewInbox(ctx, a.figStream, a.translog)

		// Reset turn state.
		a.pendingTools = 0
		a.pendingToolCalls = nil
		a.systemPrompt = ""

		// Notify subscribers of the crash. Error is advisory (informs
		// the user something went wrong); Done is the terminator that
		// tells the CLI the failed turn has fully unwound. The agent
		// itself survives via the restart loop below.
		crashMsg := "agent crashed and was restarted"
		if a.backend != nil {
			crashMsg += "; context restored from last checkpoint"
		} else {
			crashMsg += "; context lost"
		}
		a.fanOut(rpc.Notification{
			JSONRPC: "2.0",
			Method:  rpc.MethodError,
			Params:  rpc.ErrorParams{Message: crashMsg},
		})
		a.fanOut(rpc.Notification{
			JSONRPC: "2.0",
			Method:  rpc.MethodDone,
			Params:  rpc.DoneParams{Reason: "error: " + crashMsg},
		})

		// Note: the credo (a.scribe) is intentionally NOT replaced. The
		// agent retains its full identity across panics; the model is
		// not informed via injected conversation content. Operators see
		// the stderr line below, the model does not.

		fmt.Fprintf(os.Stderr, "figaro %s: restarted after panic\n", a.id)
		// Loop back to restart actProtected.
	}
}

// actProtected runs the drain loop with panic recovery.
// Returns true if it exited due to a panic, false for clean exit.
func (a *Agent) actProtected(ctx context.Context) (panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			// Log the panic and stack trace.
			stack := make([]byte, 4096)
			n := runtime.Stack(stack, false)
			fmt.Fprintf(os.Stderr, "figaro %s: panic: %v\n%s\n", a.id, r, stack[:n])
			panicked = true
		}
	}()

	a.act(ctx)
	return false
}

// act is the single event loop. Recv → synchronize → dispatch.
// synchronize translates non-figaro events into figaro events and
// handles condense; the dispatch switch only sees figaro / control
// events.
func (a *Agent) act(ctx context.Context) {
	for {
		raw, ok := a.inbox.Recv()
		if !ok {
			return
		}
		for _, evt := range a.synchronize(raw) {
			switch evt.typ {

			case eventUserPrompt:
				// Capture inbox for background goroutines. If panic recovery
				// swaps a.inbox, old goroutines push to the captured (closed)
				// inbox and silently fail.
				inbox := a.inbox

				a.mu.Lock()
				a.lastActive = time.Now()
				a.mu.Unlock()

				// Start otel span for this prompt.
				turnCtx, span := figOtel.Start(ctx, "figaro.prompt",
					figOtel.WithAttributes(
						attribute.String("figaro.id", a.id),
						attribute.String("figaro.model", a.model),
						attribute.String("figaro.provider", a.prov.Name()),
					),
				)
				// Wrap in a cancelable context so interrupts can cut short
				// in-flight LLM streams and tool executions.
				turnCtx, turnCancel := context.WithCancel(turnCtx)
				a.turnCtx = turnCtx
				a.turnCancel = turnCancel
				a.interrupted = false
				a.endTurnSpan = func() { span.End() }

				fmt.Fprintf(os.Stderr, "agent: event=UserPrompt text=%q\n", truncLog(evt.text, 60))

				// Apply chalkboard input — patch attaches to the in-progress tic.
				// agent: pretty sure most events should define chalkboard.  Just always apply it if it's not null, not just in the user prompt.
				a.applyChalkboardInput(evt.chalkboard)

				// Pick up the system prompt from chalkboard.system.prompt
				// (set at bootstrap; refreshed by figaro.rehydrate).
				// Ephemeral arias without a chalkboard fall back to a
				// per-prompt scribe build for parity.
				// I'm pretty sure there is no reason to type the system prompt at all.
				// It's just chalkboard state.  So at bootstrap the chalkboard should
				// just get built with the system prompt.  remove this property altogether
				a.systemPrompt = a.getSystemPrompt()

				// Build/extend the in-progress tic with the user's text content,
				// then finalize and send.
				a.ensureInProgressTic()
				a.inProgressTic.Content = append(a.inProgressTic.Content, message.TextContent(evt.text))

				// send to llm api
				a.finalizeAndSend(inbox)

			case eventFigaro:
				a.handleFigaro(evt.figMsg)

			case eventFigaroDelta:
				if a.interrupted {
					continue
				}
				a.fanOut(rpc.Notification{
					JSONRPC: "2.0",
					Method:  rpc.MethodDelta,
					Params:  rpc.DeltaParams{Text: evt.deltaText, ContentType: evt.deltaCT},
				})

			case eventSendComplete:
				a.handleSendComplete(evt.sendSummary, evt.err)

			case eventToolOutput:
				if a.interrupted {
					continue
				}
				a.fanOut(rpc.Notification{
					JSONRPC: "2.0",
					Method:  rpc.MethodToolOutput,
					Params: rpc.ToolOutputParams{
						ToolCallID: evt.toolCallID,
						ToolName:   evt.toolName,
						Chunk:      evt.chunk,
					},
				})

			case eventToolResult:
				// Suppress tool results that arrive after an interrupt; the
				// turn has already been ended and the store/subscribers moved on.
				if a.interrupted {
					fmt.Fprintf(os.Stderr, "agent: event=ToolResult (post-interrupt, suppressed) tool=%s\n", evt.toolName)
					continue
				}
				// Summarize text content for logging and RPC notification.
				var resultText string
				for _, c := range evt.content {
					if c.Type == message.ContentText {
						resultText += c.Text
					}
				}
				fmt.Fprintf(os.Stderr, "agent: event=ToolResult tool=%s err=%v result_len=%d\n",
					evt.toolName, evt.isErr, len(resultText))
				a.fanOut(rpc.Notification{
					JSONRPC: "2.0",
					Method:  rpc.MethodToolEnd,
					Params: rpc.ToolEndParams{
						ToolCallID: evt.toolCallID, ToolName: evt.toolName,
						Result: resultText, IsError: evt.isErr,
					},
				})

				// Append a ContentToolResult block to the in-progress tic.
				// All tool results from one assistant turn accumulate into
				// a single user-role Message; we append it to memStore at
				// finalizeAndSend (when pendingTools hits zero).
				a.ensureInProgressTic()
				a.inProgressTic.Content = append(a.inProgressTic.Content,
					message.ToolResultContent(evt.toolCallID, evt.toolName, resultText, evt.isErr))

				a.pendingTools--

				if len(a.pendingToolCalls) > 0 {
					// Start the next tool in the batch.
					inbox := a.inbox // capture for goroutine
					tc := a.pendingToolCalls[0]
					a.pendingToolCalls = a.pendingToolCalls[1:]
					a.fanOut(rpc.Notification{
						JSONRPC: "2.0",
						Method:  rpc.MethodToolStart,
						Params: rpc.ToolStartParams{
							ToolCallID: tc.ToolCallID, ToolName: tc.ToolName,
							Arguments: tc.Arguments,
						},
					})
					go a.runToolAsync(a.turnCtx, inbox, tc)
				} else if a.pendingTools == 0 {
					// All tools done — finalize the tic (one Message
					// containing all tool_result blocks) and send the
					// updated context back to the LLM.
					a.finalizeAndSend(a.inbox)
				}

			case eventRehydrate:
				// Re-run the credo and write the new system.* keys as a
				// state-only tic. No LLM stream — control plane only.
				fmt.Fprintf(os.Stderr, "agent: event=Rehydrate set=%d remove=%d\n",
					len(evt.rehydratePatch.Set), len(evt.rehydratePatch.Remove))
				tic := message.Message{
					Role:      message.RoleUser,
					Patches:   []message.Patch{evt.rehydratePatch},
					Timestamp: time.Now().UnixMilli(),
				}
				// This should get sent as a standard message on the inbox to be handled generically and then automatically appended to the log that way.
				if _, err := a.figStream.Append(store.Entry[message.Message]{Payload: tic}, true); err != nil {
					fmt.Fprintf(os.Stderr, "figaro %s: rehydrate append: %v\n", a.id, err)
					continue
				}
				a.chalkboard.Apply(evt.rehydratePatch)
				if err := a.chalkboard.Save(); err != nil {
					fmt.Fprintf(os.Stderr, "figaro %s: rehydrate chalkboard save: %v\n", a.id, err)
				}

			case eventInterrupt:
				// Idempotent: ignore if we're already idle or already interrupted.
				if a.inbox.IsIdle() || a.interrupted {
					continue
				}
				fmt.Fprintf(os.Stderr, "agent: event=Interrupt\n")
				a.interrupted = true

				// Cancel the turn context — unblocks the provider HTTP
				// stream, running tool commands, etc. Their goroutines
				// will surface ctx.Canceled errors which the post-interrupt
				// guards above silently drop.
				if a.turnCancel != nil {
					a.turnCancel()
				}

				// Tell subscribers — advisory Error + terminating Done.
				a.fanOut(rpc.Notification{
					JSONRPC: "2.0",
					Method:  rpc.MethodError,
					Params:  rpc.ErrorParams{Message: "interrupted"},
				})
				a.endTurn("interrupted")
			}
		}
	}
}

// endTurn finishes the current turn. Always sends a stream.done
// notification — the reason carries either the LLM stop_reason
// (clean turn) or "error: ..." (recoverable failure). The session
// itself survives; subscribers should treat Done as the only
// terminator and Error as advisory.
func (a *Agent) endTurn(reason string) {
	a.fanOut(rpc.Notification{
		JSONRPC: "2.0",
		Method:  rpc.MethodDone,
		Params:  rpc.DoneParams{Reason: reason},
	})

	// End otel span.
	if a.endTurnSpan != nil {
		a.endTurnSpan()
		a.endTurnSpan = nil
	}

	// Cancel the turn context so any lingering provider/tool
	// goroutines unwind. Idempotent — cancel() on a cancelled ctx
	// is a no-op.
	if a.turnCancel != nil {
		a.turnCancel()
		a.turnCancel = nil
	}

	// Accumulate token counts from the latest assistant message.
	a.mu.Lock()
	a.lastActive = time.Now()
	for _, e := range a.figStream.ScanFromEnd(64) {
		if u := e.Payload.Usage; u != nil {
			a.tokensIn += u.InputTokens
			a.tokensOut += u.OutputTokens
			a.cacheRead += u.CacheReadTokens
			a.cacheWrite += u.CacheWriteTokens
			break // only count the latest turn's usage
		}
	}
	a.mu.Unlock()

	// Persist the chalkboard snapshot at turn boundary. The figaro IR
	// stream needs no explicit checkpoint — FileStream already
	// fsyncs each Append.
	if a.chalkboard != nil {
		if err := a.chalkboard.Save(); err != nil {
			fmt.Fprintf(os.Stderr, "figaro %s: chalkboard snapshot save: %v\n", a.id, err)
		}
	}

	// Reset turn state.
	a.pendingTools = 0
	a.pendingToolCalls = nil
	a.systemPrompt = ""
	a.inProgressTic = nil

	// Release next patient message (or mark as idle).
	a.inbox.Yield()
}

// startLLMStream sends the current context to the LLM in a background
// goroutine. The provider blocks on Send while pushing events into
// the translog (live deltas, durable final). On return the goroutine
// posts eventSendComplete so the act loop can persist the projection
// summary and reset turn state.
func (a *Agent) startLLMStream(ctx context.Context, inbox *Inbox) {
	msgs := unwrapMessages(a.figStream.Durable())
	if len(msgs) == 0 {
		inbox.SendSelfish(event{typ: eventSendComplete, err: fmt.Errorf("empty context")})
		return
	}

	// agent: we always expect chalkboard snapshot to be non-nil.  Don't read from the system prompt
	// except via the chalkboard.
	var snapshot chalkboard.Snapshot
	if a.chalkboard != nil {
		snapshot = a.chalkboard.Snapshot()
	} else if a.systemPrompt != "" {
		if b, err := json.Marshal(a.systemPrompt); err == nil {
			snapshot = chalkboard.Snapshot{"system.prompt": b}
		}
	}

	// TODO: I don't think we necessarily expect there to be same number provider translations as there
	// are figaro messages.
	// This should not be called buildPriorTranslations, it should instead just get a slice of the
	// translog based on a range of figaro logical times.  That's one of the reasons why we should be
	// using a common interface when interacting with the stream.  I want to be able to get indexed
	// events from either topic more or less arbitrarily.  The inbox / bus signaling system may as well
	// be that interface.  Can just be called log, with something of a "topic" or "kind" primitive
	// all of which are indexed by a figaro logical time
	priorTranslations := a.buildPriorTranslations(msgs)

	fmt.Fprintf(os.Stderr, "agent: starting LLM stream, %d messages in context\n", len(msgs))
	go func() {
		var (
			summary provider.ProjectionSummary
			err     error
		)
		func() {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("provider send panic: %v", r)
				}
			}()
			summary, err = a.prov.Send(ctx, msgs, snapshot, priorTranslations, a.toolDefs(), a.maxTokens, inbox)
		}()
		inbox.SendSelfish(event{typ: eventSendComplete, sendSummary: summary, err: err})
	}()
}

// handleFigaro fans out a figaro IR entry as MethodMessage and
// triggers tool dispatch on assistant messages with tool_use content.
// The figStream.Append already happened — either via inbox routing
// (for figaro events from external sources) or via synchronize (for
// figaro events synthesized from a decoded translog condense).
func (a *Agent) handleFigaro(msg message.Message) {
	tail, ok := a.figStream.PeekTail()
	if !ok {
		return
	}
	stamped := tail.Payload
	stamped.LogicalTime = tail.LT
	a.fanOut(rpc.Notification{
		JSONRPC: "2.0",
		Method:  rpc.MethodMessage,
		Params:  rpc.MessageParams{LogicalTime: tail.LT, Message: stamped},
	})
	if stamped.Role != message.RoleAssistant {
		return
	}
	var toolCalls []message.Content
	for _, c := range stamped.Content {
		if c.Type == message.ContentToolCall {
			toolCalls = append(toolCalls, c)
		}
	}
	if len(toolCalls) == 0 {
		return
	}
	a.pendingToolCalls = toolCalls[1:]
	a.pendingTools = len(toolCalls)
	tc := toolCalls[0]
	a.fanOut(rpc.Notification{
		JSONRPC: "2.0",
		Method:  rpc.MethodToolStart,
		Params: rpc.ToolStartParams{
			ToolCallID: tc.ToolCallID, ToolName: tc.ToolName,
			Arguments: tc.Arguments,
		},
	})
	go a.runToolAsync(a.turnCtx, a.inbox, tc)
}

// handleSendComplete fans out the turn-end notification. Condense and
// projection persistence have already happened in synchronize.
func (a *Agent) handleSendComplete(_ provider.ProjectionSummary, sendErr error) {
	if a.interrupted {
		return
	}
	if sendErr != nil {
		a.fanOut(rpc.Notification{
			JSONRPC: "2.0",
			Method:  rpc.MethodError,
			Params:  rpc.ErrorParams{Message: sendErr.Error()},
		})
		a.endTurn("error: " + sendErr.Error())
		return
	}
	if a.pendingTools == 0 {
		var stopReason message.StopReason
		if last, ok := a.figStream.PeekTail(); ok {
			stopReason = last.Payload.StopReason
		}
		if stopReason == "" {
			stopReason = message.StopEnd
		}
		a.endTurn(string(stopReason))
	}
}

// runToolAsync executes a tool in a background goroutine and pushes
// events as selfish into the captured inbox.
func (a *Agent) runToolAsync(ctx context.Context, inbox *Inbox, tc message.Content) {
	t, ok := a.tools.Get(tc.ToolName)
	if !ok {
		inbox.SendSelfish(event{
			typ:        eventToolResult,
			toolCallID: tc.ToolCallID,
			toolName:   tc.ToolName,
			content:    []message.Content{message.TextContent(fmt.Sprintf("Unknown tool: %s", tc.ToolName))},
			isErr:      true,
		})
		return
	}
	onOutput := func(chunk []byte) {
		inbox.SendSelfish(event{
			typ:        eventToolOutput,
			toolCallID: tc.ToolCallID,
			toolName:   tc.ToolName,
			chunk:      string(chunk),
		})
	}
	content, err := t.Execute(ctx, tc.Arguments, onOutput)
	if err != nil {
		inbox.SendSelfish(event{
			typ:        eventToolResult,
			toolCallID: tc.ToolCallID,
			toolName:   tc.ToolName,
			content:    []message.Content{message.TextContent(fmt.Sprintf("Error: %s", err))},
			isErr:      true,
		})
		return
	}
	inbox.SendSelfish(event{
		typ:        eventToolResult,
		toolCallID: tc.ToolCallID,
		toolName:   tc.ToolName,
		content:    content,
	})
}

func (a *Agent) toolDefs() []provider.Tool {
	if a.tools == nil {
		return nil
	}
	list := a.tools.List()
	defs := make([]provider.Tool, len(list))
	for i, t := range list {
		defs[i] = provider.Tool{Name: t.Name(), Description: t.Description(), Parameters: t.Parameters()}
	}
	return defs
}

func (a *Agent) fanOut(n rpc.Notification) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	// Log to per-figaro event file.
	if a.logEncoder != nil {
		a.logEncoder.Encode(n)
	}

	// Channel-based subscribers (in-process).
	for ch := range a.subscribers {
		select {
		case ch <- n:
		default:
			// Subscriber is slow — drop notification rather than blocking the agent.
		}
	}

	// Socket subscribers — direct notification, ordered by the writer mutex.
	for sub := range a.serverSubs {
		if err := sub.srv.Notify(n.Method, n.Params); err != nil {
			fmt.Fprintf(os.Stderr, "figaro: notify error: %v\n", err)
		}
	}
}

func truncLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// applyChalkboardInput merges the client's chalkboard input with the
// persisted snapshot, attaches the resulting patch to the in-progress
// tic, and advances the in-memory chalkboard.State. No-op if no
// chalkboard.State is configured (ephemeral mode) or no input.
//
// Wire-protocol semantics:
//   - patch only           → apply patch directly
//   - context only         → diff context vs current, apply diff
//   - context + patch      → apply diff(context, current), then patch on top
//   - neither              → no-op
//
// The patch is attached to the in-progress tic (creating one if
// needed). When that tic is finalized via finalizeAndSend, the patch
// rides with it as part of the same Message — the IR record of "this
// turn brought these state changes."
func (a *Agent) getSystemPrompt() string {
	if a.chalkboard != nil {
		snap := a.chalkboard.Snapshot()
		if raw, ok := snap["system.prompt"]; ok {
			var s string
			if err := json.Unmarshal(raw, &s); err == nil {
				return s
			}
		}
	}
	if a.scribe == nil {
		return ""
	}
	credoCtx := credo.CurrentContext(a.prov.Name(), a.id)
	p, err := a.scribe.Build(credoCtx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "figaro %s: credo build error: %v\n", a.id, err)
		return ""
	}
	return p
}

// ensureInProgressTic guarantees that a.inProgressTic is non-nil and
// ready to accumulate Content blocks and Patches. Called by both the
// user-prompt and tool-result paths. The tic is finalized by
// finalizeAndSend.
func (a *Agent) ensureInProgressTic() {
	if a.inProgressTic == nil {
		a.inProgressTic = &message.Message{
			Role:      message.RoleUser,
			Timestamp: time.Now().UnixMilli(),
		}
	}
}

// finalizeAndSend appends the in-progress tic to the figaro IR
// stream (which allocates its LogicalTime) and starts the LLM
// stream. Clears inProgressTic. No-op if inProgressTic is nil.
func (a *Agent) finalizeAndSend(inbox *Inbox) {
	if a.inProgressTic == nil {
		return
	}
	tic := *a.inProgressTic
	a.inProgressTic = nil

	if _, err := a.figStream.Append(store.Entry[message.Message]{Payload: tic}, true); err != nil {
		fmt.Fprintf(os.Stderr, "figaro %s: append tic: %v\n", a.id, err)
		a.fanOut(rpc.Notification{
			JSONRPC: "2.0",
			Method:  rpc.MethodError,
			Params:  rpc.ErrorParams{Message: fmt.Sprintf("append tic: %s", err)},
		})
		a.endTurn("error: append tic")
		return
	}

	a.startLLMStream(a.turnCtx, inbox)
}

// sumUsage totals InputTokens / OutputTokens / CacheReadTokens /
// CacheWriteTokens across the messages. Used to seed cumulative
// counters after restore or panic recovery so they reflect the full
// history, not just this process's lifetime.
func sumUsage(msgs []message.Message) (in, out, cacheRead, cacheWrite int) {
	for _, m := range msgs {
		if m.Usage != nil {
			in += m.Usage.InputTokens
			out += m.Usage.OutputTokens
			cacheRead += m.Usage.CacheReadTokens
			cacheWrite += m.Usage.CacheWriteTokens
		}
	}
	return in, out, cacheRead, cacheWrite
}
