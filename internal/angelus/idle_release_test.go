package angelus

import (
	"testing"
	"time"
)

type residentCount struct{ n int }

func (b *residentCount) EvictIdle(map[string]bool, time.Duration) int { return 0 }
func (b *residentCount) Resident() int                                { return b.n }

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
