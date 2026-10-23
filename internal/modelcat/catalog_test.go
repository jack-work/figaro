package modelcat

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/internal/provider"
)

type fakeProvider struct {
	mu    sync.Mutex
	list  []provider.ModelInfo
	err   error
	gate  chan struct{}
	calls *atomic.Int32
}

func (p *fakeProvider) fail(err error) {
	p.mu.Lock()
	p.err = err
	p.mu.Unlock()
}

func (c *Catalog) inflight(providerName string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.provs[providerName]
	return st != nil && st.inflight
}

func (p *fakeProvider) Name() string        { return "fake" }
func (p *fakeProvider) Fingerprint() string { return "fake/v1" }
func (p *fakeProvider) SetModel(string)     {}
func (p *fakeProvider) Send(context.Context, provider.SendInput, provider.Bus) error {
	return errors.New("not a sender")
}
func (p *fakeProvider) Models(ctx context.Context) ([]provider.ModelInfo, error) {
	p.calls.Add(1)
	if p.gate != nil {
		select {
		case <-p.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.list, p.err
}

type tableProvider struct {
	*fakeProvider
	table map[string]int
}

func (p *tableProvider) TableModel(model string) (provider.ModelInfo, bool) {
	w, ok := p.table[model]
	return provider.ModelInfo{ID: model, ContextWindow: w}, ok
}

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func newClock() *clock { return &clock{now: time.UnixMilli(1_760_000_000_000)} }

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	require.Eventually(t, cond, 2*time.Second, 2*time.Millisecond)
}

func limitOf(c *Catalog, providerName, model string) int {
	return models.EntryLimit(c.Snapshot(), providerName, model, "")
}

// A miss returns at once and the fill arrives as a commit: nothing that reads
// the catalog may wait on a provider's network.
func TestWantNeverBlocksAndFillsFromTheListing(t *testing.T) {
	var calls atomic.Int32
	gate := make(chan struct{})
	fp := &fakeProvider{calls: &calls, gate: gate, list: []provider.ModelInfo{
		{ID: "m1", Name: "Model One", ContextWindow: 200_000, LongContextWindow: 1_000_000, MaxTokens: 64_000},
		{ID: "m2", ContextWindow: 128_000},
	}}
	clk := newClock()
	c := New(Config{Build: func(string) (provider.Provider, error) { return fp, nil }, Now: clk.Now})

	var commits atomic.Int32
	c.Form().OnCommit(func(uint64, form.Patch) { commits.Add(1) })

	done := make(chan struct{})
	go func() { c.Want("gw", "m1"); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Want blocked on a provider that has not answered")
	}
	require.Equal(t, 0, limitOf(c, "gw", "m1"))

	close(gate)
	waitFor(t, func() bool { return limitOf(c, "gw", "m1") == 200_000 })

	e, ok := models.Lookup(c.Snapshot(), "gw", "m1")
	require.True(t, ok)
	require.Equal(t, models.Entry{Name: "Model One", Context: models.Context{Default: 200_000, Long: 1_000_000},
		MaxOutput: 64_000, Source: models.SourceAPI}, e)
	require.Equal(t, 128_000, limitOf(c, "gw", "m2"))
	st, ok := models.ProviderStatus(c.Snapshot(), "gw")
	require.True(t, ok)
	require.Equal(t, clk.Now().UnixMilli(), st.RefreshedAt.UnixMilli())
	require.Empty(t, st.Error)
	require.Equal(t, int32(1), calls.Load())
	require.Positive(t, commits.Load())
}

// The table lands before the listing answers, so an alias the endpoint never
// lists has a capacity in milliseconds, and keeps it through the refresh.
func TestTableSeedsAWantedAliasBeforeTheNetworkAnswers(t *testing.T) {
	var calls atomic.Int32
	gate := make(chan struct{})
	tp := &tableProvider{
		fakeProvider: &fakeProvider{calls: &calls, gate: gate, list: []provider.ModelInfo{{ID: "claude-x-20260101", ContextWindow: 400_000}}},
		table:        map[string]int{"claude-x": 1_000_000},
	}
	c := New(Config{Build: func(string) (provider.Provider, error) { return tp, nil }, Now: newClock().Now})

	c.Want("anthropic", "claude-x")
	waitFor(t, func() bool { return limitOf(c, "anthropic", "claude-x") == 1_000_000 })
	require.Equal(t, int32(1), calls.Load(), "the listing is still in flight")

	close(gate)
	waitFor(t, func() bool { return limitOf(c, "anthropic", "claude-x-20260101") == 400_000 })
	e, ok := models.Lookup(c.Snapshot(), "anthropic", "claude-x")
	require.True(t, ok, "a successful listing must not drop the table's alias")
	require.Equal(t, models.SourceTable, e.Source)
}

// A failing provider is not hammered: one attempt per retry window, its error
// on the record, and whatever it knew before kept.
func TestFailureIsRecordedAndRetriedOnlyAfterTheWindow(t *testing.T) {
	var calls, builds atomic.Int32
	fp := &fakeProvider{calls: &calls, list: []provider.ModelInfo{{ID: "m", ContextWindow: 100}}}
	clk := newClock()
	c := New(Config{
		Build: func(string) (provider.Provider, error) { builds.Add(1); return fp, nil },
		Now:   clk.Now, TTL: time.Hour, Retry: 10 * time.Minute,
	})

	c.Want("p", "m")
	waitFor(t, func() bool { return limitOf(c, "p", "m") == 100 })

	fp.fail(errors.New("401 unauthorized"))
	clk.advance(2 * time.Hour)
	c.Want("p", "m")
	waitFor(t, func() bool {
		st, _ := models.ProviderStatus(c.Snapshot(), "p")
		return st.Error == "401 unauthorized"
	})
	require.Equal(t, 100, limitOf(c, "p", "m"), "a failed refresh keeps the last good entry")

	waitFor(t, func() bool { return !c.inflight("p") })
	listings, before := calls.Load(), builds.Load()
	for i := 0; i < 5; i++ {
		c.Want("p", "m")
		c.Want("p", "unknown")
	}
	waitFor(t, func() bool { return !c.inflight("p") })
	time.Sleep(20 * time.Millisecond)
	require.Equal(t, listings, calls.Load(), "inside the retry window the provider is not listed again")
	require.Equal(t, before+1, builds.Load(), "a new id costs one table pass, and asking again costs nothing")

	fp.fail(nil)
	clk.advance(11 * time.Minute)
	c.Want("p", "m")
	waitFor(t, func() bool {
		st, _ := models.ProviderStatus(c.Snapshot(), "p")
		return st.Error == "" && calls.Load() == listings+1
	})
}

// A listing that just succeeded must not make an alias it never names wait out
// the retry window: the table answers it now, and nothing is listed again.
func TestAnUnlistedAliasAfterAFreshListingIsSeededAtOnce(t *testing.T) {
	var calls atomic.Int32
	tp := &tableProvider{
		fakeProvider: &fakeProvider{calls: &calls, list: []provider.ModelInfo{{ID: "claude-x-20260101", ContextWindow: 400_000}}},
		table:        map[string]int{"claude-old": 200_000},
	}
	c := New(Config{Build: func(string) (provider.Provider, error) { return tp, nil }, Now: newClock().Now})
	c.Want("anthropic", "claude-x-20260101")
	waitFor(t, func() bool { return limitOf(c, "anthropic", "claude-x-20260101") == 400_000 })
	waitFor(t, func() bool { return !c.inflight("anthropic") })

	c.Want("anthropic", "claude-old")
	waitFor(t, func() bool { return limitOf(c, "anthropic", "claude-old") == 200_000 })
	require.Equal(t, int32(1), calls.Load())
}

func TestFreshEntriesAreNotRefetched(t *testing.T) {
	var calls atomic.Int32
	fp := &fakeProvider{calls: &calls, list: []provider.ModelInfo{{ID: "m", ContextWindow: 100}}}
	clk := newClock()
	c := New(Config{Build: func(string) (provider.Provider, error) { return fp, nil }, Now: clk.Now, TTL: time.Hour})

	c.Want("p", "m")
	waitFor(t, func() bool { return limitOf(c, "p", "m") == 100 })
	waitFor(t, func() bool { return !c.inflight("p") })

	for i := 0; i < 10; i++ {
		c.Want("p", "m")
	}
	time.Sleep(20 * time.Millisecond)
	require.Equal(t, int32(1), calls.Load())

	clk.advance(2 * time.Hour)
	c.Want("p", "m")
	waitFor(t, func() bool { return calls.Load() == 2 })
}

// An unchanged refresh moves one key, the refresh stamp; it must not rewrite
// every entry and wake every client for nothing.
func TestAnUnchangedRefreshTouchesOnlyTheStamp(t *testing.T) {
	var calls atomic.Int32
	fp := &fakeProvider{calls: &calls, list: []provider.ModelInfo{{ID: "a", ContextWindow: 1}, {ID: "b", ContextWindow: 2}}}
	clk := newClock()
	c := New(Config{Build: func(string) (provider.Provider, error) { return fp, nil }, Now: clk.Now, TTL: time.Hour})
	c.Want("p", "a")
	waitFor(t, func() bool { return limitOf(c, "p", "b") == 2 })
	waitFor(t, func() bool { return !c.inflight("p") })

	var mu sync.Mutex
	var patches []form.Patch
	c.Form().OnCommit(func(_ uint64, p form.Patch) { mu.Lock(); patches = append(patches, p); mu.Unlock() })
	clk.advance(2 * time.Hour)
	c.Want("p", "a")
	waitFor(t, func() bool { return calls.Load() == 2 && !c.inflight("p") })

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, patches, 1)
	var keys []string
	for _, e := range patches[0].Entries() {
		keys = append(keys, e.Key)
	}
	sort.Strings(keys)
	require.Equal(t, []string{"p.attempted_at", "p.refreshed_at"}, keys)
}

func TestTheCatalogSurvivesARestart(t *testing.T) {
	var calls atomic.Int32
	fp := &fakeProvider{calls: &calls, list: []provider.ModelInfo{{ID: "gpt-4.1", ContextWindow: 1_047_576}}}
	path := filepath.Join(t.TempDir(), "models.json")
	cfg := Config{Build: func(string) (provider.Provider, error) { return fp, nil }, Path: path, PersistDelay: time.Millisecond}

	c := New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan struct{})
	go func() { c.Run(ctx); close(ran) }()
	c.Want("openai", "gpt-4.1")
	waitFor(t, func() bool { return limitOf(c, "openai", "gpt-4.1") == 1_047_576 })
	cancel()
	<-ran

	again := New(Config{Path: path})
	require.Equal(t, 1_047_576, limitOf(again, "openai", "gpt-4.1"), "a cold daemon answers from the file before any network")
}

func TestANilCatalogAnswersNothing(t *testing.T) {
	var c *Catalog
	c.Want("p", "m")
	require.Equal(t, 0, c.Limit("p", "m"))
	require.Equal(t, 0, c.Snapshot().Len())
}

func TestBuildFailureIsRecorded(t *testing.T) {
	c := New(Config{Build: func(string) (provider.Provider, error) { return nil, errors.New("unknown provider: \"nope\"") }, Now: newClock().Now})
	c.Want("nope", "m")
	waitFor(t, func() bool {
		st, ok := models.ProviderStatus(c.Snapshot(), "nope")
		return ok && st.Error != ""
	})
}

// Once Run has stopped, a late want must not start a refresh: it would race
// the WaitGroup the shutdown is already waiting on, and write after the save.
func TestAWantAfterShutdownStartsNothing(t *testing.T) {
	var builds atomic.Int32
	c := New(Config{Build: func(string) (provider.Provider, error) {
		builds.Add(1)
		return &fakeProvider{calls: new(atomic.Int32)}, nil
	}})
	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan struct{})
	go func() { c.Run(ctx); close(ran) }()
	cancel()
	<-ran

	c.Want("p", "m")
	time.Sleep(20 * time.Millisecond)
	require.Zero(t, builds.Load())
}

func runCatalog(t *testing.T, c *Catalog) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan struct{})
	go func() { c.Run(ctx); close(ran) }()
	t.Cleanup(func() { cancel(); <-ran })
}

func perProvider(fps map[string]*fakeProvider) Builder {
	return func(name string) (provider.Provider, error) {
		fp, ok := fps[name]
		if !ok {
			return nil, errors.New("unknown provider: " + name)
		}
		return fp, nil
	}
}

// The daemon lists every configured provider on its own clock, so a reader
// never waits on a provider: nobody has to want a model first.
func TestThePollListsEveryProviderWithoutAWant(t *testing.T) {
	a := &fakeProvider{calls: new(atomic.Int32), list: []provider.ModelInfo{{ID: "a1", ContextWindow: 100}}}
	b := &fakeProvider{calls: new(atomic.Int32), list: []provider.ModelInfo{{ID: "b1", ContextWindow: 200}}}
	c := New(Config{
		Build:     perProvider(map[string]*fakeProvider{"a": a, "b": b}),
		Providers: func() []string { return []string{"a", "b"} },
		Poll:      20 * time.Millisecond,
	})
	runCatalog(t, c)
	waitFor(t, func() bool { return a.calls.Load() >= 3 && b.calls.Load() >= 3 })
	require.Equal(t, 100, limitOf(c, "a", "a1"))
	require.Equal(t, 200, limitOf(c, "b", "b1"))
}

// A restart lists only what the snapshot holds stale; a fresh provider waits for
// the poll.
func TestStartListsOnlyStaleProviders(t *testing.T) {
	clk := newClock()
	fresh := &fakeProvider{calls: new(atomic.Int32), list: []provider.ModelInfo{{ID: "f1", ContextWindow: 1}}}
	stale := &fakeProvider{calls: new(atomic.Int32), list: []provider.ModelInfo{{ID: "s1", ContextWindow: 1}}}
	path := filepath.Join(t.TempDir(), "models.json")
	seed := New(Config{Build: perProvider(map[string]*fakeProvider{"fresh": fresh, "stale": stale}), Path: path, Now: clk.Now})
	require.NoError(t, seed.Refresh(context.Background(), "fresh", "stale"))
	seed.save()
	fresh.calls.Store(0)
	stale.calls.Store(0)

	clk.advance(time.Hour)
	raw := seed.current("stale")
	raw.RefreshedAt = clk.Now().Add(-7 * time.Hour).UnixMilli()
	seed.publish("stale", raw)
	seed.save()

	c := New(Config{
		Build:     perProvider(map[string]*fakeProvider{"fresh": fresh, "stale": stale}),
		Providers: func() []string { return []string{"fresh", "stale", "never"} },
		Poll:      6 * time.Hour,
		Path:      path,
		Now:       clk.Now,
	})
	runCatalog(t, c)
	waitFor(t, func() bool { return stale.calls.Load() == 1 })
	waitFor(t, func() bool {
		st, ok := models.ProviderStatus(c.Snapshot(), "never")
		return ok && st.Error != ""
	})
	require.Zero(t, fresh.calls.Load())
}

// Refresh waits for a listing that starts after the call: one already in flight
// may have begun before a login and would answer with the old credential.
func TestRefreshWaitsForAListingThatStartsAfterIt(t *testing.T) {
	gate := make(chan struct{})
	fp := &fakeProvider{calls: new(atomic.Int32), gate: gate, list: []provider.ModelInfo{{ID: "m", ContextWindow: 1}}}
	c := New(Config{Build: perProvider(map[string]*fakeProvider{"p": fp})})
	first := c.list("p")
	waitFor(t, func() bool { return fp.calls.Load() == 1 })

	refreshed := make(chan error, 1)
	go func() { refreshed <- c.Refresh(context.Background(), "p") }()
	gate <- struct{}{}
	<-first
	select {
	case <-refreshed:
		t.Fatal("Refresh returned on the listing that was already in flight")
	case <-time.After(20 * time.Millisecond):
	}
	gate <- struct{}{}
	require.NoError(t, <-refreshed)
	require.Equal(t, int32(2), fp.calls.Load())
}

func TestRefreshIsBoundedByItsContext(t *testing.T) {
	fp := &fakeProvider{calls: new(atomic.Int32), gate: make(chan struct{})}
	c := New(Config{Build: perProvider(map[string]*fakeProvider{"p": fp})})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, c.Refresh(ctx, "p"), context.DeadlineExceeded)
}

func TestThePickerFlagSurvivesIntoTheCatalog(t *testing.T) {
	fp := &fakeProvider{calls: new(atomic.Int32), list: []provider.ModelInfo{
		{ID: "shown", ContextWindow: 1}, {ID: "kept-out", ContextWindow: 1, Hidden: true},
	}}
	c := New(Config{Build: perProvider(map[string]*fakeProvider{"p": fp})})
	require.NoError(t, c.Refresh(context.Background(), "p"))
	e, ok := models.Lookup(c.Snapshot(), "p", "kept-out")
	require.True(t, ok)
	require.True(t, e.Hidden)
	e, _ = models.Lookup(c.Snapshot(), "p", "shown")
	require.False(t, e.Hidden)
}
