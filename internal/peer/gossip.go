package peer

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/troupe"
)

const (
	defaultGossipEvery = time.Hour
	defaultDriftQuiet  = 10 * time.Second
	exchangeTimeout    = 30 * time.Second
)

type Gossip struct {
	Members *Ledger
	Dialer  *Dialer
	Seed    func()
	Every   time.Duration
	Quiet   time.Duration

	mu    sync.Mutex
	locks map[string]*sync.Mutex
	last  map[string]time.Time
	sent  map[string]uint64
}

func (g *Gossip) ready() {
	if g.locks == nil {
		g.locks, g.last, g.sent = map[string]*sync.Mutex{}, map[string]time.Time{}, map[string]uint64{}
	}
}

func (g *Gossip) lock(node string) *sync.Mutex {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ready()
	l, ok := g.locks[node]
	if !ok {
		l = &sync.Mutex{}
		g.locks[node] = l
	}
	return l
}

func (g *Gossip) Exchange(ctx context.Context, node string) error {
	ctx, cancel := context.WithTimeout(ctx, exchangeTimeout)
	defer cancel()
	l, err := g.Dialer.Open(ctx, node, rpc.Passage{}, nil)
	if err != nil {
		return err
	}
	defer l.Close()
	es, digest := g.Members.Snapshot()
	resp, err := l.TroupeEntries(ctx, rpc.TroupeEntriesRequest{Troupe: g.Members.Troupe(), Entries: troupe.Encode(es)})
	if err != nil {
		return err
	}
	g.mu.Lock()
	g.ready()
	g.sent[node], g.last[node] = digest, time.Now()
	g.mu.Unlock()
	theirs, err := troupe.Decode(resp.Entries)
	if err != nil {
		return fmt.Errorf("node %s: %w", node, err)
	}
	added, err := g.Members.Merge(theirs...)
	if len(added) > 0 {
		slog.Info("troupe entries learned", "from", node, "added", len(added), "version", g.Members.Version())
	}
	if resp.Version != 0 {
		g.Dialer.Versions.Saw(node, resp.Version, g.Members.Version(), resp.Policy)
	}
	if err != nil {
		slog.Warn("troupe entries refused", "from", node, "err", err)
	}
	return nil
}

func (g *Gossip) Round(ctx context.Context) {
	if g.Seed != nil {
		g.Seed()
	}
	g.Members.Prune()
	var wg sync.WaitGroup
	for _, node := range g.Members.Nodes() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l := g.lock(node)
			if !l.TryLock() {
				return
			}
			defer l.Unlock()
			if err := g.Exchange(ctx, node); err != nil {
				slog.Info("troupe gossip", "node", node, "err", err)
			}
		}()
	}
	wg.Wait()
}

func (g *Gossip) Run(ctx context.Context) {
	every := g.Every
	if every <= 0 {
		every = defaultGossipEvery
	}
	g.Round(ctx)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			g.Round(ctx)
		}
	}
}

func (g *Gossip) Drift(ctx context.Context) func(node string) {
	return func(node string) {
		quiet := g.Quiet
		if quiet <= 0 {
			quiet = defaultDriftQuiet
		}
		g.soon(ctx, node, quiet, "drift")
	}
}

func (g *Gossip) Greet(ctx context.Context) func(node string) {
	return func(node string) { g.soon(ctx, node, 0, "new member") }
}

func (g *Gossip) soon(ctx context.Context, node string, quiet time.Duration, why string) {
	l := g.lock(node)
	if !l.TryLock() {
		return
	}
	g.mu.Lock()
	g.ready()
	recent := time.Since(g.last[node]) < quiet
	g.mu.Unlock()
	if recent {
		l.Unlock()
		return
	}
	go func() {
		defer l.Unlock()
		if err := g.Exchange(ctx, node); err != nil {
			slog.Info("troupe gossip", "node", node, "on", why, "err", err)
		}
	}()
}

func (g *Gossip) Spread(ctx context.Context) map[string]error {
	nodes := g.Members.Nodes()
	errs := make([]error, len(nodes))
	var wg sync.WaitGroup
	for i, node := range nodes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l := g.lock(node)
			l.Lock()
			defer l.Unlock()
			g.mu.Lock()
			g.ready()
			delivered := g.sent[node] == g.Members.Version()
			g.mu.Unlock()
			if !delivered {
				errs[i] = g.Exchange(ctx, node)
			}
		}()
	}
	wg.Wait()
	out := make(map[string]error, len(nodes))
	for i, node := range nodes {
		out[node] = errs[i]
	}
	return out
}
