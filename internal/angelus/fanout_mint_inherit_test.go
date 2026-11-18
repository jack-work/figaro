package angelus

import (
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

// Defends a lane fork's own patch from the drop that keeps its parent's
// system.fanout.mint off the child: the grant in the patch must survive.
func TestALaneForkOfAMintHolderKeepsItsGrantedMaxChildren(t *testing.T) {
	hs, b, outfit := fanoutHandlers(t)
	top, _ := b.CreateConversation(outfit)
	applyKeys(t, b, top, map[string]any{form.FanoutMintKey: true})
	out, err := callForkAsFigaro(t, hs, top, rpc.ForkRequest{FigaroID: top, Patch: fanoutPatch(2)})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := b.FormState(out.Alternative)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := snap.Get(form.FanoutMaxChildrenKey); !ok || string(v) != "2" {
		t.Fatalf("a lane fork granted max_children 2 was born with %s (present %v)", v, ok)
	}
	if _, ok := snap.Get(form.FanoutMintKey); ok {
		t.Fatal("the lane fork copied its parent's system.fanout.mint")
	}
}
