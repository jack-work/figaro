package angelus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/jkrpc"
)

// ariaHub owns one aria's endpoint; its lifetime is independent of the agent's.
type ariaHub struct {
	id       string
	sockPath string

	// wake is told the method so the restore is attributed (FigaroInfo.WokeBy).
	wake func(ctx context.Context, id, method string) (figaro.AgentServer, error)
	// read and write answer from the store; ok=false hands the request onward.
	// write runs after read and before wake, so a board patch never wakes a
	// sleeper and can reach a naked figaro whose wake would fail for want of
	// the provider keys the patch carries.
	read  func(id, method string, params json.RawMessage) (v any, ok bool, err error)
	write func(id, method string, params json.RawMessage) (v any, ok bool, err error)
	// dress resolves outfit names before routing: no writer below it reads a file.
	dress func(method string, params json.RawMessage) (json.RawMessage, error)
	// kind is the node's species; a form has no agent to wake, ever.
	kind   string
	policy authz.Policy

	mu    sync.Mutex
	ln    net.Listener
	conns map[*jkrpc.Server]struct{}
	agent figaro.AgentServer
	// Nothing is buffered: a client that attaches catches up with a read, not a replay.
	closed bool
}

func newAriaHub(id, sockPath string, policy authz.Policy) *ariaHub {
	return &ariaHub{id: id, sockPath: sockPath, policy: policy, conns: map[*jkrpc.Server]struct{}{}}
}

func (hb *ariaHub) listen(ctx context.Context) error {
	os.Remove(hb.sockPath)
	ln, err := net.Listen("unix", hb.sockPath)
	if err != nil {
		return fmt.Errorf("hub %s: listen: %w", hb.id, err)
	}
	if err := os.Chmod(hb.sockPath, 0600); err != nil {
		ln.Close()
		return fmt.Errorf("hub %s: chmod: %w", hb.id, err)
	}
	hb.mu.Lock()
	hb.ln = ln
	hb.mu.Unlock()

	go func() {
		<-ctx.Done()
		hb.Close()
	}()
	go hb.accept(ctx)
	return nil
}

func (hb *ariaHub) accept(ctx context.Context) {
	for {
		conn, err := hb.ln.Accept()
		if err != nil {
			hb.mu.Lock()
			done := hb.closed
			hb.mu.Unlock()
			if done || ctx.Err() != nil {
				return
			}
			continue
		}
		cctx, err := authz.Admit(ctx, conn)
		if err != nil {
			conn.Close()
			continue
		}
		go hb.serve(cctx, conn)
	}
}

func (hb *ariaHub) serve(ctx context.Context, conn net.Conn) {
	srv := jkrpc.NewServer(jkrpc.NewConn(conn), hb.handlers())

	hb.mu.Lock()
	if hb.closed {
		hb.mu.Unlock()
		conn.Close()
		return
	}
	hb.conns[srv] = struct{}{}
	hb.mu.Unlock()

	defer func() {
		hb.mu.Lock()
		delete(hb.conns, srv)
		hb.mu.Unlock()
	}()

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

// Notify is the agent's only subscriber: a connection coming or going never touches the agent.
func (hb *ariaHub) Notify(method string, params any) error {
	hb.mu.Lock()
	targets := make([]*jkrpc.Server, 0, len(hb.conns))
	for c := range hb.conns {
		targets = append(targets, c)
	}
	hb.mu.Unlock()

	var firstErr error
	for _, c := range targets {
		if err := c.Notify(method, params); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// bind returns the unsubscribe the caller must run on teardown.
func (hb *ariaHub) bind(agent subscribableAgent) func() {
	hb.mu.Lock()
	hb.agent = agent
	hb.mu.Unlock()

	unsub := agent.Subscribe(hb)
	return func() {
		unsub()
		hb.mu.Lock()
		hb.agent = nil
		hb.mu.Unlock()
	}
}

// boundAgent is routed to for reads: it holds the in-flight turn the store does not.
func (hb *ariaHub) boundAgent() figaro.AgentServer {
	hb.mu.Lock()
	defer hb.mu.Unlock()
	return hb.agent
}

// Attached is for status and tests; a hibernation sweep must not consult
// it, or an attached client pins an aria forever.
func (hb *ariaHub) Attached() int {
	hb.mu.Lock()
	defer hb.mu.Unlock()
	return len(hb.conns)
}

func (hb *ariaHub) Close() {
	hb.mu.Lock()
	if hb.closed {
		hb.mu.Unlock()
		return
	}
	hb.closed = true
	ln := hb.ln
	conns := make([]*jkrpc.Server, 0, len(hb.conns))
	for c := range hb.conns {
		conns = append(conns, c)
	}
	hb.mu.Unlock()

	if ln != nil {
		ln.Close()
	}
	for _, c := range conns {
		c.Stop()
	}
	os.Remove(hb.sockPath)
}

// subscribableAgent is narrow on purpose: the hub must not be able to run a turn.
type subscribableAgent interface {
	figaro.AgentServer
	Subscribe(figaro.Notifier) func()
}

var errDormantMethod = errors.New("aria is dormant and this method needs a live agent")

func (hb *ariaHub) handlers() map[string]jkrpc.HandlerFunc {
	methods := figaro.AgentMethods()
	out := make(map[string]jkrpc.HandlerFunc, len(methods))
	for _, m := range methods {
		method := m
		out[method] = func(ctx context.Context, params json.RawMessage) (any, error) {
			return hb.route(ctx, method, params)
		}
	}
	return authz.Guard(out, authz.AriaHeader{}, hb.policy)
}

// route dresses first, so an outfit that names nothing on disk fails at the
// boundary rather than landing as a literal on a board.
func (hb *ariaHub) route(ctx context.Context, method string, params json.RawMessage) (any, error) {
	if hb.dress != nil {
		dressed, err := hb.dress(method, params)
		if err != nil {
			return nil, err
		}
		params = dressed
	}
	if agent := hb.boundAgent(); agent != nil {
		return agent.Handle(ctx, method, params)
	}
	if hb.read != nil && !rpc.MethodNeedsAgent(method) {
		if v, ok, err := hb.read(hb.id, method, params); ok {
			return v, err
		}
	}
	if hb.write != nil {
		if v, ok, err := hb.write(hb.id, method, params); ok {
			return v, err
		}
	}
	if hb.kind == kindFormNode {
		return nil, fmt.Errorf("%s is a form, not a figaro: %s needs a turn loop and a form has none (bind it first)", hb.id, method)
	}
	if hb.wake == nil {
		return nil, errDormantMethod
	}
	agent, err := hb.wake(ctx, hb.id, method)
	if err != nil {
		return nil, fmt.Errorf("hub %s: wake: %w", hb.id, err)
	}
	if agent == nil {
		return nil, errDormantMethod
	}
	slog.Debug("hub woke aria", "aria", hb.id, "method", method)
	return agent.Handle(ctx, method, params)
}

const kindFormNode = "form"
