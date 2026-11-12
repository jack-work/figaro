package angelus

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/slotpool"
)

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
