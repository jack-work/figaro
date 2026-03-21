package angelus

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/creachadair/jrpc2/handler"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/credo"
	"github.com/jack-work/figaro/internal/figaro"
	figOtel "github.com/jack-work/figaro/internal/otel"
	providerPkg "github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/tool"
)

// ProviderFactory creates a provider from a name and model.
// Injected by main so the angelus doesn't import provider implementations.
type ProviderFactory func(providerName, model string) (providerPkg.Provider, error)

// ServerConfig holds dependencies for the angelus JSON-RPC handlers.
type ServerConfig struct {
	Angelus         *Angelus
	Config          *config.Loaded
	ProviderFactory ProviderFactory
	Ctx             context.Context // long-lived context for spawned figaros
}

// NewHandlerMap creates the jrpc2 handler map for the angelus socket.
func NewHandlerMap(cfg ServerConfig) handler.Map {
	h := &handlers{
		angelus: cfg.Angelus,
		config:  cfg.Config,
		factory: cfg.ProviderFactory,
		ctx:     cfg.Ctx,
	}
	return handler.Map{
		rpc.MethodCreate:      handler.New(h.create),
		rpc.MethodKill:        handler.New(h.kill),
		rpc.MethodList:        handler.New(h.list),
		rpc.MethodBind:        handler.New(h.bind),
		rpc.MethodResolve:     handler.New(h.resolve),
		rpc.MethodUnbind:      handler.New(h.unbind),
		rpc.MethodStatus:      handler.New(h.status),
	}
}

type handlers struct {
	angelus *Angelus
	config  *config.Loaded
	factory ProviderFactory
	ctx     context.Context // long-lived context for spawned figaros
}

func (h *handlers) create(ctx context.Context, req *rpc.CreateRequest) (rpc.CreateResponse, error) {
	_, span := figOtel.Start(ctx, "angelus.create",
		figOtel.WithAttributes(
			attribute.String("figaro.provider", req.Provider),
			attribute.String("figaro.model", req.Model),
		),
	)
	defer span.End()

	prov, err := h.factory(req.Provider, req.Model)
	if err != nil {
		return rpc.CreateResponse{}, fmt.Errorf("create provider: %w", err)
	}

	id := uuid.New().String()[:8]
	sockPath := filepath.Join(h.angelus.FigaroSocketDir(), id+".sock")

	// Build credo scribe from config directory.
	scribe := credo.NewDefaultScribe(h.config.ConfigDir)

	cwd, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	logDir := filepath.Join(home, ".local", "state", "figaro", "figaros")

	// Wire tools with the figaro's working directory.
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
		Root:       cwd, // TODO: detect git root
		MaxTokens:  8192,
		Tools:      tools,
		LogDir:     logDir,
	})

	if err := h.angelus.Registry.Register(agent); err != nil {
		agent.Kill()
		return rpc.CreateResponse{}, err
	}

	// Start the figaro's socket listener with the long-lived context.
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

func (h *handlers) kill(ctx context.Context, req *rpc.KillRequest) (rpc.KillResponse, error) {
	if err := h.angelus.Registry.Kill(req.FigaroID); err != nil {
		return rpc.KillResponse{}, err
	}
	h.angelus.Logger.Printf("killed figaro %s", req.FigaroID)
	return rpc.KillResponse{OK: true}, nil
}

func (h *handlers) list(ctx context.Context) (rpc.ListResponse, error) {
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

func (h *handlers) bind(ctx context.Context, req *rpc.BindRequest) (rpc.BindResponse, error) {
	if err := h.angelus.Registry.Bind(req.PID, req.FigaroID); err != nil {
		return rpc.BindResponse{}, err
	}
	return rpc.BindResponse{OK: true}, nil
}

func (h *handlers) resolve(ctx context.Context, req *rpc.ResolveRequest) (rpc.ResolveResponse, error) {
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

func (h *handlers) unbind(ctx context.Context, req *rpc.UnbindRequest) (rpc.UnbindResponse, error) {
	h.angelus.Registry.Unbind(req.PID)
	return rpc.UnbindResponse{OK: true}, nil
}

func (h *handlers) status(ctx context.Context) (rpc.StatusResponse, error) {
	return rpc.StatusResponse{
		Uptime:      h.angelus.StartedAt.UnixMilli(),
		FigaroCount: h.angelus.Registry.FigaroCount(),
		BoundPIDs:   h.angelus.Registry.BoundPIDCount(),
	}, nil
}


