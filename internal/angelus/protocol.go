package angelus

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"text/template"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/credo"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/jsonrpc"
	figOtel "github.com/jack-work/figaro/internal/otel"
	providerPkg "github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/rpc"
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

	// ChalkboardTemplates is the shared body-template set used by
	// providers to render per-message Patches as system reminders.
	// Optional — nil disables chalkboard reminder rendering. Per-aria
	// chalkboard.State handles are opened by the create / RestoreArias
	// handlers under arias/{id}/chalkboard.json, alongside aria.jsonl.
	ChalkboardTemplates *template.Template
}

// Handlers wraps the angelus JSON-RPC handler map and provides
// additional methods like aria restoration.
type Handlers struct {
	Map map[string]jsonrpc.HandlerFunc
	h   *handlers
}

// NewHandlers creates the handler set for the angelus socket.
func NewHandlers(cfg ServerConfig) *Handlers {
	h := &handlers{
		angelus: cfg.Angelus,
		config:  cfg.Config,
		factory: cfg.ProviderFactory,
		ctx:     cfg.Ctx,
		cbTmpls: cfg.ChalkboardTemplates,
	}
	return &Handlers{
		Map: map[string]jsonrpc.HandlerFunc{
			rpc.MethodCreate:       h.create,
			rpc.MethodKill:         h.kill,
			rpc.MethodList:         h.list,
			rpc.MethodBind:         h.bind,
			rpc.MethodResolve:      h.resolve,
			rpc.MethodUnbind:       h.unbind,
			rpc.MethodStatus:       h.status,
			rpc.MethodSaveBindings: h.saveBindings,
		},
		h: h,
	}
}

// RestoreArias scans the store directory and re-creates agents for
// persisted arias. Call once on angelus startup, before accepting clients.
func (hs *Handlers) RestoreArias(ctx context.Context) {
	hs.h.RestoreArias(ctx)
}

// NewHandlerMap creates the handler map for the angelus socket.
// Deprecated: Use NewHandlers for access to RestoreArias.
func NewHandlerMap(cfg ServerConfig) map[string]jsonrpc.HandlerFunc {
	return NewHandlers(cfg).Map
}

type handlers struct {
	angelus *Angelus
	config  *config.Loaded
	factory ProviderFactory
	ctx     context.Context
	cbTmpls *template.Template
}

// openAriaChalkboard returns a *chalkboard.State at
// arias/{ariaID}/chalkboard.json (sibling to aria.jsonl). Returns nil
// when the angelus has no FileBackend (chalkboard persistence is
// FileBackend-specific) or the open fails — in either case the agent
// runs without a chalkboard and a warning is logged.
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
		h.angelus.Logger.Printf("warning: chalkboard open %s: %v (chalkboard disabled for this aria)", path, err)
		return nil
	}
	return st
}

func (h *handlers) create(ctx context.Context, params json.RawMessage) (interface{}, error) {
	_, span := figOtel.Start(ctx, "angelus.create")
	defer span.End()

	var req rpc.CreateRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}

	span.SetAttributes(
		attribute.String("figaro.provider", req.Provider),
		attribute.String("figaro.model", req.Model),
	)

	prov, err := h.factory(req.Provider, req.Model)
	if err != nil {
		return nil, fmt.Errorf("create provider: %w", err)
	}

	id := uuid.New().String()[:8]
	sockPath := filepath.Join(h.angelus.FigaroSocketDir(), id+".sock")

	scribe := credo.NewDefaultScribe(h.config.ConfigDir)

	cwd, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	logDir := filepath.Join(home, ".local", "state", "figaro", "figaros")

	// Ephemeral figaros skip the backend entirely — WAL lives only in
	// RAM, no file written, agent vanishes on Kill.
	backend := h.angelus.Backend
	if req.Ephemeral {
		backend = nil
	}

	// Ephemeral figaros skip the chalkboard — no persistence path makes
	// sense for a transient prompt. Persistent figaros open a per-aria
	// chalkboard.State at arias/{id}/chalkboard.json.
	var cbState *chalkboard.State
	if !req.Ephemeral {
		cbState = h.openAriaChalkboard(id)
	}

	agent := figaro.NewAgent(figaro.Config{
		ID:                  id,
		SocketPath:          sockPath,
		Provider:            prov,
		Model:               req.Model,
		Scribe:              scribe,
		Cwd:                 cwd,
		Root:                cwd,
		MaxTokens:           8192,
		Tools:               tool.DefaultRegistry(cwd),
		LogDir:              logDir,
		Backend:             backend,
		Chalkboard:          cbState,
		ChalkboardTemplates: h.cbTmpls,
	})

	if err := h.angelus.Registry.Register(agent); err != nil {
		agent.Kill()
		return nil, err
	}

	go agent.StartSocket(h.ctx)

	h.angelus.Logger.Printf("created figaro %s, model=%s, socket=%s", id, req.Model, sockPath)

	return rpc.CreateResponse{
		FigaroID: id,
		Endpoint: rpc.Endpoint{
			Scheme:  "unix",
			Address: sockPath,
		},
	}, nil
}

func (h *handlers) kill(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.KillRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	if err := h.angelus.Registry.Kill(req.FigaroID); err != nil {
		return nil, err
	}

	// Remove persisted aria via the backend. Non-fatal: the agent is
	// already gone, and a stale aria file is harmless (will be shown
	// by `figaro list` but not singable).
	if h.angelus.Backend != nil {
		if err := h.angelus.Backend.Remove(req.FigaroID); err != nil {
			h.angelus.Logger.Printf("warning: failed to remove aria for %s: %v", req.FigaroID, err)
		}
	}

	h.angelus.Logger.Printf("killed figaro %s", req.FigaroID)
	return rpc.KillResponse{OK: true}, nil
}

func (h *handlers) list(ctx context.Context, params json.RawMessage) (interface{}, error) {
	infos := h.angelus.Registry.List()
	result := make([]rpc.FigaroInfoResponse, len(infos))
	for i, info := range infos {
		result[i] = rpc.FigaroInfoResponse{
			ID:               info.ID,
			Label:            info.Label,
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
		}
	}
	return rpc.ListResponse{Figaros: result}, nil
}

func (h *handlers) bind(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.BindRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	if err := h.angelus.Registry.Bind(req.PID, req.FigaroID); err != nil {
		return nil, err
	}
	return rpc.BindResponse{OK: true}, nil
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
	h.angelus.Logger.Printf("saved pid bindings to %s (%d)", path, h.angelus.Registry.BoundPIDCount())
	return rpc.SaveBindingsResponse{
		OK:    true,
		Count: h.angelus.Registry.BoundPIDCount(),
	}, nil
}

// RestoreArias enumerates persisted arias via the backend and
// re-creates agents for each. Called on angelus startup.
func (h *handlers) RestoreArias(ctx context.Context) {
	if h.angelus.Backend == nil {
		return
	}
	arias, err := h.angelus.Backend.List()
	if err != nil {
		h.angelus.Logger.Printf("restore: failed to list arias: %v", err)
		return
	}

	home, _ := os.UserHomeDir()
	logDir := filepath.Join(home, ".local", "state", "figaro", "figaros")

	for _, aria := range arias {
		if aria.Meta == nil {
			h.angelus.Logger.Printf("restore: skipping %s (no metadata)", aria.ID)
			continue
		}
		if aria.MessageCount == 0 {
			// Empty aria — clean up.
			h.angelus.Backend.Remove(aria.ID)
			continue
		}

		meta := aria.Meta
		prov, err := h.factory(meta.Provider, meta.Model)
		if err != nil {
			h.angelus.Logger.Printf("restore: skipping %s: create provider: %v", aria.ID, err)
			continue
		}

		sockPath := filepath.Join(h.angelus.FigaroSocketDir(), aria.ID+".sock")
		scribe := credo.NewDefaultScribe(h.config.ConfigDir)

		cwd := meta.Cwd
		root := meta.Root
		// Fallback if the directories no longer exist.
		if _, err := os.Stat(cwd); err != nil {
			cwd, _ = os.Getwd()
		}
		if _, err := os.Stat(root); err != nil {
			root = cwd
		}

		agent := figaro.NewAgent(figaro.Config{
			ID:                  aria.ID,
			Label:               meta.Label,
			SocketPath:          sockPath,
			Provider:            prov,
			Model:               meta.Model,
			Scribe:              scribe,
			Cwd:                 cwd,
			Root:                root,
			MaxTokens:           8192,
			Tools:               tool.DefaultRegistry(cwd),
			LogDir:              logDir,
			Backend:             h.angelus.Backend,
			Chalkboard:          h.openAriaChalkboard(aria.ID),
			ChalkboardTemplates: h.cbTmpls,
		})

		if err := h.angelus.Registry.Register(agent); err != nil {
			agent.Kill()
			h.angelus.Logger.Printf("restore: skipping %s: register: %v", aria.ID, err)
			continue
		}

		go agent.StartSocket(ctx)

		h.angelus.Logger.Printf("restored figaro %s, provider=%s, model=%s, messages=%d",
			aria.ID, meta.Provider, meta.Model, aria.MessageCount)
	}
}
