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

	mu   sync.Mutex
	busy map[string]bool
	last map[string]time.Time
}

func (g *Gossip) Exchange(ctx context.Context, node string) error {
	ctx, cancel := context.WithTimeout(ctx, exchangeTimeout)
	defer cancel()
	l, err := g.Dialer.Open(ctx, node, rpc.Passage{}, nil)
	if err != nil {
		return err
	}
	defer l.Close()
	resp, err := l.TroupeEntries(ctx, rpc.TroupeEntriesRequest{Troupe: g.Members.Troupe(), Entries: troupe.Encode(g.Members.Entries())})
	if err != nil {
		return err
	}
	es, err := troupe.Decode(resp.Entries)
	if err != nil {
		return fmt.Errorf("node %s: %w", node, err)
	}
	added, err := g.Members.Merge(es...)
	if len(added) > 0 {
		slog.Info("troupe entries learned", "from", node, "added", len(added), "version", g.Members.Version())
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
	var wg sync.WaitGroup
	for _, node := range g.Members.Nodes() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if g.claim(node, 0) {
				defer g.release(node)
				if err := g.Exchange(ctx, node); err != nil {
					slog.Info("troupe gossip", "node", node, "err", err)
				}
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
	if !g.claim(node, quiet) {
		return
	}
	go func() {
		defer g.release(node)
		if err := g.Exchange(ctx, node); err != nil {
			slog.Info("troupe gossip", "node", node, "on", why, "err", err)
		}
	}()
}

func (g *Gossip) claim(node string, quiet time.Duration) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.busy == nil {
		g.busy, g.last = map[string]bool{}, map[string]time.Time{}
	}
	if g.busy[node] || time.Since(g.last[node]) < quiet {
		return false
	}
	g.busy[node] = true
	return true
}

func (g *Gossip) release(node string) {
	g.mu.Lock()
	g.busy[node] = false
	g.last[node] = time.Now()
	g.mu.Unlock()
}
