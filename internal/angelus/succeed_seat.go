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

func (h *handlers) completeHandover(ctx context.Context, req rpc.ForkRequest, successor string, s *handover) (*rpc.SeatResponse, error) {
	source := req.FigaroID
	seat := h.seatInPlaceOf(ctx, successor, source, true)
	if seat.FailedAt != "" || seat.Demote.Unsupported {
		left := discardLeftover(successor, h.discardBirth(successor))
		if seat.FailedAt == "" {
			return nil, fmt.Errorf("fork: %s: nothing moved%s", rpc.AbdicationNeedsTrunks, left)
		}
		return nil, fmt.Errorf("fork: %s; nothing moved%s", seat.Error, left)
	}
	_, err := h.ariaMethod(rpc.MethodSet)(ctx, s.retire)
	if err == nil {
		h.clearSuccessionStamp(successor)
		return &seat, nil
	}
	if uerr := h.unseat(ctx, source, successor); uerr != nil {
		return nil, fmt.Errorf("fork: %s could not be retired (%v) and the seat could not be given back (%v): %s is still seated over %s, which is live; to finish the handover run: figaro succeed %s --successor %s -- <the succession prompt>",
			source, err, uerr, successor, source, req.Seat, successor)
	}
	return nil, fmt.Errorf("fork: %s could not be retired, so the seat was given back%s: %w", source, discardLeftover(successor, h.discardBirth(successor)), err)
}

func discardLeftover(successor string, err error) string {
	if err == nil {
		return " and the fork was discarded"
	}
	return fmt.Sprintf(", but the fork %s could not be discarded (%v): run figaro kill %s", successor, err, successor)
}

func (h *handlers) unseat(ctx context.Context, source, successor string) error {
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
	return err
}

func (h *handlers) clearSuccessionStamp(fork string) {
	snap, err := h.angelus.Backend.FormState(fork)
	if err == nil {
		_, err = h.angelus.Backend.ApplyFormPrivileged(fork, form.Build(snap, nil, []string{form.SuccessionOfKey}))
	}
	if err != nil {
		slog.Error("a finished succession could not clear its stamp", "fork", fork, "err", err)
	}
}

func (h *handlers) RecoverSuccessions() {
	a := h.angelus
	ctx := h.ownerContext()
	for pending := -1; ; {
		var left int
		for _, v := range a.Backend.EveryNode() {
			if meta, _ := a.Backend.Meta(v.ID); meta != nil && meta.SuccessionOf != "" && !h.recoverSuccession(ctx, meta.SuccessionOf, v.ID) {
				left++
			}
		}
		if left == 0 || left == pending {
			return
		}
		pending = left
	}
}

func (h *handlers) recoverSuccession(ctx context.Context, source, fork string) bool {
	defer h.handovers.lock(source)()
	a := h.angelus
	if _, known := a.Backend.Node(source); !known || !a.isLiveAria(source) {
		h.clearSuccessionStamp(fork)
		return true
	}
	if h.forkHasWorked(fork) {
		slog.Error("a stamped successor of a live source has records of its own, so it is kept: both stay live and the stamp is cleared",
			"source", source, "fork", fork)
		h.clearSuccessionStamp(fork)
		return true
	}
	if parent, drawn := a.topology().Drawn(source); drawn && parent == fork {
		if err := h.unseat(ctx, source, fork); err != nil {
			slog.Error("a succession that died halfway could not give the seat back", "source", source, "fork", fork, "err", err)
			return false
		}
	}
	if err := h.discardBirth(fork); err != nil {
		return false
	}
	slog.Warn("a succession died halfway: the fork was discarded and the source keeps its place", "source", source, "fork", fork)
	return true
}

func (h *handlers) forkHasWorked(fork string) bool {
	node, known := h.angelus.Backend.Node(fork)
	if !known {
		return true
	}
	lg, err := h.angelus.Backend.OpenFigIR(fork)
	if err != nil {
		return true
	}
	for _, e := range lg.Read() {
		if e.LT > node.BranchedLT {
			return true
		}
	}
	return false
}
