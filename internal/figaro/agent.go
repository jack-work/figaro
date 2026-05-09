package figaro

import (
	"context"
	"encoding/json"
	"log/slog"
	"runtime"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/message"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/outfit"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tokens"
	"github.com/jack-work/figaro/internal/tool"
)

type eventType int

// Inbox event set, post-Stage-2: user-RPC only. Provider deltas, tool
// results, and the turn-progress signals all live inside runTurn now
// — they never see the inbox.
const (
	eventUserPrompt eventType = iota
	eventRehydrate
	eventSet
)

type event struct {
	typ eventType

	// eventUserPrompt
	text       string
	chalkboard *rpc.ChalkboardInput

	// eventRehydrate / eventSet
	rehydratePatch message.Patch
	setPatch       message.Patch
}

// Config is the constructor input for NewAgent.
//
// Configured values (model, cwd, root, max_tokens, …) live on
// the Chalkboard under `system.*` keys — callers seed them there
// before construction. The typed fields below are runtime
// dependencies, not user config.
type Config struct {
	ID         string
	SocketPath string
	Provider   provider.Provider
	Outfitter  *outfit.Outfitter
	Tools      *tool.Registry
	Backend    store.Backend // nil = ephemeral

	// Chalkboard carries the aria's configured state. Nil creates
	// an in-memory one. When the chalkboard has no keys at first
	// prompt, the agent treats the aria as fresh and applies
	// LoadoutPatch (if set). Closed by Kill.
	Chalkboard *chalkboard.State

	// LoadoutPatch is the loadout-derived seed the agent applies
	// on first prompt only when the chalkboard is empty (i.e. this
	// is a fresh aria). Restored arias load from chalkboard.json
	// and ignore this. nil = no loadout to apply.
	LoadoutPatch *chalkboard.Patch
}

// Agent is the Figaro implementation. Single drain goroutine reads
// user-RPC events from the inbox; each user prompt fires a
// synchronous runTurn (turn.go) which owns provider streaming and
// tool dispatch. TODO: child-process isolation via --figaro flag.
type Agent struct {
	id           string
	socketPath   string
	prov         provider.Provider
	outfitter    *outfit.Outfitter
	loadoutPatch *chalkboard.Patch
	tools        *tool.Registry
	figStream    store.Stream[message.Message]
	backend      store.Backend // nil = ephemeral
	chalkboard   *chalkboard.State
	derived      *derivedFanout // nil = ephemeral; per-figaro durable derivations

	inbox *Inbox

	// Turn state — owned by runTurn while a turn is active. Guarded
	// by mu so Interrupt() can read turnCancel from any goroutine.
	turnCtx     context.Context
	turnCancel  context.CancelFunc
	interrupted bool

	mu   sync.RWMutex
	subs map[Notifier]struct{} // socket clients + in-process listeners

	createdAt  time.Time
	lastActive time.Time
	tokensIn   int
	tokensOut  int
	cacheRead  int
	cacheWrite int

	cancel context.CancelFunc
	done   chan struct{}
}

// NewAgent creates and starts a figaro agent.
// The agent begins draining its inbox immediately.
func NewAgent(cfg Config) *Agent {
	ctx, cancel := context.WithCancel(context.Background())

	a := &Agent{
		id:           cfg.ID,
		socketPath:   cfg.SocketPath,
		prov:         cfg.Provider,
		outfitter:    cfg.Outfitter,
		loadoutPatch: cfg.LoadoutPatch,
		tools:        cfg.Tools,
		backend:      cfg.Backend,
		chalkboard:   cfg.Chalkboard,
		createdAt:    time.Now(),
		lastActive:   time.Now(),
		cancel:       cancel,
		done:         make(chan struct{}),
	}

	a.figStream = a.newStream()
	if a.chalkboard == nil {
		// Ephemeral arias get an in-memory chalkboard so system prompt
		// flow stays uniform.
		a.chalkboard, _ = chalkboard.Open("")
	}
	// Chalkboard is a derived view of the figStream's tic patches. If
	// it loaded empty (file missing, ephemeral, or freshly opened),
	// replay the stream so prior patches materialize. The on-disk
	// chalkboard.json is just a cache of this projection.  See
	// derived.go for the analogous summary/translator/usage actors.
	if len(a.chalkboard.Snapshot()) == 0 {
		replayed := false
		for _, entry := range a.figStream.Durable() {
			for _, p := range entry.Payload.Patches {
				a.chalkboard.Apply(p)
				replayed = true
			}
		}
		if replayed {
			_ = a.chalkboard.Save()
		}
	}
	a.inbox = NewInbox(ctx)

	a.tokensIn, a.tokensOut, a.cacheRead, a.cacheWrite = sumUsage(unwrapMessages(a.figStream.Durable()))

	// Spin up the per-figaro durable-derivation fanout. Each
	// registered DurableDerivation gets its own goroutine + inbox;
	// each writes to arias/<id>/<filename>. Only lives for backed
	// agents. The translator stream is owned by the provider now;
	// derivations that need cache stats would need a different hook.
	a.derived = startDerived(ctx, a.id, a.prov.Name(), a.backend, a.figStream, nil)
	a.derived.Tick(0, a.chalkboard.Snapshot()) // initial materialization

	go a.runWithRecovery(ctx)
	return a
}

// newStream opens the figaro IR stream — FileStream when persisted,
// MemStream when ephemeral.
func (a *Agent) newStream() store.Stream[message.Message] {
	if a.backend == nil {
		return store.NewMemStream[message.Message]()
	}
	stream, err := a.backend.Open(a.id)
	if err != nil {
		slog.Warn("backend open (falling back to ephemeral)", "aria", a.id, "err", err)
		return store.NewMemStream[message.Message]()
	}
	return stream
}

func (a *Agent) ID() string { return a.id }

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

// chalkboardInt reads a numeric system.* key. Zero when missing.
func (a *Agent) chalkboardInt(key string) int {
	if a.chalkboard == nil {
		return 0
	}
	raw, ok := a.chalkboard.Snapshot()[key]
	if !ok {
		return 0
	}
	var n int
	json.Unmarshal(raw, &n)
	return n
}

func (a *Agent) currentModel() string { return a.chalkboardString("system.model") }

// Prompt is a tests-only helper; plans/dead-code-audit.md tracks
// the eventual removal.
func (a *Agent) Prompt(text string) {
	a.inbox.SendPatient(event{typ: eventUserPrompt, text: text})
}

// SubmitPrompt enqueues a prompt with optional chalkboard input.
func (a *Agent) SubmitPrompt(req rpc.QuaRequest) {
	a.inbox.SendPatient(event{
		typ:        eventUserPrompt,
		text:       req.Text,
		chalkboard: req.Chalkboard,
	})
}

// Interrupt aborts the current turn. Cancels turnCtx so the
// in-flight provider HTTP request and any running tools observe the
// cancellation; runTurn fans out the error and emits stream.done.
// On interrupt the partial assistant turn is dropped (the provider
// returns ctx.Err() rather than persisting a half-formed message),
// so figStream stays well-formed without any synthetic patching.
// Idempotent when idle.
func (a *Agent) Interrupt() {
	a.mu.Lock()
	if a.turnCancel == nil {
		a.mu.Unlock()
		return
	}
	a.interrupted = true
	cancel := a.turnCancel
	a.mu.Unlock()
	cancel()
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

// Notifier is a sink for fanout notifications. *jsonrpc.Server
// implements it natively (writes JSON-RPC frames down a socket); tests
// register an in-memory adapter.
type Notifier interface {
	Notify(method string, params any) error
}

// Subscribe registers a Notifier for fanout. The returned function
// removes it.
func (a *Agent) Subscribe(n Notifier) func() {
	a.mu.Lock()
	if a.subs == nil {
		a.subs = make(map[Notifier]struct{})
	}
	a.subs[n] = struct{}{}
	a.mu.Unlock()
	return func() {
		a.mu.Lock()
		delete(a.subs, n)
		a.mu.Unlock()
	}
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
	<-a.done         // wait for drain loop to exit
	a.derived.Wait() // wait for derivation loops (so disk writes finish before close)

	a.mu.Lock()
	a.subs = nil
	a.mu.Unlock()

	if err := a.figStream.Close(); err != nil {
		slog.Error("figStream close", "aria", a.id, "err", err)
	}
	if a.chalkboard != nil {
		if err := a.chalkboard.Close(); err != nil {
			slog.Error("chalkboard close", "aria", a.id, "err", err)
		}
	}
	// translator/cache close is the provider's responsibility now.
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
		a.figStream = a.newStream()
		a.tokensIn, a.tokensOut, a.cacheRead, a.cacheWrite = sumUsage(unwrapMessages(a.figStream.Durable()))
		if a.turnCancel != nil {
			a.turnCancel()
			a.turnCancel = nil
		}
		a.turnCtx = nil
		a.interrupted = false
		a.mu.Unlock()

		a.inbox.Close()
		a.inbox = NewInbox(ctx)

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

		slog.Error("restarted after panic", "aria", a.id)
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
			slog.Error("panic", "aria", a.id, "panic", r, "stack", string(stack[:n]))
			panicked = true
		}
	}()

	a.act(ctx)
	return false
}

// act is the inbox drain loop. Only user-RPC events land here; each
// user prompt drives a synchronous runTurn. Interrupt bypasses the
// inbox (Agent.Interrupt cancels turnCtx directly).
func (a *Agent) act(ctx context.Context) {
	for {
		evt, ok := a.inbox.Recv()
		if !ok {
			return
		}
		switch evt.typ {
		case eventUserPrompt:
			slog.Debug("event UserPrompt", "aria", a.id, "text", truncLog(evt.text, 60))
			a.runTurn(ctx, evt)
		case eventRehydrate:
			a.applyControlPatch(evt.rehydratePatch, "rehydrate")
		case eventSet:
			a.applyControlPatch(evt.setPatch, "set")
		}
	}
}

// applyControlPatch persists a state-only patch (figaro.set or
// figaro.reload_config). No LLM round-trip — the patch lands as a
// state-only tic on the figStream and the chalkboard advances.
func (a *Agent) applyControlPatch(patch message.Patch, kind string) {
	slog.Debug("event "+kind, "aria", a.id, "set", len(patch.Set), "remove", len(patch.Remove))
	tic := message.Message{
		Role:      message.RoleUser,
		Patches:   []message.Patch{patch},
		Timestamp: time.Now().UnixMilli(),
	}
	if _, err := a.figStream.Append(store.Entry[message.Message]{Payload: tic}, true); err != nil {
		slog.Error(kind+" append", "aria", a.id, "err", err)
		return
	}
	a.chalkboard.Apply(patch)
	if err := a.chalkboard.Save(); err != nil {
		slog.Error(kind+" chalkboard save", "aria", a.id, "err", err)
	}
	a.derived.Tick(0, a.chalkboard.Snapshot())
}

// endTurn fans out stream.done with the reason (LLM stop_reason or
// "error: …"), persists chalkboard + meta.
func (a *Agent) endTurn(reason string) {
	a.fanOut(rpc.Notification{
		JSONRPC: "2.0",
		Method:  rpc.MethodDone,
		Params:  rpc.DoneParams{Reason: reason},
	})

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
			slog.Error("chalkboard save", "aria", a.id, "err", err)
		}
	}
	a.derived.Tick(0, a.chalkboard.Snapshot())
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

	ctx := a.turnCtx
	if ctx == nil {
		ctx = context.Background()
	}
	slog.DebugContext(ctx, "rpc notify", "aria", a.id, "method", n.Method, "params", n.Params)

	figOtel.Event(ctx, "agent.fanout.pre",
		attribute.String("method", n.Method),
		attribute.Int("subscribers", len(a.subs)),
	)
	for sub := range a.subs {
		if err := sub.Notify(n.Method, n.Params); err != nil {
			slog.Warn("notify subscriber", "aria", a.id, "err", err)
		}
	}
	figOtel.Event(ctx, "agent.fanout.post",
		attribute.String("method", n.Method),
	)
}

func truncLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
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
