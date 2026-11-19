package angelus

import (
	"testing"
	"time"
)

// #183: two quota writes on one child each priced their charge against the
// child's landed quota. A lower write landing first freed budget a fork then
// took, and a higher write priced before it landed short, so the parent
// ended past its quota.
func TestAQuotaWriteIsPricedAgainstTheWriteBeforeIt(t *testing.T) {
	c := newFanoutCache()
	c.reset([]fanoutNode{
		{id: "top", live: true, quota: 6, capped: true},
		{id: "kid", parent: "top", live: true, quota: 2, capped: true},
	}, nil)
	pass := func(err error) error { return err }
	lower, err := c.takeCharge("kid", 1, true, pass)
	if err != nil {
		t.Fatal(err)
	}
	raised := make(chan func(), 1)
	go func() {
		release, err := c.takeCharge("kid", 3, true, pass)
		if err != nil {
			t.Error(err)
			release = func() {}
		}
		raised <- release
	}()
	select {
	case release := <-raised:
		release()
		t.Fatal("a second write on kid was priced while the first was still in flight")
	case <-time.After(100 * time.Millisecond):
	}
	c.setOwn("kid", 1, true)
	lower()
	release := <-raised
	if err := c.fits("top", 3); err == nil {
		t.Fatalf("top spends %d of 6 with a raise to 3 in flight, and a fork of 3 still fits", c.spent("top"))
	}
	c.setOwn("kid", 3, true)
	release()
	if got := c.budget("top").used; got != 4 {
		t.Fatalf("top uses %d, want 4", got)
	}
}
