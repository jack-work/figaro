package routed

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	apimodels "github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/internal/store"
)

var routeWait = 3 * time.Second

type catalogCache struct {
	mu      sync.Mutex
	entries map[string]apimodels.Entry
	fetched time.Time
	tried   time.Time
	flight  chan struct{}
	lastErr error
}

var catalogCaches sync.Map

func sharedCatalog(name, base string) *catalogCache {
	c, _ := catalogCaches.LoadOrStore(name+"\x00"+base, newCatalogCache(nil))
	return c.(*catalogCache)
}

func newCatalogCache(entries map[string]apimodels.Entry) *catalogCache {
	if entries == nil {
		entries = map[string]apimodels.Entry{}
	}
	return &catalogCache{entries: entries}
}

func (c *catalogCache) lookup(model string) (apimodels.Entry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[model]
	return e, ok
}

func (c *catalogCache) empty() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries) == 0 && c.fetched.IsZero()
}

func (c *catalogCache) stale(now time.Time, ttl time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return now.Sub(c.fetched) >= ttl
}

func (c *catalogCache) seed(entries map[string]apimodels.Entry, at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) > 0 || !c.fetched.IsZero() {
		return
	}
	c.entries, c.fetched = entries, at
}

func (c *catalogCache) store(entries map[string]apimodels.Entry, at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries, c.fetched, c.lastErr = entries, at, nil
}

func (c *catalogCache) due(now time.Time, every time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.tried.IsZero() && now.Sub(c.tried) < every {
		return false
	}
	c.tried = now
	return true
}

func (c *catalogCache) begin() (chan struct{}, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.flight != nil {
		return c.flight, false
	}
	c.flight = make(chan struct{})
	return c.flight, true
}

func (c *catalogCache) end(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.lastErr = err
	}
	close(c.flight)
	c.flight = nil
}

func (c *catalogCache) failure() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastErr
}

type modelsFormer interface {
	ModelsForm() (*store.Form, error)
}

func (p *Provider) seedFromIR(backend any) {
	if p.spec.Catalog == "" {
		return
	}
	b, ok := backend.(modelsFormer)
	if !ok {
		return
	}
	f, err := b.ModelsForm()
	if err != nil {
		return
	}
	snap, _ := f.Snapshot()
	provs, err := apimodels.Providers(snap)
	if err != nil {
		return
	}
	known := provs[p.spec.Name]
	if len(known.Models) == 0 {
		return
	}
	p.cat.seed(known.Models, time.UnixMilli(known.RefreshedAt))
}

func (p *Provider) Warm() {
	if p.spec.Catalog != "" && p.cat.stale(p.now(), catalogTTL) {
		p.refreshCatalog(catalogRetry)
	}
}

func (p *Provider) refreshCatalog(every time.Duration) <-chan struct{} {
	if flight, started := p.cat.inFlight(); started {
		return flight
	}
	if !p.cat.due(p.now(), every) {
		return nil
	}
	flight, mine := p.cat.begin()
	if !mine {
		return flight
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), catalogTimeout)
		defer cancel()
		_, err := p.fetchCatalog(ctx)
		if err != nil {
			slog.Warn("provider catalog refresh failed; routing by what is known", "provider", p.spec.Name, "err", err)
		}
		p.cat.end(err)
	}()
	return flight
}

func (c *catalogCache) inFlight() (chan struct{}, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.flight, c.flight != nil
}

func (p *Provider) awaitCatalog(ctx context.Context) error {
	flight := p.refreshCatalog(catalogRetry)
	if flight == nil {
		if err := p.cat.failure(); err != nil {
			return err
		}
		return nil
	}
	t := time.NewTimer(routeWait)
	defer t.Stop()
	select {
	case <-flight:
		return p.cat.failure()
	case <-t.C:
		return fmt.Errorf("the model catalog did not answer within %s", routeWait)
	case <-ctx.Done():
		return ctx.Err()
	}
}
