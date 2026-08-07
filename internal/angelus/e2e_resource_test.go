package angelus_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"runtime"
	"testing"
	"text/template"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/transport"
	"github.com/jack-work/jkrpc"
)

// TestEndToEndResourceProfile is the before/after for the whole hibernation
// arc, measured through a real daemon over a real store.
//
// It builds N arias, attaches a terminal to every one, and then compares the
// two regimes the change is about:
//
//	BEFORE  every aria resident, every terminal attached  (today's behaviour
//	        with reclamation disabled)
//	AFTER   every agent reclaimed, every terminal STILL attached and still
//	        able to read
//
// The second state was previously unreachable: reclaiming an agent closed
// its listener and disconnected every client, so "attached" and "reclaimed"
// could not both be true. That is the whole point, and the memory difference
// is the dividend.
//
//	go test ./internal/angelus/ -run EndToEndResourceProfile -v
func TestEndToEndResourceProfile(t *testing.T) {
	if testing.Short() {
		t.Skip("builds many arias")
	}
	const (
		arias           = 12
		turnsPerAria    = 20
		clientsPerAria  = 1
		bigMessageBytes = 16 * 1024
	)

	a, acli, ctx := benchDaemon(t)

	// --- build: N arias, each with real history on disk ------------------
	ids := make([]string, 0, arias)
	socks := make([]string, 0, arias)
	for i := 0; i < arias; i++ {
		created, err := acli.Create(ctx, "mock", nil)
		require.NoError(t, err)
		ids = append(ids, created.FigaroID)
		socks = append(socks, created.Endpoint.Address)
	}

	// --- attach: a terminal per aria, held open for the whole test -------
	clients := make([]*jkrpc.Client, 0, arias*clientsPerAria)
	for _, sock := range socks {
		for c := 0; c < clientsPerAria; c++ {
			conn, err := net.Dial("unix", sock)
			require.NoError(t, err)
			t.Cleanup(func() { conn.Close() })
			cl := jkrpc.NewClient(jkrpc.NewConn(conn), func(string, json.RawMessage) {})
			t.Cleanup(func() { cl.Close() })
			clients = append(clients, cl)
		}
	}

	// --- drive turns so the arias have weight ---------------------------
	body := make([]byte, bigMessageBytes)
	for i := range body {
		body[i] = 'x'
	}
	for _, cl := range clients {
		for turn := 0; turn < turnsPerAria; turn++ {
			cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			var out rpc.QuaResponse
			err := cl.Call(cctx, rpc.MethodQua,
				rpc.QuaRequest{Text: fmt.Sprintf("turn %d %s", turn, body)}, &out)
			cancel()
			require.NoError(t, err)
		}
	}
	// Let every turn land before weighing anything.
	for _, id := range ids {
		require.Eventually(t, func() bool {
			f := a.Registry.Get(id)
			return f == nil || f.Info().State == "idle"
		}, 30*time.Second, 20*time.Millisecond)
	}

	before := sample(a)
	require.Equal(t, arias, before.mem.LiveArias, "not every aria is resident")
	require.Equal(t, len(clients), before.mem.AttachedClients)

	// --- reclaim every agent; terminals stay attached -------------------
	reclaimStart := time.Now()
	for _, id := range ids {
		if a.Registry.Get(id) != nil {
			require.NoError(t, a.Registry.Hibernate(id))
		}
	}
	reclaimWall := time.Since(reclaimStart)

	// Caches become eligible the moment the agent is gone; force the sweep
	// rather than waiting out a ticker.
	a.EvictNow()
	after := sample(a)

	require.Zero(t, after.mem.LiveArias, "an agent survived reclamation")
	require.Zero(t, after.mem.ResidentArias,
		"eviction could not reach the caches an agent was pinning")
	require.Equal(t, len(clients), after.mem.AttachedClients,
		"reclamation disconnected a terminal — the regression this arc exists to prevent")
	require.Equal(t, arias, after.mem.Endpoints, "an endpoint vanished with its agent")

	// --- every terminal still works, and reads do not wake -------------
	readStart := time.Now()
	for i, cl := range clients {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		var page map[string]any
		err := cl.Call(cctx, rpc.MethodRead, rpc.ReadRequest{}, &page)
		cancel()
		require.NoError(t, err, "client %d lost its connection", i)
	}
	readWall := time.Since(readStart)
	require.Zero(t, a.Registry.FigaroCount(), "a read woke an aria")

	// --- one prompt wakes exactly one aria -----------------------------
	wakeStart := time.Now()
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	var out rpc.QuaResponse
	require.NoError(t, clients[0].Call(cctx, rpc.MethodQua, rpc.QuaRequest{Text: "wake"}, &out))
	cancel()
	require.Eventually(t, func() bool { return a.Registry.Get(ids[0]) != nil },
		20*time.Second, 10*time.Millisecond)
	wakeWall := time.Since(wakeStart)
	require.Equal(t, 1, a.Registry.FigaroCount(), "a wake woke more than its own aria")

	report(t, arias, turnsPerAria, len(clients), before, after,
		reclaimWall, readWall, wakeWall)
}

type snapshot struct {
	mem  *rpc.MemStatus
	heap uint64
}

func sample(a *angelus.Angelus) snapshot {
	runtime.GC()
	runtime.GC()
	m := a.MemStatus()
	return snapshot{mem: m, heap: m.HeapAllocBytes}
}

func report(t *testing.T, arias, turns, clients int, before, after snapshot,
	reclaim, read, wake time.Duration) {
	t.Helper()

	t.Logf("")
	t.Logf("%d arias x %d turns, %d attached terminals", arias, turns, clients)
	t.Logf("")
	t.Logf("                        BEFORE        AFTER      delta")
	t.Logf("  live arias        %10d %12d %10d",
		before.mem.LiveArias, after.mem.LiveArias, after.mem.LiveArias-before.mem.LiveArias)
	t.Logf("  resident handles  %10d %12d %10d",
		before.mem.ResidentArias, after.mem.ResidentArias,
		after.mem.ResidentArias-before.mem.ResidentArias)
	t.Logf("  attached clients  %10d %12d %10d",
		before.mem.AttachedClients, after.mem.AttachedClients,
		after.mem.AttachedClients-before.mem.AttachedClients)
	t.Logf("  open endpoints    %10d %12d %10d",
		before.mem.Endpoints, after.mem.Endpoints,
		after.mem.Endpoints-before.mem.Endpoints)
	t.Logf("  goroutines        %10d %12d %10d",
		before.mem.Goroutines, after.mem.Goroutines,
		after.mem.Goroutines-before.mem.Goroutines)
	t.Logf("  resident IR rows  %10d %12d %10d",
		before.mem.ResidentIRRows, after.mem.ResidentIRRows,
		after.mem.ResidentIRRows-before.mem.ResidentIRRows)
	t.Logf("  resident IR bytes %10s %12s %10s",
		humanB(uint64(before.mem.ResidentIRBytes)), humanB(uint64(after.mem.ResidentIRBytes)),
		deltaB(uint64(before.mem.ResidentIRBytes), uint64(after.mem.ResidentIRBytes)))
	t.Logf("  heap alloc        %10s %12s %10s",
		humanB(before.heap), humanB(after.heap), deltaB(before.heap, after.heap))
	t.Logf("  heap inuse        %10s %12s %10s",
		humanB(before.mem.HeapInuseBytes), humanB(after.mem.HeapInuseBytes),
		deltaB(before.mem.HeapInuseBytes, after.mem.HeapInuseBytes))

	if before.heap > after.heap && before.mem.LiveArias > 0 {
		freed := before.heap - after.heap
		t.Logf("")
		t.Logf("  reclaimed %s across %d arias = %s per aria (%.0f%% of heap)",
			humanB(freed), before.mem.LiveArias,
			humanB(freed/uint64(before.mem.LiveArias)),
			100*float64(freed)/float64(before.heap))
	}
	t.Logf("")
	t.Logf("  wall: reclaim %d arias %v (%v each) | %d reads %v | one wake %v",
		arias, reclaim.Round(time.Millisecond),
		(reclaim / time.Duration(arias)).Round(time.Microsecond),
		clients, read.Round(time.Millisecond), wake.Round(time.Millisecond))
}

func humanB(b uint64) string {
	switch {
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(b)/(1<<10))
	}
	return fmt.Sprintf("%d B", b)
}

func deltaB(before, after uint64) string {
	if after >= before {
		return "+" + humanB(after-before)
	}
	return "-" + humanB(before-after)
}

// benchDaemon is daemonFixture with reclamation off, so the sweep never
// fires behind the measurement's back.
func benchDaemon(t *testing.T) (*angelus.Angelus, *angelus.Client, context.Context) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(dir+"/loadouts", 0700))
	require.NoError(t, os.WriteFile(dir+"/loadouts/mock.toml", []byte(`
[system]
provider = "mock"
model = "mock-model"
`), 0600))
	// dormant_after_minutes = 0 disables the sweep: this test drives
	// reclamation by hand so the numbers are attributable.
	require.NoError(t, os.WriteFile(dir+"/config.toml", []byte(`
default_loadout = "mock"

[memory]
dormant_after_minutes = 0
`), 0600))

	backend, err := store.NewXwalBackend(dir+"/arias", 0)
	require.NoError(t, err)

	loaded, err := config.Load(dir)
	require.NoError(t, err)

	a := angelus.New(angelus.Config{
		RuntimeDir: testRuntimeDir(t, dir),
		Backend:    backend,
		Settings:   loaded,
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	a.Handlers = angelus.NewHandlers(angelus.ServerConfig{
		Angelus: a,
		Config:  loaded,
		ProviderFactory: func(string, provider.Knobs) (provider.Provider, error) {
			return &mockProviderForIntegration{}, nil
		},
		Ctx:                 ctx,
		ChalkboardTemplates: template.New("t"),
	}).Map

	go a.Run(ctx)
	t.Cleanup(func() { a.Shutdown(0) })
	waitForAngelus(t, a.SocketPath)

	acli, err := angelus.DialClient(transport.UnixEndpoint(a.SocketPath))
	require.NoError(t, err)
	t.Cleanup(func() { acli.Close() })
	return a, acli, ctx
}
