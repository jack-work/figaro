package figaro

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/jack-work/figaro/internal/agent"
	"github.com/jack-work/figaro/internal/message"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/store"
)

// Config holds everything needed to construct an Agent.
type Config struct {
	ID           string
	SocketPath   string
	Provider     provider.Provider
	Model        string // model ID (e.g. "claude-sonnet-4-20250514"), for metadata
	SystemPrompt string
	MaxTokens    int
}

// Agent is the goroutine-based implementation of Figaro.
// TODO: convert to child process via --figaro flag for full isolation.
type Agent struct {
	id         string
	socketPath string
	prov       provider.Provider
	model      string
	sysPrompt  string
	maxTokens  int
	memStore   *store.MemStore

	// Prompt FIFO — single goroutine drains this.
	promptQ chan string

	// Subscriber fan-out.
	mu          sync.RWMutex
	subscribers map[chan rpc.Notification]struct{}
	serverSubs  map[*serverSubscriber]struct{} // jrpc2 server-based subscribers

	// Metrics.
	createdAt  time.Time
	lastActive time.Time
	tokensIn   int
	tokensOut  int

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
		sysPrompt:   cfg.SystemPrompt,
		maxTokens:   cfg.MaxTokens,
		memStore:    store.NewMemStore(),
		promptQ:     make(chan string, 64),
		subscribers: make(map[chan rpc.Notification]struct{}),
		createdAt:   time.Now(),
		lastActive:  time.Now(),
		cancel:      cancel,
		done:        make(chan struct{}),
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

		// Inject crash prompt so the agent knows to inform the user.
		a.sysPrompt = crashPrompt

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

	ag := &agent.Agent{
		Store:        a.memStore,
		Provider:     a.prov,
		SystemPrompt: a.sysPrompt,
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

	// Channel-based subscribers (in-process).
	for ch := range a.subscribers {
		select {
		case ch <- n:
		default:
			// Subscriber is slow — drop notification rather than blocking the agent.
		}
	}

	// jrpc2 server-based subscribers (socket connections).
	// Each notification is stamped with a per-subscriber sequence number.
	// The CLI uses this to reorder notifications that arrive out of order
	// due to jrpc2's concurrent dispatch model.
	for sub := range a.serverSubs {
		sub.seq++
		envelope := rpc.SequencedEvent{
			Seq:    sub.seq,
			Method: n.Method,
			Params: n.Params,
		}
		if err := sub.srv.Notify(context.Background(), rpc.MethodEvent, envelope); err != nil {
			fmt.Fprintf(os.Stderr, "figaro: notify error: %v\n", err)
		}
	}
}


