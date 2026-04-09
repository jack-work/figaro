package figaro

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/jack-work/figaro/internal/credo"
	"github.com/jack-work/figaro/internal/message"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/store"
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
)

type event struct {
	typ eventType

	// eventUserPrompt
	text string

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
	result string
	isErr  bool
}

// Config holds everything needed to construct an Agent.
type Config struct {
	ID         string
	SocketPath string
	Provider   provider.Provider
	Model      string // model ID (e.g. "claude-sonnet-4-20250514"), for metadata
	Scribe     credo.Scribe
	Cwd        string // working directory
	Root       string // project root
	MaxTokens  int
	Tools      []tool.Tool // tools available to the agent
	LogDir     string      // directory for per-figaro JSONL event log (empty = no logging)
	StoreDir   string      // directory for aria persistence (empty = ephemeral)
}

// Agent is the goroutine-based implementation of Figaro.
// All events flow through an Inbox (selfish/patient priority mailbox).
// One goroutine drains the inbox — no concurrent dispatch, no races.
// TODO: convert to child process via --figaro flag for full isolation.
type Agent struct {
	id         string
	socketPath string
	prov       provider.Provider
	model      string
	scribe     credo.Scribe
	cwd        string
	root       string
	maxTokens  int
	tools      []tool.Tool
	memStore   *store.MemStore
	storeDir   string // persisted if non-empty

	// Actor inbox — priority mailbox with selfish/patient queues.
	inbox *Inbox

	// Turn state (only accessed by the drain loop goroutine).
	pendingTools     int
	pendingToolCalls []message.Content
	systemPrompt     string
	turnCtx          context.Context // carries otel span for current turn
	endTurnSpan      func()

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
		socketPath:  cfg.SocketPath,
		prov:        cfg.Provider,
		model:       cfg.Model,
		scribe:      cfg.Scribe,
		cwd:         cfg.Cwd,
		root:        cfg.Root,
		maxTokens:   cfg.MaxTokens,
		tools:       cfg.Tools,
		storeDir:    cfg.StoreDir,
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

	// Open per-figaro event log.
	if cfg.LogDir != "" {
		os.MkdirAll(cfg.LogDir, 0700)
		logPath := filepath.Join(cfg.LogDir, cfg.ID+".jsonl")
		if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600); err == nil {
			a.logFile = f
			a.logEncoder = json.NewEncoder(f)
		}
	}

	go a.runWithRecovery(ctx)
	return a
}

// newStore creates the appropriate store for this agent.
// If StoreDir is set, creates a FileStore → MemStore chain (persistent).
// Otherwise, creates a standalone MemStore (ephemeral).
func (a *Agent) newStore() *store.MemStore {
	if a.storeDir == "" {
		return store.NewMemStore()
	}
	storePath := filepath.Join(a.storeDir, a.id+".json")
	fs, err := store.NewFileStore(storePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "figaro %s: store open error: %v (falling back to ephemeral)\n", a.id, err)
		return store.NewMemStore()
	}
	// Persist metadata for aria restoration on angelus restart.
	fs.SetMeta(&store.AriaMeta{
		Provider: a.prov.Name(),
		Model:    a.model,
		Cwd:      a.cwd,
		Root:     a.root,
	})
	return store.NewMemStoreWith(fs)
}

func (a *Agent) ID() string        { return a.id }
func (a *Agent) SocketPath() string { return a.socketPath }

func (a *Agent) SetModel(model string) {
	a.mu.Lock()
	a.model = model
	a.mu.Unlock()
	a.prov.SetModel(model)
}

func (a *Agent) Prompt(text string) {
	a.inbox.SendPatient(event{typ: eventUserPrompt, text: text})
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

	return FigaroInfo{
		ID:           a.id,
		State:        state,
		Provider:     a.prov.Name(),
		Model:        a.model,
		MessageCount: len(msgs),
		TokensIn:     a.tokensIn,
		TokensOut:    a.tokensOut,
		CreatedAt:    a.createdAt,
		LastActive:   a.lastActive,
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

	if a.logFile != nil {
		a.logFile.Close()
	}
}

// staticScribe is a Scribe that always returns the same prompt.
// Used for crash recovery and testing.
type staticScribe struct {
	prompt string
}

func (s *staticScribe) Build(ctx credo.Context) (string, error) {
	return s.prompt, nil
}

const crashPrompt = `[System: This agent crashed and was restarted. The previous ` +
	`conversation context was lost. Inform the user that you experienced ` +
	`an unexpected restart and that prior context is no longer available.]`

// runWithRecovery runs the drain loop and restarts it on panic.
// On panic: logs the stack, resets the store, swaps the inbox,
// notifies subscribers of the error, and restarts the loop.
// The figaro's registry entry, pid bindings, and socket all survive.
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
		a.tokensIn = 0
		a.tokensOut = 0
		a.mu.Unlock()

		// End any active otel span.
		if a.endTurnSpan != nil {
			a.endTurnSpan()
			a.endTurnSpan = nil
		}

		// Swap the inbox. Old goroutines captured the old inbox —
		// their SendSelfish calls will return false (closed).
		a.inbox.Close()
		a.inbox = NewInbox(ctx)

		// Reset turn state.
		a.pendingTools = 0
		a.pendingToolCalls = nil
		a.systemPrompt = ""

		// Notify subscribers of the crash.
		crashMsg := "agent crashed and was restarted"
		if a.storeDir != "" {
			crashMsg += "; context restored from last checkpoint"
		} else {
			crashMsg += "; context lost"
		}
		a.fanOut(rpc.Notification{
			JSONRPC: "2.0",
			Method:  rpc.MethodError,
			Params:  rpc.ErrorParams{Message: crashMsg},
		})

		// Inject crash scribe so the agent knows to inform the user.
		// The original scribe is replaced; crash prompt takes priority.
		a.scribe = &staticScribe{prompt: crashPrompt}

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
			a.turnCtx = turnCtx
			a.endTurnSpan = func() { span.End() }

			fmt.Fprintf(os.Stderr, "agent: event=UserPrompt text=%q\n", truncLog(evt.text, 60))

			// Build system prompt from scribe (re-templated on each prompt).
			a.systemPrompt = ""
			if a.scribe != nil {
				credoCtx := credo.CurrentContext(a.cwd, a.root, a.prov.Name(), a.model, a.id, "")
				if p, err := a.scribe.Build(credoCtx); err == nil {
					a.systemPrompt = p
				} else {
					fmt.Fprintf(os.Stderr, "figaro %s: credo build error: %v\n", a.id, err)
				}
			}

			// Append user message to store.
			userMsg := message.Message{
				Role:      message.RoleUser,
				Content:   []message.Content{message.TextContent(evt.text)},
				Timestamp: time.Now().UnixMilli(),
			}
			lt, err := a.memStore.Append(userMsg)
			if err != nil {
				fmt.Fprintf(os.Stderr, "figaro %s: append user message: %v\n", a.id, err)
				a.fanOut(rpc.Notification{
					JSONRPC: "2.0",
					Method:  rpc.MethodError,
					Params:  rpc.ErrorParams{Message: fmt.Sprintf("append user message: %s", err)},
				})
				a.endTurn("")
				continue
			}
			userMsg.LogicalTime = lt
			a.fanOut(rpc.Notification{
				JSONRPC: "2.0",
				Method:  rpc.MethodMessage,
				Params:  rpc.MessageParams{LogicalTime: lt, Message: userMsg},
			})

			// Start LLM streaming.
			a.startLLMStream(ctx, inbox)

		case eventLLMDelta:
			figOtel.Event(a.turnCtx, "figaro.notify.delta",
				attribute.String("text", evt.delta),
			)
			a.fanOut(rpc.Notification{
				JSONRPC: "2.0",
				Method:  rpc.MethodDelta,
				Params:  rpc.DeltaParams{Text: evt.delta, ContentType: evt.contentType},
			})

		case eventLLMDone:
			if evt.message == nil {
				a.fanOut(rpc.Notification{
					JSONRPC: "2.0",
					Method:  rpc.MethodError,
					Params:  rpc.ErrorParams{Message: "no response from provider"},
				})
				a.endTurn("")
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
				a.endTurn("")
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
			go a.runToolAsync(ctx, inbox, tc)

		case eventLLMError:
			fmt.Fprintf(os.Stderr, "agent: event=LLMError err=%v\n", evt.err)
			a.fanOut(rpc.Notification{
				JSONRPC: "2.0",
				Method:  rpc.MethodError,
				Params:  rpc.ErrorParams{Message: evt.err.Error()},
			})
			a.endTurn("")

		case eventToolOutput:
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
			fmt.Fprintf(os.Stderr, "agent: event=ToolResult tool=%s err=%v result_len=%d\n",
				evt.toolName, evt.isErr, len(evt.result))
			a.fanOut(rpc.Notification{
				JSONRPC: "2.0",
				Method:  rpc.MethodToolEnd,
				Params: rpc.ToolEndParams{
					ToolCallID: evt.toolCallID, ToolName: evt.toolName,
					Result: evt.result, IsError: evt.isErr,
				},
			})

			// Append tool result to store.
			resultMsg := message.NewToolResult(
				evt.toolCallID, evt.toolName,
				[]message.Content{message.TextContent(evt.result)},
				evt.isErr, 0, time.Now().UnixMilli(),
			)
			lt, err := a.memStore.Append(resultMsg)
			if err != nil {
				fmt.Fprintf(os.Stderr, "figaro %s: append tool result: %v\n", a.id, err)
			}
			resultMsg.LogicalTime = lt
			a.fanOut(rpc.Notification{
				JSONRPC: "2.0",
				Method:  rpc.MethodMessage,
				Params:  rpc.MessageParams{LogicalTime: lt, Message: resultMsg},
			})

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
				go a.runToolAsync(ctx, inbox, tc)
			} else if a.pendingTools == 0 {
				// All tools done — send results back to LLM.
				a.startLLMStream(ctx, a.inbox)
			}
		}
	}
}

// endTurn finishes the current turn. If reason is non-empty,
// a stream.done notification is sent. Resets turn state and
// yields the inbox to release the next patient message.
func (a *Agent) endTurn(reason string) {
	if reason != "" {
		a.fanOut(rpc.Notification{
			JSONRPC: "2.0",
			Method:  rpc.MethodDone,
			Params:  rpc.DoneParams{Reason: reason},
		})
	}

	// End otel span.
	if a.endTurnSpan != nil {
		a.endTurnSpan()
		a.endTurnSpan = nil
	}

	// Accumulate token counts from the latest assistant message.
	a.mu.Lock()
	a.lastActive = time.Now()
	if msgs := a.memStore.Context(); msgs != nil {
		for i := len(msgs.Messages) - 1; i >= 0; i-- {
			if m := msgs.Messages[i]; m.Usage != nil {
				a.tokensIn += m.Usage.InputTokens
				a.tokensOut += m.Usage.OutputTokens
				break // only count the latest turn's usage
			}
		}
	}
	a.mu.Unlock()

	// Flush to disk at turn boundary (no-op if no downstream FileStore).
	if err := a.memStore.Flush(); err != nil {
		fmt.Fprintf(os.Stderr, "figaro %s: store flush error: %v\n", a.id, err)
	}

	// Reset turn state.
	a.pendingTools = 0
	a.pendingToolCalls = nil
	a.systemPrompt = ""

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

	// Inject system prompt.
	if block.Header == nil && a.systemPrompt != "" {
		block.Header = &message.Message{
			Role:    message.RoleSystem,
			Content: []message.Content{message.TextContent(a.systemPrompt)},
		}
	}

	fmt.Fprintf(os.Stderr, "agent: starting LLM stream, %d messages in context\n", len(block.Messages))
	ch, err := a.prov.Send(ctx, block, a.toolDefs(), a.maxTokens)
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
	var found bool
	for _, t := range a.tools {
		if t.Name() == tc.ToolName {
			found = true
			onOutput := func(chunk []byte) {
				inbox.SendSelfish(event{
					typ:        eventToolOutput,
					toolCallID: tc.ToolCallID,
					toolName:   tc.ToolName,
					chunk:      string(chunk),
				})
			}
			result, err := t.Execute(ctx, tc.Arguments, onOutput)
			if err != nil {
				inbox.SendSelfish(event{
					typ:        eventToolResult,
					toolCallID: tc.ToolCallID,
					toolName:   tc.ToolName,
					result:     fmt.Sprintf("Error: %s", err),
					isErr:      true,
				})
			} else {
				inbox.SendSelfish(event{
					typ:        eventToolResult,
					toolCallID: tc.ToolCallID,
					toolName:   tc.ToolName,
					result:     result,
				})
			}
			return
		}
	}
	if !found {
		inbox.SendSelfish(event{
			typ:        eventToolResult,
			toolCallID: tc.ToolCallID,
			toolName:   tc.ToolName,
			result:     fmt.Sprintf("Unknown tool: %s", tc.ToolName),
			isErr:      true,
		})
	}
}

func (a *Agent) toolDefs() []provider.Tool {
	defs := make([]provider.Tool, len(a.tools))
	for i, t := range a.tools {
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
