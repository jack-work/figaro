package figaro

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

// --- Event types (actor mailbox) ---

type eventType int

const (
	eventUserPrompt eventType = iota
	eventLLMDelta
	eventLLMDone
	eventLLMError
	eventToolOutput
	eventToolResult
	eventInterrupt
	eventRehydrate
)

type event struct {
	typ eventType

	// eventUserPrompt
	text       string
	chalkboard *rpc.ChalkboardInput // optional client-supplied state input

	// eventLLMDelta
	delta       string
	contentType message.ContentType

	// eventLLMDone
	message *message.Message

	// eventLLMError, eventToolResult (when isErr)
	err error

	// eventToolOutput, eventToolResult
	toolCallID string
	toolName   string

	// eventToolOutput
	chunk string

	// eventToolResult
	content []message.Content
	isErr   bool

	// eventRehydrate
	rehydratePatch message.Patch
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
	memStore   *store.MemStore
	backend    store.Backend // nil = ephemeral

	// Chalkboard. Optional. *chalkboard.State owns the in-memory
	// snapshot AND the on-disk cache file. cbTmpls renders patches
	// to system-reminder bodies. The agent's drain loop is the only
	// goroutine that touches these fields; no locking needed.
	chalkboard *chalkboard.State
	cbTmpls    *template.Template

	// inProgressTic is a mutable user-role Message accumulated
	// between LLM sends. Built up by eventUserPrompt and
	// eventToolResult; finalized (memStore.Append + send) when
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
		inbox:       NewInbox(ctx),
		subscribers: make(map[chan rpc.Notification]struct{}),
		createdAt:   time.Now(),
		lastActive:  time.Now(),
		cancel:      cancel,
		done:        make(chan struct{}),
	}

	// Create the store — either persistent (FileStore → MemStore chain)
	// or ephemeral (standalone MemStore).
	a.memStore = a.newStore()

	// Seed cumulative token counters from persisted Usage so restored
	// arias don't start over at zero.
	a.tokensIn, a.tokensOut, a.cacheRead, a.cacheWrite = sumUsage(a.memStore.Context())

	// Open per-figaro event log.
	if cfg.LogDir != "" {
		os.MkdirAll(cfg.LogDir, 0700)
		logPath := filepath.Join(cfg.LogDir, cfg.ID+".jsonl")
		if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600); err == nil {
			a.logFile = f
			a.logEncoder = json.NewEncoder(f)
		}
	}

	// Bootstrap: on a fresh aria, run the Scribe once, snapshot the
	// system prompt + a few related keys into chalkboard.system.*, and
	// emit a state-only tic carrying that patch. Subsequent turns read
	// the system prompt from the chalkboard snapshot — no re-templating
	// per turn.
	a.bootstrapIfNeeded()

	go a.runWithRecovery(ctx)
	return a
}

// bootstrapIfNeeded runs the Scribe once and emits a state-only tic
// (a user-role Message with no Content, only Patches) when the aria
// has no chalkboard system.prompt yet. Idempotent: a restored aria
// whose chalkboard.json already carries system.prompt is left alone.
//
// Requires both a chalkboard.State and a Scribe; either being nil
// short-circuits (ephemeral arias have no chalkboard, and tests may
// pass nil scribes).
func (a *Agent) bootstrapIfNeeded() {
	if a.chalkboard == nil || a.scribe == nil {
		return
	}
	snap := a.chalkboard.Snapshot()
	if _, ok := snap["system.prompt"]; ok {
		return // already bootstrapped (restored aria)
	}
	credoCtx := credo.CurrentContext(a.prov.Name(), a.id)
	prompt, err := a.scribe.Build(credoCtx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "figaro %s: bootstrap credo build: %v\n", a.id, err)
		return
	}

	patch := chalkboard.Patch{Set: map[string]json.RawMessage{}}
	setStr := func(key, val string) {
		if b, err := json.Marshal(val); err == nil {
			patch.Set[key] = b
		}
	}
	setStr("system.prompt", prompt)
	setStr("system.model", a.model)
	setStr("system.provider", a.prov.Name())

	tic := message.Message{
		Role:      message.RoleUser,
		Patches:   []message.Patch{patch},
		Timestamp: time.Now().UnixMilli(),
	}
	if _, err := a.memStore.Append(tic); err != nil {
		fmt.Fprintf(os.Stderr, "figaro %s: bootstrap append tic: %v\n", a.id, err)
		return
	}
	a.chalkboard.Apply(patch)
	if err := a.chalkboard.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "figaro %s: bootstrap chalkboard save: %v\n", a.id, err)
	}
	if err := a.memStore.Flush(); err != nil {
		fmt.Fprintf(os.Stderr, "figaro %s: bootstrap memStore flush: %v\n", a.id, err)
	}
}

// newStore creates the appropriate store for this agent.
// With a Backend: opens a per-aria Downstream, wraps in MemStore.
// Without: a standalone MemStore (ephemeral).
func (a *Agent) newStore() *store.MemStore {
	if a.backend == nil {
		return store.NewMemStore()
	}
	ds, err := a.backend.Open(a.id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "figaro %s: backend open error: %v (falling back to ephemeral)\n", a.id, err)
		return store.NewMemStore()
	}
	// Persist metadata for aria restoration on angelus restart.
	// Preserve any existing label from disk if Config didn't supply one.
	label := a.label
	if label == "" {
		if existing := ds.Meta(); existing != nil {
			label = existing.Label
		}
	}
	ds.SetMeta(&store.AriaMeta{
		Provider: a.prov.Name(),
		Model:    a.model,
		Cwd:      a.cwd,
		Root:     a.root,
		Label:    label,
	})
	a.label = label
	return store.NewMemStoreWith(ds)
}

func (a *Agent) ID() string        { return a.id }
func (a *Agent) SocketPath() string { return a.socketPath }

func (a *Agent) SetModel(model string) {
	a.mu.Lock()
	a.model = model
	a.mu.Unlock()
	a.prov.SetModel(model)
}

// SetLabel updates the aria's label and persists it to disk. Empty
// string clears the label. Returns any error from the persistence
// flush. Safe to call during an active turn — Flush snapshots the
// current WAL without altering it.
func (a *Agent) SetLabel(label string) error {
	a.mu.Lock()
	a.label = label
	a.mu.Unlock()

	ds := a.memStore.Downstream()
	if ds == nil {
		return nil // ephemeral — no file to update
	}
	meta := ds.Meta()
	if meta == nil {
		meta = &store.AriaMeta{
			Provider: a.prov.Name(),
			Model:    a.model,
			Cwd:      a.cwd,
			Root:     a.root,
		}
	}
	meta.Label = label
	ds.SetMeta(meta)
	return a.memStore.Flush()
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

func (a *Agent) Context() []message.Message {
	block := a.memStore.Context()
	if block == nil {
		return nil
	}
	return block.Messages
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

	ctxTokens, ctxExact := tokens.ContextSize(a.memStore.Context())

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

	// Flush and close the store (writes final state to disk).
	if err := a.memStore.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "figaro %s: store close error: %v\n", a.id, err)
	}

	// Flush the chalkboard snapshot to its on-disk cache.
	if a.chalkboard != nil {
		if err := a.chalkboard.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "figaro %s: chalkboard close error: %v\n", a.id, err)
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
		panicked := a.drainLoopProtected(ctx)
		if !panicked {
			return // clean exit (context cancelled)
		}

		// Check if we should stop entirely.
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Reset store — re-create with persistence if configured.
		// The downstream file has the last flushed state (pre-crash),
		// so NewMemStoreWith will seed from the last known-good snapshot.
		a.mu.Lock()
		a.memStore = a.newStore()
		a.tokensIn, a.tokensOut, a.cacheRead, a.cacheWrite = sumUsage(a.memStore.Context())
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
		a.inbox = NewInbox(ctx)

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
		// Loop back to restart drainLoopProtected.
	}
}

// drainLoopProtected runs the drain loop with panic recovery.
// Returns true if it exited due to a panic, false for clean exit.
func (a *Agent) drainLoopProtected(ctx context.Context) (panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			// Log the panic and stack trace.
			stack := make([]byte, 4096)
			n := runtime.Stack(stack, false)
			fmt.Fprintf(os.Stderr, "figaro %s: panic: %v\n%s\n", a.id, r, stack[:n])
			panicked = true
		}
	}()

	a.drainLoop(ctx)
	return false
}

// drainLoop is the single event loop (actor model).
// All events flow through the Inbox. Patient events (user prompts) are
// held until the current turn yields. Selfish events (LLM/tool) are
// delivered immediately. The loop just processes whatever Recv returns.
func (a *Agent) drainLoop(ctx context.Context) {
	for {
		evt, ok := a.inbox.Recv()
		if !ok {
			return // inbox closed (context cancelled or panic recovery)
		}

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
			a.applyChalkboardInput(evt.chalkboard)

			// Pick up the system prompt from chalkboard.system.prompt
			// (set at bootstrap; refreshed by figaro.rehydrate).
			// Ephemeral arias without a chalkboard fall back to a
			// per-prompt scribe build for parity.
			a.systemPrompt = a.resolveSystemPrompt()

			// Build/extend the in-progress tic with the user's text content,
			// then finalize and send.
			a.ensureInProgressTic()
			a.inProgressTic.Content = append(a.inProgressTic.Content, message.TextContent(evt.text))
			a.finalizeAndSend(inbox)

		case eventLLMDelta:
			if a.interrupted {
				continue
			}
			figOtel.Event(a.turnCtx, "figaro.notify.delta",
				attribute.String("text", evt.delta),
			)
			a.fanOut(rpc.Notification{
				JSONRPC: "2.0",
				Method:  rpc.MethodDelta,
				Params:  rpc.DeltaParams{Text: evt.delta, ContentType: evt.contentType},
			})

		case eventLLMDone:
			if a.interrupted {
				fmt.Fprintf(os.Stderr, "agent: event=LLMDone (post-interrupt, suppressed)\n")
				continue
			}
			if evt.message == nil {
				a.fanOut(rpc.Notification{
					JSONRPC: "2.0",
					Method:  rpc.MethodError,
					Params:  rpc.ErrorParams{Message: "no response from provider"},
				})
				a.endTurn("error: no response from provider")
				continue
			}
			fmt.Fprintf(os.Stderr, "agent: event=LLMDone stop_reason=%s\n", evt.message.StopReason)

			// Append assistant message to store.
			lt, err := a.memStore.Append(*evt.message)
			if err != nil {
				a.fanOut(rpc.Notification{
					JSONRPC: "2.0",
					Method:  rpc.MethodError,
					Params:  rpc.ErrorParams{Message: fmt.Sprintf("append assistant message: %s", err)},
				})
				a.endTurn("error: append assistant message")
				continue
			}
			evt.message.LogicalTime = lt
			a.fanOut(rpc.Notification{
				JSONRPC: "2.0",
				Method:  rpc.MethodMessage,
				Params:  rpc.MessageParams{LogicalTime: lt, Message: *evt.message},
			})

			// Check for tool calls.
			var toolCalls []message.Content
			for _, c := range evt.message.Content {
				if c.Type == message.ContentToolCall {
					toolCalls = append(toolCalls, c)
				}
			}

			if len(toolCalls) == 0 {
				// No tool calls — turn is complete.
				a.endTurn(string(evt.message.StopReason))
				continue
			}

			// Execute tools sequentially: start the first one,
			// queue the rest.
			inbox := a.inbox // capture for goroutine
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
			go a.runToolAsync(a.turnCtx, inbox, tc)

		case eventLLMError:
			// If we were interrupted, the provider's ctx.Done() will
			// usually surface as an error here. Swallow it silently —
			// the interrupt handler already ended the turn.
			if a.interrupted {
				fmt.Fprintf(os.Stderr, "agent: event=LLMError (post-interrupt, suppressed) err=%v\n", evt.err)
				continue
			}
			fmt.Fprintf(os.Stderr, "agent: event=LLMError err=%v\n", evt.err)
			a.fanOut(rpc.Notification{
				JSONRPC: "2.0",
				Method:  rpc.MethodError,
				Params:  rpc.ErrorParams{Message: evt.err.Error()},
			})
			a.endTurn("error: " + evt.err.Error())

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
			lt, err := a.memStore.Append(tic)
			if err != nil {
				fmt.Fprintf(os.Stderr, "figaro %s: rehydrate append: %v\n", a.id, err)
				continue
			}
			tic.LogicalTime = lt
			a.chalkboard.Apply(evt.rehydratePatch)
			if err := a.chalkboard.Save(); err != nil {
				fmt.Fprintf(os.Stderr, "figaro %s: rehydrate chalkboard save: %v\n", a.id, err)
			}
			if err := a.memStore.Flush(); err != nil {
				fmt.Fprintf(os.Stderr, "figaro %s: rehydrate flush: %v\n", a.id, err)
			}
			a.fanOut(rpc.Notification{
				JSONRPC: "2.0",
				Method:  rpc.MethodMessage,
				Params:  rpc.MessageParams{LogicalTime: lt, Message: tic},
			})

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
	if msgs := a.memStore.Context(); msgs != nil {
		for i := len(msgs.Messages) - 1; i >= 0; i-- {
			if m := msgs.Messages[i]; m.Usage != nil {
				a.tokensIn += m.Usage.InputTokens
				a.tokensOut += m.Usage.OutputTokens
				a.cacheRead += m.Usage.CacheReadTokens
				a.cacheWrite += m.Usage.CacheWriteTokens
				break // only count the latest turn's usage
			}
		}
	}
	a.mu.Unlock()

	// Flush to disk at turn boundary (no-op if no downstream FileStore).
	if err := a.memStore.Flush(); err != nil {
		fmt.Fprintf(os.Stderr, "figaro %s: store flush error: %v\n", a.id, err)
	}

	// Persist the chalkboard snapshot at the same lifecycle point as
	// MemStore.Flush.
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
// goroutine. Events are pushed as selfish into the captured inbox.
func (a *Agent) startLLMStream(ctx context.Context, inbox *Inbox) {
	block := a.memStore.Context()
	if block == nil {
		inbox.SendSelfish(event{typ: eventLLMError, err: fmt.Errorf("empty context")})
		return
	}

	// Snapshot the chalkboard for the provider — system.prompt and
	// any other harness-managed keys ride here. Ephemeral arias have
	// no chalkboard; in that case we synthesize a transient snapshot
	// from a.systemPrompt so the provider's "read system.prompt" path
	// works uniformly.
	var snapshot chalkboard.Snapshot
	if a.chalkboard != nil {
		snapshot = a.chalkboard.Snapshot()
	} else if a.systemPrompt != "" {
		if b, err := json.Marshal(a.systemPrompt); err == nil {
			snapshot = chalkboard.Snapshot{"system.prompt": b}
		}
	}

	fmt.Fprintf(os.Stderr, "agent: starting LLM stream, %d messages in context\n", len(block.Messages))
	ch, err := a.prov.Send(ctx, block, snapshot, a.toolDefs(), a.maxTokens)
	if err != nil {
		inbox.SendSelfish(event{typ: eventLLMError, err: fmt.Errorf("provider send: %w", err)})
		return
	}

	go func() {
		for sev := range ch {
			if sev.Delta != "" {
				if !inbox.SendSelfish(event{
					typ: eventLLMDelta,
					delta: sev.Delta, contentType: sev.ContentType,
				}) {
					return // inbox closed
				}
			}
			if sev.Done {
				if sev.Err != nil {
					inbox.SendSelfish(event{typ: eventLLMError, err: sev.Err})
				} else {
					inbox.SendSelfish(event{typ: eventLLMDone, message: sev.Message})
				}
				return
			}
		}
		inbox.SendSelfish(event{typ: eventLLMError, err: fmt.Errorf("stream ended unexpectedly")})
	}()
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
func (a *Agent) applyChalkboardInput(input *rpc.ChalkboardInput) {
	if a.chalkboard == nil || input == nil {
		return
	}

	// Convert wire-shape patch (rpc.ChalkboardPatch) to chalkboard.Patch.
	var clientPatch chalkboard.Patch
	if input.Patch != nil {
		clientPatch = chalkboard.Patch{Set: input.Patch.Set, Remove: input.Patch.Remove}
	}

	// system.* is harness-reserved (set by bootstrap / rehydrate);
	// clients can't manage those keys. Strip them from the snapshot
	// the client's Context is diffed against, so unmentioned system.*
	// keys aren't interpreted as removals.
	snap := withoutSystemNS(a.chalkboard.Snapshot())

	var combined chalkboard.Patch
	switch {
	case input.Context != nil && input.Patch != nil:
		// Drift-detection: diff context vs persisted; apply patch on top.
		ctx := withoutSystemNS(chalkboard.Snapshot(input.Context))
		drift := ctx.Diff(snap)
		combined = chalkboard.Merge(drift, clientPatch)
	case input.Context != nil:
		// Context-only: server diffs and applies.
		ctx := withoutSystemNS(chalkboard.Snapshot(input.Context))
		combined = ctx.Diff(snap)
	case input.Patch != nil:
		// Patch-only: apply directly.
		combined = clientPatch
	}

	if combined.IsEmpty() {
		return
	}

	// Attach to the in-progress tic.
	a.ensureInProgressTic()
	a.inProgressTic.Patches = append(a.inProgressTic.Patches, combined)

	// Advance the chalkboard's in-memory snapshot. (Persisted to disk
	// at endTurn via chalkboard.Save.)
	a.chalkboard.Apply(combined)
}

// Rehydrate re-runs the Scribe and writes its output to
// chalkboard.system.* as a fresh state-only tic. The diff (vs the
// current chalkboard) is what actually gets stored; if nothing
// changed, no tic is appended and the response reports an empty
// patch.
//
// dryRun computes the diff but doesn't persist anything — used by
// `figaro rehydrate --dry-run` to preview what would change.
//
// Returns the keys set / removed by the patch. Errors from the
// scribe (e.g. credo.md unreadable) bubble up unchanged.
func (a *Agent) Rehydrate(dryRun bool) (set []string, removed []string, applied bool, err error) {
	if a.chalkboard == nil || a.scribe == nil {
		return nil, nil, false, fmt.Errorf("rehydrate requires both a chalkboard and a scribe")
	}
	credoCtx := credo.CurrentContext(a.prov.Name(), a.id)
	prompt, buildErr := a.scribe.Build(credoCtx)
	if buildErr != nil {
		return nil, nil, false, fmt.Errorf("rehydrate build: %w", buildErr)
	}

	desired := chalkboard.Snapshot{}
	setStr := func(key, val string) {
		if b, mErr := json.Marshal(val); mErr == nil {
			desired[key] = b
		}
	}
	setStr("system.prompt", prompt)
	setStr("system.model", a.model)
	setStr("system.provider", a.prov.Name())

	// Diff against just the system.* slice of current — leave any
	// non-system keys (cwd, datetime, etc.) untouched.
	currentSystem := systemNSOnly(a.chalkboard.Snapshot())
	patch := desired.Diff(currentSystem)

	for k := range patch.Set {
		set = append(set, k)
	}
	removed = append(removed, patch.Remove...)

	if patch.IsEmpty() || dryRun {
		return set, removed, false, nil
	}

	// Apply via the inbox so we don't race the drain loop.
	a.inbox.SendPatient(event{typ: eventRehydrate, rehydratePatch: patch})
	return set, removed, true, nil
}

// withoutSystemNS returns a clone of the snapshot with all keys
// under the harness-reserved system.* namespace removed. Used to
// keep client-supplied Context inputs from accidentally erasing
// bootstrap / rehydrate state during diffing.
func withoutSystemNS(s chalkboard.Snapshot) chalkboard.Snapshot {
	out := make(chalkboard.Snapshot, len(s))
	for k, v := range s {
		if strings.HasPrefix(k, "system.") {
			continue
		}
		out[k] = v
	}
	return out
}

// systemNSOnly is the symmetric helper: returns a clone containing
// only system.* keys. Used by Rehydrate to compute its diff against
// the harness-managed slice of state.
func systemNSOnly(s chalkboard.Snapshot) chalkboard.Snapshot {
	out := chalkboard.Snapshot{}
	for k, v := range s {
		if strings.HasPrefix(k, "system.") {
			out[k] = v
		}
	}
	return out
}

// resolveSystemPrompt returns the system prompt for the next Send.
// Reads from chalkboard.system.prompt (set at bootstrap) when available,
// otherwise falls back to a per-prompt Scribe build (ephemeral arias).
func (a *Agent) resolveSystemPrompt() string {
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

// finalizeAndSend appends the in-progress tic to the memStore (which
// allocates its LogicalTime) and starts the LLM stream. Clears
// inProgressTic. No-op if inProgressTic is nil.
func (a *Agent) finalizeAndSend(inbox *Inbox) {
	if a.inProgressTic == nil {
		return
	}
	tic := *a.inProgressTic
	a.inProgressTic = nil

	lt, err := a.memStore.Append(tic)
	if err != nil {
		fmt.Fprintf(os.Stderr, "figaro %s: append tic: %v\n", a.id, err)
		a.fanOut(rpc.Notification{
			JSONRPC: "2.0",
			Method:  rpc.MethodError,
			Params:  rpc.ErrorParams{Message: fmt.Sprintf("append tic: %s", err)},
		})
		a.endTurn("error: append tic")
		return
	}
	tic.LogicalTime = lt
	a.fanOut(rpc.Notification{
		JSONRPC: "2.0",
		Method:  rpc.MethodMessage,
		Params:  rpc.MessageParams{LogicalTime: lt, Message: tic},
	})

	a.startLLMStream(a.turnCtx, inbox)
}

// sumUsage totals InputTokens / OutputTokens / CacheReadTokens /
// CacheWriteTokens across a block's messages. Used to seed cumulative
// counters after restore or panic recovery so they reflect the full
// history, not just this process's lifetime.
func sumUsage(block *message.Block) (in, out, cacheRead, cacheWrite int) {
	if block == nil {
		return 0, 0, 0, 0
	}
	for _, m := range block.Messages {
		if m.Usage != nil {
			in += m.Usage.InputTokens
			out += m.Usage.OutputTokens
			cacheRead += m.Usage.CacheReadTokens
			cacheWrite += m.Usage.CacheWriteTokens
		}
	}
	return in, out, cacheRead, cacheWrite
}
