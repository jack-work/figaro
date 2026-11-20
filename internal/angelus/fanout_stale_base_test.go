package angelus

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/store"
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

// #183 M2: lowering a quota while births under it were in flight priced the
// change against its landed use alone, so it freed budget above that those
// births then took back when they landed.
func TestLoweringAQuotaUnderInFlightBirthsNeverOverdrawsTheParent(t *testing.T) {
	pass := func(err error) error { return err }
	c := newFanoutCache()
	c.put(fanoutNode{id: "top", live: true, capped: true, quota: 10})
	c.put(fanoutNode{id: "a", parent: "top", live: true, capped: true, quota: 5})
	c.put(fanoutNode{id: "b", parent: "top", live: true, capped: true, quota: 3})
	var holds []fanoutHold
	for range 3 {
		h, err := c.take("a", 100, false, 1, false, pass)
		if err != nil {
			t.Fatal(err)
		}
		holds = append(holds, h)
	}
	lower, err := c.takeCharge("a", 0, true, pass)
	if err != nil {
		t.Fatalf("lowering a: %v", err)
	}
	c.setOwn("a", 0, true)
	lower()
	free := 10 - c.budget("top").used
	if free != 2 {
		t.Errorf("lowering a to 0 with 3 births in flight frees %d at top, want 2", free)
	}
	raise, err := c.takeCharge("b", 3+free, true, pass)
	if err != nil {
		t.Fatalf("raising b into what is free: %v", err)
	}
	c.setOwn("b", 3+free, true)
	raise()
	if _, err := c.takeCharge("b", 4+free, true, pass); err == nil {
		t.Error("b was raised past what top has free")
	}
	for i, h := range holds {
		c.put(fanoutNode{id: "a" + string(rune('0'+i)), parent: "a", live: true})
		c.give(h, "")
	}
	if got := c.budget("top"); got.used > got.quota {
		t.Errorf("top uses %d of quota %d", got.used, got.quota)
	}
	if d := c.drift([]fanoutNode{
		{id: "top", live: true, capped: true, quota: 10},
		{id: "a", parent: "top", live: true, capped: true, quota: 0},
		{id: "b", parent: "top", live: true, capped: true, quota: 3 + free},
		{id: "a0", parent: "a", live: true}, {id: "a1", parent: "a", live: true}, {id: "a2", parent: "a", live: true},
	}, nil); len(d) > 0 {
		t.Errorf("the cache drifted from the tree once the births landed: %+v", d)
	}
}

// #183 L1: a seat wrote its successor's quota outside the serialization of
// quota writes, so it raced a write already in flight on the successor.
func TestASeatsQuotaMoveWaitsForAQuotaWriteOnTheSuccessor(t *testing.T) {
	be, err := store.NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { be.Close() })
	born := func(kv map[string]json.RawMessage) string {
		id, _, err := be.ForkWith("", 0, form.Build(form.Snapshot{}, kv, nil))
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	pred := born(map[string]json.RawMessage{form.FanoutQuotaKey: json.RawMessage(`4`)})
	succ := born(map[string]json.RawMessage{"mantra": json.RawMessage(`"s"`)})
	a := &Angelus{Backend: be}
	release, err := a.fanoutCounts().takeCharge(succ, 2, true, func(err error) error { return err })
	if err != nil {
		t.Fatal(err)
	}
	moved := make(chan error, 1)
	go func() { moved <- a.moveQuota(pred, succ) }()
	select {
	case err := <-moved:
		release()
		t.Fatalf("the seat wrote the successor's quota with a quota write in flight on it (err %v)", err)
	case <-time.After(100 * time.Millisecond):
	}
	release()
	if err := <-moved; err != nil {
		t.Fatal(err)
	}
}
