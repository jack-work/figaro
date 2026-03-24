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

	"github.com/jack-work/figaro/internal/agent"
	"github.com/jack-work/figaro/internal/credo"
	"github.com/jack-work/figaro/internal/message"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/tool"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/store"
)

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
}

// Agent is the goroutine-based implementation of Figaro.
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

	// Prompt FIFO — single goroutine drains this.
	promptQ chan string

	// Subscriber fan-out.
	mu          sync.RWMutex
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
// The agent begins draining its prompt queue immediately.
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
		memStore:    store.NewMemStore(),
		promptQ:     make(chan string, 64),
		subscribers: make(map[chan rpc.Notification]struct{}),
		createdAt:   time.Now(),
		lastActive:  time.Now(),
		cancel:      cancel,
		done:        make(chan struct{}),
	}

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

func (a *Agent) ID() string         { return a.id }
func (a *Agent) SocketPath() string  { return a.socketPath }

func (a *Agent) SetModel(model string) {
	a.mu.Lock()
	a.model = model
	a.mu.Unlock()
	a.prov.SetModel(model)
}

func (a *Agent) Prompt(text string) {
	a.promptQ <- text
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
	if len(a.promptQ) > 0 {
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
// On panic: logs the stack, resets the store, injects a crash system prompt,
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

		// Reset store — context is lost.
		a.mu.Lock()
		a.memStore = store.NewMemStore()
		a.tokensIn = 0
		a.tokensOut = 0
		a.mu.Unlock()

		// Notify subscribers of the crash.
		a.fanOut(rpc.Notification{
			JSONRPC: "2.0",
			Method:  rpc.MethodError,
			Params:  rpc.ErrorParams{Message: "agent crashed and was restarted; context lost"},
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

// drainLoop processes prompts one at a time (FIFO / actor model).
func (a *Agent) drainLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case text := <-a.promptQ:
			a.processPrompt(ctx, text)
		}
	}
}

func (a *Agent) processPrompt(ctx context.Context, text string) {
	ctx, span := figOtel.Start(ctx, "figaro.prompt",
		figOtel.WithAttributes(
			attribute.String("figaro.id", a.id),
			attribute.String("figaro.model", a.model),
			attribute.String("figaro.provider", a.prov.Name()),
		),
	)
	defer span.End()

	a.mu.Lock()
	a.lastActive = time.Now()
	a.mu.Unlock()

	// Build a notification sink that fans out to subscribers.
	// Pass ctx so otel events are recorded on the prompt span.
	out := make(chan rpc.Notification, 128)
	go func() {
		seq := 0
		for n := range out {
			if n.Method == rpc.MethodDelta {
				if p, ok := n.Params.(rpc.DeltaParams); ok {
					figOtel.Event(ctx, "figaro.notify.delta",
						attribute.Int("seq", seq),
						attribute.String("text", p.Text),
					)
				}
			}
			seq++
			a.fanOut(n)
		}
	}()

	// Build system prompt from scribe (re-templated on each prompt).
	sysPrompt := ""
	if a.scribe != nil {
		credoCtx := credo.CurrentContext(a.cwd, a.root, a.prov.Name(), a.model, a.id, "")
		if p, err := a.scribe.Build(credoCtx); err == nil {
			sysPrompt = p
		} else {
			fmt.Fprintf(os.Stderr, "figaro %s: credo build error: %v\n", a.id, err)
		}
	}

	ag := &agent.Agent{
		Store:        a.memStore,
		Provider:     a.prov,
		Tools:        a.tools,
		SystemPrompt: sysPrompt,
		MaxTokens:    a.maxTokens,
		Out:          out,
	}

	err := ag.Prompt(ctx, text)
	close(out)

	if err != nil {
		a.fanOut(rpc.Notification{
			JSONRPC: "2.0",
			Method:  rpc.MethodError,
			Params:  rpc.ErrorParams{Message: fmt.Sprintf("prompt error: %s", err)},
		})
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


