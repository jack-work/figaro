package figaro

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/config"
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
	"github.com/jack-work/figaro/internal/turns"
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

	// Identity, eventUserPrompt only. id is minted by Inbox.Send and is unique
	// within the inbox's epoch; merged names the ids folded INTO this event by
	// an interrupt-time coalesce, so an id that no longer exists on its own can
	// still be resolved to the message that absorbed it.
	id     uint64
	at     int64
	merged []uint64

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
	// ProviderFactory lets the agent REBIND its provider mid-conversation
	// when system.provider (or a build-time knob) changes on the
	// chalkboard. Nil pins the agent to Provider for life — fine for
	// tests, wrong for a live aria, because the board is authoritative.
	ProviderFactory ProviderFactory
	Outfitter       *outfit.Outfitter
	Tools           *tool.Registry
	// Projector renders fig IR as UI IR. nil ships an engine with no display.
	Projector  Projector
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

	// Settings is the loaded user configuration. Today the agent reads only
	// the wire page budget from it, via ClampPageBudget — which is the SINGLE
	// policy point deciding how many bytes a paginated read may cost. Nil is
	// safe (the accessors are nil-safe and return the built-in defaults,
	// ceiling included), so tests and ephemeral agents need not supply one.
	Settings *config.Loaded
}

// Agent is the Figaro implementation.
type Agent struct {
	id         string
	socketPath string
	// provBind is the live provider binding (instance + the chalkboard
	// coordinates that produced it). Written by the drain loop via
	// syncProvider, read lock-free by status/metrics on RPC goroutines.
	provBind    atomic.Pointer[providerBinding]
	provFactory ProviderFactory
	outfitter   *outfit.Outfitter
	tools       *tool.Registry
	// proj converts fig IR to UI IR. nil in a core-only build.
	proj       Projector
	inlineBoot *chalkboard.Patch // ephemeral first-turn boot fold
	figLog     store.Log[message.Message]
	backend    store.Backend // nil = ephemeral
	chalkboard *chalkboard.State
	settings   *config.Loaded // wire budget policy; nil-safe

	inbox *Inbox

	// Turn state. Guarded by mu for Interrupt().
	turnCtx     context.Context
	turnCancel  context.CancelFunc
	turnRunning atomic.Bool // mirrors turnCancel != nil; lock-free point-in-time read
	interrupted bool

	mu   sync.RWMutex
	subs map[Notifier]struct{} // socket clients + in-process listeners

	// Live-render state, owned by the drain loop. turnStartLT is the FigaroLT
	// (main LT) of the last figLog entry before this turn's agent messages —
	// composeTurn reads strictly after it. It must be an LT, not an entry
	// count: main LTs are trunk-global (patches/transitions consume them too),
	// so they run far ahead of the message channel's entry count, and passing
	// a count to ReadFrom re-includes prior turns in every live frame.
	turnStartLT   uint64
	turnStartTurn uint64            // turn the window was pinned for; the region base moves only with the turn
	turnID        uint64            // in-flight turn id, stamped onto every append
	gov           *toolout.Governor // bounded live tool-output tails (coalesced emits)
	lastEmit      time.Time         // throttle for live streaming emits
	argPartials   map[string]string
	turn          *turnState

	// ariaSrv materializes turn-shaped UI IR plus the newest mutable suffix. It
	// is the single source of both figaro.aria pushes and figaro.read pulls.
	ariaSrv *aria.Server

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
		id:          cfg.ID,
		socketPath:  cfg.SocketPath,
		provFactory: cfg.ProviderFactory,
		outfitter:   cfg.Outfitter,
		tools:       cfg.Tools,
		proj:        cfg.Projector,
		inlineBoot:  cfg.InlineBoot,
		backend:     cfg.Backend,
		chalkboard:  cfg.Chalkboard,
		settings:    cfg.Settings,
		createdAt:   createdAt,
		lastActive:  lastActive,
		cancel:      cancel,
		done:        make(chan struct{}),
	}

	a.figLog = a.newLog()
	repairInterruptedTail(a.figLog, a.id)
	if a.chalkboard == nil {
		// Ephemeral arias get an in-memory chalkboard. Backed arias are
		// pre-seeded from the reducible chalkboard channel by the caller.
		a.chalkboard, _ = chalkboard.Open("")
	}
	// The caller built cfg.Provider from this very board, so pairing the
	// instance with the board's current knobs makes the first syncProvider
	// a no-op — and any later divergence a genuine rebind.
	a.bindProvider(cfg.Provider)
	a.inbox = NewInbox(ctx)

	messages := unwrapMessages(a.figLog.Read())
	a.refreshMetricsFrom(messages)

	// Build sealed UI turns from canonical IR, then broadcast every aria-server
	// change to socket subscribers as one aria.Page.
	a.ariaSrv = aria.NewServer()
	for _, t := range a.projTurns(messages) {
		a.ariaSrv.Commit(t)
	}
	a.ariaSrv.Subscribe(func(p aria.Page) {
		p.Metrics = a.sessionMetrics()
		a.fanOut(rpc.Notification{JSONRPC: "2.0", Method: rpc.MethodAriaFrame, Params: p})
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
		// Falling back to memory here would silently orphan the on-disk
		// content: subsequent Reads return 0 units and any live-subscribe
		// stream goes silent (nothing to fan out) until the process is
		// restarted. That is the exact head/fork-ancestry-resolution
		// symptom of the interrupted-mid-turn bug. Surface it loudly and
		// keep the previous log (if any) so we do not wipe state.
		slog.Error("backend open failed — keeping previous log", "aria", a.id, "err", err)
		if a.figLog != nil {
			return a.figLog
		}
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
	raw, ok := a.chalkboard.Snapshot().Get(key)
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
	raw, ok := a.chalkboard.Snapshot().Get(key)
	if !ok {
		return 0
	}
	var n int
	json.Unmarshal(raw, &n)
	return n
}

func (a *Agent) currentModel() string { return a.chalkboardString("system.model") }

func snapshotString(snapshot chalkboard.Snapshot, key string) string {
	raw, ok := snapshot.Get(key)
	if !ok {
		return ""
	}
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}

// resolveContextLimit reports the effective prompt cap for the current model.
//
// Precedence: an explicit system.max_context_tokens on the chalkboard wins
// outright — it is an override, so a user pinning a smaller (or larger) window
// must not be second-guessed by provider metadata. Only when it is unset does
// the provider get asked. (This used to be the other way round, which made the
// key unreachable for any provider that reported a limit.)
func resolveContextLimit(prov provider.Provider, model string, snapshot chalkboard.Snapshot) int {
	if limit, ok := provider.ContextLimitOverride(snapshot); ok {
		return limit
	}
	if resolver, ok := prov.(provider.ContextLimitProvider); ok {
		return resolver.ContextLimit(model, snapshot)
	}
	return 0
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
			contextTokens = tokens.ContextFromUsage(m.Usage)
			contextExact = true
		} else {
			contextTokens += tokens.EstimateMessage(m)
			contextExact = false
		}
		if !message.IsCeremonial(m) {
			messageCount++
		}
		if m.Role == message.RoleOutput {
			turnCount++
		}
		metricsLT = e.LT
	}

	snapshot := a.Snapshot()
	model := snapshotString(snapshot, "system.model")
	contextLimit := resolveContextLimit(a.provider(), model, snapshot)

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
		if m.Role == message.RoleOutput {
			turnCount++
		}
		if m.LogicalTime > metricsLT {
			metricsLT = m.LogicalTime
		}
	}
	snapshot := a.Snapshot()
	model := snapshotString(snapshot, "system.model")
	contextLimit := resolveContextLimit(a.provider(), model, snapshot)

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

// SubmitPrompt enqueues a prompt; the reply streams as log.* frames.
func (a *Agent) SubmitPrompt(req rpc.QuaRequest) {
	a.inbox.Send(event{
		typ:        eventUserPrompt,
		text:       req.Text,
		chalkboard: req.Chalkboard,
	})
}

// QueuedPrompts returns a read-only snapshot of the messages this aria has
// accepted but not yet answered, in FIFO order, plus the epoch those ids
// belong to. The inbox is untouched.
//
// carriers opts in to empty-text prompts (pure chalkboard carriers): the CRUD
// surface must be able to address everything it can delete, while every
// display surface wants them omitted — which is what this has always done.
func (a *Agent) QueuedPrompts(carriers bool) (string, []rpc.QueuedPrompt) {
	events := a.inbox.SnapshotPrompts(carriers)
	out := make([]rpc.QueuedPrompt, 0, len(events))
	for _, e := range events {
		out = append(out, rpc.QueuedPrompt{
			ID:     e.id,
			Text:   e.text,
			State:  rpc.QueueStateQueued,
			At:     e.at,
			Merged: e.merged,
		})
	}
	return a.inbox.Epoch(), out
}

// turnActive reports whether a turn is in flight (a prompt submitted now would
// queue/steer rather than start fresh). Lock-free: it's a point-in-time read
// needing no consistency with other a.mu-guarded state.
func (a *Agent) turnActive() bool {
	return a.turnRunning.Load()
}

// Interrupt aborts the current turn, keeping the queue. It is the shape the
// Figaro interface uses (and the angelus's graceful shutdown, where dropping
// what someone queued would be exactly the wrong courtesy).
func (a *Agent) Interrupt() { a.Hangup(rpc.QueueKeep) }

// Hangup aborts the current turn and says what became of the messages waiting
// behind it.
//
// It also COALESCES the queue on the keep path — each contiguous run of
// waiting prompts folds into one message, with the same semantics steering
// already has (texts joined in order, chalkboard input merged so a later value
// wins). Three messages typed during a long turn and then cut short are one
// question to answer, not three turns to sit through.
//
// The fold happens only here. There is no mode threaded into the submit path
// and no shared helper that checks whether it is being interrupted: this
// function IS the interrupt path, and Inbox.CoalesceUserPromptRuns has exactly
// one caller in the tree. A normal submit therefore cannot reach it by
// construction rather than by convention.
//
// An IDLE aria coalesces nothing. There is no turn to interrupt, the drain
// loop is already working through the queue, and folding under it would change
// what a plain submit means — which is the one thing this must not do.
//
// Two dispositions, named rather than negated, because the CLI verbs that
// carry them are two different intentions:
//
//	QueueKeep  — stop the turn; the queue is answered next (`figaro hup`).
//	QueueClear — stop the turn AND drop the queue, handing it back so it can
//	             be persisted rather than lost (`figaro cut`).
//
// The response's Queue is THE QUEUE AS OF THE HANGUP either way — one field,
// one meaning — and Cleared says which happened to it.
//
// Order is why clear does not simply reuse the keep path: the drain happens
// BEFORE any fold, so what comes back is the messages as they were typed, each
// with its own id. Coalescing first would hand back one blob and defeat the
// point of returning it at all.
//
// Clearing does not require a live turn. A queue can be worth dropping between
// turns, and refusing then would be a distinction with no meaning to the
// person asking.
func (a *Agent) Hangup(disposition rpc.QueueDisposition) rpc.InterruptResponse {
	resp := rpc.InterruptResponse{OK: true, Epoch: a.inbox.Epoch()}

	if disposition == rpc.QueueClear {
		for _, e := range a.inbox.DrainUserPrompts() {
			resp.Queue = append(resp.Queue, rpc.QueuedPrompt{
				ID:         e.id,
				Text:       e.text,
				State:      rpc.QueueStateQueued,
				At:         e.at,
				Merged:     e.merged,
				Chalkboard: e.chalkboard,
			})
		}
		resp.Cleared = true
	}

	a.mu.Lock()
	if a.turnCancel == nil {
		a.mu.Unlock()
		if !resp.Cleared {
			_, resp.Queue = a.QueuedPrompts(true)
		}
		return resp
	}
	a.interrupted = true
	cancel := a.turnCancel
	a.mu.Unlock()
	if !resp.Cleared {
		a.inbox.CoalesceUserPromptRuns()
		_, resp.Queue = a.QueuedPrompts(true)
	}
	cancel()
	return resp
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

// appendMsg stamps the in-flight turn id onto m and appends it. Every durable
// write of a conversation message goes through here, so the turn id can never
// drift from the log.
func (a *Agent) appendMsg(m message.Message) (store.Entry[message.Message], error) {
	m.TurnID = a.turnID
	return a.figLog.Append(store.Entry[message.Message]{Payload: m})
}

// openTurn mints the next turn id. The seed is derived from the log on first
// use, which is what makes a forked child continue its parent's numbering
// without any explicit hand-off.
func (a *Agent) openTurn() {
	if a.turnID == 0 {
		a.turnID = turns.StampIDs(unwrapMessages(a.figLog.Read()))
	}
	a.turnID++
}

// unwrapMessages projects entries to a flat []Message, stamping the two
// coordinates that live outside the payload: LT (the WAL frame index) and,
// for legacy entries written before turn ids existed, TurnID (derived).
func unwrapMessages(entries []store.Entry[message.Message]) []message.Message {
	if len(entries) == 0 {
		return nil
	}
	out := make([]message.Message, len(entries))
	for i, e := range entries {
		out[i] = e.Payload
		out[i].LogicalTime = e.LT
	}
	turns.StampIDs(out)
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
		Provider:         a.providerName(),
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
		if _, err := a.repairTurnTail(); err != nil {
			slog.Error("repair turn tail after panic", "aria", a.id, "err", err)
		}
		repairInterruptedTail(a.figLog, a.id)
		a.refreshMetrics()

		crashMsg := "agent crashed and was restarted"
		if a.backend != nil {
			crashMsg += "; context restored from last checkpoint"
		} else {
			crashMsg += "; context lost"
		}
		a.reconcileAriaServer()
		a.finishTurn("error: " + crashMsg)

		slog.Error("restarted after panic", "aria", a.id)
	}
}

func (a *Agent) reconcileAriaServer() {
	oldLast := a.ariaSrv.LastTurn()
	hadOpen := a.ariaSrv.HasOpen()
	history := a.projTurns(a.Context())
	// Defensive: never wipe already-materialized state with a shorter history.
	// reconcileAriaServer runs on mid-turn error paths whose only source of
	// truth is a.Context() (the durable figLog). If that read returns fewer
	// turns than the server already holds — a backend that transiently failed
	// to open and fell back to a memory log, a cachedLog built on stale fork
	// ancestry — replacing the good state with the short one makes Read return
	// nothing and the live stream go silent. Keep what we have and log loudly.
	shorter := len(history) == 0 || history[len(history)-1].ID < oldLast
	if oldLast > 0 && shorter {
		newLast := uint64(0)
		if len(history) > 0 {
			newLast = history[len(history)-1].ID
		}
		slog.Warn("reconcileAriaServer: refusing to shrink history",
			"aria", a.id, "old_last_turn", oldLast, "new_last_turn", newLast)
		if hadOpen {
			a.ariaSrv.Abandon()
		}
		return
	}
	a.ariaSrv.Restore(history)
	for _, t := range history {
		if t.ID <= oldLast {
			continue
		}
		a.fanOut(rpc.Notification{
			JSONRPC: "2.0",
			Method:  rpc.MethodAriaFrame,
			Params: aria.Page{
				Parts:   []aria.TurnPart{{Turn: t}},
				Metrics: a.sessionMetrics(),
			},
		})
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

// serviceForks executes any queued forks at a round boundary. Returns true
// when it serviced at least one, so a drain loop can re-check for events the
// fork uncovered. Statement callers may ignore the result.
func (a *Agent) serviceForks() bool {
	evts := a.inbox.TakeReadyForks()
	for _, evt := range evts {
		a.executeFork(evt)
	}
	return len(evts) > 0
}

// serviceSets applies any queued chalkboard patches at a round boundary, the
// same points steering prompts drain. Each patch lands on the in-memory
// chalkboard (so the next provider round reads it via the snapshot) and rides
// the next IR LT as a transition. Returns true when it serviced at least one.
func (a *Agent) serviceSets() bool {
	evts := a.inbox.TakeReadySet()
	for _, evt := range evts {
		a.applyControlPatch(evt.setPatch, "set")
	}
	return len(evts) > 0
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
			Role:      message.RoleInput,
			Patches:   []message.Patch{patch},
			Timestamp: time.Now().UnixMilli(),
		}
		if _, err := a.appendMsg(msg); err != nil {
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
	// The turn stopped moving. This is the one place the word "seal" means
	// anything now: every node in the turn is immutable from here, and it is
	// the moment a persisted UI-IR channel would write it (Phase 4).
	a.ariaSrv.Seal(nil)
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
		Provider:         a.providerName(),
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
