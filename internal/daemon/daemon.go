// Package daemon is the angelus entry: it owns the store and the provider
// wiring, and borrows configuration and credentials from cli.
package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"syscall"
	"text/template"
	"time"

	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/cli"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/figaro/wire"
	"github.com/jack-work/figaro/internal/mark"
	figOtel "github.com/jack-work/figaro/internal/otel"
	providerPkg "github.com/jack-work/figaro/internal/provider"
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
	dir := cli.StateDir()
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

func incumbentAnswers() bool {
	c, err := sdk.DialAngelus(transport.UnixEndpoint(cli.AngelusSocketPath()))
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// keepHushAlive pings the embedded hush agent on an interval and respawns it
// if it has died, so the token-refresh machinery outlives the agent TTL for
// the whole daemon session. Errors are logged, never fatal.
func keepHushAlive(ctx context.Context) {
	interval := cli.HushAgentTTL() / 3
	if interval > 5*time.Minute {
		interval = 5 * time.Minute
	}
	if interval < 30*time.Second {
		interval = 30 * time.Second
	}
	h := cli.MustHush()
	claimed := false
	// Claim only an agent we have watched come up: the claim is what
	// `figaro stop` acts on, and a claim written before there is anything
	// to claim would name a socket that never existed.
	ensure := func(stage string) {
		if err := h.EnsureReady(); err != nil {
			slog.Warn("hush keep-alive: "+stage+" ensure failed", "err", err)
			return
		}
		if claimed {
			return
		}
		if err := cli.ClaimHushAgent(h.Mode(), h.Config().RuntimeDir); err != nil {
			slog.Warn("hush keep-alive: could not advertise the agent", "err", err)
			return
		}
		claimed = true
	}
	ensure("initial")
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			ensure("periodic")
		}
	}
}

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
// construction, and this applies only what the config file actually says, so
// an omission here cannot make a daemon unbounded.
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

// The soft ceiling is `[memory] soft_limit_mb`. Go collects harder as it
// approaches instead of growing to meet whatever the last big sweep asked
// for, which makes the ceiling a licence as much as a limit: a high one
// leaves the runtime no reason to give memory back. A backstop, not the fix;
// idle-aria eviction is the fix.
func armMemoryLimit(loaded *config.Loaded) {
	if os.Getenv("GOMEMLIMIT") != "" {
		return
	}
	limit := loaded.SoftLimitBytes()
	if limit <= 0 {
		return
	}
	debug.SetMemoryLimit(limit)
	slog.Info("daemon memory limit armed", "soft_limit_bytes", limit)
}

// ariaBackend constructs the XWAL aria tree under the configured state root,
// sized by `[store] segment_size` (config owns the default and its floor).
func ariaBackend(loaded *config.Loaded) (store.Backend, error) {
	root := cli.AriaRoot()
	b, err := store.NewXwalBackend(root, loaded.SegmentSize())
	if err != nil {
		return nil, err
	}
	// The one place the trunk capability is decided. With it off, nothing
	// constructs a trunk pstate and the hierarchy is the fork topology.
	if err := wire.Install(b.Store(), root, wire.Capabilities{Trunks: loaded.Trunks()}); err != nil {
		return nil, err
	}
	return b, nil
}

// buildProviderFactory wires per-aria provider construction via the
// registry. No provider-specific switches.
func buildProviderFactory(loaded *config.Loaded, formTmpls *template.Template, backend store.Backend) angelus.ProviderFactory {
	return func(providerName string, knobs providerPkg.Knobs) (providerPkg.Provider, error) {
		reg := providerPkg.Lookup(providerName)
		if reg == nil {
			return nil, fmt.Errorf("unknown provider: %q", providerName)
		}
		resolver, err := cli.BuildResolver(loaded, providerName)
		if err != nil {
			return nil, err
		}
		cacheOpen := func(aria string) (store.Log[[]json.RawMessage], error) {
			if backend == nil {
				return nil, fmt.Errorf("no backend")
			}
			return backend.OpenTranslator(aria, providerName)
		}
		p, err := reg.Build(providerPkg.BuildContext{
			Loaded:    loaded,
			Knobs:     knobs,
			Resolver:  resolver,
			Templates: formTmpls,
			CacheOpen: cacheOpen,
			Backend:   backend,
		})
		if err != nil {
			return nil, err
		}
		registerOnAppend(backend, p)
		return p, nil
	}
}

// registerOnAppend puts a provider's encoder behind the fig IR write path, so
// an entry is translated WHEN IT LANDS -- by the only site that holds its
// repaired payload -- instead of by the next send's catch-up.
//
// A provider that does not implement EntryEncoder is simply not registered,
// and its channel is filled by the catch-up exactly as before. That is the
// whole fallback: no flag, no mode, and no second code path.
func registerOnAppend(backend store.Backend, p providerPkg.Provider) {
	setter, ok := backend.(interface {
		AddTranslatorEncoders(...store.TranslatorEncoder)
	})
	if !ok {
		return
	}
	enc, ok := p.(providerPkg.EntryEncoder)
	if !ok || enc.TranslatorChannel() == "" {
		return
	}
	setter.AddTranslatorEncoders(providerPkg.NewOnAppend(
		enc.TranslatorChannel(),
		enc.EncodeMessage,
		enc.Fingerprint,
		func(aria string) providerPkg.Form { return figaro.BoardAccessorFor(backend, aria) },
		func(aria string) map[string]providerPkg.Form { return figaro.StudyAccessorsFor(backend, aria) },
		func(aria string) (store.Log[[]json.RawMessage], error) {
			return backend.OpenTranslator(aria, enc.TranslatorChannel())
		},
	))
}

// Run is the supervisor side of the binary.
func Run() {
	defer cli.RecoverExit()
	mark.Init("angelus")
	loaded := cli.MustLoadConfig()
	runtimeDir := cli.AngelusRuntimeDir()

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

	otelShutdown, err := figOtel.Init(context.Background(), cli.StateDir())
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: otel init: %s\n", err)
	} else {
		defer otelShutdown(context.Background())
	}

	applyStoreSettings(loaded)

	backend, err := ariaBackend(loaded)
	if err != nil {
		slog.Error("angelus aria backend", "err", err)
		fmt.Fprintf(os.Stderr, "angelus: aria backend: %v\n", err)
		os.Exit(1)
	}

	armMemoryLimit(loaded)
	applyCacheSettings(loaded, backend)

	a := angelus.New(angelus.Config{
		RuntimeDir: runtimeDir,
		Backend:    backend,
		Settings:   loaded,
	})
	a.Build = cli.BuildRevision()

	formTmpls := cli.BuildFormTemplates()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	handlers := angelus.NewHandlers(angelus.ServerConfig{
		Angelus:            a,
		Config:             loaded,
		ProviderFactory:    buildProviderFactory(loaded, formTmpls, backend),
		AvailableProviders: cli.KnownProviders(),
		Ctx:                ctx,
		FormTemplates:      formTmpls,
	})
	a.Handlers = handlers.Map
	// The TTL sweep deletes through the same four steps `figaro kill` does.
	a.RemoveAria = handlers.RemoveAria

	// The agent self-terminates after its TTL, and the daemon (unlike the
	// CLI) issues no activity to respawn it: without this, a turn running
	// past the TTL loses its credential mid-session.
	go keepHushAlive(ctx)

	// Rebind surviving shells WITHOUT restoring their arias: the binding
	// alone is what a shell needs; the aria wakes on its next prompt.
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
