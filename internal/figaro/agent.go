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

// Inbox event types (user-RPC only).
const (
	eventUserPrompt eventType = iota
	eventSet
)

type event struct {
	typ eventType

	// eventUserPrompt
	text       string
	chalkboard *rpc.ChalkboardInput

	// eventSet
	setPatch message.Patch
}

// Config is the constructor input for NewAgent. Configured values
// (model, cwd, etc.) live on the chalkboard under system.* keys.
type Config struct {
	ID         string
	SocketPath string
	Provider   provider.Provider
	Outfitter  *outfit.Outfitter
	Tools      *tool.Registry
	Backend    store.Backend // nil = ephemeral

	// LogCache is the shared process-wide refcounted cache of aria
	// logs. When set, NewAgent acquires the IR log through the cache
	// (so a concurrent read RPC sees the same instance) and releases
	// on Kill. When nil, the agent falls back to Backend.Open.
	LogCache *store.LogCache

	// Chalkboard carries the aria's state. Nil creates an in-memory
	// one. Empty at first prompt means fresh aria. Closed by Kill.
	Chalkboard *chalkboard.State

	// BootPatch is applied to the chalkboard AND folded onto the
	// first user tic of a fresh aria (figLog empty). Carries the
	// loadout-resolved values (credo, skills, model knobs) plus
	// runtime fill-ins (system.cwd, system.root, system.max_tokens).
	// Ignored when the aria is restored — the chalkboard file already
	// holds those keys and the log already records their arrival.
	BootPatch *chalkboard.Patch
}

// Agent is the Figaro implementation.
// TODO: child-process isolation via --figaro flag.
type Agent struct {
	id           string
	socketPath   string
	prov         provider.Provider
	outfitter    *outfit.Outfitter
	bootPatch    *chalkboard.Patch
	tools        *tool.Registry
	figLog        store.Log[message.Message]
	figLogRelease func() // nil unless figLog came from LogCache
	logCache      *store.LogCache
	backend       store.Backend // nil = ephemeral
	chalkboard   *chalkboard.State
	derived      *derivedFanout // nil = ephemeral; per-figaro durable derivations

	inbox *Inbox

	// Turn state. Guarded by mu for Interrupt().
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
func NewAgent(cfg Config) *Agent {
	ctx, cancel := context.WithCancel(context.Background())

	a := &Agent{
		id:           cfg.ID,
		socketPath:   cfg.SocketPath,
		prov:         cfg.Provider,
		outfitter:    cfg.Outfitter,
		bootPatch:    cfg.BootPatch,
		tools:        cfg.Tools,
		backend:      cfg.Backend,
		logCache:     cfg.LogCache,
		chalkboard:   cfg.Chalkboard,
		createdAt:    time.Now(),
		lastActive:   time.Now(),
		cancel:       cancel,
		done:         make(chan struct{}),
	}

	a.figLog = a.newLog()
	appendInterruptSentinelIfDangling(a.figLog, a.id)
	if a.chalkboard == nil {
		// Ephemeral arias get an in-memory chalkboard.
		a.chalkboard, _ = chalkboard.Open("")
	}
	// If chalkboard loaded empty, replay stream patches to rebuild it.
	// On-disk chalkboard.json is just a cache of this projection.
	if len(a.chalkboard.Snapshot()) == 0 {
		replayed := false
		for _, entry := range a.figLog.Read() {
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

	a.tokensIn, a.tokensOut, a.cacheRead, a.cacheWrite = sumUsage(unwrapMessages(a.figLog.Read()))

	// Spin up durable-derivation fanout (backed agents only).
	a.derived = startDerived(ctx, a.id, a.prov.Name(), a.backend, a.figLog, nil)
	a.derived.Tick(0, a.chalkboard.Snapshot()) // initial materialization

	go a.runWithRecovery(ctx)
	return a
}

// newLog opens the figaro IR log, preferring the shared LogCache so
// concurrent readers see the same instance. Records the release
// callback when one is returned; Kill calls it.
func (a *Agent) newLog() store.Log[message.Message] {
	if a.logCache != nil {
		log, release, err := a.logCache.AcquireIR(a.id)
		if err == nil {
			a.figLogRelease = release
			return log
		}
		slog.Warn("log cache acquire (falling back)", "aria", a.id, "err", err)
	}
	if a.backend == nil {
		return store.NewMemLog[message.Message]()
	}
	log, err := a.backend.Open(a.id)
	if err != nil {
		slog.Warn("backend open (falling back to ephemeral)", "aria", a.id, "err", err)
		return store.NewMemLog[message.Message]()
	}
	return log
}

func (a *Agent) ID() string { return a.id }

func (a *Agent) SocketPath() string { return a.socketPath }

// chalkboardString reads a system.* string key. Empty when missing.
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

// chalkboardInt reads a numeric system.* key.
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

// Prompt is a tests-only helper.
func (a *Agent) Prompt(text string) {
	a.inbox.SendPatient(event{typ: eventUserPrompt, text: text})
}

// SubmitPrompt enqueues a prompt.
func (a *Agent) SubmitPrompt(req rpc.QuaRequest) {
	a.inbox.SendPatient(event{
		typ:        eventUserPrompt,
		text:       req.Text,
		chalkboard: req.Chalkboard,
	})
}

// Interrupt aborts the current turn. Idempotent when idle.
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
	return unwrapMessages(a.figLog.Read())
}

// unwrapMessages projects entries to a flat []Message.
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

// Notifier is a sink for fanout notifications.
type Notifier interface {
	Notify(method string, params any) error
}

// Subscribe registers a Notifier. Returns an unsubscribe function.
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

	// When the log came from the LogCache, the cache owns the
	// underlying instance and closes it on eviction. We only call
	// Close directly for ephemeral / fallback logs.
	if a.figLogRelease != nil {
		a.figLogRelease()
	} else if err := a.figLog.Close(); err != nil {
		slog.Error("figLog close", "aria", a.id, "err", err)
	}
	if a.chalkboard != nil {
		if err := a.chalkboard.Close(); err != nil {
			slog.Error("chalkboard close", "aria", a.id, "err", err)
		}
	}

}

// runWithRecovery drives the drain loop and restarts on panic.
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
		a.figLog = a.newLog()
		a.tokensIn, a.tokensOut, a.cacheRead, a.cacheWrite = sumUsage(unwrapMessages(a.figLog.Read()))
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

// actProtected runs the drain loop under recover.
func (a *Agent) actProtected(ctx context.Context) (panicked bool) {
	defer func() {
		if r := recover(); r != nil {

			stack := make([]byte, 4096)
			n := runtime.Stack(stack, false)
			slog.Error("panic", "aria", a.id, "panic", r, "stack", string(stack[:n]))
			panicked = true
		}
	}()

	a.act(ctx)
	return false
}

// act is the inbox drain loop.
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
		case eventSet:
			a.applyControlPatch(evt.setPatch, "set")
		}
	}
}

// applyControlPatch persists a state-only patch. No LLM round-trip.
func (a *Agent) applyControlPatch(patch message.Patch, kind string) {
	slog.Debug("event "+kind, "aria", a.id, "set", len(patch.Set), "remove", len(patch.Remove))
	tic := message.Message{
		Role:      message.RoleUser,
		Patches:   []message.Patch{patch},
		Timestamp: time.Now().UnixMilli(),
	}
	if _, err := a.figLog.Append(store.Entry[message.Message]{Payload: tic}); err != nil {
		slog.Error(kind+" append", "aria", a.id, "err", err)
		return
	}
	a.chalkboard.Apply(patch)
	if err := a.chalkboard.Save(); err != nil {
		slog.Error(kind+" chalkboard save", "aria", a.id, "err", err)
	}
	a.derived.Tick(0, a.chalkboard.Snapshot())
}

// endTurn fans out stream.done and persists chalkboard + meta.
func (a *Agent) endTurn(reason string) {
	a.fanOut(rpc.Notification{
		JSONRPC: "2.0",
		Method:  rpc.MethodDone,
		Params:  rpc.DoneParams{Reason: reason},
	})

	a.mu.Lock()
	a.lastActive = time.Now()
	for _, e := range a.figLog.ScanFromEnd(64) {
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

// sumUsage totals tokens across messages.
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
