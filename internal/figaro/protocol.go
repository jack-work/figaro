package figaro

import (
	"context"
	"net"
	"os"

	"github.com/jack-work/jkrpc"
	"github.com/jack-work/figaro/internal/transport"
)

// StartSocket starts the JSON-RPC socket listener.
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

// serveConn handles a single JSON-RPC connection.
func (a *Agent) serveConn(ctx context.Context, conn net.Conn) {
	jconn := jkrpc.NewConn(conn)
	srv := jkrpc.NewServer(jconn, buildHandlers(a))

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
var _ Notifier = (*jkrpc.Server)(nil)
