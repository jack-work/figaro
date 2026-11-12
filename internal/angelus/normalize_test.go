package angelus

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/topo"
)

// The report names arias by handle and says why each override went. A
// detached aria keeps the place it had, so an override drawing it elsewhere
// still says something and stays; one naming an aria that is gone goes.
func TestNormalizeHandlerReportsDetachedAndDropped(t *testing.T) {
	hs, b, outfit := demoteHandlers(t)
	conv, err := b.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	const gone = "0123456789abcdef0123456789abcdef"
	if err := b.HierarchyEngine().WriteDrawn(map[string]string{conv: "null", gone: "null"}, nil); err != nil {
		t.Fatal(err)
	}
	for _, dryRun := range []bool{true, false} {
		params, _ := json.Marshal(rpc.NormalizeRequest{DryRun: dryRun})
		out, err := hs.h.normalize(context.Background(), params)
		if err != nil {
			t.Fatal(err)
		}
		resp := out.(rpc.NormalizeResponse)
		if resp.DryRun != dryRun || len(resp.Detached) != 1 || len(resp.Dropped) != 1 {
			t.Fatalf("dry run %v: %+v", dryRun, resp)
		}
		if a := resp.Detached[0]; a.ID != conv || a.Handle == "" {
			t.Fatalf("detached %+v, want %s by handle", a, conv)
		}
		d := resp.Dropped[0]
		if d.ID != gone || d.Under != "null" || d.Reason != topo.StaleAriaGone {
			t.Fatalf("dropped %+v, want %s, under the root, its aria gone", d, gone)
		}
	}
	n, _ := b.Node(conv)
	if n.Parent != outfit || n.Present != "null" {
		t.Fatalf("after normalize the aria answers to %q and is drawn under %q, want its outfit %s and the root still", n.Parent, n.Present, outfit)
	}
	if n.ForkPoint == outfit {
		t.Fatal("normalize left the aria reading its history through its outfit")
	}
}
