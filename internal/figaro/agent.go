package figaro

import (
	"context"
	"encoding/json"
	"github.com/jack-work/figaro/internal/mark"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/methods"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tokens"
	"github.com/jack-work/figaro/internal/tool"
	"github.com/jack-work/figaro/internal/toolout"
	"github.com/jack-work/figaro/internal/turns"
)

type eventType int

const (
	eventUserPrompt eventType = iota
	// A study mark rides the inbox so it lands between rounds; a direct append
	// from the RPC goroutine can split a tool_use from its tool_result.
	eventStudyMark
)

type promptSegment struct {
	sender    string
	principal string
	text      string
}

type event struct {
	typ eventType

	// id is unique within the inbox epoch; merged names the ids folded into this event.
	id     uint64
	at     int64
	merged []uint64

	studyMark *message.StudyMark

	text string
	form *rpc.FormInput
	// segments keeps who said what across a fold; text stays the joined form.
	segments []promptSegment
}

type setVerdict struct {
	version uint64
	applied message.Patch
	err     error
}

// Config is the constructor input for NewAgent.
type Config struct {
	ID       string
	Provider provider.Provider
	// ProviderFactory rebinds the provider when system.provider changes; nil pins it.
	ProviderFactory ProviderFactory
	Tools           *tool.Registry
	Projector       Projector
	Backend         store.Backend // nil = ephemeral
	CreatedAt       time.Time
	LastActive      time.Time
	// WokeBy names the RPC method restoring this aria from dormancy; empty for a birth.
	WokeBy string

	// Form is the in-memory view of the form channel, which is the durable truth. Closed by Kill.
	Form *form.State

	// Settings is nil-safe: accessors return built-in defaults.
	Settings *config.Loaded

	// UICache is the process-wide composed UI IR cache; nil gives a private unbounded node.
	UICache *aria.ComposedCache

	Models ModelCatalog

	Decider     methods.Asker
	DefaultForm func() form.Snapshot
}

type ModelCatalog interface {
	Snapshot() form.Snapshot
	Want(provider, model string)
}

type Agent struct {
	id string
	// provBind is written by the drain loop, read lock-free on RPC goroutines.
	provBind    atomic.Pointer[providerBinding]
	provFactory ProviderFactory
	models      ModelCatalog
	decider     methods.Asker
	defaultForm func() form.Snapshot
	tools       *tool.Registry
	proj        Projector
	// figLog is read directly and written through journal only; see journal.go.
	figLog  store.Log[message.Message]
	journal *journal
	// turnFirstLT is the LT of the record that opened the current turn; zero between turns.
	turnFirstLT uint64
	backend     store.Backend // nil = ephemeral
	form        *form.State
	settings    *config.Loaded // wire budget policy; nil-safe

	// Casts run on the caller's goroutine, so publishStudies refuses any set not
	// newer than studiesVersion: the mirror follows durable order, not arrival order.
	studiesMu      sync.Mutex
	studiesVersion uint64

	inbox *Inbox

	// Intrinsic forms, addressed as `<id>/runtime` and `<id>/queue`.
	runtime *intrinsic
	queue   *intrinsic

	// Turn state, guarded by mu for Interrupt().
	turnCtx     context.Context
	turnCancel  context.CancelFunc
	turnRunning atomic.Bool // mirrors turnCancel != nil
	interrupted bool
	turnReason  string

	mu   sync.RWMutex
	subs map[Notifier]struct{}
	// teardown runs after the drain loop exits; the hub unbind lives here.
	teardown []func()

	// turnStartLT is the main LT of the last figLog entry before this turn.
	// It must be an LT, not an entry count: main LTs are trunk-global.
	turnStartLT   uint64
	turnStartTurn uint64
	turnID        uint64
	gov           *toolout.Governor
	lastEmit      time.Time
	argPartials   map[string]string
	turn          *turnState

	// regionMsgs is the open turn's decoded messages held across frames;
	// regionStart is the turnStartLT it belongs to, regionLast the highest LT folded in.
	regionMsgs  []message.Message
	regionStart uint64
	regionLast  uint64

	ariaSrv *aria.Server

	createdAt     time.Time
	lastActive    time.Time
	agentSince    time.Time
	wokeBy        string
	tokensIn      int
	tokensOut     int
	cacheRead     int
	cacheWrite    int
	messageCount  int
	turnCount     int
	metricsLT     uint64
	contextTokens int
	contextExact  bool
	model         string
	mantra        string
	cwd           string
	outfitName    string
	outfitVer     string

	cancel context.CancelFunc
	done   chan struct{}
}

func NewAgent(cfg Config) *Agent {
	if cfg.Backend == nil {
		panic("figaro.NewAgent: Backend is required -- there is no ephemeral aria (store.NewTestBackend in tests)")
	}
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
		provFactory: cfg.ProviderFactory,
		tools:       cfg.Tools,
		proj:        cfg.Projector,
		backend:     cfg.Backend,
		form:        cfg.Form,
		settings:    cfg.Settings,
		models:      cfg.Models,
		decider:     cfg.Decider,
		defaultForm: cfg.DefaultForm,
		createdAt:   createdAt,
		lastActive:  lastActive,
		agentSince:  now,
		wokeBy:      cfg.WokeBy,
		cancel:      cancel,
		done:        make(chan struct{}),
	}

	a.figLog = a.newLog()
	a.journal = a.newJournal(a.figLog)
	repairInterruptedTail(a.figLog, a.id)
	if a.form == nil {
		a.form, _ = form.Open("")
	}
	a.bindProvider(cfg.Provider)
	a.inbox = NewInbox(ctx)

	entries := a.figLog.Read()
	messages := unwrapMessages(entries)
	// The _meta sidecar is current after Backend.Open (healMeta folds the suffix).
	if !a.seedMetricsFromMeta() {
		a.refreshMetricsFrom(messages)
	}

	a.ariaSrv = aria.NewServer()
	a.ariaSrv.BindCache(a.id, cfg.UICache)
	for _, t := range a.attachFormDeltas(a.projTurns(unwrapMessages(entries)), entries) {
		a.ariaSrv.Commit(t)
	}
	a.ariaSrv.Subscribe(func(p aria.Page) {
		p.Metrics = a.sessionMetrics()
		a.fanOut(rpc.Notification{JSONRPC: "2.0", Method: rpc.MethodAriaFrame, Params: p})
	})

	// Form transitions ride the same fanout as frames. The sink runs on the form's writer.
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

	a.openIntrinsics()
	a.resumeStudies()
	a.publishMetadata()
	go a.runWithRecovery(ctx)
	return a
}

// The backend owns the log instance and closes it; the agent never does.
func (a *Agent) newLog() store.Log[message.Message] {
	log, err := a.backend.OpenFigIR(a.id)
	if err != nil {
		// Falling back to memory would silently orphan the on-disk content.
		slog.Error("backend open failed: keeping previous log", "aria", a.id, "err", err)
		if a.figLog != nil {
			return a.figLog
		}
		return store.GuardIR(store.NewMemLog[message.Message]())
	}
	return log
}

func (a *Agent) ID() string { return a.id }

// dukeTitle is what this aria calls its end user.
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

// snapshotOutfit falls back to the pre-rename loadout keys.
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

func (a *Agent) wantModel(snapshot form.Snapshot) {
	if a.models == nil {
		return
	}
	a.models.Want(models.Subject(snapshot))
}

func (a *Agent) contextLimit() int {
	if a.models == nil {
		return 0
	}
	return models.ContextLimit(a.models.Snapshot(), a.Snapshot())
}

// refreshMetrics runs at durable message boundaries, never on streaming deltas.
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
			figOtel.AddTokens(context.Background(), m.Usage.InputTokens, m.Usage.OutputTokens,
				m.Usage.CacheReadTokens, m.Usage.CacheWriteTokens,
				attribute.String("provider", a.providerName()), attribute.String("model", a.currentModel()))
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
	a.wantModel(snapshot)

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
	a.model = model
	a.mantra = snapshotString(snapshot, "mantra")
	a.cwd = snapshotString(snapshot, "system.cwd")
	a.outfitName, a.outfitVer = snapshotOutfit(snapshot)
	a.mu.Unlock()
}

// seedMetricsFromMeta reports false when the sidecar cannot be trusted.
func (a *Agent) seedMetricsFromMeta() bool {
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
	a.wantModel(snapshot)

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
	// Form-owned fields come from the board, not the sidecar.
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
	a.wantModel(snapshot)

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
	a.model = model
	a.mantra = snapshotString(snapshot, "mantra")
	a.cwd = snapshotString(snapshot, "system.cwd")
	a.outfitName, a.outfitVer = snapshotOutfit(snapshot)
	a.mu.Unlock()
}

func (a *Agent) SubmitPrompt(req rpc.QuaRequest) {
	_ = a.SubmitPromptFrom(context.Background(), req, "")
}

// SubmitPromptFrom applies form input synchronously, so the caller learns of a refusal.
func (a *Agent) SubmitPromptFrom(ctx context.Context, req rpc.QuaRequest, sender string) error {
	// A refused token leaves the form unpatched and the inbox untouched.
	text, err := a.rewriteInput(ctx, req.Text)
	if err != nil {
		return err
	}
	req.Text = text
	if patch := a.combineFormInput(req.Form); !patch.IsIdentity() {
		if _, err := a.backend.ApplyForm(a.id, patch); err != nil {
			return err
		}
		a.form.Apply(patch)
	}
	evt := event{
		typ:  eventUserPrompt,
		text: req.Text,
	}
	if req.Text != "" {
		evt.segments = []promptSegment{{sender: sender, principal: authz.PrincipalText(authz.PrincipalOf(ctx)), text: req.Text}}
	}
	a.inbox.Send(evt)
	if !a.turnActive() {
		a.publishRuntime(rpc.RuntimeAccepted, "")
	}
	return nil
}

// promptIDs is the id plus every id folded into it: the set a turn stamp must cover.
func promptIDs(e event) []uint64 {
	if e.id == 0 {
		return nil
	}
	return append([]uint64{e.id}, e.merged...)
}

// QueuedPrompts reports what is still in flight; the departed live in the queue intrinsic.
func (a *Agent) QueuedPrompts(carriers bool) (string, []rpc.QueuedPrompt) {
	snap := a.inbox.Project()
	out := make([]rpc.QueuedPrompt, 0, len(snap.Items))
	for _, it := range snap.Items {
		if it.State != rpc.QueueStateQueued && it.State != rpc.QueueStateCommitting {
			continue
		}
		if !carriers && it.Text == "" {
			continue
		}
		out = append(out, rpc.QueuedPrompt{
			ID:     it.ID,
			Text:   it.Text,
			State:  it.State,
			At:     it.At,
			Merged: it.Merged,
		})
	}
	return snap.Epoch, out
}

// turnActive is a lock-free point-in-time read.
func (a *Agent) turnActive() bool {
	return a.turnRunning.Load()
}

// Interrupt aborts the current turn and keeps the queue.
func (a *Agent) Interrupt() { a.Hangup(rpc.QueueKeep) }

// Hangup aborts the current turn and reports what became of the queue.
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
	resp.Stopped = true
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

// appendMsg stamps the in-flight turn id; every durable message write goes through here.
func (a *Agent) appendMsg(m message.Message) (store.Entry[message.Message], error) {
	m.TurnID = a.turnID
	return a.writer().Append(store.Entry[message.Message]{Payload: m})
}

// openTurn seeds from the log on first use, so a forked child continues its parent's numbering.
func (a *Agent) openTurn() {
	if a.turnID == 0 {
		a.turnID = a.seedTurnID()
	}
	a.turnID++
}

// seedTurnID walks the log only when the tail carries no id (pre-prompt records only).
func (a *Agent) seedTurnID() uint64 {
	if tail, ok := a.figLog.PeekTail(); ok && tail.Payload.TurnID != 0 {
		return tail.Payload.TurnID
	}
	return turns.StampIDs(unwrapMessages(a.figLog.Read()))
}

// unwrapMessages stamps LT and, for entries predating turn ids, a derived TurnID.
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

type Notifier interface {
	Notify(method string, params any) error
}

// Subscribe returns an unsubscribe func.
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
	if !a.inbox.Quiet() {
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
		ContextExact:     a.contextExact,
		CreatedAt:        a.createdAt,
		LastActive:       a.lastActive,
		AgentSince:       a.agentSince,
		WokeBy:           a.wokeBy,
		Mantra:           a.mantra,
		Cwd:              a.cwd,
		OutfitName:       a.outfitName,
		OutfitVersion:    a.outfitVer,
		LastFigaroLT:     a.metricsLT,
	}
	a.mu.RUnlock()
	info.ContextLimit = a.contextLimit()
	return info
}

func (a *Agent) sessionMetrics() *aria.Metrics {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return &aria.Metrics{
		ContextTokens:    a.contextTokens,
		ContextExact:     a.contextExact,
		TokensIn:         a.tokensIn,
		TokensOut:        a.tokensOut,
		CacheReadTokens:  a.cacheRead,
		CacheWriteTokens: a.cacheWrite,
		Mantra:           a.mantra,
	}
}

// OnTeardown runs fn after the drain loop exits; the hub endpoint must outlive the agent.
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
	<-a.done

	a.mu.Lock()
	a.subs = nil
	teardown := a.teardown
	a.teardown = nil
	a.mu.Unlock()

	// Release the sealed section's bytes to the shared UI budget.
	if a.ariaSrv != nil {
		a.ariaSrv.ReleaseCache()
	}

	for _, fn := range teardown {
		fn()
	}

	if a.form != nil {
		if err := a.form.Close(); err != nil {
			slog.Error("form close", "aria", a.id, "err", err)
		}
	}

}

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
		a.journal = a.newJournal(a.figLog)
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
	history := a.materializeTurns()
	// Never shrink materialized state: this runs on error paths whose only truth
	// is figLog, which may be a transient fallback.
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

func (a *Agent) act(ctx context.Context) {
	for {
		evt, ok := a.inbox.Recv()
		if !ok {
			return
		}
		switch evt.typ {
		case eventUserPrompt:
			batch := append([]event{evt}, a.inbox.TakeReadyUserPrompts()...)
			merged, ok := mergePromptEvents(batch)
			if !ok {
				continue
			}
			slog.Debug("event UserPrompt", "aria", a.id,
				"text", truncLog(merged.text, 60), "folded", len(batch))
			a.runTurn(ctx, merged)
		case eventStudyMark:
			a.writeStudyMark(evt.studyMark)
		}
	}
}

// serviceSets drains study marks at a round boundary, where a user record is legal.
func (a *Agent) serviceSets() bool {
	evts := a.inbox.TakeReadySet()
	for _, evt := range evts {
		a.writeStudyMark(evt.studyMark)
	}
	return len(evts) > 0
}

// applyFormPatch is safe off the drain loop: the durable write is serialized by
// the form's actor and the in-memory board applies by CAS over an immutable snapshot.
func (a *Agent) applyFormPatch(patch message.Patch, ifVersion uint64, assert bool, kind string) (uint64, message.Patch, error) {
	v := a.applyControlPatchVerdict(patch, ifVersion, assert, kind, nil)
	return v.version, v.applied, v.err
}

func (a *Agent) applyControlPatchVerdict(patch message.Patch, ifVersion uint64, assert bool, kind string, done chan setVerdict) setVerdict {
	verdict := setVerdict{}
	if done != nil {
		defer func() {
			select {
			case done <- verdict:
			default:
			}
		}()
	}
	slog.Debug("event "+kind, "aria", a.id, "entries", len(patch.Entries()))
	if a.backend != nil {
		intent := store.Ensure
		if assert {
			intent = store.Assert
		}
		version, applied, err := a.backend.ApplyFormEffectIntent(a.id, patch, ifVersion, intent)
		verdict.version, verdict.applied, verdict.err = version, applied, err
		if err != nil {
			slog.Error(kind+" form append", "aria", a.id, "err", err)
			return verdict
		}
		// Publish what the writer actually wrote, not the requested bytes.
		patch = applied
	} else {
		msg := message.Message{
			Role:      message.RoleInput,
			Patches:   []message.Patch{patch},
			Timestamp: time.Now().UnixMilli(),
		}
		if _, err := a.appendMsg(msg); err != nil {
			verdict.err = err
			slog.Error(kind+" append", "aria", a.id, "err", err)
			return verdict
		}
		verdict.applied = patch
	}
	a.form.Apply(patch)
	a.refreshMetrics()
	a.publishMetadata()
	return verdict
}

// formAccessor is nil for an unopenable form; the provider then falls back to inline IR patches.
func (a *Agent) formAccessor() provider.Form {
	if _, err := a.backend.FormVersion(a.id); err != nil {
		slog.Warn("form patches (transitions disabled this turn)", "aria", a.id, "err", err)
		return nil
	}
	return formView{backend: a.backend, id: a.id}
}

// studyAccessors reads each studied form's libretto, never the source, so a
// deleted source needs no special case.
func (a *Agent) studyAccessors() map[string]provider.Form {
	lb, ok := a.backend.(librettoBackend)
	if !ok {
		return nil
	}
	ids := StudiesFromSnapshot(a.form.Snapshot())
	if len(ids) == 0 {
		return nil
	}
	out := make(map[string]provider.Form, len(ids))
	for _, fid := range ids {
		lib, err := lb.Libretto(fid)
		if err != nil {
			continue
		}
		out[fid] = librettoView{lib}
	}
	return out
}

// librettoBackend is optional: an ephemeral backend has none.
type librettoBackend interface {
	Libretto(sourceFormID string) (*store.Libretto, error)
}

// librettoView strips the libretto's bookkeeping keys.
type librettoView struct{ lib *store.Libretto }

func (v librettoView) PatchesBetween(after, upTo uint64) []message.Patch {
	ps := v.lib.PatchesBetween(after, upTo)
	out := make([]message.Patch, 0, len(ps))
	for _, vp := range ps {
		if p := withoutBookkeeping(vp.Patch); !p.IsIdentity() {
			out = append(out, p)
		}
	}
	return out
}

// withoutBookkeeping rebuilds the patch rather than editing it: the input is the
// store's published value, shared by every reader.
func withoutBookkeeping(p message.Patch) message.Patch {
	keep := map[string]json.RawMessage{}
	hidden := false
	for _, ent := range p.Entries() {
		k, raw := ent.Key, ent.New
		if ent.IsRemoval() {
			continue
		}
		if store.HiddenLibrettoKey(k) {
			hidden = true
			continue
		}
		keep[k] = raw
	}
	var drops []string
	for _, ent := range p.Entries() {
		if !ent.IsRemoval() {
			continue
		}
		k := ent.Key
		if store.HiddenLibrettoKey(k) {
			hidden = true
			continue
		}
		drops = append(drops, k)
	}
	if !hidden {
		return p
	}
	return form.Build(form.Snapshot{}, keep, drops)
}

// formView answers absolute patch ranges from the store, holding no position.
type formView struct {
	backend store.Backend
	id      string
}

func (v formView) PatchesBetween(after, upTo uint64) []message.Patch {
	ps, err := v.backend.FormPatchesBetween(v.id, after, upTo)
	if err != nil {
		slog.Debug("form patches between", "form", v.id, "after", after, "upTo", upTo, "err", err)
		return nil
	}
	if len(ps) == 0 {
		return nil
	}
	out := make([]message.Patch, len(ps))
	for i := range ps {
		out[i] = ps[i].Patch
	}
	return out
}

// endTurn commits the live unit and signals idle.
func (a *Agent) endTurn(reason string) {
	a.refreshMetrics()
	a.emitCommit()
	a.finishTurn(reason)
}

// endTurnDiscarding drops the live unit: the assistant message never reached figLog,
// and committing it would show content the next turn regenerates.
func (a *Agent) endTurnDiscarding(reason string) {
	a.refreshMetrics()
	a.abandonLive()
	a.finishTurn(reason)
}

func (a *Agent) finishTurn(reason string) {
	a.turnReason = reason
	// asm holds the whole streamed reply; an idle aria must not keep it.
	if a.turn != nil {
		a.turn.asm = nil
	}

	// Close open tool calls before announcing turn.done, or a fork or read between
	// the announcement and the next message sees an invoke with no result.
	if a.backend != nil {
		if n, err := a.backend.CloseOpenToolCalls(a.id); err != nil {
			slog.Error("close open tool calls at turn end", "aria", a.id, "err", err)
		} else if n > 0 {
			slog.Info("closed open tool calls at turn end", "aria", a.id, "calls", n)
		}
	}
	// A failed turn belongs in the durable log, not only on the watching terminal.
	if strings.HasPrefix(reason, "error:") {
		slog.Error("turn failed", "aria", a.id, "reason", strings.TrimSpace(strings.TrimPrefix(reason, "error:")))
	}
	var lts []uint64
	if a.turnFirstLT > 0 && a.figLog != nil {
		if last, _ := a.figLog.ReadPage(0, ^uint64(0), 1); len(last) > 0 {
			lts = []uint64{a.turnFirstLT, last[0].LT}
		}
	}
	a.stampSealDeltas()
	a.ariaSrv.Seal(lts)
	idle := a.inbox.IsIdle()
	a.mu.Lock()
	a.lastActive = time.Now()
	a.mu.Unlock()
	a.fanOut(rpc.Notification{
		JSONRPC: "2.0",
		Method:  rpc.MethodTurnDone,
		Params:  rpc.DoneEntry{Reason: reason, Idle: &idle},
	})
	// The runtime intrinsic carries the same fact as turn.done in a resyncable form.
	a.publishRuntime(rpc.RuntimeIdle, reason)

	a.publishMetadata()
}

func (a *Agent) publishMetadata() {
	a.mu.RLock()
	mine := store.AriaMeta{
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
		ContextExact:     a.contextExact,
		CreatedAtMS:      a.createdAt.UnixMilli(),
		LastFigaroLT:     a.metricsLT,
	}
	a.mu.RUnlock()
	// The agent owns the counts and identity fields, not TTLMS.
	if err := a.backend.UpdateMeta(a.id, func(m *store.AriaMeta) {
		ttl := m.TTLMS
		*m = mine
		m.TTLMS = ttl
	}); err != nil {
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
	mark.Mark("fanout", "aria", a.id, "method", n.Method)

	for sub := range a.subs {
		if err := sub.Notify(n.Method, n.Params); err != nil {
			slog.Warn("notify subscriber", "aria", a.id, "err", err)
		}
	}
}

func truncLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

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

// DeleteQueued reports rejections as results, not errors.
func (a *Agent) DeleteQueued(epoch string, ids []uint64, all bool) (string, []rpc.QueueResult) {
	return a.inbox.DeletePrompts(epoch, ids, all)
}

// UpdateQueued rewrites one queued message's text.
func (a *Agent) UpdateQueued(epoch string, id uint64, text string) (string, rpc.QueueResult) {
	return a.inbox.UpdatePrompt(epoch, id, text)
}

// BoardAccessorFor and StudyAccessorsFor serve the fig IR write path, which has
// no agent; an entry must translate the same way whether it lands through a
// daemon, a repair, or a study sweep.
func BoardAccessorFor(backend store.Backend, ariaID string) provider.Form {
	if backend == nil {
		return nil
	}
	if _, err := backend.FormVersion(ariaID); err != nil {
		return nil
	}
	return formView{backend: backend, id: ariaID}
}

func StudyAccessorsFor(backend store.Backend, ariaID string) map[string]provider.Form {
	lb, ok := backend.(librettoBackend)
	if !ok {
		return nil
	}
	snap, err := backend.FormState(ariaID)
	if err != nil {
		return nil
	}
	ids := StudiesFromSnapshot(snap)
	if len(ids) == 0 {
		return nil
	}
	out := make(map[string]provider.Form, len(ids))
	for _, id := range ids {
		lib, err := lb.Libretto(id)
		if err != nil || lib == nil {
			continue
		}
		out[id] = librettoView{lib: lib}
	}
	return out
}
