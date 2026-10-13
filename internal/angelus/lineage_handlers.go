package angelus

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
)

// The fork base the tree records is an LT; the answer is a turn, read from
// the parent's log, never the child's: the child's record at its base
// composes into the turn below the cut, and a client told that number would
// keep one turn too many.

type baseTurnRecorder interface {
	RecordBaseTurn(trunk string, baseLT, turn uint64, exact bool) error
}

// baseTurnOf is the turn link l starts at, in its parent's turn space. The
// common answer is recorded at birth and reads no log; a missing record or
// a base past the parent's end costs a short backward read, never a walk.
func (a *Angelus) baseTurnOf(parent string, l store.Link) uint64 {
	if l.BaseTurnKnown && l.BaseTurnExact {
		return l.BaseTurn
	}
	log := a.openNodeIR(parent)
	if log == nil {
		return l.BaseTurn // 0 when unknown keeps nothing, which is safe
	}
	if l.BaseTurnKnown {
		// Recorded past the end: the record stands until the parent writes at the base.
		if tail, ok := log.PeekTail(); !ok || tail.LT < l.BaseLT {
			return l.BaseTurn
		}
	}
	turn, exact := store.TurnAtLT(log, l.BaseLT)
	if rec, ok := a.Backend.(baseTurnRecorder); ok {
		if err := rec.RecordBaseTurn(l.Node, l.BaseLT, turn, exact); err != nil {
			slog.Debug("lineage: base turn not recorded", "node", l.Node, "err", err)
		}
	}
	return turn
}

// chainOf is the ancestry, conversations only, root first, each with the first turn it owns.
func (a *Angelus) chainOf(id string) ([]rpc.LineageLink, error) {
	cb, ok := a.Backend.(store.ChainBackend)
	if !ok {
		return nil, fmt.Errorf("%s: this store has no ancestry", rpc.MethodLineage)
	}
	links := cb.LineageLinks(id)
	out := make([]rpc.LineageLink, 0, len(links))
	for i, l := range links {
		// Anything above the first conversation is dropped, so two chains stay
		// comparable link by link.
		if l.Kind != "conversation" {
			out = out[:0]
			continue
		}
		base := uint64(0)
		if len(out) > 0 {
			base = a.baseTurnOf(links[i-1].Node, l)
		}
		out = append(out, rpc.LineageLink{Node: l.Node, Base: base})
	}
	return out, nil
}

// divergence is the deepest shared conversation and the first turn not
// shared: the minimum of the two bases at the first differing link.
func divergence(a, b []rpc.LineageLink) (ancestor string, turn uint64) {
	i := 0
	for i < len(a) && i < len(b) && a[i].Node == b[i].Node {
		i++
	}
	if i == 0 {
		return "", 0
	}
	ancestor = a[i-1].Node
	switch {
	case i == len(a) && i == len(b):
		// The same aria: the maximum turn keeps "turn < divergence" true for every turn.
		return ancestor, ^uint64(0)
	case i == len(a):
		return ancestor, b[i].Base
	case i == len(b):
		return ancestor, a[i].Base
	default:
		return ancestor, min(a[i].Base, b[i].Base)
	}
}

func (h *handlers) lineage(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.LineageRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, fmt.Errorf("%s: parse params: %w", rpc.MethodLineage, err)
	}
	if err := rpc.ValidateAriaID(req.FigaroID); err != nil {
		return nil, err
	}
	chain, err := h.angelus.chainOf(req.FigaroID)
	if err != nil {
		return nil, err
	}
	out := rpc.LineageResponse{Chain: chain}
	if cb, ok := h.angelus.Backend.(store.ChainBackend); ok {
		out.Epoch = cb.TopologyRev()
	}
	if req.Against == "" {
		return out, nil
	}
	if err := rpc.ValidateAriaID(req.Against); err != nil {
		return nil, err
	}
	against, err := h.angelus.chainOf(req.Against)
	if err != nil {
		return nil, err
	}
	out.Against = against
	out.Ancestor, out.Divergence = divergence(chain, against)
	return out, nil
}
