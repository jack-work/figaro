package modelcat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
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
	Known        func(providerName string) bool
	Interval     func(providerName string) time.Duration
	Form         *store.Form
	Plans        *store.Form
	Retry        time.Duration
	FetchTimeout time.Duration
	Now          func() time.Time
	After        func(time.Duration) <-chan time.Time
}

const (
	DefaultInterval     = 24 * time.Hour
	Tick                = 30 * time.Second
	DefaultRetry        = 10 * time.Minute
	DefaultFetchTimeout = 30 * time.Second
)

type Catalog struct {
	cfg   Config
	form  *store.Form
	plans *store.Form

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
	if cfg.Retry <= 0 {
		cfg.Retry = DefaultRetry
	}
	if cfg.FetchTimeout <= 0 {
		cfg.FetchTimeout = DefaultFetchTimeout
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.After == nil {
		cfg.After = time.After
	}
	f := cfg.Form
	if f == nil {
		f = store.NewMemForm()
	}
	plans := cfg.Plans
	if plans == nil {
		plans = store.NewMemForm()
	}
	c := &Catalog{
		cfg:   cfg,
		form:  f,
		plans: plans,
		provs: map[string]*provState{},
		ctx:   context.Background(),
	}
	c.prune()
	return c
}

func (c *Catalog) known(providerName string) bool {
	return providerName != "" && (c.cfg.Known == nil || c.cfg.Known(providerName))
}

func (c *Catalog) prune() {
	snap, _ := c.form.Snapshot()
	provs, _ := models.Providers(snap)
	var gone []string
	for name := range provs {
		if !c.known(name) || !models.Current(snap, name) {
			gone = append(gone, name)
		}
	}
	if len(gone) == 0 {
		return
	}
	sort.Strings(gone)
	slog.Info("models: dropping entries that are not providers or not this catalog version", "names", gone)
	if _, _, err := c.form.ApplyEffect(form.Build(snap, nil, gone), 0); err != nil {
		slog.Error("models: drop refused", "names", gone, "err", err)
	}
}

func (c *Catalog) Form() *store.Form { return c.form }

func (c *Catalog) PlanUsageForm() *store.Form { return c.plans }

func (c *Catalog) PlanUsage() form.Snapshot {
	if c == nil {
		return form.Snapshot{}
	}
	snap, _ := c.plans.Snapshot()
	return snap
}

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
	for {
		c.listDue()
		select {
		case <-ctx.Done():
			c.stopWanting()
			c.wg.Wait()
			return
		case <-c.cfg.After(Tick):
		}
	}
}

func (c *Catalog) providers() []string {
	if c.cfg.Providers == nil {
		return nil
	}
	return c.cfg.Providers()
}

func (c *Catalog) interval(providerName string) time.Duration {
	if c.cfg.Interval != nil {
		if d := c.cfg.Interval(providerName); d > 0 {
			return d
		}
	}
	return DefaultInterval
}

func (c *Catalog) listDue() {
	snap := c.Snapshot()
	now := c.cfg.Now()
	for _, name := range c.providers() {
		if c.due(snap, name, now) {
			c.list(name)
		}
	}
}

func (c *Catalog) due(snap form.Snapshot, providerName string, now time.Time) bool {
	if c.fresh(snap, providerName, now, c.interval(providerName)) {
		return false
	}
	if status, ok := models.ProviderStatus(snap, providerName); !ok || status.Error == "" {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.provs[providerName]
	return st == nil || st.attempt.IsZero() || now.Sub(st.attempt) >= c.cfg.Retry
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
	if !c.known(providerName) || c.cfg.Build == nil {
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
	if c == nil || !c.known(providerName) || model == "" || c.cfg.Build == nil {
		return
	}
	snap := c.Snapshot()
	now := c.cfg.Now()
	_, known := models.Lookup(snap, providerName, model)
	if known && c.fresh(snap, providerName, now, c.interval(providerName)) {
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
			c.record(providerName, nil, nil, false, now, err)
		}
		return
	}
	catalogOff := false
	if cs, ok := prov.(provider.CatalogStatus); ok && cs.CatalogOff() {
		catalogOff = true
	}
	seeder, _ := prov.(provider.ModelSeeder)
	if seeder != nil || catalogOff {
		seeded := map[string]models.Entry{}
		snap := c.Snapshot()
		for _, m := range wanted {
			if _, have := models.Lookup(snap, providerName, m); have {
				continue
			}
			if seeder != nil {
				if e, ok := seeder.SeedModel(m); ok {
					seeded[m] = e
					continue
				}
			}
			if catalogOff {
				seeded[m] = models.Entry{}
			}
		}
		if len(seeded) > 0 {
			c.merge(providerName, seeded)
		}
	}

	if !list {
		return
	}
	fctx, cancel := context.WithTimeout(ctx, c.cfg.FetchTimeout)
	listed, err := prov.Models(fctx)
	cancel()
	c.record(providerName, seeder, listed, catalogOff, now, err)
}

func (c *Catalog) merge(providerName string, seeded map[string]models.Entry) {
	cur := c.current(providerName)
	for id, e := range seeded {
		cur.Place(id, withoutEchoedName(id, e))
	}
	c.publish(providerName, cur)
}

func (c *Catalog) record(providerName string, seeder provider.ModelSeeder, listed []provider.ModelInfo, catalogOff bool, now time.Time, err error) {
	cur := c.current(providerName)
	cur.CatalogOff = catalogOff
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
	usage := map[string]models.PlanUsage{}
	for _, info := range listed {
		if info.ID == "" {
			continue
		}
		e := withoutEchoedName(info.ID, info.Entry)
		if e.PlanUsage != nil {
			usage[info.ID] = *e.PlanUsage
			e.PlanUsage = nil
		}
		next[info.ID] = e
	}
	for id, prev := range cur.Flat() {
		if _, listed := next[id]; listed {
			continue
		}
		if seeder != nil {
			if e, ok := seeder.SeedModel(id); ok {
				next[id] = withoutEchoedName(id, e)
				continue
			}
		}
		if catalogOff {
			next[id] = prev
		}
	}
	cur.Node = models.NewNode(next)
	cur.RefreshedAt = now.UnixMilli()
	cur.Error = ""
	c.publish(providerName, cur)
	c.publishPlanUsage(providerName, usage)
}

func (c *Catalog) publishPlanUsage(providerName string, usage map[string]models.PlanUsage) {
	snap, _ := c.plans.Snapshot()
	var held models.PlanNode
	if raw, ok := snap.Get(providerName); ok {
		_ = json.Unmarshal(raw, &held)
	}
	var next models.PlanNode
	for id, u := range usage {
		if before, ok := held.Find(id); ok && before.SameShown(u) {
			u = before
		}
		next.Place(id, u)
	}
	var patch form.Patch
	if next.Empty() {
		patch = form.Build(snap, nil, []string{providerName})
	} else {
		raw, err := json.Marshal(next)
		if err != nil {
			slog.Error("models: encode plan usage", "provider", providerName, "err", err)
			return
		}
		patch = form.Build(snap, map[string]json.RawMessage{providerName: raw}, nil)
	}
	if patch.IsIdentity() {
		return
	}
	if _, _, err := c.plans.ApplyEffect(patch, 0); err != nil {
		slog.Error("models: publish plan usage refused", "provider", providerName, "err", err)
	}
}

func withoutEchoedName(id string, e models.Entry) models.Entry {
	if e.Name == id {
		e.Name = ""
		delete(e.Src, "name")
	}
	return e
}

func (c *Catalog) current(providerName string) models.Provider {
	var p models.Provider
	if raw, ok := c.Snapshot().Get(providerName); ok {
		_ = json.Unmarshal(raw, &p)
	}
	if p.IR != models.IRVersion {
		p.Node = models.Node{}
	}
	p.IR = models.IRVersion
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
