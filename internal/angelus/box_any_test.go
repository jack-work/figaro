package angelus_test

import (
	"testing"

	"github.com/jack-work/figaro/api/rpc"
)

// A box whose lists say "*" is the explicit form of the vanilla box: it dresses
// in any outfit and runs on any provider. judged-elsewhere runs on anthropic.
func TestABoxWhoseListsSayAnyDressesAndRunsAnywhere(t *testing.T) {
	d := boxDoorRigConfigured(t, defaultPolicy, "true", "outfits = [\"*\"]\nproviders = [\"*\"]\n")
	c := d.dial(t)
	for _, outfits := range [][]string{{"judged"}, {"judged-elsewhere"}} {
		if err := c.Call(t.Context(), rpc.MethodCreate, rpc.CreateRequest{Outfits: outfits}, nil); err != nil {
			t.Errorf("create in %v: %v", outfits, err)
		}
	}
}
