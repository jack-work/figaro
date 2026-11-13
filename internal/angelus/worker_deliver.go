package angelus

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/workerpool"
)

type takesDeliveries interface {
	SubmitDelivery(principal, delivery, text string) error
	UnansweredDeliveries() []string
}

func (h *handlers) deliveriesInStore(l workerpool.Lease, woken takesDeliveries) (map[string]bool, error) {
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

func (h *handlers) workerDeliver(ctx context.Context, params json.RawMessage) (any, error) {
	var req rpc.WorkerDeliverRequest
	if err := decodeExactly(params, &req); err != nil {
		return nil, fmt.Errorf("%s takes pool, key, generation, id and text and nothing else: %w", rpc.MethodWorkerDeliver, err)
	}
	workers, err := h.workerPool(rpc.MethodWorkerDeliver, req.Pool)
	if err != nil {
		return nil, err
	}
	if err := workerpool.CheckKey(req.Key); err != nil {
		return nil, fmt.Errorf("%s: %w", rpc.MethodWorkerDeliver, err)
	}
	if err := workerpool.CheckKey(req.ID); err != nil {
		return nil, fmt.Errorf("%s: the message id: %w", rpc.MethodWorkerDeliver, err)
	}
	loaded, _ := h.settings()
	pool, _ := loaded.Pool(req.Pool)
	if !pool.Enabled {
		return nil, workerRefused(rpc.WorkerRefusal{Reason: rpc.WorkerPoolDisabled, Pool: req.Pool}, "%s: pool %s is disabled", rpc.MethodWorkerDeliver, req.Pool)
	}
	if req.Text == "" {
		return nil, fmt.Errorf("%s: a delivery with no text is no message", rpc.MethodWorkerDeliver)
	}
	if len(req.Text) > pool.DeliverCap() {
		return nil, workerRefused(rpc.WorkerRefusal{Reason: rpc.WorkerTooLarge, Pool: req.Pool}, "%s: the text is %d bytes and the pool takes %d", rpc.MethodWorkerDeliver, len(req.Text), pool.DeliverCap())
	}
	holder := authz.PrincipalText(authz.PrincipalOf(ctx))
	workers.leasing.Lock()
	defer workers.leasing.Unlock()
	held, ok := workers.Ledger.Held(req.Pool, req.Key)
	if !ok || held.Holder != holder {
		return nil, workerRefused(rpc.WorkerRefusal{Reason: rpc.WorkerNoSuchLease, Pool: req.Pool}, "%s: no such lease", rpc.MethodWorkerDeliver)
	}
	if held.Generation != req.Generation || held.State != workerpool.Live {
		return nil, workerRefused(rpc.WorkerRefusal{Reason: rpc.WorkerStale, Pool: req.Pool}, "%s: that generation of the lease takes no delivery", rpc.MethodWorkerDeliver)
	}
	aria, _ := h.leaseSeat(held)
	if aria == "" {
		return nil, fmt.Errorf("%s: the lease's role names no figaro", rpc.MethodWorkerDeliver)
	}
	f, err := h.restoreByID(ctx, aria, rpc.MethodWorkerDeliver)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", rpc.MethodWorkerDeliver, err)
	}
	seat, ok := f.(takesDeliveries)
	if !ok {
		return nil, fmt.Errorf("%s: the lease's figaro takes no delivery", rpc.MethodWorkerDeliver)
	}
	seen, err := workers.deliveredTo(config.LeasedBoxName(held.Worker, held.Generation), func() (map[string]bool, error) {
		return h.deliveriesInStore(held, seat)
	})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", rpc.MethodWorkerDeliver, err)
	}
	if seen[req.ID] {
		return rpc.WorkerDeliverResponse{Queued: true}, nil
	}
	if err := seat.SubmitDelivery(holder, req.ID, req.Text); err != nil {
		return nil, fmt.Errorf("%s: %w", rpc.MethodWorkerDeliver, err)
	}
	seen[req.ID] = true
	return rpc.WorkerDeliverResponse{Queued: true}, nil
}
