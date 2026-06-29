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
	// The lock lives in the state dir, NOT inside arias/ — a lock file inside
	// arias/ would make backupLegacyAriaDir see a "non-empty" store and move it
	// aside before the real store opens.
	dir := stateDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, false
	}
	f, err := os.OpenFile(filepath.Join(dir, ".daemon.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, false
	}
	return f, true
}

// runAngelus runs the supervisor side of the binary.
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
		AvailableProviders:  KnownProviders,
		Ctx:                 ctx,
		ChalkboardTemplates: cbTmpls,
	})
	a.Handlers = handlers.Map

	angelus.RestoreBindings(a.Registry, a.BindingsPath(), func(ariaID string) error {
		_, err := handlers.Restore(ctx, ariaID)
		return err
	})

	go func() {
		<-ctx.Done()
		slog.Info("angelus signal received, draining figaros")
		a.Shutdown(5 * time.Second)
	}()

	if err := a.Run(ctx); err != nil {
		slog.Error("angelus run", "err", err)
		fmt.Fprintf(os.Stderr, "angelus: %v\n", err)
		os.Exit(1)
	}
}
