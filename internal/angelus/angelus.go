package angelus

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/livelog/aria"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/store"
	fwtree "github.com/jack-work/figaro/internal/store/tree"
	"github.com/jack-work/figaro/internal/tool"
	"github.com/jack-work/figaro/internal/uiir"
	"github.com/jack-work/jkrpc"
)

// Angelus is the figaro supervisor.
type Angelus struct {
	Registry   *Registry
	Handlers   map[string]jkrpc.HandlerFunc // set before Run()
	Backend    store.Backend                // aria persistence
	SocketPath string
	RuntimeDir string
	StartedAt  time.Time
	// Build is this daemon's VCS revision, reported over angelus.status so a
	// CLI can refuse to speak to a daemon from a different build.
	Build string

	// RemoveAria deletes a node the way `figaro kill` does -- agent,
	// sessions, bytes, endpoint. Wired from the handlers, because the TTL
	// sweep must delete through the SAME four steps and not a fifth spelling
	// of them. nil disables the sweep's deletions.
	RemoveAria func(id string, recursive bool) error

	// Sessions is the daemon-wide registry of backgrounded exec sessions,
	// keyed by aria id as scope. Deliberately not per-agent: an agent that
	// is killed or hibernated must not take its children's addressability
	// with it. See tool.WithSessions.
	Sessions *tool.SessionRegistry

	// Settings is the loaded config, read for reclamation policy. nil is
	// legal and means every default.
	Settings *config.Loaded

	// UICache is the process-wide composed UI IR cache: the canonical
	// tree with a node per aria, shared by every agent's turn cache and by
	// the reader, against one budget and one eviction order. Its source is
	// composeTurns, which reads the store directly, so a node answers
	// whether or not anything has opened that aria.
	UICache *aria.ComposedCache

	// uiProj renders fig IR as UI IR for the cache's source and the read
	// path. One per daemon.
	uiProj Projector

	// Hubs is the set of aria endpoints. Each outlives the agent behind it,
	// so reclaiming an agent does not disconnect anybody. See ariaHub.
	Hubs *hubs

	// capShortBy is the last reported shortfall against max_live_arias, so a
	// standing condition is logged on change rather than on every sweep.
	capShortBy atomic.Int32

	// quietSweeps counts consecutive sweeps with nothing live and nothing
	// resident. Touched only by the sweep goroutine.
	quietSweeps int

	// residentFor overrides where the latch reads the resident count, for a
	// test that must not implement the whole backend to answer one question.
	residentFor idleEvictor

	// lastLibrettoSweep is what the boot reconciliation found, published for
	// `doctor mem`. The sweep runs in the background and repairs quietly;
	// without this the only way to learn whether it did anything was to stop
	// the daemon and run the audit by hand, which is a poor way to discover
	// that a migration happened.
	lastLibrettoSweep atomic.Pointer[store.LibrettoAudit]

	listener  net.Listener
	cancel    context.CancelFunc
	pprofPath string // set by StartPprof; empty when profiling is not armed
}

// Config holds the settings for creating an Angelus.
type Config struct {
	RuntimeDir string         // e.g. $XDG_RUNTIME_DIR/figaro
	Backend    store.Backend  // aria persistence
	Settings   *config.Loaded // reclamation policy; nil = defaults
}

// New creates an Angelus. Call Run() to start it.
// Set a.Handlers before calling Run() to enable JSON-RPC.
func New(cfg Config) *Angelus {
	a := &Angelus{
		Registry:   NewRegistry(),
		Backend:    cfg.Backend,
		SocketPath: filepath.Join(cfg.RuntimeDir, "angelus.sock"),
		RuntimeDir: cfg.RuntimeDir,
		StartedAt:  time.Now(), // set-once at construction; read concurrently (Uptime)
		Sessions:   tool.NewSessionRegistry(tool.DefaultSessionTTL),
		Settings:   cfg.Settings,
		uiProj:     uiir.New(nil),
		Hubs:       newHubs(),
	}
	a.UICache = aria.NewComposedCache(uiBudget(cfg.Settings), a.composeTurns, a.uiLineage)
	return a
}

// uiWindowMB is the composed-UI bound: the package that holds those bytes owns
// the default, and a config file TUNES it. Nil settings and an unconfigured
// key both mean "leave the owner's number alone"; an explicit 0 is the user
// asking for unbounded and is honoured.
func uiWindowMB(settings *config.Loaded) int {
	if mb, set := settings.UIWindowMB(); set {
		return mb
	}
	return aria.DefaultUIWindowMB
}

// uiBudget is the composed cache's accountant, in the same units as every
// other tree budget: bytes.
func uiBudget(settings *config.Loaded) *fwtree.Budget {
	return fwtree.NewBudget(int64(uiWindowMB(settings)) << 20)
}

// FigaroSocketDir returns the directory for figaro sockets.
func (a *Angelus) FigaroSocketDir() string {
	return filepath.Join(a.RuntimeDir, "figaros")
}

// BindingsPath returns the path for persisted PID bindings.
func (a *Angelus) BindingsPath() string {
	return filepath.Join(a.RuntimeDir, "bindings.json")
}

// Run starts the angelus and blocks until ctx is cancelled.
func (a *Angelus) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	a.cancel = cancel

	ctx, span := figOtel.Start(ctx, "angelus.run",
		figOtel.WithAttributes(
			attribute.String("angelus.socket", a.SocketPath),
			attribute.Int("angelus.pid", os.Getpid()),
		),
	)
	defer span.End()

	if err := os.MkdirAll(a.FigaroSocketDir(), 0700); err != nil {
		return err
	}
	os.Remove(a.SocketPath)

	ln, err := net.Listen("unix", a.SocketPath)
	if err != nil {
		return err
	}
	a.listener = ln

	if err := os.Chmod(a.SocketPath, 0600); err != nil {
		ln.Close()
		return err
	}

	pidPath := filepath.Join(a.RuntimeDir, "angelus.pid")
	os.WriteFile(pidPath, []byte(fmt.Sprintf("%d", os.Getpid())), 0600)
	defer os.Remove(pidPath)

	// The socket is NOT removed here: `figaro rest` treats its removal as
	// "shutdown complete", and Run returns before the drain does. The
	// daemon main (runAngelus) removes it after Shutdown finishes.

	slog.Info("angelus started", "pid", os.Getpid(), "socket", a.SocketPath)

	// Diagnostic and opt-in: an unavailable profiler must not keep the
	// daemon from starting.
	_ = a.StartPprof(ctx)

	go a.pidMonitor(ctx)
	a.reconcileLibrettos()
	go a.metaBackfill(ctx)

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				slog.Info("angelus shutting down")
				return nil
			default:
				slog.Warn("angelus accept", "err", err)
				continue
			}
		}
		go a.handleConn(ctx, conn)
	}
}

// handleConn serves a single JSON-RPC connection.
func (a *Angelus) handleConn(ctx context.Context, conn net.Conn) {
	if a.Handlers == nil {
		conn.Close()
		return
	}

	jconn := jkrpc.NewConn(conn)
	srv := jkrpc.NewServer(jconn, a.Handlers)

	done := make(chan struct{})
	go func() {
		srv.Serve(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		srv.Stop()
	}
}

// pidMonitor polls bound PIDs every 2 seconds and unbinds dead ones, and
// on a slower beat releases the caches of arias nobody is using.
func (a *Angelus) pidMonitor(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	evict := time.NewTicker(a.sweepInterval())
	defer evict.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.reapDeadPIDs()
			a.sweepCacheBudgets()
		case <-evict.C:
			// Order matters: reclaim agents first, then sweep caches. An
			// aria hibernated on this tick is no longer live, so its 12-14 MB
			// of decoded IR becomes eligible on the NEXT tick rather than
			// waiting a whole extra cycle to be noticed.
			a.hibernateIdleArias()
			a.capLiveArias()
			a.expireTTL()
			a.evictIdleArias()
			a.trimResident()
			a.releaseIdleMemory()
		}
	}
}

// reconcileLibrettos recounts every libretto from the boards, once, at boot.
func (a *Angelus) reconcileLibrettos() {
	rec, ok := a.Backend.(librettoReconciler)
	if !ok {
		return
	}
	go func() {
		audit, err := rec.ReconcileLibrettos()
		if err != nil {
			slog.Warn("libretto reconciliation failed", "err", err)
			return
		}
		a.lastLibrettoSweep.Store(&audit)
		if audit.Corrected > 0 || audit.Minted > 0 || audit.Missing > 0 || audit.Orphaned > 0 {
			slog.Info("librettos reconciled",
				"boards", audit.Boards, "librettos", audit.Librettos,
				"corrected", audit.Corrected, "minted", audit.Minted,
				"orphaned", audit.Orphaned, "missing", audit.Missing)
		}
	}()
}

type librettoReconciler interface {
	HasLibrettos() bool
	ReconcileLibrettos() (store.LibrettoAudit, error)
}

// releaseIdleMemory hands free heap back to the OS once a daemon has gone
// quiet — nothing live, nothing resident, twice running.
func (a *Angelus) releaseIdleMemory() {
	if !a.idleReleaseDue() {
		return
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	debug.FreeOSMemory()
	runtime.ReadMemStats(&after)
	slog.Info("idle daemon released free heap to the OS",
		"heap_sys_before", before.HeapSys, "heap_sys_after", after.HeapSys,
		"released_bytes", int64(before.HeapSys)-int64(after.HeapSys),
		"took", time.Since(start))
}

// idleReleaseDue advances the quiet latch and reports whether this sweep is
// the one that hands the arena back. Separated from the release so the policy
// — fire once per quiet period, reset on any work — is testable without a
// stop-the-world collection in a unit test.
func (a *Angelus) idleReleaseDue() bool {
	resident := 0
	ev := a.residentFor
	if ev == nil {
		if be, ok := a.Backend.(idleEvictor); ok {
			ev = be
		}
	}
	if ev != nil {
		resident = ev.Resident()
	}
	if len(a.Registry.List()) > 0 || resident > 0 {
		a.quietSweeps = 0
		return false
	}
	a.quietSweeps++
	return a.quietSweeps == quietSweepsBeforeRelease
}

// quietSweepsBeforeRelease is how many consecutive quiet sweeps must pass
// before the arena is handed back. Two, so a daemon between two requests is
// not made to re-fault its heap for a pause in the conversation.
const quietSweepsBeforeRelease = 2

// Reclamation policy. Both come from [memory] in config.toml; the constants
// here are only the fallback for a daemon started without one.
func (a *Angelus) sweepInterval() time.Duration {
	if d := a.Settings.SweepInterval(); d > 0 {
		return d
	}
	return defaultSweepInterval
}

func (a *Angelus) dormantAfter() time.Duration {
	if a.Settings == nil {
		return defaultDormantAfter
	}
	return a.Settings.DormantAfter() // 0 means never, and the caller honours it
}

const (
	defaultSweepInterval = 2 * time.Minute
	defaultDormantAfter  = 15 * time.Minute
)

// idleEvictor is the backend's half of the contract. Kept as an interface
// rather than added to store.Backend so a test backend does
// not have to implement a cache policy it does not have.
type idleEvictor interface {
	EvictIdle(live map[string]bool, idle time.Duration) int
	Resident() int
}

// budgetSweeper is the decoded caches' half of the standing sweep: charge
// raises pressure on a read, and this lowers it here, on the beat that already
// exists. A read never blocks on eviction.
type budgetSweeper interface {
	SweepCacheBudgets() (int, int64)
}

// sweepCacheBudgets brings the composed, decoded IR and translation caches
// back within their budgets. Cheap when there is no pressure: one atomic read per budget.
func (a *Angelus) sweepCacheBudgets() {
	if dropped, freed := a.UICache.Budget().Sweep(); dropped > 0 {
		slog.Debug("swept the composed cache budget", "runs", dropped, "bytes", freed)
	}
	sw, ok := a.Backend.(budgetSweeper)
	if !ok {
		return
	}
	if dropped, freed := sw.SweepCacheBudgets(); dropped > 0 {
		slog.Debug("swept decoded cache budgets", "runs", dropped, "bytes", freed)
	}
}

// ttlKeeper is the backend's half of the retention contract. An interface for
// the same reason idleEvictor is one: a test backend should not have to own a
// policy it does not have.
type ttlKeeper interface {
	TTLDue(nowMS int64) []store.TTLEntry
	TTLForget(ids ...string)
}

// expireTTL removes the nodes whose stated lifetime is spent. It runs on the
// sweep that already exists, and it reads only sidecars: finding what is due
// opens no node.
//
// SKIP UNTIL DORMANT is the whole of the live policy. An expired aria with an
// agent, or with a shell bound to it, keeps its deadline and is taken on a
// later tick -- a lifetime is a promise about storage, and nothing about it
// justifies deleting the aria somebody is typing into.
//
// The delete is RECURSIVE by construction. Branches go with their ancestor
// however new they are, and the removal is addressed at the PRESENTATION
// hierarchy, so a child promoted past an expired parent survives it: the
// parent's delete set no longer contains the child, and the child, which reads
// its history through the doomed prefix, is detached first (RemoveLeaf's
// boundary repair -- disk normalisation, forced exactly when it is owed).
//
// KNOWN SHORTCOMING, and it is Gluck's to weigh: a fork inherits its parent's
// board, so it inherits system.ttl and expires on its OWN creation time. A
// child therefore cannot outlive an ancestor's policy by being younger than
// it; it can only start its own clock later. Noted, not solved.
func (a *Angelus) expireTTL() {
	keeper, ok := a.Backend.(ttlKeeper)
	if !ok || a.RemoveAria == nil {
		return
	}
	due := keeper.TTLDue(time.Now().UnixMilli())
	if len(due) == 0 {
		return
	}
	bound := a.Registry.BoundPIDsByFigaro()
	for _, e := range due {
		if a.Registry.Get(e.ID) != nil {
			slog.Info("lifetime spent, waiting for the aria to go dormant",
				"node", e.ID, "ttl", e.TTL)
			continue
		}
		if len(bound[e.ID]) > 0 {
			slog.Info("lifetime spent, waiting for the bound shell to leave",
				"node", e.ID, "ttl", e.TTL, "pids", bound[e.ID])
			continue
		}
		if err := a.RemoveAria(e.ID, true); err != nil {
			slog.Warn("lifetime spent, removal failed", "node", e.ID, "err", err)
			continue
		}
		keeper.TTLForget(e.ID)
		slog.Info("removed a node whose lifetime was spent",
			"node", e.ID, "ttl", e.TTL,
			"created", time.UnixMilli(e.CreatedAtMS),
			"expired", time.UnixMilli(e.DeadlineMS))
	}
}

// evictIdleArias releases the cached IR, translations, board and metadata of
// every aria with no live agent that nobody has touched recently.
func (a *Angelus) evictIdleArias() {
	ev, ok := a.Backend.(idleEvictor)
	if !ok {
		return
	}
	live := map[string]bool{}
	for _, f := range a.Registry.List() {
		live[f.ID] = true
	}
	idle := a.dormantAfter()
	if idle <= 0 {
		return // reclamation disabled
	}
	if n := ev.EvictIdle(live, idle); n > 0 {
		slog.Info("released idle aria caches", "evicted", n, "live", len(live), "resident", ev.Resident())
	}
	// And the layer below: figwal's raw segment payloads. The budget bounds a
	// busy daemon; this is what a quiet one gives back. It rides the sweep
	// that already exists rather than introducing a fourth idle clock, and
	// `keep` is in SWEEPS, so the window is dormant_after / sweep_interval.
	if sw, ok := a.Backend.(segmentSweeper); ok {
		if dropped, freed := sw.SweepSegmentCache(segmentCacheKeepSweeps); dropped > 0 {
			slog.Info("released idle segment payloads",
				"blocks", dropped, "freed_bytes", freed)
		}
	}
}

// segmentCacheKeepSweeps is how many reclamation sweeps a segment's payloads
// survive without a read. Two, so the window is the same order as the aria
// dormancy it rides on, and a block used once per sweep is never dropped.
const segmentCacheKeepSweeps = 2

// segmentSweeper is figwal's half of the same contract, optional for the same
// reason idleEvictor is.
type segmentSweeper interface {
	SweepSegmentCache(keep int64) (dropped int, freed int64)
}

// hibernateIdleArias reclaims the agent of every aria that has been idle
// longer than the configured window.
func (a *Angelus) hibernateIdleArias() {
	idle := a.dormantAfter()
	if idle <= 0 {
		return // reclamation disabled
	}
	cutoff := time.Now().Add(-idle)

	for _, info := range a.Registry.List() {
		if info.State != "idle" || info.LastActive.After(cutoff) {
			continue
		}
		if err := a.Registry.Hibernate(info.ID); err != nil {
			// A refusal is ordinary: the aria took a prompt between the
			// decision and the teardown. It will be reconsidered next tick.
			slog.Debug("hibernate declined", "aria", info.ID, "err", err)
			continue
		}
		slog.Info("hibernated aria", "aria", info.ID,
			"idle_for", time.Since(info.LastActive).Round(time.Second),
			"live", a.Registry.FigaroCount())
	}
}

// capLiveArias enforces max_live_arias by reclaiming the least recently active
// idle agents until the count is under the cap.
func (a *Angelus) capLiveArias() {
	max := a.maxLiveArias()
	if max <= 0 {
		return
	}
	all := a.Registry.List()
	over := len(all) - max
	if over <= 0 {
		return
	}

	fresh := time.Now().Add(-capFlapGuard)
	victims := make([]figaro.FigaroInfo, 0, len(all))
	for _, info := range all {
		if info.State == "idle" && info.LastActive.Before(fresh) {
			victims = append(victims, info)
		}
	}
	sort.Slice(victims, func(i, j int) bool {
		return victims[i].LastActive.Before(victims[j].LastActive)
	})

	reclaimed := 0
	for _, v := range victims {
		if reclaimed >= over {
			break
		}
		if err := a.Registry.Hibernate(v.ID); err != nil {
			slog.Debug("cap declined", "aria", v.ID, "err", err)
			continue
		}
		reclaimed++
		slog.Info("reclaimed for cap", "aria", v.ID, "live", a.Registry.FigaroCount(), "cap", max)
	}
	// Say it once per situation, not once per sweep. A cap that cannot be met
	// is a standing condition, and on a fast sweep interval reporting it every
	// tick buries the events that matter: a 1-second sweep against an unmeetable
	// cap wrote 60 identical lines in a minute during the fuzz.
	shortBy := over - reclaimed
	if shortBy != int(a.capShortBy.Swap(int32(shortBy))) && shortBy > 0 {
		slog.Info("live aria cap not met",
			"cap", max, "live", a.Registry.FigaroCount(), "over_by", shortBy,
			"reason", "remaining arias are active or recently woken")
	}
}

// capFlapGuard exempts a recently woken aria from the cap. Restore costs
// O(history); reclaiming what just paid that cost is the flap the plan warned
// about.
const capFlapGuard = time.Minute

func (a *Angelus) maxLiveArias() int {
	if a.Settings == nil {
		return 0
	}
	return a.Settings.MaxLiveArias()
}

// residentTrimmer is the backend's half of the windowing contract, an optional
// interface for the same reason idleEvictor is one: a test
// backend has no window to trim.
type residentTrimmer interface {
	TrimResident(live map[string]bool, keep int) int
	ResidentRows() int
	ResidentIRBytes() int
}

// trimResident shrinks the IR window of every aria with no live agent.
func (a *Angelus) trimResident() {
	// The row cap is the only half a daemon still supplies: the byte budget is
	// the store's own (store.DefaultIRBudgetBytes), so there is no longer a
	// configuration under which this should decline to run. Unset means "let
	// the bytes decide" -- a row cap the budget binds before.
	keep := trimRowsUnbounded
	if a.Settings != nil {
		if n := a.Settings.IRWindow(); n > 0 {
			keep = n
		}
	}
	tr, ok := a.Backend.(residentTrimmer)
	if !ok {
		return
	}
	live := map[string]bool{}
	for _, f := range a.Registry.List() {
		live[f.ID] = true
	}
	if n := tr.TrimResident(live, keep); n > 0 {
		slog.Info("trimmed resident IR", "rows_released", n,
			"rows_resident", tr.ResidentRows(), "bytes_resident", tr.ResidentIRBytes())
	}
}

// trimRowsUnbounded stands in for "no row cap" when only a byte budget is set.
const trimRowsUnbounded = 1 << 30

// EvictNow drops the cached IR, translations and board of every aria with no
// live agent, regardless of how recently it was touched and regardless of
// whether the timed sweep is enabled at all.
func (a *Angelus) EvictNow() int {
	ev, ok := a.Backend.(idleEvictor)
	if !ok {
		return 0
	}
	live := map[string]bool{}
	for _, f := range a.Registry.List() {
		live[f.ID] = true
	}
	return ev.EvictIdle(live, 0)
}

// reapDeadPIDs checks all bound PIDs and unbinds any that are no longer alive.
func (a *Angelus) reapDeadPIDs() {
	pids := a.Registry.AllPIDs()
	for _, pid := range pids {
		if !isAlive(pid) {
			slog.Info("pid died, unbinding", "pid", pid)
			a.Registry.Unbind(pid)
		}
	}
}

// Stop shuts down the angelus.
func (a *Angelus) Stop() {
	if a.Hubs != nil {
		a.Hubs.closeAll()
	}
	if a.cancel != nil {
		a.cancel()
	}
}

// Shutdown drains the registry and stops every figaro. Idempotent.
func (a *Angelus) Shutdown(perAgentGrace time.Duration) {
	if a.Registry == nil {
		a.Stop()
		return
	}
	if a.Registry.IsDraining() {
		return
	}
	a.Registry.SetDraining()

	figaros := a.Registry.All()
	slog.Info("angelus graceful shutdown beginning", "figaros", len(figaros))

	for _, f := range figaros {
		f.Interrupt()
	}

	var wg sync.WaitGroup
	for _, f := range figaros {
		wg.Add(1)
		go func(f figaro.Figaro) {
			defer wg.Done()
			waitForIdle(f, perAgentGrace)
			killed := make(chan struct{})
			go func() {
				if err := a.Registry.Kill(f.ID()); err != nil {
					slog.Error("angelus kill", "id", f.ID(), "err", err)
				}
				close(killed)
			}()
			select {
			case <-killed:
			case <-time.After(perAgentGrace + time.Second):
				slog.Error("angelus kill timed out, abandoning figaro", "id", f.ID())
			}
		}(f)
	}
	wg.Wait()

	slog.Info("angelus graceful shutdown complete")

	a.Stop()

	if a.Backend != nil {
		if err := a.Backend.Close(); err != nil {
			slog.Error("angelus backend close", "err", err)
		}
	}
}

// waitForIdle polls until State is "idle" or deadline.
func waitForIdle(f figaro.Figaro, grace time.Duration) {
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if f.Info().State == "idle" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}
