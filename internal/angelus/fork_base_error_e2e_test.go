package angelus_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/sdk"
)

// An aria whose fork base cannot be read refuses its history. These tests
// defend everything else: it can still be killed and reaped, and a daemon
// with one on disk boots, restores the other bindings and lists it with the
// error rather than as base 0. Lineage comes from the topology snapshot, so a
// file corrupted under a running daemon is seen at the next topology move
// that is not a spawn; showsBaseErr makes one and checks it was seen.

func corruptIRFork(t *testing.T, root, id string) string {
	t.Helper()
	var path string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() || filepath.Base(filepath.Dir(p)) != "ir" {
			return nil
		}
		marker, _ := os.ReadFile(filepath.Join(p, ".node"))
		if slices.Contains(strings.Split(string(marker), "\n"), "trunk="+id) {
			path = filepath.Join(p, ".fork")
			return filepath.SkipAll
		}
		return filepath.SkipDir
	})
	require.NotEmpty(t, path, "no ir directory for %s", id)
	require.NoError(t, os.WriteFile(path, []byte("garbage\n"), 0o644))
	return path
}

func showsBaseErr(t *testing.T, acli *sdk.Angelus, ctx context.Context, id string) {
	t.Helper()
	throwaway, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	_, err = acli.Kill(ctx, rpc.KillRequest{FigaroID: throwaway.FigaroID, Now: true})
	require.NoError(t, err)
	resp, err := acli.List(ctx)
	require.NoError(t, err)
	for _, f := range resp.Figaros {
		if f.ID == id {
			require.NotEmpty(t, f.BaseErr, "the daemon does not see %s's unreadable fork base", id)
			return
		}
	}
	t.Fatalf("%s is not listed", id)
}

func forkTail(t *testing.T, acli *sdk.Angelus, ctx context.Context, id string) string {
	t.Helper()
	f, err := acli.Fork(ctx, id, 0, 0, nil, nil)
	require.NoError(t, err)
	return f.Alternative
}

func family(t *testing.T, acli *sdk.Angelus, ctx context.Context) (p, c, g string) {
	t.Helper()
	root, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	p = root.FigaroID
	c = forkTail(t, acli, ctx, p)
	g = forkTail(t, acli, ctx, c)
	return p, c, g
}

func TestAnAriaWithAnUnreadableForkBaseCanBeKilled(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  func(c string) rpc.KillRequest
		gone bool
	}{
		{"kill -r now", func(c string) rpc.KillRequest { return rpc.KillRequest{FigaroID: c, Recursive: true, Now: true} }, true},
		{"kill -r with a grace", func(c string) rpc.KillRequest { return rpc.KillRequest{FigaroID: c, Recursive: true} }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, acli, ctx, dir := daemonFixtureDir(t)
			p, c, g := family(t, acli, ctx)
			corruptIRFork(t, dir+"/arias", c)
			showsBaseErr(t, acli, ctx, c)
			_, err := acli.Kill(ctx, tc.req(c))
			require.NoError(t, err)
			if tc.gone {
				require.False(t, ariaExists(a, c), "the kill left the corrupt aria")
			} else {
				require.True(t, reaped(t, a, c), "the kill did not reap the corrupt aria")
			}
			require.True(t, alive(t, a, p), "the kill took the parent")
			_ = g
		})
	}

	t.Run("kill of a leaf, now and with a grace", func(t *testing.T) {
		for _, now := range []bool{true, false} {
			a, acli, ctx, dir := daemonFixtureDir(t)
			p, c, g := family(t, acli, ctx)
			corruptIRFork(t, dir+"/arias", g)
			showsBaseErr(t, acli, ctx, g)
			_, err := acli.Kill(ctx, rpc.KillRequest{FigaroID: g, Now: now})
			require.NoError(t, err)
			if now {
				require.False(t, ariaExists(a, g))
			} else {
				require.True(t, reaped(t, a, g))
			}
			require.True(t, alive(t, a, p) && alive(t, a, c))
		}
	})
}

func TestAnAriaWithAnUnreadableForkBaseIsReapedByItsTTL(t *testing.T) {
	for _, tc := range []struct {
		name  string
		extra string
		gone  bool
	}{
		{"grace", "", false},
		{"no grace", "[memory]\nreaped_grace_hours = 0\n", true},
	} {
		for _, corrupt := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s, corrupt %v", tc.name, corrupt), func(t *testing.T) {
				a, acli, ctx, dir := daemonFixtureConfig(t, tc.extra)
				root, err := acli.Create(ctx, dress(t, "mock"), nil)
				require.NoError(t, err)
				p := root.FigaroID
				c := forkTail(t, acli, ctx, p)
				if corrupt {
					corruptIRFork(t, dir+"/arias", c)
					showsBaseErr(t, acli, ctx, c)
				}
				age(t, a, p, 72*time.Hour, 48*time.Hour, map[string]string{"system.keepalive": "0"})
				age(t, a, c, 72*time.Hour, 48*time.Hour, map[string]string{"system.ttl": "0"})
				a.SweepLifetimesNow()
				for _, id := range []string{p, c} {
					if tc.gone {
						require.False(t, ariaExists(a, id), "the sweep left %s", id)
					} else {
						require.True(t, reaped(t, a, id), "the sweep did not reap %s", id)
					}
				}
			})
		}
	}
}

func TestADaemonBootsWithAnUnreadableForkBaseOnDisk(t *testing.T) {
	a, acli, ctx, dir := daemonFixtureDir(t)
	p, c, g := family(t, acli, ctx)
	d := forkTail(t, acli, ctx, p)
	acli.Close()
	a.Shutdown(0)
	path := corruptIRFork(t, dir+"/arias", c)

	b, bcli, _, hs := daemonWithHandlers(t, dir, nil)

	bindings := filepath.Join(t.TempDir(), "bindings.json")
	held := angelus.NewRegistry()
	require.NoError(t, held.Bind(os.Getpid(), c, 0))
	require.NoError(t, held.Bind(os.Getppid(), d, 0))
	require.NoError(t, angelus.SaveBindings(held, bindings))
	angelus.RestoreBindings(b.Registry, bindings, hs.RequireAria)
	got, _, _ := b.Registry.Resolve(os.Getppid())
	require.Equal(t, d, got, "the good binding was not restored")
	require.Nil(t, b.Registry.Get(c), "restoring the bindings woke the corrupt aria")
	_, err := bcli.IRAll(context.Background(), c, 0)
	require.ErrorContains(t, err, path, "reading the corrupt aria did not refuse naming its fork base")
	var out rpc.QuaResponse
	err = dialAria(t, b, c, nil).Call(context.Background(), rpc.MethodQua, rpc.QuaRequest{Text: "hello?"}, &out)
	require.ErrorContains(t, err, path, "waking the corrupt aria did not refuse naming its fork base")
	require.Nil(t, b.Registry.Get(c), "the corrupt aria woke")

	resp, err := bcli.List(context.Background())
	require.NoError(t, err)
	seen := map[string]rpc.FigaroInfoResponse{}
	for _, f := range resp.Figaros {
		seen[f.ID] = f
	}
	require.Contains(t, seen, c, "the corrupt aria fell out of the listing")
	require.True(t, strings.Contains(seen[c].BaseErr, path), "the list entry does not carry the error: %+v", seen[c])
	require.Zero(t, seen[c].BranchedLT)
	for _, id := range []string{p, d, g} {
		require.Contains(t, seen, id)
		require.Empty(t, seen[id].BaseErr, "%s carries an error", id)
	}
	require.NotZero(t, seen[d].BranchedLT)
}

type countingProvider struct {
	mockProviderForIntegration
	sends *atomic.Int64
}

func (p *countingProvider) Send(ctx context.Context, in provider.SendInput, bus provider.Bus) error {
	p.sends.Add(1)
	return p.mockProviderForIntegration.Send(ctx, in, bus)
}

func TestALiveAriaWhoseForkBaseTurnsUnreadableSendsNoRequest(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(dir+"/outfits", 0o700))
	require.NoError(t, os.WriteFile(dir+"/outfits/mock.toml", []byte("[system]\nprovider = \"mock\"\nmodel = \"mock-model\"\n"), 0o600))
	require.NoError(t, os.WriteFile(dir+"/config.toml", []byte("default_outfit = \"mock\"\n"), 0o600))
	var sends atomic.Int64
	a, acli, ctx, _ := daemonWithProvider(t, dir, nil, func() provider.Provider {
		return &countingProvider{sends: &sends}
	})
	root, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	prompt := func(id string) {
		t.Helper()
		done := make(chan struct{}, 1)
		fcli, err := sdk.DialAria(ariaAddr(a, id), func(method string, _ json.RawMessage) {
			if method == "turn.done" {
				select {
				case done <- struct{}{}:
				default:
				}
			}
		})
		require.NoError(t, err)
		defer fcli.Close()
		_, _, err = fcli.Qua(ctx, "hello", nil)
		require.NoError(t, err)
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("the turn on %s never finished", id)
		}
		waitIdle(t, a, id)
	}
	prompt(root.FigaroID)
	c := forkTail(t, acli, ctx, root.FigaroID)
	prompt(c)
	require.NotNil(t, a.Registry.Get(c), "the child is not live")
	before := sends.Load()

	corruptIRFork(t, dir+"/arias", c)
	showsBaseErr(t, acli, ctx, c)
	prompt(c)
	require.Equal(t, before, sends.Load(), "a turn over an unreadable fork base reached the provider")

	_, err = acli.Attach(ctx, "0123456789abcdef0123456789abcdef")
	require.Error(t, err, "an aria that exists nowhere woke")
	require.Equal(t, before, sends.Load(), "an aria that exists nowhere reached the provider")
}
