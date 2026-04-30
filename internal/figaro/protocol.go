package figaro

import (
	"context"
	"encoding/json"
	"net"
	"os"

	"github.com/jack-work/figaro/internal/jsonrpc"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/transport"
)

// StartSocket starts the figaro's JSON-RPC socket listener.
// Blocks until ctx is cancelled. Should be called in a goroutine.
func (a *Agent) StartSocket(ctx context.Context) error {
	ep := transport.UnixEndpoint(a.socketPath)

	os.Remove(a.socketPath)

	ln, err := transport.Listen(ep)
	if err != nil {
		return err
	}

	if err := os.Chmod(a.socketPath, 0600); err != nil {
		ln.Close()
		return err
	}

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				continue
			}
		}
		go a.serveConn(ctx, conn)
	}
}

// serveConn handles a single JSON-RPC connection to this figaro.
func (a *Agent) serveConn(ctx context.Context, conn net.Conn) {
	jconn := jsonrpc.NewConn(conn)

	handlers := map[string]jsonrpc.HandlerFunc{
		rpc.MethodPrompt: func(ctx context.Context, params json.RawMessage) (any, error) {
			var req rpc.PromptRequest
			if err := json.Unmarshal(params, &req); err != nil {
				return nil, err
			}
			a.SubmitPrompt(req)
			return rpc.PromptResponse{OK: true}, nil
		},
		rpc.MethodContext: func(ctx context.Context, params json.RawMessage) (any, error) {
			msgs := a.Context()
			iface := make([]any, len(msgs))
			for i, m := range msgs {
				iface[i] = m
			}
			return rpc.ContextResponse{Messages: iface}, nil
		},
		rpc.MethodFigaroInfo: func(ctx context.Context, params json.RawMessage) (any, error) {
			info := a.Info()
			return rpc.FigaroInfoResponse{
				ID:           info.ID,
				Label:        info.Label,
				State:        info.State,
				Provider:     info.Provider,
				Model:        info.Model,
				MessageCount: info.MessageCount,
				TokensIn:     info.TokensIn,
				TokensOut:    info.TokensOut,
				CreatedAt:    info.CreatedAt.UnixMilli(),
				LastActive:   info.LastActive.UnixMilli(),
			}, nil
		},
		rpc.MethodSetModel: func(ctx context.Context, params json.RawMessage) (any, error) {
			var req rpc.SetModelRequest
			if err := json.Unmarshal(params, &req); err != nil {
				return nil, err
			}
			a.SetModel(req.Model)
			return rpc.SetModelResponse{OK: true}, nil
		},
		rpc.MethodSetLabel: func(ctx context.Context, params json.RawMessage) (any, error) {
			var req rpc.SetLabelRequest
			if err := json.Unmarshal(params, &req); err != nil {
				return nil, err
			}
			if err := a.SetLabel(req.Label); err != nil {
				return nil, err
			}
			return rpc.SetLabelResponse{OK: true}, nil
		},
		rpc.MethodInterrupt: func(ctx context.Context, params json.RawMessage) (any, error) {
			a.Interrupt()
			return rpc.InterruptResponse{OK: true}, nil
		},
		rpc.MethodRehydrate: func(ctx context.Context, params json.RawMessage) (any, error) {
			var req rpc.RehydrateRequest
			if len(params) > 0 {
				if err := json.Unmarshal(params, &req); err != nil {
					return nil, err
				}
			}
			set, removed, applied, err := a.Rehydrate(req.DryRun)
			if err != nil {
				return nil, err
			}
			return rpc.RehydrateResponse{
				Applied: applied, SetKeys: set, RemoveKeys: removed,
			}, nil
		},
	}

	srv := jsonrpc.NewServer(jconn, handlers)

	// Register this server as a subscriber. Notifications are pushed
	// via srv.Notify — ordered, single writer.
	sub := a.addServerSubscriber(srv)
	defer a.removeServerSubscriber(sub)

	done := make(chan struct{})
	go func() {
		srv.Serve(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		srv.Stop()
	}
}

// --- Server-based subscriber management ---

type serverSubscriber struct {
	srv *jsonrpc.Server
}

func (a *Agent) addServerSubscriber(srv *jsonrpc.Server) *serverSubscriber {
	sub := &serverSubscriber{srv: srv}
	a.mu.Lock()
	if a.serverSubs == nil {
		a.serverSubs = make(map[*serverSubscriber]struct{})
	}
	a.serverSubs[sub] = struct{}{}
	a.mu.Unlock()
	return sub
}

func (a *Agent) removeServerSubscriber(sub *serverSubscriber) {
	a.mu.Lock()
	delete(a.serverSubs, sub)
	a.mu.Unlock()
}

var _ Figaro = (*Agent)(nil) // compile-time interface check
