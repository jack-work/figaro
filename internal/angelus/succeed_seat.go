package angelus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/hierarchy"
	"github.com/jack-work/figaro/internal/store"
)

type sourceLocks struct {
	mu    sync.Mutex
	locks map[string]*sourceLock
}

type sourceLock struct {
	mu   sync.Mutex
	held int
}

func (s *sourceLocks) lock(id string) func() {
	s.mu.Lock()
	if s.locks == nil {
		s.locks = map[string]*sourceLock{}
	}
	l := s.locks[id]
	if l == nil {
		l = &sourceLock{}
		s.locks[id] = l
	}
	l.held++
	s.mu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		s.mu.Lock()
		if l.held--; l.held == 0 {
			delete(s.locks, id)
		}
		s.mu.Unlock()
	}
}

type handover struct {
	retire json.RawMessage
	unlock func()
}

func (h *handlers) beginHandover(ctx context.Context, req rpc.ForkRequest) (*handover, error) {
	if req.Seat == "" {
		return nil, errors.New("fork: a succession is the seat's: name the role the successor takes")
	}
	if req.RetireTTL > rpc.MaxAbdicationTTL {
		return nil, fmt.Errorf("fork: a succession retires its source for a ttl of up to %s, got %s", rpc.MaxAbdicationTTL, req.RetireTTL)
	}
	unlock := h.handovers.lock(req.FigaroID)
	s := &handover{unlock: unlock}
	retire, err := h.precheckHandover(ctx, req)
	if err != nil {
		unlock()
		return nil, err
	}
	s.retire = retire
	return s, nil
}

func (h *handlers) precheckHandover(ctx context.Context, req rpc.ForkRequest) (json.RawMessage, error) {
	source := req.FigaroID
	snap, err := h.angelus.Backend.FormState(source)
	if err != nil {
		return nil, fmt.Errorf("fork: the board of %s cannot be read to check it can be retired: %w", source, err)
	}
	if store.RetiredIn(snap) || !h.angelus.isLiveAria(source) {
		return nil, fmt.Errorf("fork: %s is already retired, so it has nothing left to hand over", source)
	}
	roles, _ := h.angelus.roles()
	if err := h.refuseSucceedWithoutGrant(ctx, roles.holding(source)...); err != nil {
		return nil, err
	}
	retire, err := h.retireRequest(ctx, source, req.RetireTTL)
	if err != nil {
		return nil, fmt.Errorf("fork: the caller may not retire %s, so the succession is refused: %w", source, err)
	}
	if !h.angelus.topology().Draws() {
		return nil, fmt.Errorf("fork: %s: %s keeps no drawn tree to seat a successor in", rpc.AbdicationNeedsTrunks, source)
	}
	return retire, nil
}

func (h *handlers) retireRequest(ctx context.Context, id string, ttl time.Duration) (json.RawMessage, error) {
	var createdAt int64
	if meta, _ := h.angelus.Backend.Meta(id); meta != nil {
		createdAt = meta.CreatedAtMS
	}
	snap, err := h.angelus.Backend.FormState(id)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	patch := store.RetirePatch(store.RetiredManual, createdAt, clampToLifetime(snap, createdAt, ttl, now), now)
	params, err := json.Marshal(rpc.SetRequest{Patch: patch})
	if err != nil {
		return nil, err
	}
	if err := h.refuseCallerPatch(ctx, rpc.MethodSet, id, params); err != nil {
		return nil, err
	}
	return rpc.WithAria(params, id)
}

func clampToLifetime(snap form.Snapshot, createdAtMS int64, ttl time.Duration, now time.Time) time.Duration {
	raw, ok := snap.Get(store.SystemTTLKey)
	if !ok {
		return ttl
	}
	total, err := store.ParseTTL(lifetimeText(raw))
	if err != nil || total <= 0 {
		return ttl
	}
	var age time.Duration
	if createdAtMS > 0 && now.UnixMilli() > createdAtMS {
		age = now.Sub(time.UnixMilli(createdAtMS))
	}
	if remaining := total - age; remaining < ttl {
		return remaining
	}
	return ttl
}

func (h *handlers) completeHandover(ctx context.Context, source, successor string, s *handover) (*rpc.SeatResponse, error) {
	seat := h.seatInPlaceOf(ctx, successor, source, true)
	if seat.FailedAt != "" || seat.Demote.Unsupported {
		h.discardBirth(successor)
		if seat.FailedAt == "" {
			return nil, fmt.Errorf("fork: %s: the fork was discarded and nothing moved", rpc.AbdicationNeedsTrunks)
		}
		return nil, fmt.Errorf("fork: %s; the fork was discarded and nothing moved", seat.Error)
	}
	if _, err := h.ariaMethod(rpc.MethodSet)(ctx, s.retire); err != nil {
		h.unseat(ctx, source, successor)
		h.discardBirth(successor)
		return nil, fmt.Errorf("fork: %s could not be retired, so the seat was given back and the fork discarded: %w", source, err)
	}
	return &seat, nil
}

func (h *handlers) unseat(ctx context.Context, source, successor string) {
	h.seated.Delete(successor)
	by, err := h.angelus.topologyPrincipal(ctx)
	if err == nil {
		_, err = h.angelus.topology().Apply(ctx, by, hierarchy.Plan{Op: hierarchy.Seat, Node: successor, To: source, Retires: true})
	}
	if err == nil {
		err = h.angelus.swapFanoutKeys(successor, source)
	}
	if err != nil {
		slog.Error("a refused succession could not give the seat back", "source", source, "successor", successor, "err", err)
	}
}
