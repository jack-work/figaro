package figaro

import (
	"context"
	"net"
	"os"

	"github.com/creachadair/jrpc2"
	"github.com/creachadair/jrpc2/handler"

	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/transport"
)

// StartSocket starts the figaro's JSON-RPC socket listener.
// Blocks until ctx is cancelled. Should be called in a goroutine.
func (a *Agent) StartSocket(ctx context.Context) error {
	ep := transport.UnixEndpoint(a.socketPath)

	// Clean stale socket.
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
	ch := transport.WrapConn(conn)

	methods := handler.Map{
		rpc.MethodPrompt:     handler.New(a.handlePrompt),
		rpc.MethodContext:    handler.New(a.handleContext),
		rpc.MethodFigaroInfo: handler.New(a.handleInfo),
		rpc.MethodSetModel:   handler.New(a.handleSetModel),
	}

	srv := jrpc2.NewServer(methods, &jrpc2.ServerOptions{
		AllowPush: true,
	})

	// Register this connection as a subscriber. Notifications from the
	// agent's fan-out will be pushed to the client via srv.Notify.
	sub := a.addServerSubscriber(srv)
	defer a.removeServerSubscriber(sub)

	srv.Start(ch)

	// Wait for either the connection to close or context cancellation.
	done := make(chan struct{})
	go func() {
		srv.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		srv.Stop()
	}
}

// --- JSON-RPC handlers ---

func (a *Agent) handlePrompt(ctx context.Context, req *rpc.PromptRequest) (rpc.PromptResponse, error) {
	a.Prompt(req.Text)
	return rpc.PromptResponse{OK: true}, nil
}

func (a *Agent) handleContext(ctx context.Context) (rpc.ContextResponse, error) {
	msgs := a.Context()
	// Convert to interface{} slice for serialization.
	iface := make([]interface{}, len(msgs))
	for i, m := range msgs {
		iface[i] = m
	}
	return rpc.ContextResponse{Messages: iface}, nil
}

func (a *Agent) handleInfo(ctx context.Context) (rpc.FigaroInfoResponse, error) {
	info := a.Info()
	return rpc.FigaroInfoResponse{
		ID:           info.ID,
		State:        info.State,
		Provider:     info.Provider,
		Model:        info.Model,
		MessageCount: info.MessageCount,
		TokensIn:     info.TokensIn,
		TokensOut:    info.TokensOut,
		CreatedAt:    info.CreatedAt.UnixMilli(),
		LastActive:   info.LastActive.UnixMilli(),
	}, nil
}

func (a *Agent) handleSetModel(ctx context.Context, req *rpc.SetModelRequest) (rpc.SetModelResponse, error) {
	a.SetModel(req.Model)
	return rpc.SetModelResponse{OK: true}, nil
}

// --- Server-based subscriber management ---
// Instead of Go channels, we push notifications directly via jrpc2.Server.Notify.

type serverSubscriber struct {
	srv *jrpc2.Server
	seq uint64 // monotonic sequence number for ordered delivery
}

func (a *Agent) addServerSubscriber(srv *jrpc2.Server) *serverSubscriber {
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

// notifyServers pushes a notification to all connected jrpc2 server subscribers.
func (a *Agent) notifyServers(method string, params interface{}) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	for sub := range a.serverSubs {
		// Best-effort — if the connection is dead, Notify returns an error
		// and we silently drop it.
		sub.srv.Notify(context.Background(), method, params)
	}
}

var _ Figaro = (*Agent)(nil) // compile-time interface check
