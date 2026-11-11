package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/sdk"
	"github.com/jack-work/jkrpc"
)

func TestParseReapedFormGroupsByRoot(t *testing.T) {
	now := time.Now()
	keys := map[string]json.RawMessage{}
	put := func(k string, v any) { raw, _ := json.Marshal(v); keys[k] = raw }
	put("items.aaa.nodes", []string{"aaa", "bbb"})
	put("items.aaa.at", now.Add(-time.Hour).UnixMilli())
	put("items.aaa.purge_at", now.Add(48*time.Hour).UnixMilli())
	put("items.aaa.mantra", "old work")
	put("items.ccc.nodes", []string{"ccc"})
	put("items.ccc.purge_at", now.Add(time.Hour).UnixMilli())
	put("grace_hours", "168")
	snap := form.Snapshot{}.Apply(form.Build(form.Snapshot{}, keys, nil))

	groups, err := parseReapedForm(snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 || groups[0].Root != "ccc" {
		t.Fatalf("groups %+v, want two, soonest purge first", groups)
	}
	g, ok := groupHolding(groups, "bbb")
	if !ok || g.Root != "aaa" || g.Mantra != "old work" {
		t.Fatalf("groupHolding(bbb) = %+v, %v; a member names its group", g, ok)
	}
	if _, ok := groupHolding(groups, "zzz"); ok {
		t.Fatal("a node that is not reaped was found in a group")
	}
}

// ls must not show what the sweep reaped, however recent its use.
func TestLsWithholdsReapedRowsAndCountsThem(t *testing.T) {
	now := time.Now().UnixMilli()
	figs := []rpc.FigaroInfoResponse{
		{ID: "live", State: "dormant", Vector: []int{0}},
		{ID: "gone", State: "dormant", ReapedAt: now, Vector: []int{1}},
	}
	kept, hidden := dropExpired(figs, now)
	if len(kept) != 1 || kept[0].ID != "live" || hidden != 1 {
		t.Fatalf("kept %v, hidden %d", kept, hidden)
	}
	if n := withheldReaped(figs, kept); n != 1 {
		t.Fatalf("withheldReaped = %d, want 1", n)
	}
}

// D1: the purge line counts what the daemon removed, not the group as listed.
// The listing said P and C; the daemon took only P.
func TestReapedPurgeCountsWhatTheDaemonRemoved(t *testing.T) {
	dir, err := os.MkdirTemp(os.Getenv("TMPDIR"), "r")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "angelus.sock")
	p, c := "0123456789abcdef0123456789abcdef", "fedcba9876543210fedcba9876543210"
	fakeRPCServer(t, sock, map[string]jkrpc.HandlerFunc{
		rpc.MethodKill: func(context.Context, json.RawMessage) (any, error) {
			return rpc.KillResponse{OK: true, Removed: []string{p}}, nil
		},
	})
	acli, err := sdk.DialAngelus(transport.UnixEndpoint(sock))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { acli.Close() })
	prev := stderrw
	var errs bytes.Buffer
	stderrw = &errs
	t.Cleanup(func() { stderrw = prev })

	if err := purgeReaped(context.Background(), acli, reapedGroup{Root: p, Nodes: []string{p, c}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errs.String(), "and 0 under it") {
		t.Fatalf("stderr = %q, want the count from Removed: and 0 under it", errs.String())
	}
}
