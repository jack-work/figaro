package angelus

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/sandbox/agent"
	"github.com/jack-work/figaro/internal/sandbox/lease"
	"github.com/jack-work/figaro/internal/slotpool"
)

const (
	leaseGenerationTries = 8
	unfinishedLeaseEvery = 30 * time.Second
)

type sandboxClass struct {
	node     string
	endpoint string
	class    agent.Class
}

type SandboxLeases struct {
	Ledger      *lease.Ledger
	Generations *slotpool.Generations
	Dial        func(ctx context.Context, endpoint string) (*agent.Client, error)
	Probe       func(ctx context.Context, name, exec string, writable []string, figaro string) error

	leasing sync.Mutex

	mu        sync.Mutex
	classes   map[string]sandboxClass
	sandboxes map[string]string
}

func OpenSandboxLeases(dir string) (*SandboxLeases, error) {
	generations, err := slotpool.OpenGenerations(filepath.Join(dir, "generations"))
	if err != nil {
		return nil, err
	}
	ledger, err := lease.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		return nil, err
	}
	return &SandboxLeases{Ledger: ledger, Generations: generations, Dial: agent.Dial, Probe: agent.Probe,
		classes: map[string]sandboxClass{}, sandboxes: map[string]string{}}, nil
}

func (s *SandboxLeases) Class(name string) (sandboxClass, bool) {
	if s == nil {
		return sandboxClass{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.classes[name]
	return c, ok
}

func (s *SandboxLeases) IsClass(name string) bool {
	_, ok := s.Class(name)
	return ok
}

func (s *SandboxLeases) IsSandbox(name string) bool {
	if s == nil || name == "" {
		return false
	}
	s.mu.Lock()
	_, known := s.sandboxes[name]
	s.mu.Unlock()
	if known {
		return true
	}
	_, held := s.Ledger.Of(name)
	return held
}

func (s *SandboxLeases) Holds(sandbox, figaro string) bool {
	if s == nil {
		return false
	}
	row, ok := s.Ledger.Of(sandbox)
	return ok && row.State == lease.Leased && row.Holds(figaro)
}

func (s *SandboxLeases) Classes() []sandboxClass {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]sandboxClass, 0, len(s.classes))
	for _, c := range s.classes {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].class.Name < out[j].class.Name })
	return out
}

func (s *SandboxLeases) learn(ctx context.Context, loaded *config.Loaded) {
	classes := map[string]sandboxClass{}
	sandboxes := map[string]string{}
	for _, node := range loaded.SandboxNodes() {
		n, _ := loaded.SandboxNode(node)
		c, err := s.Dial(ctx, n.Agent)
		if err != nil {
			slog.Error("sandbox node: its agent does not answer, so none of its classes can be leased", "node", node, "err", err)
			continue
		}
		offered, err := c.Classes(ctx)
		c.Close()
		if err != nil {
			slog.Error("sandbox node: its agent lists no classes", "node", node, "err", err)
			continue
		}
		for _, cl := range offered {
			switch {
			case !n.Hosts(cl.Workload):
				slog.Error("sandbox node: a class of a workload the node does not host is not offered", "node", node, "class", cl.Name, "workload", cl.Workload, "hosts", n.Workload)
				continue
			case classes[cl.Name].node != "":
				slog.Error("sandbox node: two nodes offer one class, so neither is offered", "class", cl.Name, "nodes", []string{classes[cl.Name].node, node})
				delete(classes, cl.Name)
				continue
			}
			if _, box := loaded.Config.Sandbox.Boxes[cl.Name]; box {
				slog.Error("sandbox node: a class takes a box's name, so it is not offered", "node", node, "class", cl.Name)
				continue
			}
			classes[cl.Name] = sandboxClass{node: node, endpoint: n.Agent, class: cl}
			for _, sb := range cl.Sandboxes {
				sandboxes[sb] = cl.Name
			}
			slog.Info("sandbox node: class offered", "node", node, "class", cl.Name, "workload", cl.Workload, "sandboxes", len(cl.Sandboxes), "egress", cl.Egress)
		}
	}
	s.mu.Lock()
	s.classes, s.sandboxes = classes, sandboxes
	s.mu.Unlock()
}

func (s *SandboxLeases) endpointOf(row lease.Row, loaded *config.Loaded) (string, error) {
	n, ok := loaded.SandboxNode(row.Node)
	if !ok {
		return "", fmt.Errorf("sandbox lease %s/%d names node %s, which this config does not", row.Class, row.Generation, row.Node)
	}
	return n.Agent, nil
}

func nonce() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

var errNoFreeSandbox = errors.New("no free sandbox")

func (s *SandboxLeases) lease(ctx context.Context, loaded *config.Loaded, className, figaro string) (lease.Row, error) {
	cl, ok := s.Class(className)
	if !ok {
		return lease.Row{}, fmt.Errorf("no sandbox node offers class %s", className)
	}
	if !agent.ValidFigaro(figaro) {
		return lease.Row{}, fmt.Errorf("lease a %s sandbox: figaro id %q is not one the node agent takes", className, figaro)
	}
	s.leasing.Lock()
	defer s.leasing.Unlock()
	c, err := s.Dial(ctx, cl.endpoint)
	if err != nil {
		return lease.Row{}, fmt.Errorf("lease a %s sandbox: %w", className, err)
	}
	defer c.Close()
	floor := s.Ledger.Floor(className)
	for range leaseGenerationTries {
		generation, err := s.Generations.Next(className, floor)
		if err != nil {
			return lease.Row{}, err
		}
		if err := s.Ledger.Begin(cl.node, className, generation, figaro); err != nil {
			return lease.Row{}, err
		}
		got, err := c.Lease(ctx, agent.LeaseRequest{Class: className, Generation: generation, Figaro: figaro})
		if agent.RefusedFor(err, agent.GenerationUsed) {
			s.Ledger.Close(className, generation)
			floor = generation
			continue
		}
		if err != nil {
			s.Ledger.Close(className, generation)
			if agent.RefusedFor(err, agent.NoFreeSandbox) {
				return lease.Row{}, fmt.Errorf("%w of class %s: %v", errNoFreeSandbox, className, err)
			}
			return lease.Row{}, fmt.Errorf("lease a %s sandbox: %w", className, err)
		}
		if _, err := s.Ledger.Picked(className, generation, got.Sandbox, got.Exec); err != nil {
			return lease.Row{}, err
		}
		if err := s.Probe(ctx, got.Sandbox, got.Exec, cl.class.Writable, figaro); err != nil {
			slog.Error("sandbox lease: the sandbox failed its probe at lease, so it is cordoned", "node", cl.node, "class", className, "sandbox", got.Sandbox, "generation", generation, "err", err)
			c.Cordon(ctx, agent.CordonRequest{Sandbox: got.Sandbox, Generation: generation, Reason: "probe failed at lease"})
			s.Ledger.Cordon(className, generation)
			return lease.Row{}, fmt.Errorf("lease a %s sandbox: %w", className, err)
		}
		path, _ := agent.SocketPath(got.Exec)
		if err := loaded.BindSandbox(got.Sandbox, path); err != nil {
			s.release(ctx, loaded, className, generation)
			return lease.Row{}, err
		}
		row, err := s.Ledger.Leased(className, generation)
		if err != nil {
			loaded.UnbindSandbox(got.Sandbox)
			return lease.Row{}, err
		}
		slog.Info("sandbox leased", "node", cl.node, "class", className, "sandbox", got.Sandbox, "generation", generation, "figaro", figaro)
		return row, nil
	}
	return lease.Row{}, fmt.Errorf("lease a %s sandbox: the node agent refused %d generations in a row as used", className, leaseGenerationTries)
}

func (s *SandboxLeases) release(ctx context.Context, loaded *config.Loaded, className string, generation uint64) error {
	n, err := nonce()
	if err != nil {
		return err
	}
	row, err := s.Ledger.Releasing(className, generation, n)
	if err != nil {
		return err
	}
	if row.Sandbox == "" {
		return s.Ledger.Close(className, generation)
	}
	loaded.UnbindSandbox(row.Sandbox)
	endpoint, err := s.endpointOf(row, loaded)
	if err != nil {
		return err
	}
	c, err := s.Dial(ctx, endpoint)
	if err != nil {
		slog.Warn("sandbox release: the node agent does not answer; it is retried", "sandbox", row.Sandbox, "generation", generation, "err", err)
		return err
	}
	defer c.Close()
	receipt, err := c.Reset(ctx, agent.ResetRequest{Sandbox: row.Sandbox, Generation: generation, Nonce: n})
	if err == nil {
		writable := []string{}
		if cl, ok := s.Class(className); ok {
			writable = cl.class.Writable
		}
		err = s.Probe(ctx, row.Sandbox, row.Exec, writable, row.Leaser)
	}
	if err != nil {
		slog.Error("sandbox release: the sandbox did not come back clean, so it is cordoned until reinstated", "node", row.Node, "class", className, "sandbox", row.Sandbox, "generation", generation, "err", err)
		c.Cordon(ctx, agent.CordonRequest{Sandbox: row.Sandbox, Generation: generation, Reason: "reset or probe failed"})
		s.Ledger.Cordon(className, generation)
		return err
	}
	if err := s.Ledger.Close(className, generation); err != nil {
		return err
	}
	slog.Info("sandbox released", "node", row.Node, "class", className, "sandbox", row.Sandbox, "generation", generation, "receipt_at", receipt.At)
	return nil
}

func (s *SandboxLeases) Release(ctx context.Context, loaded *config.Loaded, className string, generation uint64) error {
	s.leasing.Lock()
	defer s.leasing.Unlock()
	return s.release(ctx, loaded, className, generation)
}

func (s *SandboxLeases) AddHolder(sandbox, figaro string) error {
	row, ok := s.Ledger.Of(sandbox)
	if !ok || row.State != lease.Leased {
		return fmt.Errorf("sandbox %s holds no lease to join", sandbox)
	}
	_, err := s.Ledger.AddHolder(row.Class, row.Generation, figaro)
	if err == nil {
		slog.Info("sandbox lease joined", "sandbox", sandbox, "class", row.Class, "generation", row.Generation, "figaro", figaro)
	}
	return err
}

func (s *SandboxLeases) DropHolder(ctx context.Context, loaded *config.Loaded, sandbox, figaro string) {
	if s == nil {
		return
	}
	row, ok := s.Ledger.Of(sandbox)
	if !ok || row.State != lease.Leased || !row.Holds(figaro) {
		return
	}
	row, err := s.Ledger.DropHolder(row.Class, row.Generation, figaro)
	if err != nil {
		slog.Error("sandbox lease: a holder could not leave it", "sandbox", sandbox, "figaro", figaro, "err", err)
		return
	}
	slog.Info("sandbox lease left", "sandbox", sandbox, "class", row.Class, "generation", row.Generation, "figaro", figaro, "holders", len(row.Holders))
	if len(row.Holders) > 0 {
		return
	}
	loaded.UnbindSandbox(sandbox)
	go func() {
		if err := s.Release(context.WithoutCancel(ctx), loaded, row.Class, row.Generation); err != nil {
			slog.Warn("sandbox release did not finish; it is retried", "sandbox", sandbox, "generation", row.Generation, "err", err)
		}
	}()
}

func (s *SandboxLeases) Reinstate(ctx context.Context, loaded *config.Loaded, sandbox string) error {
	s.leasing.Lock()
	defer s.leasing.Unlock()
	row, ok := s.Ledger.Of(sandbox)
	if !ok {
		return fmt.Errorf("sandbox %s has no cordoned lease in this daemon's ledger", sandbox)
	}
	if row.State != lease.Cordoned && row.State != lease.Releasing {
		return fmt.Errorf("sandbox %s is %s; only a cordoned or releasing sandbox is reinstated", sandbox, row.State)
	}
	return s.release(ctx, loaded, row.Class, row.Generation)
}

func (s *SandboxLeases) Recover(ctx context.Context, loaded *config.Loaded) {
	s.learn(ctx, loaded)
	s.leasing.Lock()
	defer s.leasing.Unlock()
	held := map[string]agent.Sandbox{}
	reached := map[string]bool{}
	for _, node := range loaded.SandboxNodes() {
		n, _ := loaded.SandboxNode(node)
		c, err := s.Dial(ctx, n.Agent)
		if err != nil {
			continue
		}
		listed, err := c.Sandboxes(ctx)
		c.Close()
		if err != nil {
			slog.Error("sandbox node: its agent lists no sandboxes", "node", node, "err", err)
			continue
		}
		reached[node] = true
		for _, sb := range listed {
			held[sb.Name] = sb
		}
	}
	markForRelease := func(class string, generation uint64) {
		n, err := nonce()
		if err == nil {
			_, err = s.Ledger.Releasing(class, generation, n)
		}
		if err != nil {
			slog.Error("sandbox lease: could not be marked for release", "class", class, "generation", generation, "err", err)
		}
	}
	inLedger := map[string]bool{}
	for _, row := range s.Ledger.Rows() {
		inLedger[row.Sandbox] = true
		if row.State != lease.Leased || !reached[row.Node] {
			continue
		}
		sb, ok := held[row.Sandbox]
		if ok && sb.State == agent.Leased && sb.Generation == row.Generation {
			path, _ := agent.SocketPath(row.Exec)
			if err := loaded.BindSandbox(row.Sandbox, path); err == nil {
				slog.Info("sandbox lease recovered", "sandbox", row.Sandbox, "class", row.Class, "generation", row.Generation, "holders", len(row.Holders))
				continue
			}
		}
		slog.Warn("sandbox lease: the node agent does not hold it as the ledger does, so it is released", "sandbox", row.Sandbox, "generation", row.Generation, "agent_state", sb.State, "agent_generation", sb.Generation)
		markForRelease(row.Class, row.Generation)
	}
	for name, sb := range held {
		if inLedger[name] || sb.State != agent.Leased || sb.Generation == 0 {
			continue
		}
		cl, ok := s.Class(sb.Class)
		if !ok {
			continue
		}
		slog.Warn("sandbox lease: the node agent holds a lease this daemon's ledger does not, so it is released", "sandbox", name, "class", sb.Class, "generation", sb.Generation)
		if err := s.Ledger.Begin(cl.node, sb.Class, sb.Generation, sb.Figaro); err != nil {
			continue
		}
		if _, err := s.Ledger.Picked(sb.Class, sb.Generation, name, sb.Exec); err != nil {
			continue
		}
		markForRelease(sb.Class, sb.Generation)
	}
}

func (s *SandboxLeases) unbound(loaded *config.Loaded) bool {
	for _, row := range s.Ledger.Rows() {
		if row.State == lease.Leased && !loaded.BoundSandbox(row.Sandbox) {
			return true
		}
	}
	return false
}

func (s *SandboxLeases) finishUnfinished(ctx context.Context, loaded *config.Loaded) {
	s.leasing.Lock()
	defer s.leasing.Unlock()
	for _, row := range s.Ledger.Rows() {
		if row.State == lease.Releasing || row.State == lease.Leasing {
			s.release(ctx, loaded, row.Class, row.Generation)
		}
	}
}

func (s *SandboxLeases) KeepFinishing(ctx context.Context, settings func() *config.Loaded) {
	s.finishUnfinished(ctx, settings())
	tick := time.NewTicker(unfinishedLeaseEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			loaded := settings()
			if len(s.Classes()) == 0 || s.unbound(loaded) {
				s.Recover(ctx, loaded)
			}
			s.finishUnfinished(ctx, loaded)
		}
	}
}
