package angelus

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/figaro"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
	"github.com/jack-work/jkrpc"
)

// Angelus is the figaro supervisor.
type Angelus struct {
	Registry   *Registry
	Handlers   map[string]jkrpc.HandlerFunc // set before Run()
	Backend    store.Backend                // aria persistence (nil = ephemeral-only)
	SocketPath string
	RuntimeDir string
	StartedAt  time.Time
	// Build is this daemon's VCS revision, reported over angelus.status so a
	// CLI can refuse to speak to a daemon from a different build.
	Build string

	// Sessions is the daemon-wide registry of backgrounded exec sessions,
	// keyed by aria id as scope. Deliberately not per-agent: an agent that
	// is killed or hibernated must not take its children's addressability
	// with it. See tool.WithSessions.
	Sessions *tool.SessionRegistry

	// Settings is the loaded config, read for reclamation policy. nil is
	// legal and means every default.
	Settings *config.Loaded

	// Hubs is the set of aria endpoints. Each outlives the agent behind it,
	// so reclaiming an agent does not disconnect anybody. See ariaHub.
	Hubs *hubs

	// capShortBy is the last reported shortfall against max_live_arias, so a
	// standing condition is logged on change rather than on every sweep.
	capShortBy atomic.Int32

	listener  net.Listener
	cancel    context.CancelFunc
	pprofPath string // set by StartPprof; empty when profiling is not armed
}

// Config holds the settings for creating an Angelus.
type Config struct {
	RuntimeDir string         // e.g. $XDG_RUNTIME_DIR/figaro
	Backend    store.Backend  // aria persistence (nil = ephemeral-only)
	Settings   *config.Loaded // reclamation policy; nil = defaults
}

// New creates an Angelus. Call Run() to start it.
// Set a.Handlers before calling Run() to enable JSON-RPC.
//
// The backend (XwalBackend) owns each aria's shared log instance and
// closes it on Fork/Remove/Close, so there is no separate log cache:
// Open returns the same memoized instance to the live agent and to
// concurrent aria.read RPCs.
func New(cfg Config) *Angelus {
	a := &Angelus{
		Registry:   NewRegistry(),
		Backend:    cfg.Backend,
		SocketPath: filepath.Join(cfg.RuntimeDir, "angelus.sock"),
		RuntimeDir: cfg.RuntimeDir,
		StartedAt:  time.Now(), // set-once at construction; read concurrently (Uptime)
		Sessions:   tool.NewSessionRegistry(tool.DefaultSessionTTL),
		Settings:   cfg.Settings,
		Hubs:       newHubs(),
	}
	return a
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
		case <-evict.C:
			// Order matters: reclaim agents first, then sweep caches. An
			// aria hibernated on this tick is no longer live, so its 12-14 MB
			// of decoded IR becomes eligible on the NEXT tick rather than
			// waiting a whole extra cycle to be noticed.
			a.hibernateIdleArias()
			a.capLiveArias()
			a.evictIdleArias()
			a.trimResident()
		}
	}
}

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
// rather than added to store.Backend so an ephemeral or test backend does
// not have to implement a cache policy it does not have.
type idleEvictor interface {
	EvictIdle(live map[string]bool, idle time.Duration) int
	Resident() int
}

// evictIdleArias releases the cached IR, translations, board and metadata of
// every aria with no live agent that nobody has touched recently.
//
// An aria with an agent is NEVER evicted: the backend shares one cachedLog
// per (aria, channel) so a reader sees the writer's appends, and dropping it
// mid-life would hand the next reader a second instance built from disk.
// Everything evicted is rebuilt from the store on the next read.
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
}

// hibernateIdleArias reclaims the agent of every aria that has been idle
// longer than the configured window.
//
// The predicate is only what it needs to be:
//
//	state == "idle" && now - LastActive > dormantAfter
//
// Notably absent: bound pids, attached clients, and running background
// sessions. Each of those used to be a reason to refuse, and each would have
// made hibernation impossible for exactly the arias that cost the most — a
// terminal left open all afternoon is the common case. They are gone because
// what they protected moved: bindings survive Hibernate, clients hang off the
// hub rather than the agent, and sessions live on the daemon.
//
// LastActive is the key, not "time since the sweep last looked": restore is
// O(history), so an aria woken a moment ago must not be reclaimed again on
// the next tick. That is the flap the memo warned about.
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
//
// It is a SOFT cap and deliberately so: an aria mid-turn counts toward it and
// is skipped, because hitting a number is never worth killing a turn. When the
// cap cannot be met it says so once per sweep, so a value set below the
// working set is visible rather than silently burning restores.
//
// LastActive is the LRU key, which Registry.List already carries. It is also
// the flap guard: an aria woken moments ago is exempt, since restore is
// O(history) and evicting the thing that just paid for itself is how a cap
// becomes more expensive than the memory it saves.
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

// irWindow is the resident decoded-IR row cap per aria.
func (a *Angelus) irWindow() int {
	if a.Settings == nil {
		return 0
	}
	return a.Settings.IRWindow()
}

// irBudget is the resident decoded-IR byte budget per aria.
func (a *Angelus) irBudget() int {
	if a.Settings == nil {
		return 0
	}
	return a.Settings.IRWindowBytes()
}

// residentTrimmer is the backend's half of the windowing contract, an optional
// interface for the same reason idleEvictor is one: a test or ephemeral
// backend has no window to trim.
type residentTrimmer interface {
	TrimResident(live map[string]bool, keep int) int
	ResidentRows() int
	ResidentIRBytes() int
}

// trimResident shrinks the IR window of every aria with no live agent.
//
// The window already bounds itself on append, so this is not what keeps a busy
// aria in check — it is the lifecycle half: an aria that has just been
// reclaimed is holding a full window it will not read again until someone
// wakes it, and only the daemon knows that transition happened.
func (a *Angelus) trimResident() {
	keep := a.irWindow()
	if keep <= 0 && a.irBudget() <= 0 {
		return
	}
	if keep <= 0 {
		// Byte-budgeted only: pass a row cap the budget will bind before, so
		// the trim is decided by bytes rather than by an accidental row count.
		keep = trimRowsUnbounded
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
//
// It deliberately bypasses the policy rather than reusing it: the policy
// answers "has nobody wanted this for a while", and this answers "reclaim
// what is reclaimable, now". A measurement that had to wait out the ticker
// would be measuring the ticker.
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
