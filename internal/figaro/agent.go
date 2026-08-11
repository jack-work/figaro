package figaro

import (
	"context"
	"encoding/json"
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/form"
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
	eventCast
)

// promptSegment is one submission inside a (possibly folded) user message:
// the text and who sent it. Sender is already rendered (rpc.Attribution), so
// no consumer re-derives it and none can disagree about the spelling.
type promptSegment struct {
	sender string
	text   string
}

type event struct {
	typ eventType

	// Identity, eventUserPrompt only. id is minted by Inbox.Send and is unique
	// within the inbox's epoch; merged names the ids folded INTO this event by
	// an interrupt-time coalesce, so an id that no longer exists on its own can
	// still be resolved to the message that absorbed it.
	id     uint64
	at     int64
	merged []uint64

	// eventCast
	cast *castOp

	// eventUserPrompt
	text string
	form *rpc.FormInput
	// segments is this event's attributed payloads, in submission order.
	// A fresh submit has exactly one; mergePromptEvents concatenates them, so
	// a folded message keeps WHO SAID WHAT instead of flattening it into one
	// anonymous blob. text stays the joined display/mantra form.
	segments []promptSegment

	// eventSet
	setPatch message.Patch
	// setIfVersion refuses the patch unless the form still stands there. The
	// check rides the event to the writer, where it is atomic with the append —
	// checking at accept would answer about a version the patch never met.
	setIfVersion uint64
}

// Config is the constructor input for NewAgent. Configured values
// (model, cwd, etc.) live on the form under system.* keys.
type Config struct {
	ID         string
	SocketPath string
	Provider   provider.Provider
	// ProviderFactory lets the agent REBIND its provider mid-conversation
	// when system.provider (or a build-time knob) changes on the
	// form. Nil pins the agent to Provider for life — fine for
	// tests, wrong for a live aria, because the board is authoritative.
	ProviderFactory ProviderFactory
	Outfitter       *outfit.Outfitter
	Tools           *tool.Registry
	// Projector renders fig IR as UI IR. nil ships an engine with no display.
	Projector  Projector
	Backend    store.Backend // nil = ephemeral
	CreatedAt  time.Time
	LastActive time.Time

	// Form carries the aria's state, pre-seeded from the
	// reducible form channel (backed) or empty (ephemeral). The
	// channel is the durable truth; State is the in-memory hot view the
	// agent reads (model/max_tokens/cwd) and write-throughs on set.
	// Nil creates an empty in-memory one. Closed by Kill.
	Form *form.State

	// InlineBoot is the ephemeral-only boot patch. Backed arias hold
	// their boot transition in the form channel; ephemeral arias
	// have no channel, so this patch is folded onto the first IR turn so
	// the outfit reminders still render. Ignored when Backend != nil.
	InlineBoot *form.Patch

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
	// studies is the live study-subscription state (roles/forms this aria
	// observes). See study.go.
	studies studyState
	// provBind is the live provider binding (instance + the form
	// coordinates that produced it). Written by the drain loop via
	// syncProvider, read lock-free by status/metrics on RPC goroutines.
	provBind    atomic.Pointer[providerBinding]
	provFactory ProviderFactory
	outfitter   *outfit.Outfitter
	tools       *tool.Registry
	// proj converts fig IR to UI IR. nil in a core-only build.
	proj       Projector
	inlineBoot *form.Patch // ephemeral first-turn boot fold
	figLog     store.Log[message.Message]
	backend    store.Backend // nil = ephemeral
	form       *form.State
	settings   *config.Loaded // wire budget policy; nil-safe

	inbox *Inbox

	// Turn state. Guarded by mu for Interrupt().
	turnCtx     context.Context
	turnCancel  context.CancelFunc
	turnRunning atomic.Bool // mirrors turnCancel != nil; lock-free point-in-time read
	interrupted bool

	mu   sync.RWMutex
	subs map[Notifier]struct{} // normally exactly one: the aria's hub
	// teardown runs after the drain loop exits. Holds the hub unbind, so
	// the endpoint outlives the agent.
	teardown []func()

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
	outfitName    string
	outfitVer     string

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
		form:        cfg.Form,
		settings:    cfg.Settings,
		createdAt:   createdAt,
		lastActive:  lastActive,
		cancel:      cancel,
		done:        make(chan struct{}),
	}

	a.figLog = a.newLog()
	repairInterruptedTail(a.figLog, a.id)
	if a.form == nil {
		// Ephemeral arias get an in-memory form. Backed arias are
		// pre-seeded from the reducible form channel by the caller.
		a.form, _ = form.Open("")
	}
	// The caller built cfg.Provider from this very board, so pairing the
	// instance with the board's current knobs makes the first syncProvider
	// a no-op — and any later divergence a genuine rebind.
	a.bindProvider(cfg.Provider)
	a.inbox = NewInbox(ctx)

	messages := unwrapMessages(a.figLog.Read())
	// Metrics come off the _meta sidecar when it is current, which after
	// Backend.Open it always is: healMeta folds any suffix past the watermark
	// on the read path. That turns construction's metric pass from a walk of
	// every message into a struct copy — and, more importantly, removes one of
	// the reasons the whole decoded log had to be materialized here at all.
	if !a.seedMetricsFromMeta() {
		a.refreshMetricsFrom(messages)
	}

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

	// Form transitions ride the SAME fanout as the aria's frames, so a form
	// listener is an ordinary subscriber on the same socket and there is no
	// second transport to keep honest. The sink runs on the form's writer, so
	// it does exactly one thing and returns.
	if a.backend != nil {
		if err := a.backend.WatchForm(a.id, func(version uint64, patch message.Patch) {
			a.fanOut(rpc.Notification{JSONRPC: "2.0", Method: rpc.MethodFormDelta,
				Params: rpc.FormDelta{
					Schema: rpc.FormDeltaSchema, AriaID: a.id, Version: version,
					Patch: patch, At: time.Now().UnixMilli(),
				}})
		}); err != nil {
			slog.Warn("watch form", "aria", a.id, "err", err)
		}
	}

	a.resumeStudies()
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

// formString reads a system.* string key. Empty when missing.
// dukeTitle is what THIS aria calls its end user, from its form, or the
// generic default when it does not say. Passed to rpc.SenderFrom so the duke
// placeholder an interactive CLI sends resolves against the aria being
// addressed rather than against the shell that sent it.
func (a *Agent) dukeTitle() string {
	if t := a.formString(rpc.DukeTitleKey); t != "" {
		return t
	}
	return rpc.DefaultDukeTitle
}

func (a *Agent) formString(key string) string {
	if a.form == nil {
		return ""
	}
	raw, ok := a.form.Snapshot().Get(key)
	if !ok {
		return ""
	}
	var s string
	json.Unmarshal(raw, &s)
	return s
}

// formInt reads a numeric system.* key.
func (a *Agent) formInt(key string) int {
	if a.form == nil {
		return 0
	}
	raw, ok := a.form.Snapshot().Get(key)
	if !ok {
		return 0
	}
	var n int
	json.Unmarshal(raw, &n)
	return n
}

func (a *Agent) currentModel() string { return a.formString("system.model") }

func snapshotString(snapshot form.Snapshot, key string) string {
	raw, ok := snapshot.Get(key)
	if !ok {
		return ""
	}
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}

// snapshotOutfit reads the outfit stamp, falling back to the pre-rename keys
// that arias minted before the rename carry.
func snapshotOutfit(snapshot form.Snapshot) (name, version string) {
	name = snapshotString(snapshot, "system.outfit_name")
	if name == "" {
		name = snapshotString(snapshot, "system.loadout_name")
	}
	version = snapshotString(snapshot, "system.outfit_version")
	if version == "" {
		version = snapshotString(snapshot, "system.loadout_version")
	}
	return name, version
}

// resolveContextLimit reports the effective prompt cap for the current model.
//
// Precedence: an explicit system.max_context_tokens on the form wins
// outright — it is an override, so a user pinning a smaller (or larger) window
// must not be second-guessed by provider metadata. Only when it is unset does
// the provider get asked. (This used to be the other way round, which made the
// key unreachable for any provider that reported a limit.)
func resolveContextLimit(prov provider.Provider, model string, snapshot form.Snapshot) int {
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
	a.outfitName, a.outfitVer = snapshotOutfit(snapshot)
	a.mu.Unlock()
}

// seedMetricsFromMeta loads the checkpointed counters instead of recomputing
// them. Returns false when the sidecar cannot be trusted, in which case the
// caller must fall back to the full fold.
//
// The trust condition is the sidecar's watermark matching the log's tail: the
// sidecar is a checkpoint, not a mirror, and healMeta only runs on the read
// path. A mismatch means a crash mid-turn or an aria older than the sidecar,
// both of which the walk handles and then publishMetadata repairs.
func (a *Agent) seedMetricsFromMeta() bool {
	if a.backend == nil {
		return false
	}
	meta, err := a.backend.Meta(a.id)
	if err != nil || meta == nil || meta.LastFigaroLT == 0 {
		return false
	}
	tail, ok := a.figLog.PeekTail()
	if !ok || tail.LT != meta.LastFigaroLT {
		return false
	}

	snapshot := a.Snapshot()
	model := snapshotString(snapshot, "system.model")

	a.mu.Lock()
	a.tokensIn = meta.TokensIn
	a.tokensOut = meta.TokensOut
	a.cacheRead = meta.CacheReadTokens
	a.cacheWrite = meta.CacheWriteTokens
	a.messageCount = meta.MessageCount
	a.turnCount = meta.TurnCount
	a.metricsLT = meta.LastFigaroLT
	a.contextTokens = meta.ContextTokens
	a.contextExact = meta.ContextExact
	// The limit is a live provider+model lookup, never a checkpointed number:
	// a model swap between runs must not be reported from a stale sidecar.
	a.contextLimit = resolveContextLimit(a.provider(), model, snapshot)
	// Form-owned fields come from the board, which is authoritative and
	// already open. Taking them from the sidecar would let a stale mantra
	// survive a `figaro set`.
	a.model = model
	a.mantra = snapshotString(snapshot, "mantra")
	a.cwd = snapshotString(snapshot, "system.cwd")
	a.outfitName, a.outfitVer = snapshotOutfit(snapshot)
	a.mu.Unlock()
	return true
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
	a.outfitName, a.outfitVer = snapshotOutfit(snapshot)
	a.mu.Unlock()
}

// SubmitPrompt enqueues a prompt; the reply streams as log.* frames.
func (a *Agent) SubmitPrompt(req rpc.QuaRequest) { a.SubmitPromptFrom(req, "") }

// SubmitPromptFrom is SubmitPrompt with the caller's rendered attribution.
// sender is "" when nobody said who they were, which stays unattributed all
// the way down rather than becoming "unknown".
func (a *Agent) SubmitPromptFrom(req rpc.QuaRequest, sender string) {
	evt := event{
		typ:  eventUserPrompt,
		text: req.Text,
		form: req.Form,
	}
	if req.Text != "" {
		evt.segments = []promptSegment{{sender: sender, text: req.Text}}
	}
	a.inbox.Send(evt)
}

// QueuedPrompts returns a read-only snapshot of the messages this aria has
// accepted but not yet answered, in FIFO order, plus the epoch those ids
// belong to. The inbox is untouched.
//
// carriers opts in to empty-text prompts (pure form carriers): the CRUD
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

// TurnActive implements Figaro. Lock-free by design; see the interface doc.
func (a *Agent) TurnActive() bool { return a.turnActive() }

// Interrupt aborts the current turn, keeping the queue. It is the shape the
// Figaro interface uses (and the angelus's graceful shutdown, where dropping
// what someone queued would be exactly the wrong courtesy).
func (a *Agent) Interrupt() { a.Hangup(rpc.QueueKeep) }

// Hangup aborts the current turn and says what became of the messages waiting
// behind it.
//
// It also COALESCES the queue on the keep path — each contiguous run of
// waiting prompts folds into one message, with the same semantics steering
// already has (texts joined in order, form input merged so a later value
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
				ID:     e.id,
				Text:   e.text,
				State:  rpc.QueueStateQueued,
				At:     e.at,
				Merged: e.merged,
				Form:   e.form,
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
		OutfitName:       a.outfitName,
		OutfitVersion:    a.outfitVer,
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

// OnTeardown registers a func to run when the agent is torn down, after the
// drain loop has exited. The angelus uses it to unbind from the aria's hub:
// the endpoint must survive the agent, so the agent cannot own that cleanup
// and cannot be trusted to know the endpoint exists.
func (a *Agent) OnTeardown(fn func()) {
	if fn == nil {
		return
	}
	a.mu.Lock()
	a.teardown = append(a.teardown, fn)
	a.mu.Unlock()
}

func (a *Agent) Kill() {
	a.cancel()
	<-a.done // wait for drain loop to exit

	a.mu.Lock()
	a.subs = nil
	teardown := a.teardown
	a.teardown = nil
	a.mu.Unlock()

	for _, fn := range teardown {
		fn()
	}

	if a.form != nil {
		if err := a.form.Close(); err != nil {
			slog.Error("form close", "aria", a.id, "err", err)
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
			// COALESCE THE WAITING RUN. Everything queued behind this prompt
			// with no control event in between is part of the same ask: three
			// notes typed while the previous turn was finishing are one
			// question, not three turns to sit through — and that is true
			// whether the turn ahead of them completed or was interrupted.
			//
			// This is the third and last drain site to fold, and it is why the
			// fold is a property of DRAINING rather than of interrupting. The
			// mid-turn drains (prepareProviderRound, appendSteeringPrompts)
			// have always folded their batch; only this one, the idle path,
			// took a single event and gave each message its own turn.
			//
			// A lone prompt is still exactly itself: TakeReadyUserPrompts
			// returns nothing, mergePromptEvents short-circuits, and one
			// submit remains one message.
			batch := append([]event{evt}, a.inbox.TakeReadyUserPrompts()...)
			merged, ok := mergePromptEvents(batch)
			if !ok {
				continue
			}
			slog.Debug("event UserPrompt", "aria", a.id,
				"text", truncLog(merged.text, 60), "folded", len(batch))
			a.runTurn(ctx, merged)
		case eventSet:
			a.applyControlPatch(evt.setPatch, evt.setIfVersion, "set")
		case eventCast:
			a.serviceCast(evt.cast)
		}
	}
}

// serviceSets applies any queued form patches at a round boundary, the
// same points steering prompts drain. Each patch lands on the in-memory
// form (so the next provider round reads it via the snapshot) and rides
// the next IR LT as a transition. Returns true when it serviced at least one.
func (a *Agent) serviceSets() bool {
	evts := a.inbox.TakeReadySet()
	for _, evt := range evts {
		a.applyControlPatch(evt.setPatch, evt.setIfVersion, "set")
	}
	return len(evts) > 0
}

// applyControlPatch persists a state-only patch. No LLM round-trip.
// Backed arias append it to the reducible form channel (keyed to
// the next IR LT, so it rides the next turn as a transition); ephemeral
// arias fold it onto an IR control-turn (no channel to hold it).
func (a *Agent) applyControlPatch(patch message.Patch, ifVersion uint64, kind string) {
	slog.Debug("event "+kind, "aria", a.id, "set", len(patch.Set), "remove", len(patch.Remove))
	if a.backend != nil {
		if _, err := a.backend.ApplyFormIf(a.id, patch, ifVersion); err != nil {
			slog.Error(kind+" form append", "aria", a.id, "err", err)
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
	a.form.Apply(patch)
	a.refreshMetrics()
	a.publishMetadata()
}

// chalkAccessor returns the per-LT transition source for the provider:
// for backed arias, the form's patches in version order; nil for
// ephemeral (the provider falls back to inline IR patches).
//
// TODO: FormPatches copies the aria's ENTIRE patch history on every
// Send, which is O(total board) on a path that renders O(delta). See
// plans/form-projection-followups.md §1.
func (a *Agent) chalkAccessor() provider.Form {
	if a.backend == nil {
		return nil
	}
	ps, err := a.backend.FormPatches(a.id)
	if err != nil {
		slog.Warn("form patches (transitions disabled this turn)", "aria", a.id, "err", err)
		return nil
	}
	return &patchCursor{patches: ps}
}

// patchCursor walks the patch list forward, once. The projection asks for an
// absolute range -- (after, upTo] -- so the cursor never has to guess where it
// is: it skips anything at or below the low bound and takes what is in range.
// The index only ever advances, so a pass costs the patches it actually
// touches, not a scan per entry.
//
// The range is absolute rather than implicit BECAUSE the projection warm-starts
// mid-log. A cursor that assumed "you have already been driven over everything
// before this" replayed the whole board onto the first new message, and the
// per-LT cache made that permanent.
type patchCursor struct {
	patches []store.VersionedPatch
	i       int
}

func (c *patchCursor) PatchesBetween(after, upTo uint64) []message.Patch {
	for c.i < len(c.patches) && c.patches[c.i].Version <= after {
		c.i++
	}
	var out []message.Patch
	for c.i < len(c.patches) && c.patches[c.i].Version <= upTo {
		out = append(out, c.patches[c.i].Patch)
		c.i++
	}
	return out
}

// endTurn fans out turn.done and persists form + meta.
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
		Provider:         a.providerName(),
		Model:            a.model,
		Mantra:           a.mantra,
		Cwd:              a.cwd,
		OutfitName:       a.outfitName,
		OutfitVersion:    a.outfitVer,
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

// DeleteQueued asks the aria to drop queued messages, and reports what became
// of each requested id. Rejections are results, not errors: asking to delete
// something the agent has already committed is a legitimate request and a
// legitimate refusal, and the caller is told which of the two it got.
func (a *Agent) DeleteQueued(epoch string, ids []uint64, all bool) (string, []rpc.QueueResult) {
	return a.inbox.DeletePrompts(epoch, ids, all)
}

// UpdateQueued rewrites one queued message's text, under the same rules.
func (a *Agent) UpdateQueued(epoch string, id uint64, text string) (string, rpc.QueueResult) {
	return a.inbox.UpdatePrompt(epoch, id, text)
}
