package angelus

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/credo"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/jsonrpc"
	figOtel "github.com/jack-work/figaro/internal/otel"
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
	}
	return &Handlers{
		Map: map[string]jsonrpc.HandlerFunc{
			rpc.MethodCreate:  h.create,
			rpc.MethodKill:    h.kill,
			rpc.MethodList:    h.list,
			rpc.MethodBind:    h.bind,
			rpc.MethodResolve: h.resolve,
			rpc.MethodUnbind:  h.unbind,
			rpc.MethodStatus:  h.status,
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
	storeDir := filepath.Join(home, ".local", "state", "figaro", "arias")
	if req.Ephemeral {
		// Empty StoreDir → NewMemStore() (no downstream). WAL lives in RAM
		// only; Flush and Close become no-ops. Agent vanishes on Kill.
		storeDir = ""
	}

	agent := figaro.NewAgent(figaro.Config{
		ID:         id,
		SocketPath: sockPath,
		Provider:   prov,
		Model:      req.Model,
		Scribe:     scribe,
		Cwd:        cwd,
		Root:       cwd,
		MaxTokens:  8192,
		Tools:      tool.DefaultRegistry(cwd),
		LogDir:     logDir,
		StoreDir:   storeDir,
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

	// Remove persisted aria from disk.
	home, _ := os.UserHomeDir()
	storeDir := filepath.Join(home, ".local", "state", "figaro", "arias")
	if err := store.RemoveAria(storeDir, req.FigaroID); err != nil {
		// Log but don't fail the kill — the agent is already dead.
		h.angelus.Logger.Printf("warning: failed to remove aria file for %s: %v", req.FigaroID, err)
	}

	h.angelus.Logger.Printf("killed figaro %s", req.FigaroID)
	return rpc.KillResponse{OK: true}, nil
}

func (h *handlers) list(ctx context.Context, params json.RawMessage) (interface{}, error) {
	infos := h.angelus.Registry.List()
	result := make([]rpc.FigaroInfoResponse, len(infos))
	for i, info := range infos {
		result[i] = rpc.FigaroInfoResponse{
			ID:            info.ID,
			State:         info.State,
			Provider:      info.Provider,
			Model:         info.Model,
			MessageCount:  info.MessageCount,
			TokensIn:      info.TokensIn,
			TokensOut:     info.TokensOut,
			ContextTokens: info.ContextTokens,
			ContextExact:  info.ContextExact,
			CreatedAt:     info.CreatedAt.UnixMilli(),
			LastActive:    info.LastActive.UnixMilli(),
			BoundPIDs:     h.angelus.Registry.BoundPIDs(info.ID),
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

// RestoreArias scans the aria store directory and re-creates agents
// for any persisted arias that have metadata. Called on angelus startup.
func (h *handlers) RestoreArias(ctx context.Context) {
	home, _ := os.UserHomeDir()
	storeDir := filepath.Join(home, ".local", "state", "figaro", "arias")

	arias, err := store.ListArias(storeDir)
	if err != nil {
		h.angelus.Logger.Printf("restore: failed to list arias: %v", err)
		return
	}

	for _, aria := range arias {
		if aria.Meta == nil {
			h.angelus.Logger.Printf("restore: skipping %s (no metadata)", aria.ID)
			continue
		}
		if aria.MessageCount == 0 {
			// Empty aria — clean up the file.
			store.RemoveAria(storeDir, aria.ID)
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

		logDir := filepath.Join(home, ".local", "state", "figaro", "figaros")

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
			ID:         aria.ID,
			SocketPath: sockPath,
			Provider:   prov,
			Model:      meta.Model,
			Scribe:     scribe,
			Cwd:        cwd,
			Root:       root,
			MaxTokens:  8192,
			Tools:      tool.DefaultRegistry(cwd),
			LogDir:     logDir,
			StoreDir:   storeDir,
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
