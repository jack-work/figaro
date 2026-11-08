package angelus

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/outfit"
	"github.com/jack-work/figaro/internal/store"
)

// The outfit's content version is kept between listings, so it must move
// the moment the outfit's file does: a stale version would show every aria
// as on the current outfit after an edit.
func TestAListedOutfitVersionFollowsAnEditToTheOutfit(t *testing.T) {
	cfg := t.TempDir()
	require := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	require(os.MkdirAll(filepath.Join(cfg, "outfits"), 0o700))
	file := filepath.Join(cfg, "outfits", "perf.toml")
	require(os.WriteFile(file, []byte("[system]\nmodel = \"a\"\n"), 0o600))
	of := outfit.New(cfg)
	of.SetStaleWindow(0)
	h := &handlers{outfitter: of}

	first, _ := h.currentOutfitHash("perf")
	if first == "" {
		t.Fatal("the outfit does not resolve")
	}
	if again, _ := h.currentOutfitHash("perf"); again != first {
		t.Fatalf("an unchanged outfit hashed %q then %q", first, again)
	}
	require(os.WriteFile(file, []byte("[system]\nmodel = \"bb\"\n"), 0o600))
	if edited, _ := h.currentOutfitHash("perf"); edited == first {
		t.Fatalf("the outfit was edited and still lists as %q", first)
	}
	h.outfitter = outfit.New(cfg)
	h.outfitter.SetStaleWindow(0)
	if got, _ := h.currentOutfitHash("perf"); got == first {
		t.Fatalf("a reloaded outfitter at the same generation took the old outfitter's %q", first)
	}
}

// A pager opening @arias reads @topology a moment later; both forms come out
// of one rebuild, so the second read takes it instead of listing every aria
// again. Past the pair window a @topology read rebuilds, and a change made in
// the meantime shows.
func TestATopologyReadTakesTheRebuildAnAriasReadJustMade(t *testing.T) {
	r := newAriasRig(t)
	ctx := authz.WithPrincipal(context.Background(), rpc.Principal{Kind: rpc.PrincipalUser, ID: "0"})
	if _, err := r.h.callerFormMethod(ctx, rpc.AriasFormID, rpc.MethodForm, nil); err != nil {
		t.Fatal(err)
	}
	w, _ := r.h.ariasWatchFor(ctx, rpc.AriasFormID)
	built := w.builtAt
	if _, err := r.h.callerFormMethod(ctx, rpc.TopologyFormID, rpc.MethodForm, nil); err != nil {
		t.Fatal(err)
	}
	if !w.builtAt.Equal(built) {
		t.Fatal("a @topology read straight after an @arias read listed every aria again")
	}

	fresh, _, err := r.backend.ForkWith(r.owned, 0, form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"mantra": json.RawMessage(`"born later"`),
	}, nil), store.MintedBy("user:0"))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(ariasPair)
	if _, err := r.h.callerFormMethod(ctx, rpc.TopologyFormID, rpc.MethodForm, nil); err != nil {
		t.Fatal(err)
	}
	if w.builtAt.Equal(built) {
		t.Fatal("a @topology read past the pair window took the old rebuild")
	}
	snap, _ := w.forms[rpc.AriasFormID].form.Snapshot()
	held := false
	for k, v := range snap.All() {
		if strings.HasSuffix(k, ".id") && strings.Trim(string(v), `"`) == fresh {
			held = true
		}
	}
	if !held {
		t.Errorf("the rebuild a late @topology read made does not hold %s, born before it", fresh)
	}
}

type listCounter struct {
	store.Backend
	lists atomic.Int32
}

func (c *listCounter) Conversations() []store.NodeView {
	c.lists.Add(1)
	return c.Backend.Conversations()
}

// A cold read builds the caller's watch and answers from that build: a second
// listing in the same read doubles the cold cost of l.
func TestAColdCallerFormReadListsOnce(t *testing.T) {
	for _, id := range []string{rpc.AriasFormID, rpc.TopologyFormID, rpc.RecentFormID} {
		t.Run(id, func(t *testing.T) {
			r := newAriasRig(t)
			counter := &listCounter{Backend: r.backend}
			r.h.angelus.Backend = counter
			ctx := authz.WithPrincipal(context.Background(), rpc.Principal{Kind: rpc.PrincipalUser, ID: "0"})
			if _, err := r.h.callerFormMethod(ctx, id, rpc.MethodForm, nil); err != nil {
				t.Fatal(err)
			}
			if n := counter.lists.Load(); n != 1 {
				t.Errorf("a cold %s read listed the store %d times, want 1", id, n)
			}
			if _, err := r.h.callerFormMethod(ctx, rpc.AriasFormID, rpc.MethodForm, nil); err != nil {
				t.Fatal(err)
			}
			if n := counter.lists.Load(); n != 2 {
				t.Errorf("a warm @arias read after it listed %d times in all, want 2: a read is current", n)
			}
		})
	}
}
