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

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
	figOtel "github.com/jack-work/figaro/internal/otel"
)

// lockStore takes a non-blocking exclusive flock on the aria store so only one
// angelus ever has it open. Returns the open handle (keep it alive for the
// daemon's lifetime: closing it releases the lock) and whether it was
// acquired. A crashed holder's lock is released by the kernel, so the next
// daemon can take over.
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
	if err := tryLockFile(f); err != nil {
		f.Close()
		return nil, false
	}
	return f, true
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
//
// GOMEMLIMIT in the environment always wins: Go reads it at startup, and a
// user who has set one has an opinion worth more than a default.

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
		fmt.Fprintf(os.Stderr, "warning: otel init: %s\n", err)
	} else {
		defer otelShutdown(context.Background())
	}

	backend, err := ariaBackend(loaded)
	if err != nil {
		slog.Error("angelus aria backend", "err", err)
		fmt.Fprintf(os.Stderr, "angelus: aria backend: %v\n", err)
		os.Exit(1)
	}

	armMemoryLimit(loaded)

	// The window has to be set before any aria is opened, or the first handles
	// built are unbounded for the daemon's whole life. Optional interface, for
	// the same reason the other cache policies are: a backend without a window
	// should not have to pretend it has one.
	// Before any form opens, for the same reason as the IR window: a form
	// built earlier would keep the old value for the daemon's whole life.
	store.SetFormLinger(loaded.ActorLinger())

	if w, ok := backend.(interface {
		SetIRWindow(int)
		SetIRBudget(int)
	}); ok {
		w.SetIRWindow(loaded.IRWindow())
		w.SetIRBudget(loaded.IRWindowBytes())
	}

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
		fmt.Fprintf(os.Stderr, "angelus: %v\n", err)
		os.Exit(1)
	}
}
