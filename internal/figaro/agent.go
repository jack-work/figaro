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
	"github.com/jack-work/figaro/internal/compose"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/message"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/outfit"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tokens"
	"github.com/jack-work/figaro/internal/tool"
	"github.com/jack-work/figaro/internal/toolout"
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

	// Chalkboard carries the aria's state, pre-seeded from the
	// reducible chalkboard channel (backed) or empty (ephemeral). The
	// channel is the durable truth; State is the in-memory hot view the
	// agent reads (model/max_tokens/cwd) and write-throughs on set.
	// Nil creates an empty in-memory one. Closed by Kill.
	Chalkboard *chalkboard.State

	// InlineBoot is the ephemeral-only boot patch. Backed arias hold
	// their boot transition in the chalkboard channel; ephemeral arias
	// have no channel, so this patch is folded onto the first IR turn so
	// the loadout reminders still render. Ignored when Backend != nil.
	InlineBoot *chalkboard.Patch
}

// Agent is the Figaro implementation.
type Agent struct {
	id         string
	socketPath string
	prov       provider.Provider
	outfitter  *outfit.Outfitter
	tools      *tool.Registry
	summarize  compose.ToolSummary
	previewArg compose.ToolPreviewArg
	inlineBoot *chalkboard.Patch // ephemeral first-turn boot fold
	figLog     store.Log[message.Message]
	backend    store.Backend // nil = ephemeral
	chalkboard *chalkboard.State
	derived    *derivedFanout // nil = ephemeral; per-figaro durable derivations

	inbox *Inbox

	// Turn state. Guarded by mu for Interrupt().
	turnCtx     context.Context
	turnCancel  context.CancelFunc
	interrupted bool

	mu   sync.RWMutex
	subs map[Notifier]struct{} // socket clients + in-process listeners

	// Live-render state (drain-loop owned; liveMu guards liveActive). turnStart
	// is the figLog index where the current turn's agent messages begin;
	// liveActive is true while an assistant unit is live; partials holds streamed
	// output for in-flight tools (keyed by tool_call_id).
	liveMu      sync.Mutex
	turnStart   int
	liveActive  bool
	gov         *toolout.Governor // bounded live tool-output tails (coalesced emits)
	lastEmit    time.Time         // throttle for live streaming emits
	argPartials map[string]string

	// ariaSrv is the rendered conversation (committed units + the open one),
	// the single source of the aria-read wire: it serves both the live push
	// (MethodAriaFrame) and the catch-up pull (figaro.read). unitLT is the
	// monotonic figaro LT assigned to each unit as it opens.
	ariaSrv  *aria.Server
	unitLT   int
	liveRole string // role of the currently-open (live) message, for its durable blob

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
		id:         cfg.ID,
		socketPath: cfg.SocketPath,
		prov:       cfg.Provider,
		outfitter:  cfg.Outfitter,
		tools:      cfg.Tools,
		summarize:  compose.ToolSummary(tool.Summarizer(cfg.Tools)),
		previewArg: compose.ToolPreviewArg(tool.PreviewArger(cfg.Tools)),
		inlineBoot: cfg.InlineBoot,
		backend:    cfg.Backend,
		chalkboard: cfg.Chalkboard,
		createdAt:  time.Now(),
		lastActive: time.Now(),
		cancel:     cancel,
		done:       make(chan struct{}),
	}

	a.figLog = a.newLog()
	appendInterruptSentinelIfDangling(a.figLog, a.id)
	if a.chalkboard == nil {
		// Ephemeral arias get an in-memory chalkboard. Backed arias are
		// pre-seeded from the reducible chalkboard channel by the caller.
		a.chalkboard, _ = chalkboard.Open("")
	}
	a.inbox = NewInbox(ctx)

	a.tokensIn, a.tokensOut, a.cacheRead, a.cacheWrite = sumUsage(unwrapMessages(a.figLog.Read()))

	// Build the rendered conversation from the log (each prior unit a closed
	// aria message), then register the broadcast: every aria-server change is
	// pushed to subscribers as one aria read.
	a.ariaSrv = aria.NewServer()
	for i, u := range compose.Units(unwrapMessages(a.figLog.Read()), a.summarize, a.previewArg) {
		a.unitLT = i + 1
		a.ariaSrv.Commit(aria.Message{LT: a.unitLT, Role: u.Role, Nodes: u.Nodes})
	}
	// Discard any leftover open-message blob from a prior crash: the partial
	// turn never reached the IR (the committed messages above are rebuilt from
	// it), so we resume from the last committed message. (Policy: discard.)
	a.clearLive()
	a.ariaSrv.Subscribe(func(r aria.AriaRead) {
		a.fanOut(rpc.Notification{JSONRPC: "2.0", Method: rpc.MethodAriaFrame, Params: r})
	})

	// Spin up durable-derivation fanout (backed agents only).
	a.derived = startDerived(ctx, a.id, a.prov.Name(), a.backend, a.figLog, nil)
	a.derived.Tick(0, a.chalkboard.Snapshot()) // initial materialization

	go a.runWithRecovery(ctx)
	return a
}

// newLog opens the figaro IR log. The backend owns and memoizes one
// shared instance per aria (so a concurrent aria.read RPC sees the same
// rows, lock-free), and closes it on Fork/Remove/Close — the agent never
// closes what Open returns.
func (a *Agent) newLog() store.Log[message.Message] {
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

// SubmitPrompt enqueues a prompt; the reply streams as log.* frames.
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

// Subscribe registers a Notifier for the live-render frame stream.
// Returns an unsubscribe func.
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
		MessageCount:     message.CountMessages(msgs),
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
		// Freeze whatever partial unit the panic left behind, then end
		// the turn (endTurn commits the live unit + emits turn.done).
		a.endTurn("error: " + crashMsg)

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
// Backed arias append it to the reducible chalkboard channel (keyed to
// the next IR LT, so it rides the next turn as a transition); ephemeral
// arias fold it onto an IR control-turn (no channel to hold it).
func (a *Agent) applyControlPatch(patch message.Patch, kind string) {
	slog.Debug("event "+kind, "aria", a.id, "set", len(patch.Set), "remove", len(patch.Remove))
	if a.backend != nil {
		if err := a.backend.ApplyChalkboard(a.id, patch); err != nil {
			slog.Error(kind+" chalkboard append", "aria", a.id, "err", err)
			return
		}
	} else {
		msg := message.Message{
			Role:      message.RoleUser,
			Patches:   []message.Patch{patch},
			Timestamp: time.Now().UnixMilli(),
		}
		if _, err := a.figLog.Append(store.Entry[message.Message]{Payload: msg}); err != nil {
			slog.Error(kind+" append", "aria", a.id, "err", err)
			return
		}
	}
	a.chalkboard.Apply(patch)
	a.derived.Tick(0, a.chalkboard.Snapshot())
}

// chalkAccessor returns the per-LT transition source for the provider:
// for backed arias, the reducible chalkboard channel grouped by IR LT;
// nil for ephemeral (the provider falls back to inline IR patches).
func (a *Agent) chalkAccessor() provider.Chalkboard {
	if a.backend == nil {
		return nil
	}
	m, err := a.backend.ChalkboardPatches(a.id)
	if err != nil {
		slog.Warn("chalkboard patches (transitions disabled this turn)", "aria", a.id, "err", err)
		return nil
	}
	return patchMap(m)
}

// patchMap implements provider.Chalkboard over a pre-read LT->patches map.
type patchMap map[uint64][]message.Patch

func (m patchMap) PatchesAt(lt uint64) []message.Patch { return m[lt] }

// endTurn fans out turn.done and persists chalkboard + meta.
// endTurn commits the live unit (it became a real IR message) and signals idle.
func (a *Agent) endTurn(reason string) {
	a.emitCommit() // freeze the live unit before signaling the turn idle
	a.finishTurn(reason)
}

// endTurnDiscarding ends a turn WITHOUT committing the live unit — for a
// mid-turn failure where the assistant message never reached figLog. Committing
// it would leave a UI message the model log doesn't have, so the next turn
// regenerates equivalent content and the aria shows it twice. Discarding drops
// the partial; the client resets its single open unit when the next turn opens
// at a new LT, so nothing duplicates.
func (a *Agent) endTurnDiscarding(reason string) {
	a.abandonLive()
	a.finishTurn(reason)
}

func (a *Agent) finishTurn(reason string) {
	a.liveMu.Lock()
	a.liveActive = false
	a.liveMu.Unlock()
	idle := a.inbox.IsIdle()
	a.fanOut(rpc.Notification{
		JSONRPC: "2.0",
		Method:  rpc.MethodTurnDone,
		Params:  rpc.DoneEntry{Reason: reason, Idle: &idle},
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

	a.writeMeta()
	a.derived.Tick(0, a.chalkboard.Snapshot())
}

// writeMeta persists the per-aria summary sidecar so `figaro list` shows
// counts/tokens/recency for dormant arias. Backed arias only.
func (a *Agent) writeMeta() {
	if a.backend == nil {
		return
	}
	a.mu.RLock()
	meta := &store.AriaMeta{
		TokensIn:         a.tokensIn,
		TokensOut:        a.tokensOut,
		CacheReadTokens:  a.cacheRead,
		CacheWriteTokens: a.cacheWrite,
		LastActiveMS:     a.lastActive.UnixMilli(),
	}
	a.mu.RUnlock()
	for _, e := range a.figLog.Read() {
		if e.Payload.Role == message.RoleAssistant {
			meta.TurnCount++
		}
		meta.LastFigaroLT = e.LT
	}
	meta.MessageCount = message.CountMessages(unwrapMessages(a.figLog.Read()))
	if err := a.backend.SetMeta(a.id, meta); err != nil {
		slog.Warn("write aria meta", "aria", a.id, "err", err)
	}
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
