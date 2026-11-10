package angelus_test

import (
	"encoding/json"
	"os"
	"strconv"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
)

// local confines nothing, so an unusable cwd falls back to the daemon's.
func TestALocalBirthWithAnUnusableCwdTakesTheDaemonsCwd(t *testing.T) {
	r := newGrantRig(t)
	daemon, _ := os.Getwd()
	raw, _ := json.Marshal(rpc.CreateRequest{Outfits: []string{"mock"}, Cwd: "/nowhere/on/this/host"})
	out, err := r.h[rpc.MethodCreate](as("user", strconv.Itoa(os.Getuid())), raw)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := r.backend.FormState(out.(rpc.CreateResponse).FigaroID)
	if err != nil {
		t.Fatal(err)
	}
	if got := snap.Lookup("system.cwd"); got == nil || *got != daemon {
		t.Fatalf("local cwd %v, want the daemon's %q", got, daemon)
	}
}
