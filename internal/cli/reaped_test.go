package cli

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
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
