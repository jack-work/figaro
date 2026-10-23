package modelcat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"sync"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

type Builder func(providerName string) (provider.Provider, error)

type Config struct {
	Build        Builder
	Providers    func() []string
	Poll         time.Duration
	Form         *store.Form
	TTL          time.Duration
	Retry        time.Duration
	FetchTimeout time.Duration
	Now          func() time.Time
}

const (
	DefaultTTL          = 24 * time.Hour
	DefaultRetry        = 10 * time.Minute
	DefaultFetchTimeout = 30 * time.Second
)

type Catalog struct {
	cfg  Config
	form *store.Form

	mu     sync.Mutex
	provs  map[string]*provState
	closed bool

	wg  sync.WaitGroup
	ctx context.Context
}

type provState struct {
	inflight bool
	listing  bool
	listNext bool
	listDone chan struct{}
	again    bool
	attempt  time.Time
	wanted   map[string]bool
	tried    map[string]time.Time
}

func New(cfg Config) *Catalog {
	if cfg.TTL <= 0 {
		cfg.TTL = DefaultTTL
	}
	if cfg.Retry <= 0 {
		cfg.Retry = DefaultRetry
	}
	if cfg.FetchTimeout <= 0 {
		cfg.FetchTimeout = DefaultFetchTimeout
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	f := cfg.Form
	if f == nil {
		f = store.NewMemForm()
	}
	return &Catalog{
		cfg:   cfg,
		form:  f,
		provs: map[string]*provState{},
		ctx:   context.Background(),
	}
}

func (c *Catalog) Form() *store.Form { return c.form }

func (c *Catalog) Snapshot() form.Snapshot {
	if c == nil {
		return form.Snapshot{}
	}
	snap, _ := c.form.Snapshot()
	return snap
}

func (c *Catalog) Limit(providerName, model string) int {
	if c == nil {
		return 0
	}
	c.Want(providerName, model)
	return models.EntryLimit(c.Snapshot(), providerName, model, "")
}

func (c *Catalog) Run(ctx context.Context) {
	c.mu.Lock()
	c.ctx = ctx
	c.mu.Unlock()
	c.listStale()
	var poll *time.Timer
	var polled <-chan time.Time
	if c.cfg.Poll > 0 && c.cfg.Providers != nil {
		poll = time.NewTimer(c.jittered())
		defer poll.Stop()
		polled = poll.C
	}
	for {
		select {
		case <-ctx.Done():
			c.stopWanting()
			c.wg.Wait()
			return
		case <-polled:
			c.listAll()
			poll.Reset(c.jittered())
		}
	}
}

func (c *Catalog) jittered() time.Duration {
	spread := int64(c.cfg.Poll / 5)
	if spread <= 0 {
		return c.cfg.Poll
	}
	return c.cfg.Poll - time.Duration(spread/2) + time.Duration(rand.Int64N(spread))
}

func (c *Catalog) providers() []string {
	if c.cfg.Providers == nil {
		return nil
	}
	return c.cfg.Providers()
}

func (c *Catalog) staleAfter() time.Duration {
	if c.cfg.Poll > 0 && c.cfg.Poll < c.cfg.TTL {
		return c.cfg.Poll
	}
	return c.cfg.TTL
}

func (c *Catalog) listStale() {
	snap := c.Snapshot()
	now := c.cfg.Now()
	for _, name := range c.providers() {
		if !c.fresh(snap, name, now, c.staleAfter()) {
			c.list(name)
		}
	}
}

func (c *Catalog) listAll() {
	for _, name := range c.providers() {
		c.list(name)
	}
}

func (c *Catalog) Refresh(ctx context.Context, names ...string) error {
	for _, done := range c.Kick(names...) {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (c *Catalog) Kick(names ...string) []<-chan struct{} {
	if c == nil {
		return nil
	}
	if len(names) == 0 {
		names = c.providers()
	}
	waits := make([]<-chan struct{}, 0, len(names))
	for _, name := range names {
		waits = append(waits, c.list(name))
	}
	return waits
}

func (c *Catalog) list(providerName string) <-chan struct{} {
	if providerName == "" || c.cfg.Build == nil {
		return closedChan
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return closedChan
	}
	st := c.state(providerName)
	st.listNext = true
	st.attempt = c.cfg.Now()
	if st.listDone == nil {
		st.listDone = make(chan struct{})
	}
	done := st.listDone
	c.start(providerName, st)
	return done
}

var closedChan = func() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}()

func (c *Catalog) state(providerName string) *provState {
	st := c.provs[providerName]
	if st == nil {
		st = &provState{wanted: map[string]bool{}, tried: map[string]time.Time{}}
		c.provs[providerName] = st
	}
	return st
}

func (c *Catalog) start(providerName string, st *provState) {
	if st.inflight || c.closed {
		return
	}
	st.inflight = true
	c.wg.Add(1)
	go c.refresh(c.ctx, providerName)
}

func (c *Catalog) Want(providerName, model string) {
	if c == nil || providerName == "" || model == "" || c.cfg.Build == nil {
		return
	}
	snap := c.Snapshot()
	now := c.cfg.Now()
	_, known := models.Lookup(snap, providerName, model)
	if known && c.fresh(snap, providerName, now, c.cfg.TTL) {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.state(providerName)
	list := st.attempt.IsZero() || now.Sub(st.attempt) >= c.cfg.Retry
	if !known {
		if at, ok := st.tried[model]; ok && now.Sub(at) < c.cfg.Retry && !list {
			return
		}
		st.tried[model] = now
		st.wanted[model] = true
		st.again = true
	} else if !list {
		return
	}
	if list && !st.listing {
		st.listNext = true
		st.attempt = now
	}
	if !st.again && !st.listNext {
		return
	}
	c.start(providerName, st)
}

func (c *Catalog) stopWanting() {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
}

func (c *Catalog) fresh(snap form.Snapshot, providerName string, now time.Time, within time.Duration) bool {
	st, ok := models.ProviderStatus(snap, providerName)
	if !ok || st.RefreshedAt.IsZero() {
		return false
	}
	return now.Sub(st.RefreshedAt) < within
}

func (c *Catalog) refresh(ctx context.Context, providerName string) {
	defer c.wg.Done()
	for {
		c.mu.Lock()
		st := c.provs[providerName]
		wanted := make([]string, 0, len(st.wanted))
		for m := range st.wanted {
			wanted = append(wanted, m)
		}
		st.again = false
		list := st.listNext
		var done chan struct{}
		if list {
			st.listNext, st.listing = false, true
			done, st.listDone = st.listDone, nil
		}
		c.mu.Unlock()

		c.fetch(ctx, providerName, wanted, list)
		if done != nil {
			close(done)
		}

		c.mu.Lock()
		st.listing = false
		if (!st.again && !st.listNext) || ctx.Err() != nil {
			st.inflight = false
			if st.listDone != nil {
				close(st.listDone)
				st.listDone = nil
			}
			st.listNext = false
			c.mu.Unlock()
			return
		}
		c.mu.Unlock()
	}
}

func (c *Catalog) fetch(ctx context.Context, providerName string, wanted []string, list bool) {
	now := c.cfg.Now()
	prov, err := c.cfg.Build(providerName)
	if err != nil {
		if list {
			c.record(providerName, nil, now, err)
		}
		return
	}
	if table, ok := prov.(provider.ModelTable); ok {
		var seeded []provider.ModelInfo
		snap := c.Snapshot()
		for _, m := range wanted {
			if _, have := models.Lookup(snap, providerName, m); have {
				continue
			}
			if info, ok := table.TableModel(m); ok {
				seeded = append(seeded, info)
			}
		}
		if len(seeded) > 0 {
			c.merge(providerName, seeded, models.SourceTable)
		}
	}

	if !list {
		return
	}
	fctx, cancel := context.WithTimeout(ctx, c.cfg.FetchTimeout)
	listed, err := prov.Models(fctx)
	cancel()
	c.record(providerName, listed, now, err)
}

func (c *Catalog) merge(providerName string, infos []provider.ModelInfo, source string) {
	cur := c.current(providerName)
	if cur.Models == nil {
		cur.Models = map[string]models.Entry{}
	}
	for _, info := range infos {
		cur.Models[info.ID] = entryOf(info, source)
	}
	c.publish(providerName, cur)
}

func (c *Catalog) record(providerName string, listed []provider.ModelInfo, now time.Time, err error) {
	cur := c.current(providerName)
	cur.AttemptedAt = now.UnixMilli()
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			slog.Warn("models: refresh failed", "provider", providerName, "err", err)
		}
		cur.Error = err.Error()
		c.publish(providerName, cur)
		return
	}
	next := map[string]models.Entry{}
	for id, e := range cur.Models {
		if e.Source == models.SourceTable {
			next[id] = e
		}
	}
	for _, info := range listed {
		if info.ID == "" {
			continue
		}
		next[info.ID] = entryOf(info, models.SourceAPI)
	}
	cur.Models = next
	cur.RefreshedAt = now.UnixMilli()
	cur.Error = ""
	c.publish(providerName, cur)
}

func entryOf(info provider.ModelInfo, source string) models.Entry {
	name := info.Name
	if name == info.ID {
		name = ""
	}
	return models.Entry{
		Name:      name,
		Context:   models.Context{Default: info.ContextWindow, Long: info.LongContextWindow},
		MaxOutput: info.MaxTokens,
		Hidden:    info.Hidden,
		Source:    source,
	}
}

func (c *Catalog) current(providerName string) models.Provider {
	var p models.Provider
	if raw, ok := c.Snapshot().Get(providerName); ok {
		_ = json.Unmarshal(raw, &p)
	}
	return p
}

func (c *Catalog) publish(providerName string, p models.Provider) {
	raw, err := json.Marshal(p)
	if err != nil {
		slog.Error("models: encode provider", "provider", providerName, "err", err)
		return
	}
	snap, _ := c.form.Snapshot()
	patch := form.Build(snap, map[string]json.RawMessage{providerName: raw}, nil)
	if patch.IsIdentity() {
		return
	}
	if _, _, err := c.form.ApplyEffect(patch, 0); err != nil {
		slog.Error("models: publish refused", "provider", providerName, "err", err)
	}
}

func Import(f *store.Form, path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if cur, _ := f.Snapshot(); cur.Len() == 0 {
		var snap form.Snapshot
		if err := json.Unmarshal(data, &snap); err != nil {
			slog.Warn("models: old cache unreadable, dropping it", "path", path, "err", err)
		} else if snap.Len() > 0 {
			if _, _, err := f.ApplyEffect(snap.AsPatch(), 0); err != nil {
				return fmt.Errorf("models: import %s: %w", path, err)
			}
		}
	}
	return os.Remove(path)
}
