package modelcat

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

type Builder func(providerName string) (provider.Provider, error)

type Config struct {
	Build        Builder
	Path         string
	TTL          time.Duration
	Retry        time.Duration
	FetchTimeout time.Duration
	PersistDelay time.Duration
	Now          func() time.Time
}

const (
	DefaultTTL          = 24 * time.Hour
	DefaultRetry        = 10 * time.Minute
	DefaultFetchTimeout = 30 * time.Second
	DefaultPersistDelay = 2 * time.Second
)

type Catalog struct {
	cfg  Config
	form *store.Form

	mu     sync.Mutex
	provs  map[string]*provState
	closed bool

	persist chan struct{}
	wg      sync.WaitGroup
	ctx     context.Context
}

type provState struct {
	inflight bool
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
	if cfg.PersistDelay <= 0 {
		cfg.PersistDelay = DefaultPersistDelay
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	c := &Catalog{
		cfg:     cfg,
		form:    store.NewMemForm(),
		provs:   map[string]*provState{},
		persist: make(chan struct{}, 1),
		ctx:     context.Background(),
	}
	c.load()
	c.form.OnCommit(func(uint64, message.Patch) {
		select {
		case c.persist <- struct{}{}:
		default:
		}
	})
	return c
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
	var timer *time.Timer
	var fire <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			c.stopWanting()
			c.wg.Wait()
			c.save()
			return
		case <-c.persist:
			if timer == nil {
				timer = time.NewTimer(c.cfg.PersistDelay)
				fire = timer.C
			}
		case <-fire:
			timer, fire = nil, nil
			c.save()
		}
	}
}

func (c *Catalog) Want(providerName, model string) {
	if c == nil || providerName == "" || model == "" || c.cfg.Build == nil {
		return
	}
	snap := c.Snapshot()
	now := c.cfg.Now()
	_, known := models.Lookup(snap, providerName, model)
	if known && c.fresh(snap, providerName, now) {
		return
	}

	c.mu.Lock()
	st := c.provs[providerName]
	if st == nil {
		st = &provState{wanted: map[string]bool{}, tried: map[string]time.Time{}}
		c.provs[providerName] = st
	}
	list := st.attempt.IsZero() || now.Sub(st.attempt) >= c.cfg.Retry
	if !known {
		if at, ok := st.tried[model]; ok && now.Sub(at) < c.cfg.Retry && !list {
			c.mu.Unlock()
			return
		}
		st.tried[model] = now
		st.wanted[model] = true
	} else if !list {
		c.mu.Unlock()
		return
	}
	if st.inflight {
		if !known {
			st.again = true
		}
		c.mu.Unlock()
		return
	}
	if c.closed {
		c.mu.Unlock()
		return
	}
	ctx := c.ctx
	st.inflight = true
	if list {
		st.attempt = now
	}
	c.wg.Add(1)
	c.mu.Unlock()

	go c.refresh(ctx, providerName, list)
}

func (c *Catalog) stopWanting() {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
}

func (c *Catalog) fresh(snap form.Snapshot, providerName string, now time.Time) bool {
	st, ok := models.ProviderStatus(snap, providerName)
	if !ok || st.RefreshedAt.IsZero() {
		return false
	}
	return now.Sub(st.RefreshedAt) < c.cfg.TTL
}

func (c *Catalog) refresh(ctx context.Context, providerName string, list bool) {
	defer c.wg.Done()
	for {
		c.mu.Lock()
		st := c.provs[providerName]
		wanted := make([]string, 0, len(st.wanted))
		for m := range st.wanted {
			wanted = append(wanted, m)
		}
		st.again = false
		c.mu.Unlock()

		c.fetch(ctx, providerName, wanted, list)
		list = false

		c.mu.Lock()
		if !st.again || ctx.Err() != nil {
			st.inflight = false
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

func (c *Catalog) load() {
	if c.cfg.Path == "" {
		return
	}
	data, err := os.ReadFile(c.cfg.Path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("models: read cache", "path", c.cfg.Path, "err", err)
		}
		return
	}
	var snap form.Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		slog.Warn("models: cache unreadable, starting empty", "path", c.cfg.Path, "err", err)
		return
	}
	if snap.Len() == 0 {
		return
	}
	if _, _, err := c.form.ApplyEffect(snap.AsPatch(), 0); err != nil {
		slog.Warn("models: load cache", "err", err)
	}
}

func (c *Catalog) save() {
	if c.cfg.Path == "" {
		return
	}
	data, err := json.Marshal(c.Snapshot())
	if err != nil {
		slog.Warn("models: encode cache", "err", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(c.cfg.Path), 0o755); err != nil {
		slog.Warn("models: cache dir", "err", err)
		return
	}
	tmp := c.cfg.Path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		slog.Warn("models: write cache", "err", err)
		return
	}
	if err := os.Rename(tmp, c.cfg.Path); err != nil {
		slog.Warn("models: commit cache", "err", err)
	}
}
