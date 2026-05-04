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
	eventSet
	eventFigaro         // durable assistant Message landed in figStream
	eventFigaroDelta    // partial assistant text — translated from a translator live entry
	eventTranslatorLive // routed to translator live on Recv (translated by synchronize)
	eventStartLLMStream // figStream is finalized for this turn; ship it
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

	// eventRehydrate / eventSet
	rehydratePatch message.Patch
	setPatch       message.Patch

	// eventFigaro
	figMsg message.Message
	figLT  uint64

	// eventFigaroDelta
	deltaText string
	deltaCT   message.ContentType

	// eventTranslatorLive
	translatorPayload []json.RawMessage
}

// Config is the constructor input for NewAgent.
type Config struct {
	ID         string
	Label      string
	SocketPath string
	Provider   provider.Provider
	Model      string
	Scribe     credo.Scribe
	Cwd        string
	Root       string
	MaxTokens  int
	Tools      *tool.Registry
	LogDir     string        // empty = no per-agent event log
	Backend    store.Backend // nil = ephemeral

	// Chalkboard — nil creates an in-memory one. Closed by Kill.
	Chalkboard *chalkboard.State
	// TranslatorStream — per-aria, per-provider cache. Nil falls
	// back to MemStream. Closed by Kill.
	TranslatorStream store.Stream[[]json.RawMessage]
}

// Agent is the actor implementation of Figaro. One inbox, one drain
// goroutine — no concurrent dispatch.
// TODO: child-process isolation via --figaro flag.
type Agent struct {
	id         string
	socketPath string
	prov       provider.Provider
	scribe     credo.Scribe
	maxTokens  int
	tools      *tool.Registry
	figStream  store.Stream[message.Message]
	translator store.Stream[[]json.RawMessage]
	backend    store.Backend // nil = ephemeral
	chalkboard *chalkboard.State
	derived    *derivedFanout // nil = ephemeral; per-figaro durable derivations

	// Live-tail watermark for catchUpFigaroDelta; reset by condenseLive.
	lastDecodedLiveLen int

	// User-role Message accumulated across one turn (text + tool
	// results). Finalized by finalizeAndSend.
	inProgressTic *message.Message

	inbox *Inbox

	// Turn state — only touched by the drain goroutine.
	pendingTools     int
	pendingToolCalls []message.Content
	turnCtx          context.Context // carries otel span for current turn; cancelled on interrupt / turn end
	turnCancel       context.CancelFunc
	endTurnSpan      func()
	interrupted      bool // true if current turn was interrupted; suppress error noise

	mu          sync.RWMutex
	subscribers map[chan rpc.Notification]struct{} // tests
	serverSubs  map[*serverSubscriber]struct{}     // socket clients

	createdAt  time.Time
	lastActive time.Time
	tokensIn   int
	tokensOut  int
	cacheRead  int
	cacheWrite int

	logEncoder *json.Encoder
	logFile    *os.File

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
		scribe:      cfg.Scribe,
		maxTokens:   cfg.MaxTokens,
		tools:       cfg.Tools,
		backend:     cfg.Backend,
		chalkboard:  cfg.Chalkboard,
		translator:  cfg.TranslatorStream,
		subscribers: make(map[chan rpc.Notification]struct{}),
		createdAt:   time.Now(),
		lastActive:  time.Now(),
		cancel:      cancel,
		done:        make(chan struct{}),
	}

	a.figStream = a.newStream(cfg.Model)
	if a.translator == nil {
		a.translator = store.NewMemStream[[]json.RawMessage]()
	}
	if a.chalkboard == nil {
		// Ephemeral arias get an in-memory chalkboard so system prompt
		// flow stays uniform.
		a.chalkboard, _ = chalkboard.Open("")
	}
	// Seed configured fields into the chalkboard. system.* keys
	// are the canonical source of truth; nothing else stores them.
	seed := chalkboard.Patch{Set: map[string]json.RawMessage{}}
	if cfg.Model != "" {
		seed.Set2("system.model", cfg.Model)
	}
	if cfg.Label != "" {
		seed.Set2("system.label", cfg.Label)
	}
	if cfg.Cwd != "" {
		seed.Set2("system.cwd", cfg.Cwd)
	}
	if cfg.Root != "" {
		seed.Set2("system.root", cfg.Root)
	}
	if cfg.Provider != nil {
		seed.Set2("system.provider", cfg.Provider.Name())
	}
	if !seed.IsEmpty() {
		a.chalkboard.Apply(seed)
	}
	a.inbox = NewInbox(ctx, a.figStream, a.translator)

	a.tokensIn, a.tokensOut, a.cacheRead, a.cacheWrite = sumUsage(unwrapMessages(a.figStream.Durable()))

	if cfg.LogDir != "" {
		os.MkdirAll(cfg.LogDir, 0700)
		logPath := filepath.Join(cfg.LogDir, cfg.ID+".jsonl")
		if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600); err == nil {
			a.logFile = f
			a.logEncoder = json.NewEncoder(f)
		}
	}

	a.invalidateTranslatorIfStale()
	a.bootstrapIfNeeded(cfg.Model)

	// Spin up the per-figaro durable-derivation fanout. Each
	// registered DurableDerivation gets its own goroutine + inbox;
	// each writes to arias/<id>/<filename>. Only lives for backed
	// agents.
	a.derived = startDerived(ctx, a.id, a.prov.Name(), a.backend, a.figStream, a.translator)
	a.derived.Tick(0, a.chalkboard.Snapshot()) // initial materialization

	go a.runWithRecovery(ctx)
	return a
}

// newStream opens the figaro IR stream — FileStream when persisted,
// MemStream when ephemeral.
func (a *Agent) newStream(model string) store.Stream[message.Message] {
	if a.backend == nil {
		return store.NewMemStream[message.Message]()
	}
	stream, err := a.backend.Open(a.id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "figaro %s: backend open error: %v (falling back to ephemeral)\n", a.id, err)
		return store.NewMemStream[message.Message]()
	}
	return stream
}

func (a *Agent) ID() string         { return a.id }
func (a *Agent) SocketPath() string { return a.socketPath }

// chalkboardString reads a system.* string key. Empty when missing
// or the chalkboard isn't configured.
func (a *Agent) chalkboardString(key string) string {
	if a.chalkboard == nil {
		return ""
	}
	raw, ok := a.chalkboard.Snapshot()[key]
	if !ok {
		return ""
	}
	var s string
	json.Unmarshal(raw, &s)
	return s
}

func (a *Agent) currentModel() string { return a.chalkboardString("system.model") }
func (a *Agent) currentLabel() string { return a.chalkboardString("system.label") }

func (a *Agent) SetModel(m string) {
	p := chalkboard.Patch{Set: map[string]json.RawMessage{}}
	p.Set2("system.model", m)
	a.chalkboard.Apply(p)
	_ = a.chalkboard.Save()
	a.prov.SetModel(m)
}

// SetLabel writes system.label to the chalkboard. Empty removes
// the key.
func (a *Agent) SetLabel(label string) error {
	if a.chalkboard == nil {
		return nil
	}
	p := chalkboard.Patch{}
	if label == "" {
		p.Remove = []string{"system.label"}
	} else {
		p.Set = map[string]json.RawMessage{}
		p.Set2("system.label", label)
	}
	a.chalkboard.Apply(p)
	return a.chalkboard.Save()
}

func (a *Agent) Prompt(text string) {
	a.inbox.SendPatient(event{typ: eventUserPrompt, text: text})
}

// SubmitPrompt enqueues a prompt with optional chalkboard input.
func (a *Agent) SubmitPrompt(req rpc.PromptRequest) {
	a.inbox.SendPatient(event{
		typ:        eventUserPrompt,
		text:       req.Text,
		chalkboard: req.Chalkboard,
	})
}

// Interrupt aborts the current turn. Selfish — cuts in. Idempotent
// when idle.
func (a *Agent) Interrupt() {
	a.inbox.SendSelfish(event{typ: eventInterrupt})
}

func (a *Agent) Context() []message.Message {
	return unwrapMessages(a.figStream.Durable())
}

// unwrapMessages projects entries to a flat []Message, stamping
// LogicalTime from the entry LT. Result shares storage with the
// stream — read-only.
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
	// The receive-only channel we got from Subscribe doesn't compare
	// equal to the send-side key in the map; iterate to match.
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
		Label:            a.currentLabel(),
		State:            state,
		Provider:         a.prov.Name(),
		Model:            a.currentModel(),
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
	<-a.done       // wait for drain loop to exit
	a.derived.Wait() // wait for derivation loops (so disk writes finish before close)

	a.mu.Lock()
	for ch := range a.subscribers {
		close(ch)
	}
	a.subscribers = nil
	a.mu.Unlock()

	if err := a.figStream.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "figaro %s: figStream close: %v\n", a.id, err)
	}
	if a.chalkboard != nil {
		if err := a.chalkboard.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "figaro %s: chalkboard close: %v\n", a.id, err)
		}
	}
	if a.translator != nil {
		if err := a.translator.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "figaro %s: translator close: %v\n", a.id, err)
		}
	}
	if a.logFile != nil {
		a.logFile.Close()
	}
}

// runWithRecovery drives the drain loop and restarts it on panic.
// Identity (id, registry entry, PID bindings, socket, credo)
// survives — recovery is invisible to the model. Operators get the
// stderr line.
func (a *Agent) runWithRecovery(ctx context.Context) {
	defer close(a.done)

	for {
		if !a.actProtected(ctx) {
			return
		}
		select {
		case <-ctx.Done():
			return
		default:
		}

		a.mu.Lock()
		a.figStream = a.newStream(a.currentModel())
		a.tokensIn, a.tokensOut, a.cacheRead, a.cacheWrite = sumUsage(unwrapMessages(a.figStream.Durable()))
		a.mu.Unlock()

		if a.endTurnSpan != nil {
			a.endTurnSpan()
			a.endTurnSpan = nil
		}
		if a.turnCancel != nil {
			a.turnCancel()
			a.turnCancel = nil
		}

		a.inbox.Close()
		a.inbox = NewInbox(ctx, a.figStream, a.translator)

		a.pendingTools = 0
		a.pendingToolCalls = nil

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

		fmt.Fprintf(os.Stderr, "figaro %s: restarted after panic\n", a.id)
	}
}

// actProtected runs the drain loop under a panic recover. Returns
// true on panic, false on clean exit.
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

// act is the single event loop: Recv → synchronize → dispatch.
// synchronize handles all translator orchestration; the switch only
// sees figaro / control events.
func (a *Agent) act(ctx context.Context) {
	for {
		raw, ok := a.inbox.Recv()
		if !ok {
			return
		}
		for _, evt := range a.synchronize(raw) {
			switch evt.typ {

			case eventUserPrompt:
				// Capture inbox in case panic recovery swaps it.
				inbox := a.inbox

				a.mu.Lock()
				a.lastActive = time.Now()
				a.mu.Unlock()

				turnCtx, span := figOtel.Start(ctx, "figaro.prompt",
					figOtel.WithAttributes(
						attribute.String("figaro.id", a.id),
						attribute.String("figaro.model", a.currentModel()),
						attribute.String("figaro.provider", a.prov.Name()),
					),
				)
				turnCtx, turnCancel := context.WithCancel(turnCtx)
				a.turnCtx = turnCtx
				a.turnCancel = turnCancel
				a.interrupted = false
				a.endTurnSpan = func() { span.End() }

				fmt.Fprintf(os.Stderr, "agent: event=UserPrompt text=%q\n", truncLog(evt.text, 60))

				a.ensureInProgressTic()
				a.inProgressTic.Content = append(a.inProgressTic.Content, message.TextContent(evt.text))
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

			case eventStartLLMStream:
				if a.interrupted {
					continue
				}
				a.startLLMStream(a.turnCtx, a.inbox)

			case eventSendComplete:
				a.handleSendComplete(evt.err)

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
				if a.interrupted {
					fmt.Fprintf(os.Stderr, "agent: event=ToolResult (post-interrupt, suppressed) tool=%s\n", evt.toolName)
					continue
				}
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

				// All tool results from one assistant turn accumulate
				// onto one user-role tic, finalized when the last
				// tool finishes.
				a.ensureInProgressTic()
				a.inProgressTic.Content = append(a.inProgressTic.Content,
					message.ToolResultContent(evt.toolCallID, evt.toolName, resultText, evt.isErr))

				a.pendingTools--

				if len(a.pendingToolCalls) > 0 {
					inbox := a.inbox
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
					a.finalizeAndSend(a.inbox)
				}

			case eventRehydrate:
				fmt.Fprintf(os.Stderr, "agent: event=Rehydrate set=%d remove=%d\n",
					len(evt.rehydratePatch.Set), len(evt.rehydratePatch.Remove))
				tic := message.Message{
					Role:      message.RoleUser,
					Patches:   []message.Patch{evt.rehydratePatch},
					Timestamp: time.Now().UnixMilli(),
				}
				if _, err := a.figStream.Append(store.Entry[message.Message]{Payload: tic}, true); err != nil {
					fmt.Fprintf(os.Stderr, "figaro %s: rehydrate append: %v\n", a.id, err)
					a.inbox.Yield()
					continue
				}
				a.chalkboard.Apply(evt.rehydratePatch)
				if err := a.chalkboard.Save(); err != nil {
					fmt.Fprintf(os.Stderr, "figaro %s: rehydrate chalkboard save: %v\n", a.id, err)
				}
				a.derived.Tick(0, a.chalkboard.Snapshot())
				a.inbox.Yield()

			case eventSet:
				fmt.Fprintf(os.Stderr, "agent: event=Set set=%d remove=%d\n",
					len(evt.setPatch.Set), len(evt.setPatch.Remove))
				tic := message.Message{
					Role:      message.RoleUser,
					Patches:   []message.Patch{evt.setPatch},
					Timestamp: time.Now().UnixMilli(),
				}
				if _, err := a.figStream.Append(store.Entry[message.Message]{Payload: tic}, true); err != nil {
					fmt.Fprintf(os.Stderr, "figaro %s: set append: %v\n", a.id, err)
					a.inbox.Yield()
					continue
				}
				a.chalkboard.Apply(evt.setPatch)
				if err := a.chalkboard.Save(); err != nil {
					fmt.Fprintf(os.Stderr, "figaro %s: set chalkboard save: %v\n", a.id, err)
				}
				a.derived.Tick(0, a.chalkboard.Snapshot())
				a.inbox.Yield()

			case eventInterrupt:
				if a.inbox.IsIdle() || a.interrupted {
					continue
				}
				fmt.Fprintf(os.Stderr, "agent: event=Interrupt\n")
				a.interrupted = true
				if a.turnCancel != nil {
					a.turnCancel()
				}
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

// endTurn fans out stream.done with the reason (LLM stop_reason or
// "error: …"), persists chalkboard + meta, releases the next
// patient prompt.
func (a *Agent) endTurn(reason string) {
	a.fanOut(rpc.Notification{
		JSONRPC: "2.0",
		Method:  rpc.MethodDone,
		Params:  rpc.DoneParams{Reason: reason},
	})

	if a.endTurnSpan != nil {
		a.endTurnSpan()
		a.endTurnSpan = nil
	}
	if a.turnCancel != nil {
		a.turnCancel()
		a.turnCancel = nil
	}

	a.mu.Lock()
	a.lastActive = time.Now()
	for _, e := range a.figStream.ScanFromEnd(64) {
		if u := e.Payload.Usage; u != nil {
			a.tokensIn += u.InputTokens
			a.tokensOut += u.OutputTokens
			a.cacheRead += u.CacheReadTokens
			a.cacheWrite += u.CacheWriteTokens
			break
		}
	}
	a.mu.Unlock()

	if a.chalkboard != nil {
		if err := a.chalkboard.Save(); err != nil {
			fmt.Fprintf(os.Stderr, "figaro %s: chalkboard save: %v\n", a.id, err)
		}
	}
	a.derived.Tick(0, a.chalkboard.Snapshot())

	a.pendingTools = 0
	a.pendingToolCalls = nil
	a.inProgressTic = nil
	a.inbox.Yield()
}

// startLLMStream projects translator.Durable() to perMsg and hands
// it to Send. The cache was caught up by synchronize.
func (a *Agent) startLLMStream(ctx context.Context, inbox *Inbox) {
	durable := a.translator.Durable()
	perMsg := make([][]json.RawMessage, 0, len(durable))
	for _, e := range durable {
		if len(e.Payload) == 0 {
			continue
		}
		perMsg = append(perMsg, e.Payload)
	}
	if len(perMsg) == 0 {
		inbox.SendSelfish(event{typ: eventSendComplete, err: fmt.Errorf("empty context")})
		return
	}
	in := provider.SendInput{
		PerMessage: perMsg,
		Snapshot:   a.chalkboard.Snapshot(),
		Tools:      a.toolDefs(),
		MaxTokens:  a.maxTokens,
	}
	fmt.Fprintf(os.Stderr, "agent: starting LLM stream, %d entries in context\n", len(perMsg))
	go func() {
		var sendErr error
		func() {
			defer func() {
				if r := recover(); r != nil {
					sendErr = fmt.Errorf("provider send panic: %v", r)
				}
			}()
			sendErr = a.prov.Send(ctx, in, inbox)
		}()
		inbox.SendSelfish(event{typ: eventSendComplete, err: sendErr})
	}()
}

// handleFigaro fans out the latest figStream entry as MethodMessage
// and dispatches any tool_use blocks. The append already happened
// (via inbox routing or synchronize.condenseLive).
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

// handleSendComplete fans out the turn end. Condense already
// happened in synchronize.
func (a *Agent) handleSendComplete(sendErr error) {
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

// runToolAsync runs one tool call in a goroutine and pushes
// selfish events back to the captured inbox.
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

	if a.logEncoder != nil {
		a.logEncoder.Encode(n)
	}
	for ch := range a.subscribers {
		select {
		case ch <- n:
		default:
			// Slow subscriber — drop rather than block the agent.
		}
	}
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

func (a *Agent) ensureInProgressTic() {
	if a.inProgressTic == nil {
		a.inProgressTic = &message.Message{
			Role:      message.RoleUser,
			Timestamp: time.Now().UnixMilli(),
		}
	}
}

// finalizeAndSend appends the in-progress tic to figStream and
// enqueues eventStartLLMStream. The next synchronize pass catches
// up the translator before dispatch reaches startLLMStream.
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

	inbox.SendSelfish(event{typ: eventStartLLMStream})
}

// sumUsage totals tokens across messages. Seeds cumulative counters
// after restore / panic recovery.
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
