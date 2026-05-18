package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jack-work/figaro/internal/angelus"
	figOtel "github.com/jack-work/figaro/internal/otel"
)

// runAngelus runs the supervisor side of the binary.
func runAngelus() {
	loaded := mustLoadConfig()
	runtimeDir := angelusRuntimeDir()

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

