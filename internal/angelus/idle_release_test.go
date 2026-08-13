package angelus

import (
	"testing"
	"time"
)

// residentCount stands in for a backend's resident-aria count. The latch
// only ever asks that one question, and store.Backend is far too large a
// surface to implement for it.
type residentCount struct{ n int }

func (b *residentCount) EvictIdle(map[string]bool, time.Duration) int { return 0 }
func (b *residentCount) Resident() int                                { return b.n }

// Handing the arena back is a full stop-the-world collection, so the POLICY
// is what a test can hold: once per quiet period, and any work at all resets
// it. A daemon that released on every sweep would collect forever while idle.
func TestIdleReleaseFiresOncePerQuietPeriod(t *testing.T) {
	a := &Angelus{Registry: NewRegistry()}
	be := &residentCount{}

	if a.idleReleaseDue() {
		t.Fatal("released on the first quiet sweep; the latch is meant to wait")
	}
	if !a.idleReleaseDue() {
		t.Fatal("did not release on the second quiet sweep")
	}
	for i := 0; i < 5; i++ {
		if a.idleReleaseDue() {
			t.Fatalf("released again on quiet sweep %d, without any work between", i+3)
		}
	}

	// Work resets it, and the count starts over.
	a.residentFor = be
	be.n = 1
	if a.idleReleaseDue() {
		t.Fatal("released while an aria was resident")
	}
	be.n = 0
	if a.idleReleaseDue() {
		t.Fatal("released on the first quiet sweep after work")
	}
	if !a.idleReleaseDue() {
		t.Fatal("did not release on the second quiet sweep after work")
	}
}
