package figaro

import (
	"context"
	"encoding/json"
	"fmt"
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
	eventFork
)

type event struct {
	typ eventType

	// eventUserPrompt
	text       string
	chalkboard *rpc.ChalkboardInput

	// eventSet
	setPatch message.Patch

	// eventFork
	fork     func() error
	forkDone chan error
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
	CreatedAt  time.Time
	LastActive time.Time

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

	inbox *Inbox

	// Turn state. Guarded by mu for Interrupt().
	turnCtx     context.Context
	turnCancel  context.CancelFunc
	interrupted bool

	mu   sync.RWMutex
	subs map[Notifier]struct{} // socket clients + in-process listeners

	// Live-render state, owned by the drain loop. turnStart is the figLog
	// index where the current turn's agent messages begin.
	turnStart   int
	gov         *toolout.Governor // bounded live tool-output tails (coalesced emits)
	lastEmit    time.Time         // throttle for live streaming emits
	argPartials map[string]string
	toolTimings map[string]compose.ToolTiming

	// ariaSrv is the rendered conversation (committed units + the open one),
	// the single source of the aria-read wire: it serves both the live push
	// (MethodAriaFrame) and the catch-up pull (figaro.read). unitLT is the
	// monotonic figaro LT assigned to each unit as it opens.
	ariaSrv  *aria.Server
	unitLT   int
	liveRole string // role of the currently-open (live) message, for its durable blob

	createdAt     time.Time
	lastActive    time.Time
	tokensIn      int
	tokensOut     int
	cacheRead     int
	cacheWrite    int
	messageCount  int
	turnCount     int
	metricsLT     uint64
	contextTokens int
	contextLimit  int
	contextExact  bool
	model         string
	mantra        string
	cwd           string
	loadoutName   string
	loadoutVer    string

	cancel context.CancelFunc
	done   chan struct{}
}

// NewAgent creates and starts a figaro agent.
func NewAgent(cfg Config) *Agent {
	ctx, cancel := context.WithCancel(context.Background())
	now := time.Now()
	createdAt := cfg.CreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}
	lastActive := cfg.LastActive
	if lastActive.IsZero() {
		lastActive = now
	}

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
		createdAt:  createdAt,
		lastActive: lastActive,
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

	messages := unwrapMessages(a.figLog.Read())
	a.refreshMetricsFrom(messages)

	// Build the rendered conversation from the log (each prior unit a closed
	// aria message), then register the broadcast: every aria-server change is
	// pushed to subscribers as one aria read.
	a.ariaSrv = aria.NewServer()
	for i, u := range compose.Units(messages, a.summarize, a.previewArg) {
		a.unitLT = i + 1
		a.ariaSrv.Commit(aria.Message{LT: a.unitLT, Role: u.Role, Nodes: u.Nodes})
	}
	// Discard any leftover open-message blob from a prior crash: the partial
	// turn never reached the IR (the committed messages above are rebuilt from
	// it), so we resume from the last committed message. (Policy: discard.)
	a.clearLive()
	a.ariaSrv.Subscribe(func(r aria.AriaRead) {
		r.Metrics = a.sessionMetrics()
		a.fanOut(rpc.Notification{JSONRPC: "2.0", Method: rpc.MethodAriaFrame, Params: r})
	})

	a.publishMetadata()

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

func snapshotString(snapshot chalkboard.Snapshot, key string) string {
	raw, ok := snapshot[key]
	if !ok {
		return ""
	}
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}

func snapshotContextLimit(snapshot chalkboard.Snapshot) int {
	raw, ok := snapshot["system.max_context_tokens"]
	if !ok {
		return 0
	}
	var limit int
	if json.Unmarshal(raw, &limit) != nil || limit <= 0 {
		return 0
	}
	return limit
}

// refreshMetrics runs at durable message boundaries, never on streaming
// deltas. That keeps the status path current without making live rendering
// repeatedly rescan the full conversation.
func (a *Agent) refreshMetrics() {
	a.mu.RLock()
	metricsLT := a.metricsLT
	in, out := a.tokensIn, a.tokensOut
	cacheRead, cacheWrite := a.cacheRead, a.cacheWrite
	messageCount, turnCount := a.messageCount, a.turnCount
	contextTokens, contextExact := a.contextTokens, a.contextExact
	a.mu.RUnlock()

	tail, hasTail := a.figLog.PeekTail()
	if metricsLT > 0 && (!hasTail || tail.LT < metricsLT) {
		a.refreshMetricsFrom(a.Context())
		return
	}
	for _, e := range a.figLog.ReadFrom(metricsLT+1, 0) {
		m := e.Payload
		if m.Usage != nil {
			in += m.Usage.InputTokens
			out += m.Usage.OutputTokens
			cacheRead += m.Usage.CacheReadTokens
			cacheWrite += m.Usage.CacheWriteTokens
			contextTokens = m.Usage.InputTokens + m.Usage.OutputTokens
			contextExact = true
		} else {
			contextTokens += tokens.EstimateMessage(m)
			contextExact = false
		}
		if !message.IsCeremonial(m) {
			messageCount++
		}
		if m.Role == message.RoleAssistant {
			turnCount++
		}
		metricsLT = e.LT
	}

	snapshot := a.Snapshot()
	model := snapshotString(snapshot, "system.model")
	contextLimit := 0
	if resolver, ok := a.prov.(provider.ContextLimitProvider); ok {
		contextLimit = resolver.ContextLimit(model, snapshot)
	}
	if contextLimit == 0 {
		contextLimit = snapshotContextLimit(snapshot)
	}

	a.mu.Lock()
	a.tokensIn = in
	a.tokensOut = out
	a.cacheRead = cacheRead
	a.cacheWrite = cacheWrite
	a.messageCount = messageCount
	a.turnCount = turnCount
	a.metricsLT = metricsLT
	a.contextTokens = contextTokens
	a.contextExact = contextExact
	a.contextLimit = contextLimit
	a.model = model
	a.mantra = snapshotString(snapshot, "mantra")
	a.cwd = snapshotString(snapshot, "system.cwd")
	a.loadoutName = snapshotString(snapshot, "system.loadout_name")
	a.loadoutVer = snapshotString(snapshot, "system.loadout_version")
	a.mu.Unlock()
}

func (a *Agent) refreshMetricsFrom(msgs []message.Message) {
	in, out, cacheRead, cacheWrite := sumUsage(msgs)
	contextTokens, contextExact := tokens.ContextSize(msgs)
	turnCount := 0
	var metricsLT uint64
	for _, m := range msgs {
		if m.Role == message.RoleAssistant {
			turnCount++
		}
		if m.LogicalTime > metricsLT {
			metricsLT = m.LogicalTime
		}
	}
	snapshot := a.Snapshot()
	model := snapshotString(snapshot, "system.model")
	contextLimit := 0
	if resolver, ok := a.prov.(provider.ContextLimitProvider); ok {
		contextLimit = resolver.ContextLimit(model, snapshot)
	}
	if contextLimit == 0 {
		contextLimit = snapshotContextLimit(snapshot)
	}

	a.mu.Lock()
	a.tokensIn = in
	a.tokensOut = out
	a.cacheRead = cacheRead
	a.cacheWrite = cacheWrite
	a.messageCount = message.CountMessages(msgs)
	a.turnCount = turnCount
	a.metricsLT = metricsLT
	a.contextTokens = contextTokens
	a.contextExact = contextExact
	a.contextLimit = contextLimit
	a.model = model
	a.mantra = snapshotString(snapshot, "mantra")
	a.cwd = snapshotString(snapshot, "system.cwd")
	a.loadoutName = snapshotString(snapshot, "system.loadout_name")
	a.loadoutVer = snapshotString(snapshot, "system.loadout_version")
	a.mu.Unlock()
}

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

// CoordinateFork runs storage fork coordination on the actor goroutine.
// Active turns service it between stream/tool events without cancellation.
func (a *Agent) CoordinateFork(run func() error) error {
	done := make(chan error, 1)
	if !a.inbox.Send(event{typ: eventFork, fork: run, forkDone: done}) {
		return fmt.Errorf("figaro %s is stopped", a.id)
	}
	select {
	case err := <-done:
		return err
	case <-a.done:
		select {
		case err := <-done:
			return err
		default:
			return fmt.Errorf("figaro %s stopped before fork", a.id)
		}
	}
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
	state := "idle"
	if a.turnCtx != nil || !a.inbox.IsIdle() {
		state = "active"
	}
	info := FigaroInfo{
		ID:               a.id,
		State:            state,
		Provider:         a.prov.Name(),
		Model:            a.model,
		MessageCount:     a.messageCount,
		TokensIn:         a.tokensIn,
		TokensOut:        a.tokensOut,
		CacheReadTokens:  a.cacheRead,
		CacheWriteTokens: a.cacheWrite,
		ContextTokens:    a.contextTokens,
		ContextLimit:     a.contextLimit,
		ContextExact:     a.contextExact,
		CreatedAt:        a.createdAt,
		LastActive:       a.lastActive,
		Mantra:           a.mantra,
		Cwd:              a.cwd,
		LoadoutName:      a.loadoutName,
		LoadoutVersion:   a.loadoutVer,
		LastFigaroLT:     a.metricsLT,
	}
	a.mu.RUnlock()
	return info
}

func (a *Agent) sessionMetrics() *aria.Metrics {
	info := a.Info()
	a.mu.RLock()
	mantra := a.mantra
	a.mu.RUnlock()
	return &aria.Metrics{
		ContextTokens:    info.ContextTokens,
		ContextLimit:     info.ContextLimit,
		ContextExact:     info.ContextExact,
		TokensIn:         info.TokensIn,
		TokensOut:        info.TokensOut,
		CacheReadTokens:  info.CacheReadTokens,
		CacheWriteTokens: info.CacheWriteTokens,
		Mantra:           mantra,
	}
}

func (a *Agent) Kill() {
	a.cancel()
	<-a.done // wait for drain loop to exit

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
		if a.turnCancel != nil {
			a.turnCancel()
			a.turnCancel = nil
		}
		a.turnCtx = nil
		a.interrupted = false
		a.mu.Unlock()
		a.refreshMetrics()

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
		case eventFork:
			a.executeFork(evt)
		}
	}
}

func (a *Agent) executeFork(evt event) {
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("fork coordination panic: %v", r)
			}
		}()
		err = evt.fork()
	}()
	evt.forkDone <- err
}

func (a *Agent) serviceForks() {
	for _, evt := range a.inbox.TakeReadyForks() {
		a.executeFork(evt)
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
	a.refreshMetrics()
	a.publishMetadata()
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
	a.refreshMetrics()
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
	a.refreshMetrics()
	a.abandonLive()
	a.finishTurn(reason)
}

func (a *Agent) finishTurn(reason string) {
	idle := a.inbox.IsIdle()
	a.mu.Lock()
	a.lastActive = time.Now()
	a.mu.Unlock()
	a.fanOut(rpc.Notification{
		JSONRPC: "2.0",
		Method:  rpc.MethodTurnDone,
		Params:  rpc.DoneEntry{Reason: reason, Idle: &idle},
	})

	a.publishMetadata()
}

// publishMetadata persists and fans out one actor-owned metrics snapshot.
func (a *Agent) publishMetadata() {
	if a.backend == nil {
		return
	}
	a.mu.RLock()
	meta := &store.AriaMeta{
		MessageCount:     a.messageCount,
		TurnCount:        a.turnCount,
		TokensIn:         a.tokensIn,
		TokensOut:        a.tokensOut,
		CacheReadTokens:  a.cacheRead,
		CacheWriteTokens: a.cacheWrite,
		LastActiveMS:     a.lastActive.UnixMilli(),
		Provider:         a.prov.Name(),
		Model:            a.model,
		Mantra:           a.mantra,
		Cwd:              a.cwd,
		LoadoutName:      a.loadoutName,
		LoadoutVersion:   a.loadoutVer,
		ContextTokens:    a.contextTokens,
		ContextLimit:     a.contextLimit,
		ContextExact:     a.contextExact,
		CreatedAtMS:      a.createdAt.UnixMilli(),
		LastFigaroLT:     a.metricsLT,
	}
	a.mu.RUnlock()
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
