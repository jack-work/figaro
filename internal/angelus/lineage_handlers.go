package angelus

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
)

// The lineage read. See api/rpc/lineage.go for what it is for; this is where
// the walk happens.
//
// THE FORK BASE THE TREE RECORDS IS AN LT, AND THE ANSWER IS A TURN. The tree
// says "this branch owns from LT 39"; the client's window, row cache and
// sticky cache are keyed by turn. The turn that owns that LT is read from the
// PARENT's log, never the child's: the child's record at its own base is its
// first own record, which composes into the turn BELOW the cut, and a client
// told that number would keep one turn too many and render the parent's
// answer under the branch's question.

// baseTurnRecorder is a backend that can write a fork base's turn down beside
// the base. Optional: without one the lineage read still answers, it just
// cannot save the next reader the trouble.
type baseTurnRecorder interface {
	RecordBaseTurn(trunk string, baseLT, turn uint64, exact bool) error
}

// baseTurnOf is the turn link l starts at, in its parent's turn space.
//
// IT IS DATA. A fork records it beside its base when it is born, so the common
// answer reads no log at all. Two cases still touch the parent's log, for one
// record or a short backward read, never a walk from LT 1:
//
//   - A fork older than the recording, or whose record was lost. Computed
//     once and recorded, so it is the last time anyone asks.
//   - A base that was PAST THE END of the parent when it was recorded, which
//     is every head fork. The recorded answer is the parent's last turn: the
//     one turn the two arias can still disagree about if the parent was
//     mid-turn when it was cut, so keeping it would be the one mistake
//     retention may never make. That costs a single re-read of one turn. Once
//     the parent writes AT the base the precise answer is one record away,
//     and it is recorded the first time it is seen.
func (a *Angelus) baseTurnOf(parent string, l store.Link) uint64 {
	if l.BaseTurnKnown && l.BaseTurnExact {
		return l.BaseTurn
	}
	log := a.openNodeIR(parent)
	if log == nil {
		return l.BaseTurn // 0 when unknown: keeps nothing, which is safe
	}
	if l.BaseTurnKnown {
		// Recorded past the end. The parent's tail says whether it has
		// written at the base since; until it has, the record stands.
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

// chainOf is an aria's ancestry as the client reads it: conversations only,
// root first, each carrying the first TURN it owns.
func (a *Angelus) chainOf(id string) ([]rpc.LineageLink, error) {
	cb, ok := a.Backend.(store.ChainBackend)
	if !ok {
		return nil, fmt.Errorf("%s: this store has no ancestry", rpc.MethodLineage)
	}
	links := cb.LineageLinks(id)
	out := make([]rpc.LineageLink, 0, len(links))
	for i, l := range links {
		// The genesis root, the outfit stumps and unbound forms carry no turns
		// a reader can see. Anything above the first conversation is dropped,
		// and a form found INSIDE a chain would break the walk rather than be
		// skipped, so the chains of two arias stay comparable link by link.
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

// divergence is where two chains part: the deepest conversation they share,
// and the first turn they do not.
//
// The MINIMUM of the two bases at the first differing link, not the deeper
// one: two cousins cut from the same parent at turns 21 and 30 share turns 1
// to 20 only, and a client that kept through 29 would paint one branch's
// turns under the other's coordinates.
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
		// The same aria: everything is shared, so nothing is below the
		// divergence. The maximum turn keeps "keep turn < divergence" true
		// without a special case at the client.
		return ancestor, ^uint64(0)
	case i == len(a):
		return ancestor, b[i].Base
	case i == len(b):
		return ancestor, a[i].Base
	default:
		return ancestor, min(a[i].Base, b[i].Base)
	}
}

// lineage serves MethodLineage.
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
