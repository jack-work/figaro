package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"syscall"
	"text/template"
	"time"

	"github.com/jack-work/hush/managed"

	"github.com/jack-work/figaro/internal/decide"
	"github.com/jack-work/figaro/internal/modelcat"
	"github.com/jack-work/figaro/internal/relay"

	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/figaro/wire"
	"github.com/jack-work/figaro/internal/mark"
	figOtel "github.com/jack-work/figaro/internal/otel"
	providerPkg "github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/sdk"
)

const lockHandoff = 10 * time.Second

// lockStore returns the flock handle that admits one angelus per store; closing it
// releases the lock.
func lockStore() (*os.File, bool) {
	dir := boot.StateDir()
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
	c, err := sdk.DialAngelus(transport.UnixEndpoint(boot.AngelusSocketPath()))
	if err != nil {
		return false
	}
	c.Close()
	return true
}

func keepHushAlive(ctx context.Context, h *managed.Hush) {
	interval := boot.HushAgentTTL() / 3
	if interval > 5*time.Minute {
		interval = 5 * time.Minute
	}
	if interval < 30*time.Second {
		interval = 30 * time.Second
	}
	claimed := false
	// Claim only after EnsureReady: a claim written earlier names a socket
	// that may never exist.
	ensure := func(stage string) {
		if err := h.EnsureReady(); err != nil {
			slog.Warn("hush keep-alive: "+stage+" ensure failed", "err", err)
			return
		}
		if claimed {
			return
		}
		if err := boot.ClaimHushAgent(h.Mode(), h.Config().RuntimeDir); err != nil {
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

// applyStoreSettings must run before the backend opens: the store reads its bounds
// at open.
func applyStoreSettings(loaded *config.Loaded) {
	store.SetFormLinger(loaded.ActorLinger())
	store.SetHandleIdle(loaded.HandleIdle())
	store.SetPatchWindow(loaded.FormPatchWindow())
	if n, set := loaded.SegmentCacheBytes(); set {
		store.SetSegmentCacheBudget(n)
	}
}

// applyCacheSettings applies only what the config file says; the store bounds
// itself at construction.
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
		return
	}
	debug.SetMemoryLimit(limit)
	slog.Info("daemon memory limit armed", "soft_limit_bytes", limit)
}

func ariaBackend(loaded *config.Loaded) (store.Backend, error) {
	root := boot.AriaRoot()
	b, err := store.NewXwalBackend(root, loaded.SegmentSize())
	if err != nil {
		return nil, err
	}
	// The one place the trunk capability is decided.
	if err := wire.Install(b.Store(), root, wire.Capabilities{Trunks: loaded.Trunks()}); err != nil {
		return nil, err
	}
	return b, nil
}

func buildDecider(loaded *config.Loaded) (*decide.Engine, error) {
	return decide.New(func(provider string) (decide.Target, error) {
		up, err := relay.Resolve(loaded, provider)
		if err != nil {
			return decide.Target{}, err
		}
		return decide.Target{BaseURL: up.Base(), Auth: up.Credential()}, nil
	}), nil
}

func buildProviderFactory(loaded *config.Loaded, formTmpls *template.Template, backend store.Backend) angelus.ProviderFactory {
	build := buildProvider(loaded, formTmpls, backend)
	return func(providerName string, knobs providerPkg.Knobs) (providerPkg.Provider, error) {
		p, err := build(providerName, knobs)
		if err != nil {
			return nil, err
		}
		registerOnAppend(backend, p)
		return p, nil
	}
}

func buildProvider(loaded *config.Loaded, formTmpls *template.Template, backend store.Backend) angelus.ProviderFactory {
	return func(providerName string, knobs providerPkg.Knobs) (providerPkg.Provider, error) {
		reg := providerPkg.Lookup(providerName)
		if reg == nil {
			return nil, fmt.Errorf("unknown provider: %q", providerName)
		}
		resolver, err := boot.BuildResolver(loaded, providerName)
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
		return p, nil
	}
}

// registerOnAppend translates an entry when it lands; a provider without
// EntryEncoder is left to catch-up.
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

func Run() {
	mark.Init("angelus")
	mark.Mark("boot.begin")
	boot.PromptPassphrase = nil
	loaded, err := boot.LoadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "angelus: %v\n", err)
		os.Exit(1)
	}
	runtimeDir := boot.AngelusRuntimeDir()

	// Lock before the backend opens or the socket binds, so a losing daemon
	// never opens the store or steals the socket.
	lockF, ok := lockStore()
	if !ok {
		slog.Info("another angelus already owns this store; exiting")
		os.Exit(0)
	}
	defer lockF.Close()

	otelShutdown, err := figOtel.Init(context.Background(), boot.StateDir())
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
	a.Build = boot.BuildRevision()
	peer, err := authz.NewPeercred(uint32(os.Getuid()), loaded.Config.Authz.Peercred)
	if err != nil {
		fmt.Fprintf(os.Stderr, "angelus: %v\n", err)
		os.Exit(1)
	}
	a.Peercred = peer
	a.Policies = authz.NewPolicies(peer.Self(), angelus.StoreFacts(backend))
	if err := a.Policies.Reload(loaded.PolicyPath()); err != nil {
		slog.Error("authz: no policy in force; every call that needs one is refused", "err", err)
		fmt.Fprintf(os.Stderr, "angelus: %v; every call that needs a policy is refused until it loads\n", err)
	}
	figOtel.SetMemProbe(func() figOtel.MemSample {
		st := a.MemStatus()
		return figOtel.MemSample{HeapInuse: int64(st.HeapInuseBytes), HeapReleased: int64(st.HeapReleasedBytes),
			ResidentArias: st.ResidentArias, Goroutines: st.Goroutines}
	})

	formTmpls := boot.BuildFormTemplates()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	boot.OnHush(func(h *managed.Hush) { go keepHushAlive(ctx, h) })

	decider, err := buildDecider(loaded)
	if err != nil {
		slog.Warn("decide: no decision model", "err", err)
	}

	build := buildProvider(loaded, formTmpls, backend)
	a.Models = modelcat.New(modelcat.Config{
		Build: func(name string) (providerPkg.Provider, error) { return build(name, providerPkg.Knobs{}) },
		Path:  filepath.Join(boot.CacheDir(), "models.json"),
	})
	go a.Models.Run(ctx)

	handlers := angelus.NewHandlers(angelus.ServerConfig{
		Angelus:            a,
		Config:             loaded,
		ProviderFactory:    buildProviderFactory(loaded, formTmpls, backend),
		AvailableProviders: providerPkg.Names(),
		Ctx:                ctx,
		FormTemplates:      formTmpls,
		Decider:            decider,
	})
	a.Handlers = handlers.Map
	a.RemoveAria = handlers.RemoveAria

	if err := serveWebDoor(ctx, a, loaded); err != nil {
		slog.Error("angelus web door", "err", err)
		fmt.Fprintf(os.Stderr, "angelus: %v\n", err)
		os.Exit(1)
	}
	if err := serveNodeDoor(ctx, a, loaded); err != nil {
		slog.Error("angelus node door", "err", err)
		fmt.Fprintf(os.Stderr, "angelus: %v\n", err)
		os.Exit(1)
	}

	// Rebind shells without restoring their arias; an aria wakes on its next
	// prompt.
	angelus.RestoreBindings(a.Registry, a.BindingsPath(), handlers.RequireAria)

	drained := make(chan struct{})
	go func() {
		defer close(drained)
		<-ctx.Done()
		slog.Info("angelus signal received, draining figaros")
		a.Shutdown(5 * time.Second)
	}()

	err = a.Run(ctx)
	if ctx.Err() != nil {
		// Run returns when the listener closes; the drain seals in-flight
		// turns behind it, and the socket goes only after.
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

func serveWebDoor(ctx context.Context, a *angelus.Angelus, loaded *config.Loaded) error {
	web, ok, err := loaded.WebDoor()
	if err != nil || !ok {
		return err
	}
	ln, err := angelus.ListenUnix(web.Path, web.Mode)
	if err != nil {
		return fmt.Errorf("door.web: %w", err)
	}
	admit, err := webAuthenticator(web.Authn, web.Path, a.SocketPath, loaded.Config.Authz)
	if err != nil {
		ln.Close()
		return fmt.Errorf("door.web: %w", err)
	}
	slog.Info("web door listening", "path", web.Path, "authn", web.Authn, "max_conn_age", web.MaxConnAge.String(), "admits", admit != nil)
	go func() {
		defer os.Remove(web.Path)
		if err := a.ServeWeb(ctx, ln, angelus.Web{Admit: admit, MaxConnAge: web.MaxConnAge}); err != nil {
			slog.Error("web door", "err", err)
		}
	}()
	return nil
}

func webAuthenticator(name, listen, cliDoor string, cfg config.AuthzConfig) (angelus.WebAdmit, error) {
	var b *authz.Bearer
	if cfg.Bearer != nil {
		var err error
		if b, err = authz.NewBearer(*cfg.Bearer, &http.Client{Timeout: 5 * time.Second}, nil); err != nil {
			return nil, err
		}
	}
	switch name {
	case "upstream":
		u, err := authz.NewUpstream(cfg.Upstream, listen, cliDoor, b)
		if err != nil {
			return nil, err
		}
		return u.AdmitRequest, nil
	case "bearer":
		if b == nil {
			return nil, fmt.Errorf("authn = \"bearer\" needs [authz.bearer]")
		}
		return b.AdmitRequest, nil
	}
	return nil, fmt.Errorf("authn %q: want \"upstream\" or \"bearer\"", name)
}
