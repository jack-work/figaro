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
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/jkrpc"
)

// ariaHub owns one aria's endpoint. It is angelus-side and its lifetime is
// independent of the agent's, which is the inversion the whole hibernation
// plan rests on.
type ariaHub struct {
	id       string
	sockPath string

	// wake restores the aria and returns its agent. Called only for methods
	// that cannot be served from the store.
	wake func(ctx context.Context, id string) (figaro.AgentServer, error)
	// read answers a method from the store. ok=false means "not my method",
	// which sends the request down the wake path instead.
	read func(id, method string, params json.RawMessage) (v any, ok bool, err error)
	// write applies a mutation store-side WITHOUT an agent: the dormant
	// half of figaro.set. Consulted after read and before wake, so a board
	// patch never wakes a sleeper (and can reach a naked figaro whose wake
	// would fail for want of the very provider keys the patch carries).
	// ok=false hands the request onward. The backend's Form is the one
	// writer per node whether an agent is live or not (the agent itself
	// writes through the backend), so this is the same writer, earlier.
	write func(id, method string, params json.RawMessage) (v any, ok bool, err error)
	// kind is the node's species from its figwal marker ("conversation",
	// "form", …). A form has no agent to wake, ever: methods that reach
	// the wake path on a form node get a named refusal instead of a
	// nonsensical restore attempt.
	kind string

	mu    sync.Mutex
	ln    net.Listener
	conns map[*jkrpc.Server]struct{}
	agent figaro.AgentServer
	// dispatch is the hub's decorated handler: authorization, then dressing,
	// then route. It is set by hubFor and is NEVER nil in production --
	// handlers() falls back to bare route only so a test can build a hub
	// without a policy, and hub_guard_test pins that distinction.
	//
	// Before this was a chain, the two concerns it composes were expressed
	// two different ways: dressing was hand-inlined at the top of route(),
	// and authorization did not exist here at all, so figaro.qua, figaro.set,
	// study/cast/drop, interrupt and the queue verbs were served with no
	// policy consulted while authz.Guard wrapped only the angelus door.
	dispatch rpc.MethodHandler
	// Nothing is buffered on purpose: a frame produced while no client is
	// attached is owed to nobody, and a client that attaches catches up with
	// a read rather than a replay.
	closed bool
}

func newAriaHub(id, sockPath string) *ariaHub {
	return &ariaHub{id: id, sockPath: sockPath, conns: map[*jkrpc.Server]struct{}{}}
}

// listen binds the socket. It is separate from construction so a failure to
// bind is the caller's error rather than a half-built hub.
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
		go hb.serve(ctx, conn)
	}
}

// serve runs one client connection. The handler map is the hub's, not an
// agent's: a request arriving for a dormant aria is answered from the store
// where it can be, and wakes the aria only where it must.
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

// Notify fans one frame out to every attached client. This is the agent's
// ONLY subscriber: the agent's subscriber set is now always <=1 and
// lifetime-independent of any client, so a connection coming or going never
// touches the agent.
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

// bind attaches an agent and makes the hub its sole notifier. Returns the
// unsubscribe the caller must run on teardown.
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

// boundAgent is the live agent or nil. Callers route reads on it: a live
// agent holds the open streaming region and the in-flight turn, which the
// store does not have.
func (hb *ariaHub) boundAgent() figaro.AgentServer {
	hb.mu.Lock()
	defer hb.mu.Unlock()
	return hb.agent
}

// Attached reports how many clients hold this endpoint open. A hibernation
// sweep must NOT consult this: an attached client is exactly the case that
// used to pin an aria forever, and the hub exists so that it no longer does.
// It is here for status and tests.
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

// subscribableAgent is the half of the agent the hub needs: something that
// serves methods and accepts one notifier. Narrow on purpose: the hub must
// not be able to run a turn.
type subscribableAgent interface {
	figaro.AgentServer
	Subscribe(figaro.Notifier) func()
}

// errDormantMethod is returned for a request that needs a running turn loop
// on an aria that has none and that the hub could not wake.
var errDormantMethod = errors.New("aria is dormant and this method needs a live agent")

func (hb *ariaHub) handlers() map[string]jkrpc.HandlerFunc {
	// One decorated handler, adapted to the map jkrpc wants. The method name
	// travels as an ARGUMENT rather than as N captured closures, which is
	// what makes the chain above possible at all.
	dispatch := hb.dispatch
	if dispatch == nil {
		dispatch = hb.route
	}
	methods := figaro.AgentMethods()
	out := make(map[string]jkrpc.HandlerFunc, len(methods))
	for _, m := range methods {
		method := m
		out[method] = func(ctx context.Context, params json.RawMessage) (any, error) {
			return dispatch(ctx, method, params)
		}
	}
	return out
}

// route sends a request to the agent, waking the aria when the method needs
// a turn loop and answering from the store when it does not.
//
// It is the INNERMOST handler of the chain hubFor builds: everything
// cross-cutting -- authorization, and the outfit dressing that used to be
// hand-inlined right here -- is middleware above it now. What is left is
// routing, which is the only thing this function was ever named for.
func (hb *ariaHub) route(ctx context.Context, method string, params json.RawMessage) (any, error) {
	if agent := hb.boundAgent(); agent != nil {
		return agent.Handle(ctx, method, params)
	}
	// No agent. Serve it from the store if the method allows, which is what
	// keeps a pager, a transcript and a `show` from waking anything.
	if hb.read != nil && !rpc.MethodNeedsAgent(method) {
		if v, ok, err := hb.read(hb.id, method, params); ok {
			return v, err
		}
	}
	// Mutations the store can absorb without a turn loop: served here so a
	// board patch neither wakes a sleeper nor needs a form to have an agent
	// it will never have.
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
	agent, err := hb.wake(ctx, hb.id)
	if err != nil {
		return nil, fmt.Errorf("hub %s: wake: %w", hb.id, err)
	}
	if agent == nil {
		return nil, errDormantMethod
	}
	slog.Debug("hub woke aria", "aria", hb.id, "method", method)
	return agent.Handle(ctx, method, params)
}

// kindFormNode is the marker kind of an unbound form, as the store mints
// it. Declared here rather than imported: the angelus knows species by
// name on the wire, not by the store's internal enum.
const kindFormNode = "form"
