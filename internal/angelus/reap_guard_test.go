package angelus

import (
	"errors"
	"testing"
)

func TestTheReapGuardLetsOneSideIn(t *testing.T) {
	var g reapGuard
	done, err := g.enter("a")
	if err != nil {
		t.Fatal(err)
	}
	if _, busy := g.seal([]string{"b", "a"}); busy != "a" {
		t.Fatalf("sealed over a wake in flight (busy %q)", busy)
	}
	done()
	release, busy := g.seal([]string{"b", "a"})
	if busy != "" {
		t.Fatalf("busy %q after the wake finished", busy)
	}
	if _, err := g.enter("a"); !errors.Is(err, errBeingReaped) {
		t.Fatalf("a wake under a seal: %v", err)
	}
	if _, busy := g.seal([]string{"a"}); busy != "a" {
		t.Fatal("two seals over one node")
	}
	release()
	if done, err := g.enter("a"); err != nil {
		t.Fatalf("after the seal: %v", err)
	} else {
		done()
	}
}
