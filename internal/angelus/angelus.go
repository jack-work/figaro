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

	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/mark"
	"github.com/jack-work/figaro/internal/modelcat"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/peer"
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
	Backend    store.Backend
	SocketPath string
	RuntimeDir string
	StartedAt  time.Time
	// Build is this daemon's VCS revision; a CLI refuses a daemon from another build.
	Build string

	// RemoveAria is the one delete path (agent, sessions, bytes, endpoint);
	// nil disables the TTL sweep's deletions.
	RemoveAria func(id string, recursive bool) error

	// Sessions is daemon-wide, scoped by aria id: killing or hibernating an
	// agent must not take its children's addressability with it.
	Sessions *tool.SessionRegistry

	// Settings nil means every default.
	Settings *config.Loaded

	// UICache is the process-wide composed UI IR cache, one budget and one
	// eviction order for every agent's turn cache and the reader.
	UICache *aria.ComposedCache

	Models *modelcat.Catalog

	Policies *authz.Policies
	Peercred *authz.Peercred
	Peers    peer.Membership

	uiProj Projector

	// Hubs outlive the agents behind them, so reclaiming an agent disconnects nobody.
	Hubs *hubs

	// capShortBy is the last reported shortfall, so a standing condition logs on change.
	capShortBy atomic.Int32

	// quietSweeps and turnlessReclaims are touched only by the sweep goroutine.
	quietSweeps      int
	turnlessReclaims map[string]int

	// residentFor overrides where the latch reads the resident count (tests).
	residentFor idleEvictor

	// lastLibrettoSweep is what the boot reconciliation found, for `doctor mem`.
	lastLibrettoSweep atomic.Pointer[store.LibrettoAudit]

	cancel    context.CancelFunc
	pprofPath string // empty when profiling is not armed
}

type Config struct {
	RuntimeDir string
	Backend    store.Backend
	Settings   *config.Loaded // nil = defaults
	Models     *modelcat.Catalog
}

// New creates an Angelus; set Handlers before Run.
func New(cfg Config) *Angelus {
	a := &Angelus{
		Registry:   NewRegistry(),
		Backend:    cfg.Backend,
		SocketPath: filepath.Join(cfg.RuntimeDir, "angelus.sock"),
		RuntimeDir: cfg.RuntimeDir,
		StartedAt:  time.Now(),
		Sessions:   tool.NewSessionRegistry(tool.DefaultSessionTTL),
		Settings:   cfg.Settings,
		Models:     cfg.Models,
		Hubs:       newHubs(),
	}
	a.uiProj = uiir.New(nil).Naming(a.senderName)
	a.UICache = aria.NewComposedCache(uiBudget(cfg.Settings), a.composeTurns, a.uiLineage)
	return a
}

// An explicit 0 means unbounded; unset means the package default.
func uiWindowMB(settings *config.Loaded) int {
	if mb, set := settings.UIWindowMB(); set {
		return mb
	}
	return aria.DefaultUIWindowMB
}

func uiBudget(settings *config.Loaded) *fwtree.Budget {
	return fwtree.NewBudget(int64(uiWindowMB(settings)) << 20)
}

func (a *Angelus) BindingsPath() string {
	return filepath.Join(a.RuntimeDir, "bindings.json")
}

// Run blocks until ctx is cancelled.
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

	os.Remove(a.SocketPath)

	ln, err := net.Listen("unix", a.SocketPath)
	if err != nil {
		return err
	}

	if err := os.Chmod(a.SocketPath, 0600); err != nil {
		ln.Close()
		return err
	}

	pidPath := filepath.Join(a.RuntimeDir, "angelus.pid")
	os.WriteFile(pidPath, []byte(fmt.Sprintf("%d", os.Getpid())), 0600)
	defer os.Remove(pidPath)

	// The socket is not removed here: `figaro rest` treats its removal as
	// "shutdown complete", and Run returns before the drain does.

	slog.Info("angelus started", "pid", os.Getpid(), "socket", a.SocketPath)
	mark.Mark("boot.ready")

	_ = a.StartPprof(ctx)

	go a.pidMonitor(ctx)
	a.migrateLibrettoRefs()

	err = a.Serve(ctx, ln, a.cliAdmit())
	slog.Info("angelus shutting down")
	return err
}

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
			sweep(ctx, "cache_budgets", a.sweepCacheBudgets)
		case <-evict.C:
			// Reclaim agents before sweeping caches: an aria hibernated on this
			// tick is eligible for eviction on the next.
			sweep(ctx, "hibernate", a.hibernateIdleArias)
			sweep(ctx, "cap_live", a.capLiveArias)
			sweep(ctx, "expire_ttl", a.expireTTL)
			sweep(ctx, "evict_idle", a.evictIdleArias)
			sweep(ctx, "trim_resident", a.trimResident)
			sweep(ctx, "release_memory", a.releaseIdleMemory)
		}
	}
}

func (a *Angelus) migrateLibrettoRefs() {
	rec, ok := a.Backend.(librettoReconciler)
	if !ok || !rec.RefsNeedMigration() {
		return
	}
	go func() {
		audit, err := rec.ReconcileLibrettos()
		if err != nil {
			slog.Warn("libretto refs migration failed", "err", err)
			return
		}
		a.lastLibrettoSweep.Store(&audit)
		slog.Info("libretto backrefs built from the boards",
			"boards", audit.Boards, "librettos", audit.Librettos,
			"corrected", audit.Corrected, "minted", audit.Minted,
			"orphaned", audit.Orphaned, "missing", audit.Missing)
	}()
}

type librettoReconciler interface {
	RefsNeedMigration() bool
	ReconcileLibrettos() (store.LibrettoAudit, error)
}

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

// idleReleaseDue fires once per quiet period and resets on any work.
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

const quietSweepsBeforeRelease = 2

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
	return a.Settings.DormantAfter() // 0 means never
}

const (
	defaultSweepInterval = 2 * time.Minute
	defaultDormantAfter  = 15 * time.Minute
)

// idleEvictor and the sibling interfaces below are optional on the backend
// so a test backend need not own a policy it does not have.
type idleEvictor interface {
	EvictIdle(live map[string]bool, idle time.Duration) int
	Resident() int
}

// budgetSweeper lowers the pressure reads raise; a read never blocks on eviction.
type budgetSweeper interface {
	SweepCacheBudgets() (int, int64)
}

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

type ttlKeeper interface {
	TTLDue(nowMS int64) []store.TTLEntry
	TTLForget(ids ...string)
}

// expireTTL reads only sidecars and opens no node. An expired aria with a
// live agent or a bound shell keeps its deadline until it is dormant. The
// delete is recursive over the presentation hierarchy, so a child promoted
// past an expired parent survives it.
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

func sweep(ctx context.Context, phase string, fn func()) {
	start := time.Now()
	fn()
	figOtel.RecordSweep(ctx, time.Since(start), attribute.String("phase", phase))
}

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
	n := ev.EvictIdle(live, idle)
	figOtel.RecordEvicted(context.Background(), n)
	if n > 0 {
		slog.Info("released idle aria caches", "evicted", n, "live", len(live), "resident", ev.Resident())
	}
	// keep is in sweeps, so the window is dormant_after / sweep_interval.
	if sw, ok := a.Backend.(segmentSweeper); ok {
		if dropped, freed := sw.SweepSegmentCache(segmentCacheKeepSweeps); dropped > 0 {
			slog.Info("released idle segment payloads",
				"blocks", dropped, "freed_bytes", freed)
		}
	}
}

const segmentCacheKeepSweeps = 2

type segmentSweeper interface {
	SweepSegmentCache(keep int64) (dropped int, freed int64)
}

// idleSince is the later of the last turn and the agent's construction: a
// wake by a non-turn method moves only the second, and reading LastActive
// alone reclaims a woken aria on the next tick and restarts the flap.
func idleSince(info figaro.FigaroInfo) time.Time {
	if info.AgentSince.After(info.LastActive) {
		return info.AgentSince
	}
	return info.LastActive
}

func (a *Angelus) hibernateIdleArias() {
	idle := a.dormantAfter()
	if idle <= 0 {
		return // reclamation disabled
	}
	cutoff := time.Now().Add(-idle)

	for _, info := range a.Registry.List() {
		since := idleSince(info)
		if info.State != "idle" || since.After(cutoff) {
			continue
		}
		if err := a.Registry.Hibernate(info.ID); err != nil {
			// Ordinary: the aria took a prompt between decision and teardown.
			slog.Debug("hibernate declined", "aria", info.ID, "err", err)
			continue
		}
		slog.Info("hibernated aria", "aria", info.ID,
			"idle_for", time.Since(since).Round(time.Second),
			"live", a.Registry.FigaroCount())
		a.noteTurnlessReclaim(info)
	}
}

// noteTurnlessReclaim names the method behind a second consecutive wake
// without a turn: something is polling the aria on a clock.
func (a *Angelus) noteTurnlessReclaim(info figaro.FigaroInfo) {
	if info.WokeBy == "" || !info.LastActive.Before(info.AgentSince) {
		delete(a.turnlessReclaims, info.ID)
		return
	}
	if a.turnlessReclaims == nil {
		a.turnlessReclaims = map[string]int{}
	}
	a.turnlessReclaims[info.ID]++
	n := a.turnlessReclaims[info.ID]
	if n < 2 {
		return
	}
	slog.Warn("reclaimed an aria that keeps being woken without a turn; something is polling it",
		"aria", info.ID, "woke_by", info.WokeBy, "consecutive", n,
		"held_for", time.Since(info.AgentSince).Round(time.Second))
}

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
		if info.State == "idle" && idleSince(info).Before(fresh) {
			victims = append(victims, info)
		}
	}
	sort.Slice(victims, func(i, j int) bool {
		return idleSince(victims[i]).Before(idleSince(victims[j]))
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
	// Logged on change, not per sweep: an unmeetable cap is a standing condition.
	shortBy := over - reclaimed
	if shortBy != int(a.capShortBy.Swap(int32(shortBy))) && shortBy > 0 {
		slog.Info("live aria cap not met",
			"cap", max, "live", a.Registry.FigaroCount(), "over_by", shortBy,
			"reason", "remaining arias are active or recently woken")
	}
}

// capFlapGuard exempts a recently woken aria from the cap: restore costs
// O(history), and reclaiming what just paid it is a flap.
const capFlapGuard = time.Minute

func (a *Angelus) maxLiveArias() int {
	if a.Settings == nil {
		return 0
	}
	return a.Settings.MaxLiveArias()
}

type residentTrimmer interface {
	TrimResident(live map[string]bool, keep int) int
	ResidentRows() int
	ResidentIRBytes() int
}

// trimResident shrinks the IR window of every aria with no live agent. The
// byte budget is the store's own; an unset row cap lets the bytes decide.
func (a *Angelus) trimResident() {
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

const trimRowsUnbounded = 1 << 30

// EvictNow drops every dormant aria's caches regardless of idle time or sweep policy.
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

func (a *Angelus) reapDeadPIDs() {
	pids := a.Registry.AllPIDs()
	for _, pid := range pids {
		if !isAlive(pid) {
			slog.Info("pid died, unbinding", "pid", pid)
			a.Registry.Unbind(pid)
		}
	}
}

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

func waitForIdle(f figaro.Figaro, grace time.Duration) {
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if f.Info().State == "idle" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}
