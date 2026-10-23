package angelus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro"
)

type ariaHub struct {
	id string

	// wake is told the method so the restore is attributed (FigaroInfo.WokeBy).
	wake func(ctx context.Context, id, method string) (figaro.AgentServer, error)
	// read and write answer from the store; ok=false hands the request onward.
	// write runs after read and before wake, so a bound form patch never wakes a
	// sleeper and can reach a naked figaro whose wake would fail for want of
	// the provider keys the patch carries.
	read  func(id, method string, params json.RawMessage) (v any, ok bool, err error)
	write func(ctx context.Context, id, method string, params json.RawMessage) (v any, ok bool, err error)
	// dress resolves outfit names before routing: no writer below it reads a file.
	dress func(method string, params json.RawMessage) (json.RawMessage, error)
	// kind is the node's species; a form has no agent to wake, ever.
	kind string

	mu        sync.Mutex
	followers map[*follower]struct{}
	agent     figaro.AgentServer
	closed    bool
}

func newAriaHub(id string) *ariaHub {
	return &ariaHub{id: id, followers: map[*follower]struct{}{}}
}

func (hb *ariaHub) add(f *follower) bool {
	hb.mu.Lock()
	defer hb.mu.Unlock()
	if hb.closed {
		return false
	}
	hb.followers[f] = struct{}{}
	return true
}

func (hb *ariaHub) remove(f *follower) {
	hb.mu.Lock()
	delete(hb.followers, f)
	hb.mu.Unlock()
}

func (hb *ariaHub) targets() []*follower {
	hb.mu.Lock()
	defer hb.mu.Unlock()
	out := make([]*follower, 0, len(hb.followers))
	for f := range hb.followers {
		out = append(out, f)
	}
	return out
}

func (hb *ariaHub) Notify(method string, params any) error {
	targets := hb.targets()
	if len(targets) == 0 {
		return nil
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	raw, err = rpc.WithAria(raw, hb.id)
	if err != nil {
		return err
	}
	var firstErr error
	for _, f := range targets {
		if err := f.notify(method, raw); err != nil && firstErr == nil {
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

func (hb *ariaHub) Attached() int {
	hb.mu.Lock()
	defer hb.mu.Unlock()
	return len(hb.followers)
}

func (hb *ariaHub) Close() {
	hb.mu.Lock()
	if hb.closed {
		hb.mu.Unlock()
		return
	}
	hb.closed = true
	hb.mu.Unlock()
	targets := hb.targets()
	hb.mu.Lock()
	hb.followers = map[*follower]struct{}{}
	hb.mu.Unlock()
	raw, _ := json.Marshal(rpc.AriaClosed{FigaroID: hb.id})
	for _, f := range targets {
		f.forget(hb)
		_ = f.notify(rpc.MethodAriaClosed, raw)
	}
}

// subscribableAgent is narrow on purpose: the hub must not be able to run a turn.
type subscribableAgent interface {
	figaro.AgentServer
	Subscribe(figaro.Notifier) func()
}

var errDormantMethod = errors.New("aria is dormant and this method needs a live agent")

// route dresses first, so an outfit that names nothing on disk fails at the
// boundary rather than landing as a literal on a form.
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
		if v, ok, err := hb.write(ctx, hb.id, method, params); ok {
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
