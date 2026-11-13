package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
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
	"github.com/jack-work/figaro/internal/provider/routed"
	"github.com/jack-work/figaro/internal/relay"
	"github.com/jack-work/figaro/internal/workerpool"

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
	return lockStoreWithin(lockHandoff, os.Stderr)
}

func lockStoreWithin(handoff time.Duration, notice io.Writer) (*os.File, bool) {
	dir := boot.StateDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, false
	}
	f, err := os.OpenFile(boot.StoreLockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false
	}
	deadline := time.Now().Add(handoff)
	said := false
	for {
		if err := boot.TryLockFile(f); err == nil {
			boot.RecordStoreLockOwner(f)
			return f, true
		}
		if incumbentAnswers() || time.Now().After(deadline) {
			f.Close()
			return nil, false
		}
		if !said {
			if who, _, held := boot.StoreLockHolder(); held {
				said = true
				fmt.Fprintf(notice, "angelus: nothing answers on %s and the store lock is held by %s; waiting up to %s for it to be released\n",
					boot.AngelusSocketPath(), who, handoff)
			}
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
	interval := hushKeepAliveInterval(boot.HushAgentTTL())
	claimed := false
	// Claim only after the agent answers: a claim written earlier names a
	// socket that may never exist.
	claim := func() {
		if claimed {
			return
		}
		if err := boot.ClaimHushAgent(h.Mode(), h.Config().RuntimeDir); err != nil {
			slog.Warn("hush keep-alive: could not advertise the agent", "err", err)
			return
		}
		claimed = true
	}
	if err := boot.ReviveHushAgent(h); err != nil {
		slog.Warn("hush keep-alive: initial ensure failed", "err", err)
	} else {
		claim()
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	claimCheck := time.NewTicker(hushClaimCheck)
	defer claimCheck.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-claimCheck.C:
			if _, err := os.Stat(boot.HushClaimPath()); claimed && errors.Is(err, fs.ErrNotExist) {
				claimed = false
				slog.Warn("hush keep-alive: the agent claim was removed; claiming again")
				claim()
			}
		case <-t.C:
			renewed, err := boot.RenewHushAgent(h, 2*interval)
			if err != nil {
				slog.Warn("hush keep-alive: renewal failed", "err", err)
				continue
			}
			if renewed {
				slog.Info("hush keep-alive: replaced the agent before its deadline")
			}
			claim()
		}
	}
}

const hushClaimCheck = 2 * time.Second

func hushKeepAliveInterval(ttl time.Duration) time.Duration {
	return min(max(ttl/3, time.Second), 5*time.Minute)
}

// applyStoreSettings must run before the backend opens: the store reads its bounds
// at open.
func applyStoreSettings(loaded *config.Loaded) {
	store.SetFormLinger(loaded.ActorLinger())
	store.SetHandleIdle(loaded.HandleIdle())
	store.SetPatchWindow(loaded.FormPatchWindow())
	store.SetPatchWindowBytes(loaded.FormPatchWindowBytes())
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
	sweepNodes(loaded, b)
	return b, nil
}

type nodeSweeper interface {
	SweepUnmarkedNodes(remove bool) (store.SweepReport, error)
}

func sweepNodes(loaded *config.Loaded, b nodeSweeper) {
	mode, err := loaded.NodeSweep()
	if err != nil {
		slog.Error("node sweep not run: bad [store] node_sweep", "err", err)
		return
	}
	if mode == config.RetentionOff {
		return
	}
	if _, err := b.SweepUnmarkedNodes(mode == config.RetentionOn); err != nil {
		slog.Error("node sweep failed; serving anyway", "err", err)
	}
}

func modelsForm(backend store.Backend) *store.Form {
	b, ok := backend.(interface{ ModelsForm() (*store.Form, error) })
	if !ok {
		return nil
	}
	f, err := b.ModelsForm()
	if err != nil {
		slog.Error("models: the catalog form will not open; keeping it in memory", "err", err)
		return nil
	}
	if err := modelcat.Import(f, filepath.Join(boot.CacheDir(), "models.json")); err != nil {
		slog.Warn("models: import the old cache", "err", err)
	}
	return f
}

func buildDecider(loaded *config.Loaded) (*decide.Engine, error) {
	return decide.New(func(provider string) (decide.Target, error) {
		up, err := relay.Resolve(loaded, provider)
		if err != nil {
			return decide.Target{}, err
		}
		return decideTarget(up), nil
	}), nil
}

func decideTarget(up relay.Upstream) decide.Target {
	return decide.Target{BaseURL: up.Base(), Auth: up.Credential(), Headers: func(path string) map[string]string {
		return up.Headers(routed.TransportForPath(path))
	}}
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
		func(aria string) providerPkg.Form { return figaro.BoundFormAccessorFor(backend, aria) },
		func(aria string) map[string]providerPkg.Form { return figaro.StudyAccessorsFor(backend, aria) },
		func(aria string) (store.Log[[]json.RawMessage], error) {
			return backend.OpenTranslator(aria, enc.TranslatorChannel())
		},
	))
}

func Run() {
	mark.Init("angelus")
	mark.Mark("boot.begin")
	hushGate := boot.TakeHushGate()
	boot.PromptPassphrase = nil
	if err := boot.MissingConfigDir(os.Getenv); err != nil {
		fmt.Fprintf(os.Stderr, "angelus: %v\n", err)
		os.Exit(1)
	}
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

	figOtel.SetConfiguredLevel(loaded.TelemetryLevel())
	if loaded.PoolDaemon() && figOtel.LogsAtDebug() {
		fmt.Fprintln(os.Stderr, "angelus: FIGARO_LOG_LEVEL asks for debug logs, which carry prompt text; a daemon that leases workers does not start at that level")
		os.Exit(1)
	}
	figOtel.SetOTLPEndpoint(loaded.TelemetryOTLPEndpoint())
	figOtel.SetServiceVersion(boot.ServiceVersion())
	otelShutdown, err := figOtel.Init(context.Background(), loaded.TelemetryDir(boot.StateDir()))
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: otel init: %s\n", err)
	} else {
		defer otelShutdown(context.Background())
	}
	applyDaemonPath(loaded.AngelusPath())

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
		RuntimeDir:      runtimeDir,
		PprofDir:        boot.StateDir(),
		SpoolDir:        filepath.Join(boot.StateDir(), "spool"),
		SuccessionsPath: angelus.SuccessionsPath(boot.StateDir()),
		Backend:         backend,
		Settings:        loaded,
	})
	a.Build = boot.BuildRevision()
	a.Release = boot.BuildRelease()
	if err := a.Spool.Prepare(); err != nil {
		slog.Error("spool: queued prompts from before this start may not be redelivered", "err", err)
	}
	peer, err := authz.NewPeercred(uint32(os.Getuid()), loaded.Config.Authz.Peercred)
	if err != nil {
		fmt.Fprintf(os.Stderr, "angelus: %v\n", err)
		os.Exit(1)
	}
	a.Peercred = peer
	a.Policies = authz.NewPolicies(peer.Self(), angelus.StoreFacts(backend))
	a.Policies.SetPoolDaemon(loaded.PoolDaemon())
	a.Policies.SetBoxes(loaded.DooredBoxes())
	a.Policies.SetBoxesRequiringClaim(loaded.BoxesRequiringClaim())
	a.Policies.SetIssuerScopes(authz.IssuerScopes(loaded.Config.Authz))
	if a.Issuers, err = newIssuers(loaded.Config.Authz.Issuers); err != nil {
		fmt.Fprintf(os.Stderr, "angelus: %v\n", err)
		os.Exit(1)
	}
	if a.APIKeys, err = newAPIKeys(loaded, backend.NodeID()); err != nil {
		fmt.Fprintf(os.Stderr, "angelus: %v\n", err)
		os.Exit(1)
	}
	if err := a.Policies.Reload(loaded.PolicyPath()); err != nil {
		if loaded.PoolDaemon() {
			fmt.Fprintf(os.Stderr, "angelus: %v; a daemon that leases workers does not start without its policy\n", err)
			os.Exit(1)
		}
		slog.Error("authz: no policy in force; every call that needs one is refused", "err", err)
		fmt.Fprintf(os.Stderr, "angelus: %v; every call that needs a policy is refused until it loads\n", err)
	}
	if loaded.PoolDaemon() {
		if a.Workers, err = angelus.OpenWorkerPools(filepath.Join(boot.StateDir(), "worker-pool"), loaded); err != nil {
			fmt.Fprintf(os.Stderr, "angelus: %v; a daemon that leases workers does not start without its ledger\n", err)
			os.Exit(1)
		}
		a.Policies.SetLeases(a.Workers)
		a.Workers.Caller = func(pool config.PoolConfig) (workerpool.Caller, error) {
			up, err := relay.Resolve(loaded, pool.Provider)
			if err != nil {
				return nil, err
			}
			api, err := workerpool.LeaseAPI(pool.Rou2URL, up.Base())
			if err != nil {
				return nil, err
			}
			return workerpool.Rou2{Base: api, Bearer: up.Credential()}, nil
		}
	}
	figOtel.SetMemProbe(func() figOtel.MemSample {
		st := a.MemStatus()
		return figOtel.MemSample{HeapInuse: int64(st.HeapInuseBytes), HeapReleased: int64(st.HeapReleasedBytes),
			ResidentArias: st.ResidentArias, Goroutines: st.Goroutines,
			LiveFigaros: st.LiveArias, NumGC: st.NumGC, GCPauseTotalNs: st.GCPauseTotalNs}
	})

	formTmpls := boot.BuildFormTemplates()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	a.StopSelf = cancel
	boot.OnHush(func(h *managed.Hush) { go keepHushAlive(ctx, h) })

	decider, err := buildDecider(loaded)
	if err != nil {
		slog.Warn("decide: no decision model", "err", err)
	}

	build := buildProvider(loaded, formTmpls, backend)
	a.Models = modelcat.New(modelcat.Config{
		Build:     func(name string) (providerPkg.Provider, error) { return build(name, providerPkg.Knobs{}) },
		Providers: func() []string { return catalogProviders(loaded) },
		Known:     func(name string) bool { return providerPkg.Lookup(name) != nil },
		Poll:      loaded.ModelsPoll(),
		Form:      modelsForm(backend),
	})
	a.Go("model catalog", func() { a.Models.Run(ctx) })
	go warmRoutes(build, catalogProviders(loaded))

	if err := federate(ctx, a, loaded); err != nil {
		slog.Error("angelus peers", "err", err)
		fmt.Fprintf(os.Stderr, "angelus: %v\n", err)
		os.Exit(1)
	}
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
	a.RemoveAriaIf = handlers.RemoveAriaIf
	if loaded.PoolDaemon() {
		handlers.RecoverWorkerPools(ctx)
		go handlers.KeepReleasingWorkers(ctx)
	}

	if !hushGate.Await() {
		slog.Error("angelus exiting", "err", boot.ErrHushGateShut)
		fmt.Fprintf(os.Stderr, "angelus: %v; exiting\n", boot.ErrHushGateShut)
		os.Exit(1)
	}
	if err := serveWebDoor(ctx, a, loaded); err != nil {
		slog.Error("angelus web door", "err", err)
		fmt.Fprintf(os.Stderr, "angelus: %v\n", err)
		os.Exit(1)
	}
	if err := a.OpenBoxDoors(ctx, loaded); err != nil {
		slog.Error("angelus box door", "err", err)
		fmt.Fprintf(os.Stderr, "angelus: %v\n", err)
		os.Exit(1)
	}
	if err := serveNodeDoor(ctx, a, loaded); err != nil {
		slog.Error("angelus node door", "err", err)
		fmt.Fprintf(os.Stderr, "angelus: %v\n", err)
		os.Exit(1)
	}

	handlers.LoadNav()
	angelus.RestoreBindings(a.Registry, a.BindingsPath(), handlers.RequireAria)
	go angelus.PersistBindings(a.Registry, a.BindingsPath())
	a.Go("nav persistence", func() { handlers.PersistNav(ctx.Done()) })
	a.Go("spool redelivery", func() { handlers.Redeliver(ctx) })

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
	admit, err := webAuthenticator(web.Authn, web.Path, a.SocketPath, loaded.Config.Authz, authz.Bearers{Issuers: a.Issuers, Keys: a.APIKeys})
	if err != nil {
		ln.Close()
		return fmt.Errorf("door.web: %w", err)
	}
	door, err := webDoorLimits(loaded, web, admit)
	if err != nil {
		ln.Close()
		return err
	}
	slog.Info("web door listening", "path", web.Path, "authn", web.Authn, "max_conn_age", web.MaxConnAge.String(), "edge_follow_max_age", door.FollowMaxAge.String(), "admits", admit != nil)
	a.Go("web door socket", func() {
		defer os.Remove(web.Path)
		if err := a.ServeWeb(ctx, ln, door); err != nil {
			slog.Error("web door", "err", err)
		}
	})
	return nil
}

func webDoorLimits(loaded *config.Loaded, web config.WebDoor, admit angelus.WebAdmit) (angelus.Web, error) {
	door := angelus.Web{Admit: admit, MaxConnAge: web.MaxConnAge}
	if web.Authn != "upstream" {
		return door, nil
	}
	var err error
	door.FollowMaxAge, err = loaded.EdgeFollowMaxAge()
	return door, err
}

func webAuthenticator(name, listen, cliDoor string, cfg config.AuthzConfig, b authz.Bearers) (angelus.WebAdmit, error) {
	if len(cfg.Issuers) == 0 {
		b.Issuers = nil
	}
	switch name {
	case "upstream":
		u, err := authz.NewUpstream(cfg.Upstream, listen, cliDoor, b.Issuers)
		if err != nil {
			return nil, err
		}
		return u.AdmitRequest, nil
	case "bearer":
		if b.Issuers == nil && b.Keys == nil {
			return nil, fmt.Errorf("authn = \"bearer\" needs [[authz.issuers]] or a [troupe] whose api keys it takes")
		}
		return b.AdmitRequest, nil
	}
	return nil, fmt.Errorf("authn %q: want \"upstream\" or \"bearer\"", name)
}

func warmRoutes(build angelus.ProviderFactory, names []string) {
	for _, name := range names {
		p, err := build(name, providerPkg.Knobs{})
		if err != nil {
			continue
		}
		if w, ok := p.(interface{ Warm() }); ok {
			w.Warm()
		}
	}
}

func catalogProviders(loaded *config.Loaded) []string {
	if names := loaded.ListProviders(); len(names) > 0 {
		return names
	}
	return providerPkg.Names()
}
