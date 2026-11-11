package angelus

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/topo"
)

// The report names arias by handle and says why each override went.
func TestNormalizeHandlerReportsDetachedAndDropped(t *testing.T) {
	hs, b, outfit := demoteHandlers(t)
	conv, err := b.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.HierarchyEngine().WriteDrawn(map[string]string{conv: "null"}, nil); err != nil {
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
		d := resp.Dropped[0]
		if d.ID != conv || d.Handle == "" || d.Under != "null" || d.Reason != topo.StaleHistoryAgrees {
			t.Fatalf("dropped %+v, want %s by handle, under the root, history agrees", d, conv)
		}
	}
}
