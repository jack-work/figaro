package angelus

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/slotpool"
	"github.com/jack-work/figaro/internal/store"
)

type takesDeliveries interface {
	SubmitDelivery(principal, delivery, text string) error
	UnansweredDeliveries() []string
}

func (h *handlers) deliveriesInStore(l slotpool.Lease, woken takesDeliveries) (map[string]bool, error) {
	seen := map[string]bool{}
	backend := h.angelus.Backend
	for _, id := range l.Minted {
		n, ok := backend.Node(id)
		if !ok || n.Kind != store.KindConversation {
			continue
		}
		log, err := backend.OpenFigIR(id)
		if err != nil {
			return nil, fmt.Errorf("the deliveries %s already holds cannot be read: %w", id, err)
		}
		for _, entry := range log.Read() {
			for _, c := range entry.Payload.Content {
				if c.Delivery != "" {
					seen[c.Delivery] = true
				}
			}
		}
	}
	for _, id := range woken.UnansweredDeliveries() {
		seen[id] = true
	}
	return seen, nil
}

func (h *handlers) slotDeliver(ctx context.Context, params json.RawMessage) (any, error) {
	var req rpc.SlotDeliverRequest
	if err := decodeExactly(params, &req); err != nil {
		return nil, fmt.Errorf("%s takes pool, key, generation, id and text and nothing else: %w", rpc.MethodSlotDeliver, err)
	}
	slots, err := h.slotPool(rpc.MethodSlotDeliver, req.Pool)
	if err != nil {
		return nil, err
	}
	if err := slotpool.CheckKey(req.Key); err != nil {
		return nil, fmt.Errorf("%s: %w", rpc.MethodSlotDeliver, err)
	}
	if err := slotpool.CheckKey(req.ID); err != nil {
		return nil, fmt.Errorf("%s: the message id: %w", rpc.MethodSlotDeliver, err)
	}
	loaded, _ := h.settings()
	pool, _ := loaded.Pool(req.Pool)
	if !pool.Enabled {
		return nil, slotRefused(rpc.SlotRefusal{Reason: rpc.SlotPoolDisabled, Pool: req.Pool}, "%s: pool %s is disabled", rpc.MethodSlotDeliver, req.Pool)
	}
	if req.Text == "" {
		return nil, fmt.Errorf("%s: a delivery with no text is no message", rpc.MethodSlotDeliver)
	}
	if len(req.Text) > pool.DeliverCap() {
		return nil, slotRefused(rpc.SlotRefusal{Reason: rpc.SlotTooLarge, Pool: req.Pool}, "%s: the text is %d bytes and the pool takes %d", rpc.MethodSlotDeliver, len(req.Text), pool.DeliverCap())
	}
	holder := authz.PrincipalText(authz.PrincipalOf(ctx))
	slots.leasing.Lock()
	defer slots.leasing.Unlock()
	held, ok := slots.Ledger.Held(req.Pool, req.Key)
	if !ok || held.Holder != holder {
		return nil, slotRefused(rpc.SlotRefusal{Reason: rpc.SlotNoSuchLease, Pool: req.Pool}, "%s: no such lease", rpc.MethodSlotDeliver)
	}
	if held.Generation != req.Generation || held.State != slotpool.Live {
		return nil, slotRefused(rpc.SlotRefusal{Reason: rpc.SlotStale, Pool: req.Pool}, "%s: that generation of the lease takes no delivery", rpc.MethodSlotDeliver)
	}
	aria, _ := h.leaseSeat(held)
	if aria == "" {
		return nil, fmt.Errorf("%s: the lease's role names no figaro", rpc.MethodSlotDeliver)
	}
	f, err := h.restoreByID(ctx, aria, rpc.MethodSlotDeliver)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", rpc.MethodSlotDeliver, err)
	}
	seat, ok := f.(takesDeliveries)
	if !ok {
		return nil, fmt.Errorf("%s: the lease's figaro takes no delivery", rpc.MethodSlotDeliver)
	}
	seen, err := slots.deliveredTo(config.LeasedBoxName(held.Slot, held.Generation), func() (map[string]bool, error) {
		return h.deliveriesInStore(held, seat)
	})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", rpc.MethodSlotDeliver, err)
	}
	if seen[req.ID] {
		return rpc.SlotDeliverResponse{Queued: true}, nil
	}
	if err := seat.SubmitDelivery(holder, req.ID, req.Text); err != nil {
		return nil, fmt.Errorf("%s: %w", rpc.MethodSlotDeliver, err)
	}
	seen[req.ID] = true
	return rpc.SlotDeliverResponse{Queued: true}, nil
}
