package agenttest

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/jack-work/figaro/internal/sandbox/agent"
	"github.com/jack-work/figaro/internal/sandbox/box"
	"github.com/jack-work/jkrpc"
)

type Spec struct {
	Name     string
	Workload string
	Count    int
}

type sandbox struct {
	name, class string
	state       string
	generation  uint64
	figaro      string
	workspace   string
	sock        string
	nonce       string
	receipt     agent.Receipt
	stop        context.CancelFunc
	done        chan struct{}
}

type Agent struct {
	Endpoint string

	mu        sync.Mutex
	dir       string
	node      string
	classes   []agent.Class
	sandboxes map[string]*sandbox
	floors    map[string]uint64
	ln        net.Listener
	calls     []string

	FailReset   map[string]string
	ForgeNonce  bool
	HoldResetBy time.Duration
}

func Start(node string, specs ...Spec) (*Agent, error) {
	dir, err := os.MkdirTemp("", "ag")
	if err != nil {
		return nil, err
	}
	a := &Agent{dir: dir, node: node, sandboxes: map[string]*sandbox{}, floors: map[string]uint64{}, FailReset: map[string]string{}}
	for _, sp := range specs {
		c := agent.Class{Name: sp.Name, Workload: sp.Workload, Writable: []string{"/work", "/tmp"}}
		for i := range sp.Count {
			name := fmt.Sprintf("%s%d", sp.Name, i)
			s := &sandbox{name: name, class: sp.Name, state: agent.Free,
				workspace: filepath.Join(dir, name, "work"), sock: filepath.Join(dir, name+".sock")}
			if err := a.serve(s); err != nil {
				a.Close()
				return nil, err
			}
			a.sandboxes[name] = s
			c.Sandboxes = append(c.Sandboxes, name)
		}
		a.classes = append(a.classes, c)
	}
	path := filepath.Join(dir, "agent.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		a.Close()
		return nil, err
	}
	a.ln, a.Endpoint = ln, "unix://"+path
	go a.accept()
	return a, nil
}

func (a *Agent) serve(s *sandbox) error {
	if err := os.MkdirAll(s.workspace, 0o700); err != nil {
		return err
	}
	srv, err := box.NewServer(s.workspace)
	if err != nil {
		return err
	}
	ln, err := box.Listen(s.sock)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.Serve(ctx, ln)
	}()
	s.stop, s.done = cancel, done
	return nil
}

func (a *Agent) Close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ln != nil {
		a.ln.Close()
	}
	for _, s := range a.sandboxes {
		if s.stop != nil {
			s.stop()
			<-s.done
		}
	}
	os.RemoveAll(a.dir)
}

func (a *Agent) Workspace(sandbox string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sandboxes[sandbox].workspace
}

func (a *Agent) State(sandbox string) (string, uint64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.sandboxes[sandbox]
	return s.state, s.generation
}

func (a *Agent) SetState(sandbox, state string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sandboxes[sandbox].state = state
}

func (a *Agent) Calls() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.calls...)
}

func (a *Agent) accept() {
	for {
		nc, err := a.ln.Accept()
		if err != nil {
			return
		}
		go func() {
			srv := jkrpc.NewServer(jkrpc.NewConn(nc), a.handlers())
			srv.Serve(context.Background())
			nc.Close()
		}()
	}
}

func refuse(reason string, data map[string]any) error {
	if data == nil {
		data = map[string]any{}
	}
	data["reason"] = reason
	raw, _ := json.Marshal(data)
	return &jkrpc.Error{Code: agent.ErrCode, Message: reason, Data: raw}
}

func handle[T any](a *Agent, method string, fn func(T) (any, error)) jkrpc.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var req T
		if len(params) > 0 {
			if err := json.Unmarshal(params, &req); err != nil {
				return nil, refuse(agent.InvalidParams, map[string]any{"field": "params"})
			}
		}
		a.mu.Lock()
		a.calls = append(a.calls, method)
		a.mu.Unlock()
		return fn(req)
	}
}

func (a *Agent) handlers() map[string]jkrpc.HandlerFunc {
	return map[string]jkrpc.HandlerFunc{
		agent.MethodHello: handle(a, agent.MethodHello, func(struct{}) (any, error) {
			return agent.Hello{Protocol: agent.Protocol, Node: a.node}, nil
		}),
		agent.MethodClasses: handle(a, agent.MethodClasses, func(struct{}) (any, error) {
			a.mu.Lock()
			defer a.mu.Unlock()
			return map[string]any{"classes": a.classes}, nil
		}),
		agent.MethodSandboxes: handle(a, agent.MethodSandboxes, func(struct{}) (any, error) {
			a.mu.Lock()
			defer a.mu.Unlock()
			var out []agent.Sandbox
			for _, s := range a.sandboxes {
				row := agent.Sandbox{Name: s.name, Class: s.class, State: s.state, Generation: s.generation, Exec: "unix://" + s.sock}
				if s.state == agent.Leased {
					row.Figaro = s.figaro
				}
				out = append(out, row)
			}
			sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
			return map[string]any{"sandboxes": out}, nil
		}),
		agent.MethodLease: handle(a, agent.MethodLease, a.lease),
		agent.MethodReset: handle(a, agent.MethodReset, a.reset),
		agent.MethodCordon: handle(a, agent.MethodCordon, func(req agent.CordonRequest) (any, error) {
			a.mu.Lock()
			defer a.mu.Unlock()
			s, ok := a.sandboxes[req.Sandbox]
			if !ok {
				return nil, refuse(agent.UnknownSandbox, nil)
			}
			s.state = agent.Cordoned
			return struct{}{}, nil
		}),
	}
}

func (a *Agent) lease(req agent.LeaseRequest) (any, error) {
	if !agent.ValidFigaro(req.Figaro) {
		return nil, refuse(agent.InvalidParams, map[string]any{"field": "figaro"})
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	known := false
	for _, c := range a.classes {
		known = known || c.Name == req.Class
	}
	if !known {
		return nil, refuse(agent.UnknownClass, nil)
	}
	var names []string
	for name, s := range a.sandboxes {
		if s.class == req.Class {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		s := a.sandboxes[name]
		if s.state == agent.Leased && s.generation == req.Generation && s.figaro == req.Figaro {
			return agent.Lease{Sandbox: name, Generation: req.Generation, Exec: "unix://" + s.sock}, nil
		}
	}
	if floor := a.floors[req.Class]; req.Generation <= floor {
		return nil, refuse(agent.GenerationUsed, map[string]any{"floor": floor})
	}
	counts := map[string]int{}
	for _, name := range names {
		s := a.sandboxes[name]
		counts[s.state]++
		if s.state == agent.Free {
			s.state, s.generation, s.figaro = agent.Leased, req.Generation, req.Figaro
			a.floors[req.Class] = req.Generation
			return agent.Lease{Sandbox: name, Generation: req.Generation, Exec: "unix://" + s.sock}, nil
		}
	}
	return nil, refuse(agent.NoFreeSandbox, map[string]any{"class": req.Class, "sandboxes": len(names),
		"leased": counts[agent.Leased], "resetting": counts[agent.Resetting], "cordoned": counts[agent.Cordoned]})
}

func (a *Agent) reset(req agent.ResetRequest) (any, error) {
	if !agent.ValidNonce(req.Nonce) {
		return nil, refuse(agent.InvalidParams, map[string]any{"field": "nonce"})
	}
	a.mu.Lock()
	s, ok := a.sandboxes[req.Sandbox]
	if !ok {
		a.mu.Unlock()
		return nil, refuse(agent.UnknownSandbox, nil)
	}
	if s.nonce == req.Nonce && s.state == agent.Free && s.generation == req.Generation {
		r := s.receipt
		a.mu.Unlock()
		return r, nil
	}
	allowed := s.state == agent.Cordoned ||
		(s.generation == req.Generation && (s.state == agent.Leased || s.state == agent.Resetting || s.state == agent.Free))
	if !allowed {
		a.mu.Unlock()
		return nil, refuse(agent.WrongState, map[string]any{"state": s.state, "generation": s.generation})
	}
	s.state = agent.Resetting
	step, fail := a.FailReset[s.name]
	hold, forge := a.HoldResetBy, a.ForgeNonce
	a.mu.Unlock()

	time.Sleep(hold)
	s.stop()
	<-s.done
	if fail {
		a.mu.Lock()
		s.state = agent.Cordoned
		a.mu.Unlock()
		if err := a.serve(s); err != nil {
			return nil, err
		}
		return nil, refuse(agent.ResetFailed, map[string]any{"step": step})
	}
	if err := os.RemoveAll(s.workspace); err != nil {
		return nil, err
	}
	if err := a.serve(s); err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s.state, s.figaro, s.nonce = agent.Free, "", req.Nonce
	s.receipt = agent.Receipt{Sandbox: s.name, Generation: req.Generation, Nonce: req.Nonce, At: time.Now().UTC()}
	if forge {
		r := s.receipt
		r.Nonce = fmt.Sprintf("%064x", 1)
		return r, nil
	}
	return s.receipt, nil
}
