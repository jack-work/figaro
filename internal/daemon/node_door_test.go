package daemon

import (
	"testing"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
)

// A node door may be configured before any peer is: the door still needs a
// membership to ask, an empty one, and must not find none.
func TestANodeWithNoPeersStillHasAnEmptyMembership(t *testing.T) {
	a := angelus.New(angelus.Config{RuntimeDir: t.TempDir(), Backend: store.NewTestBackend(t)})
	if err := federate(a, &config.Loaded{}); err != nil {
		t.Fatal(err)
	}
	if a.Peers == nil {
		t.Fatal("no membership: the node door would panic on its first log line")
	}
	if n := a.Peers.Nodes(); len(n) != 0 {
		t.Fatalf("peers %v", n)
	}
}
