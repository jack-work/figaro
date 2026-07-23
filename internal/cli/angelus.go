package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/jack-work/figaro/internal/angelus"
	figOtel "github.com/jack-work/figaro/internal/otel"
)

// lockStore takes a non-blocking exclusive flock on the aria store so only one
// angelus ever has it open. Returns the open handle (keep it alive for the
// daemon's lifetime — closing it releases the lock) and whether it was
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

	backend, err := ariaBackend()
	if err != nil {
		slog.Error("angelus aria backend", "err", err)
		fmt.Fprintf(os.Stderr, "angelus: aria backend: %v\n", err)
		os.Exit(1)
	}

	a := angelus.New(angelus.Config{
		RuntimeDir: runtimeDir,
		Backend:    backend,
	})

	cbTmpls := buildChalkboard()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	handlers := angelus.NewHandlers(angelus.ServerConfig{
		Angelus:             a,
		Config:              loaded,
		ProviderFactory:     buildProviderFactory(loaded, cbTmpls, backend),
		AvailableProviders:  KnownProviders(),
		Ctx:                 ctx,
		ChalkboardTemplates: cbTmpls,
	})
	a.Handlers = handlers.Map

	// Keep the embedded hush agent alive for the daemon's life. The agent
	// self-terminates after its TTL, and the daemon (unlike the CLI) issues no
	// activity to respawn it — so without this, a turn running past the TTL
	// loses its credential ("No provider connected") mid-session. This is the
	// primary fix for the long-autonomous-session credential loss.
	go keepHushAlive(ctx)

	angelus.RestoreBindings(a.Registry, a.BindingsPath(), func(ariaID string) error {
		_, err := handlers.Restore(ctx, ariaID)
		return err
	})

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
		// finishes would lose every active turn — wait, bounded. The socket
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
