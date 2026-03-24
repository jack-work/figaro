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

// NewHandlerMap creates the handler map for the angelus socket.
func NewHandlerMap(cfg ServerConfig) map[string]jsonrpc.HandlerFunc {
	h := &handlers{
		angelus: cfg.Angelus,
		config:  cfg.Config,
		factory: cfg.ProviderFactory,
		ctx:     cfg.Ctx,
	}
	return map[string]jsonrpc.HandlerFunc{
		rpc.MethodCreate:  h.create,
		rpc.MethodKill:    h.kill,
		rpc.MethodList:    h.list,
		rpc.MethodBind:    h.bind,
		rpc.MethodResolve: h.resolve,
		rpc.MethodUnbind:  h.unbind,
		rpc.MethodStatus:  h.status,
	}
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

	tools := []tool.Tool{
		&tool.Bash{Cwd: cwd},
		&tool.Read{Cwd: cwd},
		&tool.Write{Cwd: cwd},
		&tool.Edit{Cwd: cwd},
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
		Tools:      tools,
		LogDir:     logDir,
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
	h.angelus.Logger.Printf("killed figaro %s", req.FigaroID)
	return rpc.KillResponse{OK: true}, nil
}

func (h *handlers) list(ctx context.Context, params json.RawMessage) (interface{}, error) {
	infos := h.angelus.Registry.List()
	result := make([]rpc.FigaroInfoResponse, len(infos))
	for i, info := range infos {
		result[i] = rpc.FigaroInfoResponse{
			ID:           info.ID,
			State:        info.State,
			Provider:     info.Provider,
			Model:        info.Model,
			MessageCount: info.MessageCount,
			TokensIn:     info.TokensIn,
			TokensOut:    info.TokensOut,
			CreatedAt:    info.CreatedAt.UnixMilli(),
			LastActive:   info.LastActive.UnixMilli(),
			BoundPIDs:    h.angelus.Registry.BoundPIDs(info.ID),
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
