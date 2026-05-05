package figaro

import (
	"context"
	"net"
	"os"

	"github.com/jack-work/figaro/internal/jsonrpc"
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

// serveConn handles a single JSON-RPC connection to this figaro. The
// per-method dispatch lives on Agent.Handle (see server.go);
// serveConn just builds the wire-shape handler map, registers the
// jsonrpc.Server as a Notifier so fanout writes back down this conn,
// and runs the server.
func (a *Agent) serveConn(ctx context.Context, conn net.Conn) {
	jconn := jsonrpc.NewConn(conn)
	srv := jsonrpc.NewServer(jconn, buildHandlers(a))

	unsub := a.Subscribe(srv)
	defer unsub()

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

var _ Figaro = (*Agent)(nil) // compile-time interface check
var _ AgentServer = (*Agent)(nil)
var _ Notifier = (*jsonrpc.Server)(nil)
