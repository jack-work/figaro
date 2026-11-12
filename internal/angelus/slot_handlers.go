package angelus

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jack-work/jkrpc"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/slotpool"
	"github.com/jack-work/figaro/internal/store"
)

const leaseRetryAfter = 5

func decodeExactly(params json.RawMessage, into any) error {
	dec := json.NewDecoder(bytes.NewReader(params))
	dec.DisallowUnknownFields()
	return dec.Decode(into)
}

func (h *handlers) slotPool(method, pool string) (*SlotPools, error) {
	slots := h.angelus.Slots
	if slots == nil {
		return nil, fmt.Errorf("%s: this daemon leases no slots", method)
	}
	loaded, _ := h.settings()
	if _, ok := loaded.Pool(pool); !ok {
		return nil, fmt.Errorf("%s: no pool %q", method, pool)
	}
	return slots, nil
}

func (h *handlers) slotPages(ctx context.Context, params json.RawMessage) (any, error) {
	var req rpc.SlotPagesRequest
	if err := decodeExactly(params, &req); err != nil {
		return nil, fmt.Errorf("%s takes pool, seen and wait and nothing else: %w", rpc.MethodSlotPages, err)
	}
	slots, err := h.slotPool(rpc.MethodSlotPages, req.Pool)
	if err != nil {
		return nil, err
	}
	if req.Wait < 0 || req.Wait > int(slotpool.MaxPageWait/time.Second) {
		return nil, fmt.Errorf("%s: %w, and wait was %d", rpc.MethodSlotPages, slotpool.ErrPageWait, req.Wait)
	}
	held, err := slots.Ledger.Pages(ctx, req.Pool, req.Seen, time.Duration(req.Wait)*time.Second)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", rpc.MethodSlotPages, err)
	}
	pages := make([]rpc.SlotPage, 0, len(held))
	for _, p := range held {
		pages = append(pages, rpc.SlotPage{ID: p.ID, At: p.At, Pool: p.Pool, Key: p.Key, Generation: p.Generation, Kind: string(p.Kind), Figaro: p.Figaro, Dropped: p.Dropped})
	}
	return rpc.SlotPagesResponse{Pages: pages}, nil
}

func slotRefused(data rpc.SlotRefusal, format string, args ...any) error {
	raw, _ := json.Marshal(data)
	return &jkrpc.Error{Code: rpc.ErrCodeSlot, Message: fmt.Sprintf(format, args...), Data: raw}
}

func (h *handlers) leaseSeat(l slotpool.Lease) (aria, role string) {
	backend := h.angelus.Backend
	for _, id := range l.Minted {
		if n, ok := backend.Node(id); ok && n.Kind == store.KindForm {
			role = id
			break
		}
	}
	if snap, err := backend.FormState(role); role != "" && err == nil {
		if target := snap.Lookup("target-aria"); target != nil {
			aria = *target
		}
	}
	return aria, role
}

func (h *handlers) leaseAnswer(l slotpool.Lease) rpc.SlotLeaseResponse {
	aria, role := h.leaseSeat(l)
	return rpc.SlotLeaseResponse{Pool: l.Pool, Key: l.Key, Generation: l.Generation, Box: config.LeasedBoxName(l.Slot, l.Generation), Aria: aria, Role: role, SayBearer: l.SayBearer}
}

func freeSlot(pool config.PoolConfig, name string, leases []slotpool.Lease) (slot string, leased, releasing int) {
	taken := map[string]bool{}
	for _, l := range leases {
		if l.Pool != name || leaseIsOver(l) {
			continue
		}
		taken[l.Slot] = true
		if l.State == slotpool.Releasing {
			releasing++
		} else {
			leased++
		}
	}
	for _, s := range pool.SlotNames() {
		if !taken[s] {
			return s, leased, releasing
		}
	}
	return "", leased, releasing
}

func (h *handlers) slotLease(ctx context.Context, params json.RawMessage) (any, error) {
	var req rpc.SlotLeaseRequest
	if err := decodeExactly(params, &req); err != nil {
		return nil, fmt.Errorf("%s takes pool and key and nothing else: %w", rpc.MethodSlotLease, err)
	}
	slots, err := h.slotPool(rpc.MethodSlotLease, req.Pool)
	if err != nil {
		return nil, err
	}
	if err := slotpool.CheckKey(req.Key); err != nil {
		return nil, fmt.Errorf("%s: %w", rpc.MethodSlotLease, err)
	}
	loaded, _ := h.settings()
	pool, _ := loaded.Pool(req.Pool)
	if !pool.Enabled {
		return nil, slotRefused(rpc.SlotRefusal{Reason: rpc.SlotPoolDisabled, Pool: req.Pool}, "%s: pool %s is disabled", rpc.MethodSlotLease, req.Pool)
	}
	holder := authz.PrincipalText(authz.PrincipalOf(ctx))
	slots.leasing.Lock()
	defer slots.leasing.Unlock()
	if held, ok := slots.Ledger.Held(req.Pool, req.Key); ok {
		switch {
		case held.Holder != holder:
			return nil, slotRefused(rpc.SlotRefusal{Reason: rpc.SlotNoSuchLease, Pool: req.Pool}, "%s: no such lease", rpc.MethodSlotLease)
		case held.State == slotpool.Live:
			return h.leaseAnswer(held), nil
		}
		return nil, slotRefused(rpc.SlotRefusal{Reason: rpc.SlotReleasing, Pool: req.Pool, RetryAfter: leaseRetryAfter}, "%s: the key's last lease is still being released", rpc.MethodSlotLease)
	}
	slot, leased, releasing := freeSlot(pool, req.Pool, slots.Ledger.Leases())
	if slot == "" {
		refusal := rpc.SlotRefusal{Reason: rpc.SlotPoolEmpty, Pool: req.Pool, Slots: len(pool.Slots), Leased: leased, Releasing: releasing}
		if releasing > 0 {
			refusal.RetryAfter = leaseRetryAfter
		}
		slog.Warn("slot pool: empty, a lease was refused", "pool", req.Pool, "slots", refusal.Slots, "leased", leased, "releasing", releasing)
		return nil, slotRefused(refusal, "%s: pool %s has no free slot", rpc.MethodSlotLease, req.Pool)
	}
	generation, err := slots.Generations.Next(slot, slots.Ledger.Floor(slot))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", rpc.MethodSlotLease, err)
	}
	if err := slots.Ledger.Begin(req.Pool, slot, req.Key, generation, holder); err != nil {
		return nil, fmt.Errorf("%s: %w", rpc.MethodSlotLease, err)
	}
	live, err := h.bringLeaseLive(ctx, loaded, req.Pool, slot, req.Key, generation)
	if err != nil {
		if rerr := h.releaseLease(ctx, req.Pool, req.Key, generation); rerr != nil {
			slog.Error("slot pool: a lease that failed to mint is not yet released, and its slot is not offered", "pool", req.Pool, "slot", slot, "generation", generation, "err", rerr)
		}
		return nil, fmt.Errorf("%s: %w", rpc.MethodSlotLease, err)
	}
	return h.leaseAnswer(live), nil
}

func (h *handlers) bringLeaseLive(ctx context.Context, loaded *config.Loaded, pool, slot, key string, generation uint64) (slotpool.Lease, error) {
	if _, err := loaded.LeaseBox(pool, slot, generation); err != nil {
		return slotpool.Lease{}, err
	}
	if _, err := h.mintLeased(ctx, pool, key, generation); err != nil {
		return slotpool.Lease{}, err
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return slotpool.Lease{}, fmt.Errorf("say bearer: %w", err)
	}
	slots := h.angelus.Slots
	if err := slots.Ledger.GoLive(pool, key, generation, hex.EncodeToString(secret[:])); err != nil {
		return slotpool.Lease{}, err
	}
	live, _ := slots.Ledger.Find(pool, key, generation)
	return live, nil
}

func (h *handlers) slotRelease(ctx context.Context, params json.RawMessage) (any, error) {
	var req rpc.SlotReleaseRequest
	if err := decodeExactly(params, &req); err != nil {
		return nil, fmt.Errorf("%s takes pool, key and generation and nothing else: %w", rpc.MethodSlotRelease, err)
	}
	slots, err := h.slotPool(rpc.MethodSlotRelease, req.Pool)
	if err != nil {
		return nil, err
	}
	slots.leasing.Lock()
	defer slots.leasing.Unlock()
	row, ok := slots.Ledger.Find(req.Pool, req.Key, req.Generation)
	if !ok || row.Holder != authz.PrincipalText(authz.PrincipalOf(ctx)) {
		return nil, slotRefused(rpc.SlotRefusal{Reason: rpc.SlotNoSuchLease, Pool: req.Pool}, "%s: no such lease", rpc.MethodSlotRelease)
	}
	if leaseIsOver(row) {
		return rpc.SlotReleaseResponse{Released: true}, nil
	}
	if err := h.releaseLease(ctx, req.Pool, req.Key, req.Generation); err != nil {
		return nil, fmt.Errorf("%s: %w", rpc.MethodSlotRelease, err)
	}
	return rpc.SlotReleaseResponse{Released: true}, nil
}
