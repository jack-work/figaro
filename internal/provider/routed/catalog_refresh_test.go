package routed_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/modelcat"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/provider/routed"
)

type refreshClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *refreshClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *refreshClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func gatewayCatalog(capacity *atomic.Value, hits *atomic.Int32) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		status, window := "unknown", "null"
		if c := capacity.Load().(int); c > 0 {
			status, window = "known", fmt.Sprint(c)
		}
		fmt.Fprintf(w, `{"data":[{"id":"auto","context_length":1000000,"supported_endpoints":["/responses"],"rou2_auto":{"generation":"g1","context_status":%q,"candidates":[{"model":"a","apis":["responses"],"context_length":1000000},{"model":"b","apis":["messages"],"context_length":%s}]}}]}`, status, window)
	})
}

func catalogOf(t *testing.T, name, providerFile string, clk *refreshClock, ticks chan chan time.Time) *modelcat.Catalog {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "providers"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "providers", name+".toml"), []byte(providerFile), 0o600))
	loaded, err := config.Load(dir)
	require.NoError(t, err)
	routed.RegisterDeclared(loaded)
	c := modelcat.New(modelcat.Config{
		Build: func(n string) (provider.Provider, error) {
			return provider.Lookup(n).Build(provider.BuildContext{Loaded: loaded})
		},
		Providers: func() []string { return []string{name} },
		Interval:  loaded.CatalogRefresh,
		Now:       clk.Now,
		After: func(time.Duration) <-chan time.Time {
			ch := make(chan time.Time, 1)
			ticks <- ch
			return ch
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return c
}

func TestAGatewayCatalogChangedInPlaceIsSeenAfterCatalogRefreshWithoutAModelRequest(t *testing.T) {
	var capacity atomic.Value
	var hits atomic.Int32
	capacity.Store(0)
	srv := httptest.NewServer(gatewayCatalog(&capacity, &hits))
	defer srv.Close()
	clk := &refreshClock{now: time.UnixMilli(1_760_000_000_000)}
	ticks := make(chan chan time.Time)
	c := catalogOf(t, "gwrefresh", "preset = \"gateway\"\ncatalog_refresh = \"2m\"\nbase_url = \""+srv.URL+"/v1\"\n", clk, ticks)

	tick := <-ticks
	require.Eventually(t, func() bool { return hits.Load() == 1 }, 2*time.Second, 2*time.Millisecond)
	require.Eventually(t, func() bool { _, ok := models.ProviderStatus(c.Snapshot(), "gwrefresh"); return ok }, 2*time.Second, 2*time.Millisecond)
	require.Zero(t, models.EntryLimit(c.Snapshot(), "gwrefresh", "auto", ""))

	capacity.Store(128000)
	clk.advance(time.Minute)
	tick <- clk.Now()
	tick = <-ticks
	require.Equal(t, int32(1), hits.Load())
	require.Zero(t, models.EntryLimit(c.Snapshot(), "gwrefresh", "auto", ""))

	clk.advance(90 * time.Second)
	tick <- clk.Now()
	<-ticks
	require.Eventually(t, func() bool {
		return models.EntryLimit(c.Snapshot(), "gwrefresh", "auto", "") == 128000
	}, 2*time.Second, 2*time.Millisecond)
	require.Equal(t, int32(2), hits.Load())
}

func TestAGatewayWithoutCatalogRefreshKeepsItsCatalogForADay(t *testing.T) {
	var capacity atomic.Value
	var hits atomic.Int32
	capacity.Store(0)
	srv := httptest.NewServer(gatewayCatalog(&capacity, &hits))
	defer srv.Close()
	clk := &refreshClock{now: time.UnixMilli(1_760_000_000_000)}
	ticks := make(chan chan time.Time)
	c := catalogOf(t, "gwdaily", "preset = \"gateway\"\nbase_url = \""+srv.URL+"/v1\"\n", clk, ticks)

	tick := <-ticks
	require.Eventually(t, func() bool { return hits.Load() == 1 }, 2*time.Second, 2*time.Millisecond)

	capacity.Store(128000)
	clk.advance(23 * time.Hour)
	tick <- clk.Now()
	tick = <-ticks
	require.Equal(t, int32(1), hits.Load())
	require.Zero(t, models.EntryLimit(c.Snapshot(), "gwdaily", "auto", ""))

	clk.advance(2 * time.Hour)
	tick <- clk.Now()
	<-ticks
	require.Eventually(t, func() bool {
		return models.EntryLimit(c.Snapshot(), "gwdaily", "auto", "") == 128000
	}, 2*time.Second, 2*time.Millisecond)
}
