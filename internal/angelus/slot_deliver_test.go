package angelus_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/figaro/wire"
	"github.com/jack-work/figaro/internal/provider"
)

// A rig can wake a dormant figaro only with form templates in hand.
func thatWakesFigaros(c *angelus.ServerConfig) { c.FormTemplates = template.New("t") }

func (r grantRig) deliver(ctx context.Context, params string) (rpc.SlotDeliverResponse, error) {
	out, err := r.h[rpc.MethodSlotDeliver](ctx, []byte(params))
	if err != nil {
		return rpc.SlotDeliverResponse{}, err
	}
	return out.(rpc.SlotDeliverResponse), nil
}

func (r grantRig) delivered(t *testing.T, aria string) []message.Content {
	t.Helper()
	log, err := r.backend.OpenFigIR(aria)
	if err != nil {
		t.Fatal(err)
	}
	var out []message.Content
	for _, entry := range log.Read() {
		for _, c := range entry.Payload.Content {
			if c.Delivery != "" {
				out = append(out, c)
			}
		}
	}
	return out
}

func (r grantRig) awaitDelivered(t *testing.T, aria string, n int) []message.Content {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		got := r.delivered(t, aria)
		if len(got) >= n || time.Now().After(deadline) {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A delivery wakes the lease's figaro and becomes one input of its
// conversation, carrying broker's message id and broker's principal, with
// the text as broker sent it. The same id again is answered and queues
// nothing, also after the daemon has forgotten what it queued, which is
// what a restart does: the ids come back from the conversation.
func TestADeliveryIsQueuedOnceToTheLeasesFigaroUnderItsMessageID(t *testing.T) {
	r := leasingRig(t)
	lease, err := r.lease(asBroker(), `{"pool":"groups","key":"chat:10"}`)
	if err != nil {
		t.Fatal(err)
	}
	text := "Ada: see <412.0:23-1180>! and @everyone"
	first, err := r.deliver(asBroker(), `{"pool":"groups","key":"chat:10","generation":1,"id":"msg:-100:77","text":"`+text+`"}`)
	if err != nil || !first.Queued {
		t.Fatalf("the delivery answered %+v (%v)", first, err)
	}
	got := r.awaitDelivered(t, lease.Aria, 1)
	if len(got) != 1 || got[0].Delivery != "msg:-100:77" || got[0].Text != text || got[0].Principal != "user:broker" || got[0].Sender != "" {
		t.Fatalf("the figaro's conversation holds %+v, want one input with the id, broker's principal and the text untouched", got)
	}
	waitIdle(t, r.a, lease.Aria)
	if again, err := r.deliver(asBroker(), `{"pool":"groups","key":"chat:10","generation":1,"id":"msg:-100:77","text":"`+text+`"}`); err != nil || !again.Queued {
		t.Errorf("the same delivery again answered %+v (%v)", again, err)
	}
	r.a.Slots.ForgetDeliveries(lease.Box)
	if again, err := r.deliver(asBroker(), `{"pool":"groups","key":"chat:10","generation":1,"id":"msg:-100:77","text":"`+text+`"}`); err != nil || !again.Queued {
		t.Errorf("the same delivery after a restart answered %+v (%v)", again, err)
	}
	if _, err := r.deliver(asBroker(), `{"pool":"groups","key":"chat:10","generation":1,"id":"msg:-100:78","text":"next"}`); err != nil {
		t.Fatal(err)
	}
	got = r.awaitDelivered(t, lease.Aria, 2)
	waitIdle(t, r.a, lease.Aria)
	if got = r.delivered(t, lease.Aria); len(got) != 2 || got[0].Delivery != "msg:-100:77" || got[1].Delivery != "msg:-100:78" || got[1].Text != "next" {
		t.Errorf("after a repeat, a restart and a second message the conversation holds %+v, want each id once", got)
	}
}

// What a delivery is refused for, and that a refused one queues nothing:
// another generation, a lease that is being released, a key or a pool the
// caller does not hold, a text over the pool's cap or empty, an id that is
// no id, a field the method does not take.
func TestADeliveryIsRefusedForAnyLeaseButTheCallersLiveOne(t *testing.T) {
	r := leasingRig(t)
	lease, err := r.lease(asBroker(), `{"pool":"groups","key":"chat:10"}`)
	if err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("x", 16<<10+1)
	for why, c := range map[string]struct {
		ctx    context.Context
		params string
		reason string
	}{
		"for another generation":        {asBroker(), `{"pool":"groups","key":"chat:10","generation":2,"id":"m1","text":"t"}`, rpc.SlotStale},
		"with no generation":            {asBroker(), `{"pool":"groups","key":"chat:10","id":"m1","text":"t"}`, rpc.SlotStale},
		"by another holder":             {as("user", "rival"), `{"pool":"groups","key":"chat:10","generation":1,"id":"m1","text":"t"}`, rpc.SlotNoSuchLease},
		"for a key with no lease":       {asBroker(), `{"pool":"groups","key":"chat:11","generation":1,"id":"m1","text":"t"}`, rpc.SlotNoSuchLease},
		"to a disabled pool":            {asBroker(), `{"pool":"closed","key":"chat:10","generation":1,"id":"m1","text":"t"}`, rpc.SlotPoolDisabled},
		"over the pool's cap":           {asBroker(), `{"pool":"groups","key":"chat:10","generation":1,"id":"m1","text":"` + big + `"}`, rpc.SlotTooLarge},
		"with no text":                  {asBroker(), `{"pool":"groups","key":"chat:10","generation":1,"id":"m1","text":""}`, ""},
		"with no id":                    {asBroker(), `{"pool":"groups","key":"chat:10","generation":1,"text":"t"}`, ""},
		"with an id that is no id":      {asBroker(), `{"pool":"groups","key":"chat:10","generation":1,"id":"m 1","text":"t"}`, ""},
		"with a field it does not take": {asBroker(), `{"pool":"groups","key":"chat:10","generation":1,"id":"m1","text":"t","aria":"x"}`, ""},
		"to a pool that is not there":   {asBroker(), `{"pool":"nowhere","key":"chat:10","generation":1,"id":"m1","text":"t"}`, ""},
	} {
		_, err := r.deliver(c.ctx, c.params)
		if err == nil {
			t.Errorf("a delivery %s was queued", why)
			continue
		}
		if c.reason != "" {
			if got := slotRefusal(t, err).Reason; got != c.reason {
				t.Errorf("a delivery %s was refused as %q, want %q", why, got, c.reason)
			}
		}
		if strings.Contains(err.Error(), "slot0") || strings.Contains(err.Error(), lease.Aria) {
			t.Errorf("the refusal %s names the lease's box or figaro: %v", why, err)
		}
	}
	rou2Of(r).closeFails(errors.New("rou2 is away"))
	if err := r.release(asBroker(), `{"pool":"groups","key":"chat:10","generation":1}`); err == nil {
		t.Fatal("the release was to fail")
	}
	if _, err := r.deliver(asBroker(), `{"pool":"groups","key":"chat:10","generation":1,"id":"m1","text":"t"}`); err == nil || slotRefusal(t, err).Reason != rpc.SlotStale {
		t.Errorf("a delivery to a lease that is being released answered %v, want %s", err, rpc.SlotStale)
	}
	rou2Of(r).closeFails(nil)
	if err := r.release(asBroker(), `{"pool":"groups","key":"chat:10","generation":1}`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.deliver(asBroker(), `{"pool":"groups","key":"chat:10","generation":1,"id":"m1","text":"t"}`); err == nil || slotRefusal(t, err).Reason != rpc.SlotNoSuchLease {
		t.Errorf("a delivery to a released lease answered %v, want %s", err, rpc.SlotNoSuchLease)
	}
	next, err := r.lease(asBroker(), `{"pool":"groups","key":"chat:10"}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.delivered(t, next.Aria); len(got) != 0 {
		t.Errorf("the next generation's figaro was born holding deliveries: %+v", got)
	}
	if _, err := r.deliver(asBroker(), `{"pool":"groups","key":"chat:10","generation":1,"id":"m1","text":"t"}`); err == nil || slotRefusal(t, err).Reason != rpc.SlotStale {
		t.Errorf("a delivery for the last generation answered %v, want %s", err, rpc.SlotStale)
	}
	if got, err := r.deliver(asBroker(), `{"pool":"groups","key":"chat:10","generation":2,"id":"m1","text":"t"}`); err != nil || !got.Queued {
		t.Errorf("the id a refused delivery carried is taken for the next generation: %+v (%v)", got, err)
	}
	if got := r.awaitDelivered(t, next.Aria, 1); len(got) != 1 {
		t.Errorf("the next generation's figaro holds %+v, want the one delivery", got)
	}
	waitIdle(t, r.a, next.Aria)
}

// A figaro in a leased box spends only under its lease (B4): the provider
// it is woken with is built with the rou2 lease its ledger row names, a
// provider that would not send one is refused, and so is any provider
// once the lease is no longer live.
func TestALeasedFigarosProviderCarriesItsRou2LeaseOrIsNotBuilt(t *testing.T) {
	r := leasingRig(t)
	lease, err := r.lease(asBroker(), `{"pool":"groups","key":"chat:10"}`)
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.hs.ProviderIn(lease.Box, "mock")
	if err != nil {
		t.Fatal(err)
	}
	if got := p.(*leasedMock).Lease(); got != "groups/slot0-g1" {
		t.Errorf("the leased figaro's provider was built with lease %q, want groups/slot0-g1", got)
	}
	if p, err := r.hs.ProviderIn("", "mock"); err != nil || p.(*leasedMock).Lease() != "" {
		t.Errorf("a figaro in no box got a provider with a lease: %v (%v)", p, err)
	}
	if _, err := r.hs.ProviderIn("slot0-g9", "mock"); err == nil {
		t.Error("a provider was built for a generation of a slot that no lease holds")
	}
	if _, err := r.hs.ProviderIn("slot5-g1", "mock"); err == nil {
		t.Error("a provider was built for a slot with no lease")
	}
	rou2Of(r).closeFails(errors.New("rou2 is away"))
	if err := r.release(asBroker(), `{"pool":"groups","key":"chat:10","generation":1}`); err == nil {
		t.Fatal("the release was to fail")
	}
	if _, err := r.hs.ProviderIn(lease.Box, "mock"); err == nil {
		t.Error("a provider was built for a lease that is being released")
	}
}

func TestAProviderThatSendsNoLeaseIsNotBuiltForALeasedFigaro(t *testing.T) {
	r := newRigProviding(t, leasingPolicy, leasingConfig, func(string, provider.Knobs) (provider.Provider, error) {
		return &mockProviderForIntegration{}, nil
	}, thatWakesFigaros)
	if err := wire.Install(r.backend.Store(), t.TempDir(), wire.Capabilities{Trunks: true}); err != nil {
		t.Fatal(err)
	}
	r.writeLeasedOutfit(t, leasedOutfit)
	slots, err := angelus.OpenSlotPools(t.TempDir(), r.a.Settings)
	if err != nil {
		t.Fatal(err)
	}
	r.a.Slots = slots
	r.a.Policies.SetLeases(slots)
	installRou2(r)
	lease, err := r.lease(asBroker(), `{"pool":"groups","key":"chat:10"}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.hs.ProviderIn(lease.Box, "mock"); err == nil {
		t.Error("a provider that sends no rou2 lease was built for a figaro in a leased box")
	}
	if _, err := r.deliver(asBroker(), `{"pool":"groups","key":"chat:10","generation":1,"id":"m1","text":"t"}`); err == nil || !strings.Contains(err.Error(), "sends no rou2 lease") {
		t.Errorf("a delivery to a leased figaro on a provider that sends no rou2 lease answered %v, want the provider refused", err)
	}
	if _, err := r.hs.ProviderIn("", "mock"); err != nil {
		t.Errorf("a figaro in no box is refused the same provider: %v", err)
	}
}
