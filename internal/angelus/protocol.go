package angelus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"text/template"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/jsonrpc"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/outfit"
	providerPkg "github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
)

// ProviderFactory creates a provider from a name and model.
type ProviderFactory func(providerName, model string) (providerPkg.Provider, error)

// ServerConfig holds dependencies for the angelus JSON-RPC handlers.
type ServerConfig struct {
	Angelus         *Angelus
	Config          *config.Loaded
	ProviderFactory ProviderFactory
	Ctx             context.Context

	// ChalkboardTemplates renders Patches as system reminders. nil = skip.
	ChalkboardTemplates *template.Template
}

// Handlers wraps the angelus JSON-RPC handler map.
type Handlers struct {
	Map map[string]jsonrpc.HandlerFunc
	h   *handlers
}

// NewHandlers creates the handler set for the angelus socket.
func NewHandlers(cfg ServerConfig) *Handlers {
	h := &handlers{
		angelus:   cfg.Angelus,
		config:    cfg.Config,
		factory:   cfg.ProviderFactory,
		ctx:       cfg.Ctx,
		cbTmpls:   cfg.ChalkboardTemplates,
		outfitter: outfit.New(cfg.Config.ConfigDir),
	}
	return &Handlers{
		Map: map[string]jsonrpc.HandlerFunc{
			rpc.MethodCreate:       h.create,
			rpc.MethodKill:         h.kill,
			rpc.MethodList:         h.list,
			rpc.MethodAttach:       h.attach,
			rpc.MethodBind:         h.bind,
			rpc.MethodResolve:      h.resolve,
			rpc.MethodUnbind:       h.unbind,
			rpc.MethodStatus:       h.status,
			rpc.MethodSaveBindings: h.saveBindings,
			rpc.MethodAriaRead:     h.ariaRead,
		},
		h: h,
	}
}

// Restore lazily re-creates the agent for ariaID.
func (hs *Handlers) Restore(ctx context.Context, ariaID string) (figaro.Figaro, error) {
	return hs.h.restoreByID(ctx, ariaID)
}

type handlers struct {
	angelus   *Angelus
	config    *config.Loaded
	factory   ProviderFactory
	ctx       context.Context
	cbTmpls   *template.Template
	outfitter *outfit.Outfitter
}

// openAriaChalkboard opens the chalkboard for an aria. nil on failure.
func (h *handlers) openAriaChalkboard(ariaID string) *chalkboard.State {
	if h.cbTmpls == nil {
		return nil
	}
	fb, ok := h.angelus.Backend.(interface{ Dir() string })
	if !ok {
		return nil
	}
	path := filepath.Join(fb.Dir(), ariaID, "chalkboard.json")
	st, err := chalkboard.Open(path)
	if err != nil {
		slog.Warn("chalkboard open (disabled for aria)", "path", path, "err", err)
		return nil
	}
	return st
}

// fillFromChalkboard reads chalkboard.json and fills Provider/Model.
func (h *handlers) fillFromChalkboard(ariaID string, entry *rpc.FigaroInfoResponse) {
	fb, ok := h.angelus.Backend.(interface{ Dir() string })
	if !ok {
		return
	}
	data, err := os.ReadFile(filepath.Join(fb.Dir(), ariaID, "chalkboard.json"))
	if err != nil {
		return
	}
	var snap map[string]json.RawMessage
	if json.Unmarshal(data, &snap) != nil {
		return
	}
	get := func(key string) string {
		raw, ok := snap[key]
		if !ok {
			return ""
		}
		var s string
		_ = json.Unmarshal(raw, &s)
		return s
	}
	if entry.Provider == "" {
		entry.Provider = get("system.provider")
	}
	if entry.Model == "" {
		entry.Model = get("system.model")
	}
}

// fillFromMetaSnapshot reads derived/meta.json and fills any fields
// the dormant entry is still missing. The meta derivation runs on
// every turn for live arias, so this is the freshest accurate view
// for figaros that have since gone dormant. Silent on missing file:
// older arias predate the derivation.
func (h *handlers) fillFromMetaSnapshot(ariaID string, entry *rpc.FigaroInfoResponse) {
	fb, ok := h.angelus.Backend.(interface{ Dir() string })
	if !ok {
		return
	}
	data, err := os.ReadFile(filepath.Join(fb.Dir(), ariaID, "derived", "meta.json"))
	if err != nil {
		return
	}
	var snap figaro.MetaSnapshot
	if json.Unmarshal(data, &snap) != nil {
		return
	}
	if entry.Provider == "" {
		entry.Provider = snap.Provider
	}
	if entry.Model == "" {
		entry.Model = snap.Model
	}
	if entry.MessageCount == 0 {
		entry.MessageCount = snap.MessageCount
	}
	if entry.TokensIn == 0 {
		entry.TokensIn = snap.TokensIn
	}
	if entry.TokensOut == 0 {
		entry.TokensOut = snap.TokensOut
	}
	if entry.CacheReadTokens == 0 {
		entry.CacheReadTokens = snap.CacheReadTokens
	}
	if entry.CacheWriteTokens == 0 {
		entry.CacheWriteTokens = snap.CacheWriteTokens
	}
	if entry.ContextTokens == 0 {
		entry.ContextTokens = snap.ContextTokens
		entry.ContextExact = snap.ContextExact
	}
}

// openAriaTranslation opens the per-aria translation cache. nil on failure.
func (h *handlers) openAriaTranslation(ariaID, providerName string) store.Log[[]json.RawMessage] {
	if h.angelus.Backend == nil {
		return nil
	}
	stream, err := h.angelus.Backend.OpenTranslation(ariaID, providerName)
	if err != nil {
		slog.Warn("translator stream open (cache disabled for aria)", "aria", ariaID, "provider", providerName, "err", err)
		return nil
	}
	return stream
}

func (h *handlers) create(ctx context.Context, params json.RawMessage) (interface{}, error) {
	_, span := figOtel.Start(ctx, "angelus.create")
	defer span.End()

	var req rpc.CreateRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}

	// Resolve loadout -> chalkboard patch.
	base, err := h.outfitter.Load(req.Loadout)
	if err != nil {
		return nil, fmt.Errorf("create: load loadout %q: %w", req.Loadout, err)
	}
	if req.Patch != nil {
		if base.Set == nil {
			base.Set = map[string]json.RawMessage{}
		}
		for k, v := range req.Patch.Set {
			base.Set[k] = v
		}
		base.Remove = append(base.Remove, req.Patch.Remove...)
	}

	provName := patchString(base, "system.provider")
	model := patchString(base, "system.model")

	span.SetAttributes(
		attribute.String("figaro.loadout", req.Loadout),
		attribute.String("figaro.provider", provName),
		attribute.String("figaro.model", model),
	)

	prov, err := h.factory(provName, model)
	if err != nil {
		return nil, fmt.Errorf("create provider %q: %w", provName, err)
	}

	// Resolve aria id.
	var id string
	if req.ID != "" {
		if err := rpc.ValidateAriaID(req.ID); err != nil {
			return nil, err
		}
		if h.angelus.Registry.Get(req.ID) != nil {
			return nil, fmt.Errorf("aria %q is already live", req.ID)
		}
		if !req.Ephemeral && h.angelus.Backend != nil {
			if meta, _ := h.angelus.Backend.Meta(req.ID); meta != nil {
				return nil, fmt.Errorf("aria %q already exists on disk", req.ID)
			}
		}
		id = req.ID
	} else {
		id = uuid.New().String()[:8]
	}
	sockPath := filepath.Join(h.angelus.FigaroSocketDir(), id+".sock")

	cwd, _ := os.Getwd()

	// Ephemeral: in-memory only.
	backend := h.angelus.Backend
	if req.Ephemeral {
		backend = nil
	}

	var cbState *chalkboard.State
	if !req.Ephemeral {
		cbState = h.openAriaChalkboard(id)
	}
	if cbState == nil {
		cbState, _ = chalkboard.Open("")
	}
	// Record runtime values on the patch.
	if base.Set == nil {
		base.Set = map[string]json.RawMessage{}
	}
	if _, ok := base.Set["system.cwd"]; !ok {
		base.Set["system.cwd"], _ = json.Marshal(cwd)
	}
	if _, ok := base.Set["system.root"]; !ok {
		base.Set["system.root"], _ = json.Marshal(cwd)
	}
	if _, ok := base.Set["system.max_tokens"]; !ok {
		base.Set["system.max_tokens"] = json.RawMessage(`8192`)
	}
	cbState.Apply(base)

	agent := figaro.NewAgent(figaro.Config{
		ID:         id,
		SocketPath: sockPath,
		Provider:   prov,
		Outfitter:  h.outfitter,
		Tools:      tool.DefaultRegistryFn(cwdFromChalkboard(cbState, cwd)),
		Backend:    backend,
		LogCache:   h.angelus.LogCache,
		Chalkboard: cbState,
	})

	if err := h.angelus.Registry.Register(agent); err != nil {
		agent.Kill()
		return nil, err
	}

	go agent.StartSocket(h.ctx)

	slog.Info("created figaro",
		"id", id, "loadout", req.Loadout, "provider", provName, "model", model, "socket", sockPath)

	return rpc.CreateResponse{
		FigaroID: id,
		Endpoint: rpc.Endpoint{
			Scheme:  "unix",
			Address: sockPath,
		},
	}, nil
}

// patchString reads a string value from a chalkboard.Patch's Set map.
func patchString(p chalkboard.Patch, key string) string {
	raw, ok := p.Set[key]
	if !ok {
		return ""
	}
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

func (h *handlers) kill(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.KillRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}

	// Kill live agent or just remove dormant from disk.
	if h.angelus.Registry.Get(req.FigaroID) != nil {
		if err := h.angelus.Registry.Kill(req.FigaroID); err != nil {
			return nil, err
		}
	}

	if h.angelus.Backend != nil {
		if err := h.angelus.Backend.Remove(req.FigaroID); err != nil {
			slog.Warn("remove aria failed", "id", req.FigaroID, "err", err)
		}
	}

	slog.Info("killed figaro", "id", req.FigaroID)
	return rpc.KillResponse{OK: true}, nil
}

// list merges live and dormant arias.
func (h *handlers) list(ctx context.Context, params json.RawMessage) (interface{}, error) {
	live := h.angelus.Registry.List()
	result := make([]rpc.FigaroInfoResponse, 0, len(live))
	seen := make(map[string]struct{}, len(live))
	for _, info := range live {
		seen[info.ID] = struct{}{}
		result = append(result, rpc.FigaroInfoResponse{
			ID:               info.ID,
			State:            info.State,
			Provider:         info.Provider,
			Model:            info.Model,
			MessageCount:     info.MessageCount,
			TokensIn:         info.TokensIn,
			TokensOut:        info.TokensOut,
			CacheReadTokens:  info.CacheReadTokens,
			CacheWriteTokens: info.CacheWriteTokens,
			ContextTokens:    info.ContextTokens,
			ContextExact:     info.ContextExact,
			CreatedAt:        info.CreatedAt.UnixMilli(),
			LastActive:       info.LastActive.UnixMilli(),
			BoundPIDs:        h.angelus.Registry.BoundPIDs(info.ID),
		})
	}

	if h.angelus.Backend != nil {
		arias, err := h.angelus.Backend.List()
		if err != nil {
			slog.Warn("list backend enumerate", "err", err)
		}
		for _, aria := range arias {
			if _, ok := seen[aria.ID]; ok {
				continue
			}
			entry := rpc.FigaroInfoResponse{
				ID:           aria.ID,
				State:        "dormant",
				MessageCount: aria.MessageCount,
				LastActive:   aria.LastModified.UnixMilli(),
			}
			if aria.Meta != nil {
				entry.TokensIn = aria.Meta.TokensIn
				entry.TokensOut = aria.Meta.TokensOut
				entry.CacheReadTokens = aria.Meta.CacheReadTokens
				entry.CacheWriteTokens = aria.Meta.CacheWriteTokens
				if aria.Meta.LastActiveMS != 0 {
					entry.LastActive = aria.Meta.LastActiveMS
				}
			}

			h.fillFromChalkboard(aria.ID, &entry)
			h.fillFromMetaSnapshot(aria.ID, &entry)
			result = append(result, entry)
		}
	}

	return rpc.ListResponse{Figaros: result}, nil
}

func (h *handlers) bind(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.BindRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	// Lazy-restore dormant arias before bind.
	if _, err := h.restoreByID(ctx, req.FigaroID); err != nil {
		return nil, fmt.Errorf("bind: restore %s: %w", req.FigaroID, err)
	}
	if err := h.angelus.Registry.Bind(req.PID, req.FigaroID); err != nil {
		return nil, err
	}
	return rpc.BindResponse{OK: true}, nil
}

// attach restores a dormant aria without touching pid bindings.
func (h *handlers) attach(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.AttachRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	if err := rpc.ValidateAriaID(req.FigaroID); err != nil {
		return nil, err
	}
	f, err := h.restoreByID(ctx, req.FigaroID)
	if err != nil {
		return nil, fmt.Errorf("attach %s: %w", req.FigaroID, err)
	}
	return rpc.AttachResponse{
		FigaroID: req.FigaroID,
		Endpoint: rpc.Endpoint{
			Scheme:  "unix",
			Address: f.SocketPath(),
		},
	}, nil
}

func (h *handlers) resolve(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.ResolveRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	id, f := h.angelus.Registry.Resolve(req.PID)
	if f == nil {
		return rpc.ResolveResponse{Found: false}, nil
	}
	return rpc.ResolveResponse{
		FigaroID: id,
		Endpoint: rpc.Endpoint{
			Scheme:  "unix",
			Address: f.SocketPath(),
		},
		Found: true,
	}, nil
}

func (h *handlers) unbind(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.UnbindRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	h.angelus.Registry.Unbind(req.PID)
	return rpc.UnbindResponse{OK: true}, nil
}

func (h *handlers) status(ctx context.Context, params json.RawMessage) (interface{}, error) {
	return rpc.StatusResponse{
		Uptime:      h.angelus.StartedAt.UnixMilli(),
		FigaroCount: h.angelus.Registry.FigaroCount(),
		BoundPIDs:   h.angelus.Registry.BoundPIDCount(),
	}, nil
}

func (h *handlers) saveBindings(ctx context.Context, params json.RawMessage) (interface{}, error) {
	path := h.angelus.BindingsPath()
	if err := SaveBindings(h.angelus.Registry, path); err != nil {
		return nil, err
	}
	slog.Info("saved pid bindings", "path", path, "count", h.angelus.Registry.BoundPIDCount())
	return rpc.SaveBindingsResponse{
		OK:    true,
		Count: h.angelus.Registry.BoundPIDCount(),
	}, nil
}

// ariaReadHardCap bounds Limit on aria.read regardless of what the
// client asks for, so a misconfigured client can't pull megabytes of
// IR in a single RPC.
const ariaReadHardCap = 1000

// ariaRead serves IR entries for an aria through the shared LogCache.
// Live agents share the same Log instance, so reads run lock-free
// against the agent's writes. For dormant arias the cache opens on
// miss and the entry TTLs out naturally.
func (h *handlers) ariaRead(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.AriaReadRequest
	if len(params) > 0 {
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, fmt.Errorf("aria.read: parse params: %w", err)
		}
	}
	if req.FigaroID == "" {
		return nil, errors.New("aria.read: empty figaro_id")
	}
	if h.angelus.LogCache == nil {
		return nil, errors.New("aria.read: no log cache (ephemeral angelus)")
	}

	// Validate that the aria has on-disk state before touching the
	// cache; otherwise AcquireIR would MkdirAll an empty figwal dir
	// for typo'd IDs.
	if fb, ok := h.angelus.Backend.(*store.FileBackend); ok {
		ariaRoot := filepath.Join(fb.Dir(), req.FigaroID)
		if _, err := os.Stat(ariaRoot); os.IsNotExist(err) {
			return nil, fmt.Errorf("aria.read: no aria %q on disk", req.FigaroID)
		}
	}

	log, release, err := h.angelus.LogCache.AcquireIR(req.FigaroID)
	if err != nil {
		return nil, fmt.Errorf("aria.read: acquire: %w", err)
	}
	defer release()

	all := log.Read()
	total := len(all)

	from := req.From
	startIdx := 0
	if from > 0 {
		for i, e := range all {
			if e.LT >= from {
				startIdx = i
				break
			}
			if i == len(all)-1 {
				startIdx = len(all)
			}
		}
	}

	limit := req.Limit
	if limit <= 0 || limit > ariaReadHardCap {
		limit = ariaReadHardCap
	}
	endIdx := startIdx + limit
	if endIdx > len(all) {
		endIdx = len(all)
	}

	out := make([]rpc.AriaReadEntry, 0, endIdx-startIdx)
	for _, e := range all[startIdx:endIdx] {
		raw, mErr := json.Marshal(e.Payload)
		if mErr != nil {
			return nil, fmt.Errorf("aria.read: marshal LT=%d: %w", e.LT, mErr)
		}
		out = append(out, rpc.AriaReadEntry{LT: e.LT, Payload: raw})
	}
	var nextFrom uint64
	if endIdx < len(all) {
		nextFrom = all[endIdx].LT
	}
	return rpc.AriaReadResponse{
		Entries:  out,
		Total:    total,
		NextFrom: nextFrom,
	}, nil
}

// restoreByID re-creates a figaro from the backend.
func (h *handlers) restoreByID(ctx context.Context, ariaID string) (figaro.Figaro, error) {
	if f := h.angelus.Registry.Get(ariaID); f != nil {
		return f, nil
	}
	if h.angelus.Backend == nil {
		return nil, fmt.Errorf("no backend configured")
	}
	arias, err := h.angelus.Backend.List()
	if err != nil {
		return nil, fmt.Errorf("backend list: %w", err)
	}
	for _, aria := range arias {
		if aria.ID != ariaID {
			continue
		}
		return h.restoreOne(ctx, aria)
	}
	return nil, fmt.Errorf("aria %q not found on disk", ariaID)
}

// restoreOne builds and registers a figaro from AriaInfo.
func (h *handlers) restoreOne(ctx context.Context, aria store.AriaInfo) (figaro.Figaro, error) {
	if aria.MessageCount == 0 {
		h.angelus.Backend.Remove(aria.ID)
		return nil, fmt.Errorf("restore %s: empty aria, removed", aria.ID)
	}


	cb := h.openAriaChalkboard(aria.ID)
	if cb == nil {
		return nil, fmt.Errorf("restore %s: chalkboard unavailable", aria.ID)
	}
	cbSnap := cb.Snapshot()
	cbStr := func(key string) string {
		raw, ok := cbSnap[key]
		if !ok {
			return ""
		}
		var s string
		_ = json.Unmarshal(raw, &s)
		return s
	}
	provName := cbStr("system.provider")
	model := cbStr("system.model")
	cwd := cbStr("system.cwd")

	prov, err := h.factory(provName, model)
	if err != nil {
		return nil, fmt.Errorf("restore %s: create provider: %w", aria.ID, err)
	}

	sockPath := filepath.Join(h.angelus.FigaroSocketDir(), aria.ID+".sock")

	// Fall back if restored cwd no longer exists.
	toolRoot := cwd
	if _, err := os.Stat(toolRoot); err != nil {
		toolRoot, _ = os.Getwd()
	}

	agent := figaro.NewAgent(figaro.Config{
		ID:         aria.ID,
		SocketPath: sockPath,
		Provider:   prov,
		Outfitter:  h.outfitter,
		Tools:      tool.DefaultRegistryFn(cwdFromChalkboard(cb, toolRoot)),
		Backend:    h.angelus.Backend,
		LogCache:   h.angelus.LogCache,
		Chalkboard: cb,
	})

	if err := h.angelus.Registry.Register(agent); err != nil {
		agent.Kill()
		return nil, fmt.Errorf("restore %s: register: %w", aria.ID, err)
	}

	go agent.StartSocket(ctx)

	slog.Info("restored figaro",
		"id", aria.ID, "provider", provName, "model", model, "messages", aria.MessageCount)
	return agent, nil
}

// cwdFromChalkboard returns a closure that reads system.cwd from
// cbState at call time, falling back to fallback when the key is
// unset, the chalkboard is nil, or the value isn't a JSON string.
//
// This is the seam that lets the bash tool honor a runtime
// `figaro set system.cwd …` without rebuilding the registry.
func cwdFromChalkboard(cbState *chalkboard.State, fallback string) func() string {
	return func() string {
		if cbState == nil {
			return fallback
		}
		if s := cbState.Snapshot().Lookup("system.cwd"); s != nil && *s != "" {
			return *s
		}
		return fallback
	}
}
