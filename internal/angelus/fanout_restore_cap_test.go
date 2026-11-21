package angelus

import (
	"testing"

	"github.com/jack-work/figaro/api/rpc"
)

func TestARestoredChildCountsAgainAndIsRefusedOverTheCap(t *testing.T) {
	hs, _, top, kid := oneSlotParent(t)
	if err := callMethod(t, hs, rpc.MethodKill, rpc.KillRequest{FigaroID: kid}); err != nil {
		t.Fatal(err)
	}
	other, err := callForkAsFigaro(t, hs, top, rpc.ForkRequest{FigaroID: top})
	if err != nil {
		t.Fatal(err)
	}
	err = callMethod(t, hs, rpc.MethodReapedRestore, rpc.ReapedRestoreRequest{FigaroID: kid})
	if err == nil {
		t.Fatal("a restore past max_children 1 was admitted")
	}
	if n := hs.h.angelus.liveChildCount(top); n != 1 {
		t.Fatalf("a refused restore left %d children counted, want 1", n)
	}
	if err := callMethod(t, hs, rpc.MethodKill, rpc.KillRequest{FigaroID: other.Alternative}); err != nil {
		t.Fatal(err)
	}
	if err := callMethod(t, hs, rpc.MethodReapedRestore, rpc.ReapedRestoreRequest{FigaroID: kid}); err != nil {
		t.Fatalf("a restore into a free slot was refused: %v", err)
	}
	if n := hs.h.angelus.liveChildCount(top); n != 1 {
		t.Fatalf("a restored child counts %d, want 1", n)
	}
	requireCacheMatchesTree(t, hs, "a restore")
}
