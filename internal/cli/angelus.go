package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/config"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/sdk"
)

// lockHandoff bounds the wait for a dying incumbent to release the store.
const lockHandoff = 10 * time.Second

// lockStore takes the exclusive flock that admits one angelus per store, and
// keeps the handle: closing it releases the lock. Retries until lockHandoff,
// or until a live incumbent answers the socket.
func lockStore() (*os.File, bool) {
	// Keep the daemon lock outside the XWAL tree.
	dir := stateDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, false
	}
	f, err := os.OpenFile(filepath.Join(dir, ".daemon.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false
	}
	deadline := time.Now().Add(lockHandoff)
	for {
		if err := tryLockFile(f); err == nil {
			return f, true
		}
		if incumbentAnswers() || time.Now().After(deadline) {
			f.Close()
			return nil, false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// incumbentAnswers reports whether a live daemon is serving the socket.
func incumbentAnswers() bool {
	cli, err := sdk.DialAngelus(transport.UnixEndpoint(angelusSocketPath()))
	if err != nil {
		return false
	}
	cli.Close()
	return true
}

// runAngelus runs the supervisor side of the binary.
// keepHushAlive pings the embedded hush agent on an interval and respawns it
// if it has died (EnsureReady), so the token-refresh machinery survives for
// the whole daemon session. Interval is well under the agent TTL so a dead
// agent is revived promptly. Errors are logged, never fatal.
func keepHushAlive(ctx context.Context) {
	interval := hushAgentTTL() / 3
	if interval > 5*time.Minute {
		interval = 5 * time.Minute
	}
	if interval < 30*time.Second {
		interval = 30 * time.Second
	}
	h := mustHush()
	if err := h.EnsureReady(); err != nil {
		slog.Warn("hush keep-alive: initial ensure failed", "err", err)
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := h.EnsureReady(); err != nil {
				slog.Warn("hush keep-alive: ensure failed", "err", err)
			}
		}
	}
}

// The soft ceiling is `[memory] soft_limit_mb`, default 2048. Go collects
// harder as it approaches instead of growing to meet whatever the last big
// sweep asked for, which makes the ceiling a licence as much as a limit: a
// high one leaves the runtime no reason to give memory back. It is a
// backstop, not the fix -- idle-aria eviction is the fix.

// applyStoreSettings hands the store its clocks and bounds BEFORE the backend
// opens: the store reads its idle window at open, and a form built earlier
// would keep the old linger for the daemon's life.
func applyStoreSettings(loaded *config.Loaded) {
	store.SetFormLinger(loaded.ActorLinger())
	store.SetHandleIdle(loaded.HandleIdle())
	store.SetPatchWindow(loaded.FormPatchWindow())
	if n, set := loaded.SegmentCacheBytes(); set {
		store.SetSegmentCacheBudget(n)
	}
}

// applyCacheSettings TUNES the per-aria caches: the store bounds itself at
// construction (store.DefaultIRBudgetBytes and friends), and this applies only
// what the config file actually says. A knob nobody set is left alone, so an
// omission here can no longer make a daemon unbounded -- which it could until
// 2026-08-19, when these three calls WERE the residency policy and every other
// builder of a backend (doctor, tests, embeddings) got 0/0.
func applyCacheSettings(loaded *config.Loaded, backend any) bool {
	w, ok := backend.(interface {
		SetIRWindow(int)
		SetIRBudget(int)
		SetTranslationBudget(int)
	})
	if !ok {
		return false
	}
	if n := loaded.IRWindow(); n > 0 {
		w.SetIRWindow(n)
	}
	if n, set := loaded.IRWindowBytes(); set {
		w.SetIRBudget(n)
	}
	if n, set := loaded.TranslationWindowBytes(); set {
		w.SetTranslationBudget(n)
	}
	return true
}

func armMemoryLimit(loaded *config.Loaded) {
	if os.Getenv("GOMEMLIMIT") != "" {
		return
	}
	limit := loaded.SoftLimitBytes()
	if limit <= 0 {
		return // configured off
	}
	debug.SetMemoryLimit(limit)
	slog.Info("daemon memory limit armed", "soft_limit_bytes", limit)
}

func runAngelus() {
	loaded := mustLoadConfig()
	// BEFORE the lock, the store, or the socket: a policy this binary cannot
	// build is a refusal to start, not a warning. The alternative -- which
	// this daemon shipped until 2026-08-25 -- was to fall back to allow-all,
	// so a config naming an unimplemented policy produced a daemon that gated
	// nothing while reading as locked down.
	if err := loaded.ValidateAuthz(); err != nil {
		fmt.Fprintf(stderrw, "%s\n", err)
		os.Exit(1)
	}
	runtimeDir := angelusRuntimeDir()

	// One angelus per store. A second daemon (e.g. from an ensureAngelus
	// startup race) fails the lock and exits cleanly; the client then connects
	// to the incumbent. This must happen BEFORE the backend opens and before
	// the socket is bound, so a loser never opens the store or steals the
	// live socket.
	lockF, ok := lockStore()
	if !ok {
		slog.Info("another angelus already owns this store; exiting")
		os.Exit(0)
	}
	defer lockF.Close()

	otelShutdown, err := figOtel.Init(context.Background(), stateDir())
	if err != nil {
		fmt.Fprintf(stderrw, "warning: otel init: %s\n", err)
	} else {
		defer otelShutdown(context.Background())
	}

	applyStoreSettings(loaded)

	backend, err := ariaBackend(loaded)
	if err != nil {
		slog.Error("angelus aria backend", "err", err)
		fmt.Fprintf(stderrw, "angelus: aria backend: %v\n", err)
		os.Exit(1)
	}

	armMemoryLimit(loaded)

	applyCacheSettings(loaded, backend)

	a := angelus.New(angelus.Config{
		RuntimeDir: runtimeDir,
		Backend:    backend,
		Settings:   loaded,
	})
	a.Build = buildRevision()

	formTmpls := buildFormTemplates()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	handlers := angelus.NewHandlers(angelus.ServerConfig{
		Angelus:            a,
		Config:             loaded,
		ProviderFactory:    buildProviderFactory(loaded, formTmpls, backend),
		AvailableProviders: KnownProviders(),
		Ctx:                ctx,
		FormTemplates:      formTmpls,
	})
	a.Handlers = handlers.Map
	// The TTL sweep deletes through the same four steps `figaro kill` does.
	a.RemoveAria = handlers.RemoveAria

	// Keep the embedded hush agent alive for the daemon's life. The agent
	// self-terminates after its TTL, and the daemon (unlike the CLI) issues no
	// activity to respawn it: so without this, a turn running past the TTL
	// loses its credential ("No provider connected") mid-session. This is the
	// primary fix for the long-autonomous-session credential loss.
	go keepHushAlive(ctx)

	// Rebind surviving shells WITHOUT restoring their arias. A daemon restart
	// used to wake every aria that had a live terminal, which on a busy
	// machine is most of them: minutes of restore and gigabytes resident
	// before anyone had asked for anything. The binding alone is what a shell
	// needs; the aria wakes on its next prompt.
	angelus.RestoreBindings(a.Registry, a.BindingsPath(), handlers.OpenEndpoint)

	drained := make(chan struct{})
	go func() {
		defer close(drained)
		<-ctx.Done()
		slog.Info("angelus signal received, draining figaros")
		a.Shutdown(5 * time.Second)
	}()

	err = a.Run(ctx)
	if ctx.Err() != nil {
		// Run returns the instant the listener closes; the drain (seal
		// in-flight turns, close backend) runs behind it. Exiting before it
		// finishes would lose every active turn: wait, bounded. The socket
		// is removed only after the drain so `figaro rest` reports success
		// when turns are actually sealed.
		select {
		case <-drained:
		case <-time.After(60 * time.Second):
			slog.Error("angelus drain deadline exceeded, exiting with turns unsealed")
		}
	}
	os.Remove(a.SocketPath)
	if err != nil {
		slog.Error("angelus run", "err", err)
		fmt.Fprintf(stderrw, "angelus: %v\n", err)
		os.Exit(1)
	}
}
