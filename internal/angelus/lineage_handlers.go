package angelus

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/turns"
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

// baseTurns memoizes the LT to turn conversion. A fork base never moves, so
// within one process an entry is true forever.
type baseTurns struct {
	mu sync.Mutex
	at map[string]uint64 // "<parent node>:<base lt>" -> turn
}

func (b *baseTurns) get(key string) (uint64, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	t, ok := b.at[key]
	return t, ok
}

func (b *baseTurns) put(key string, turn uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.at == nil {
		b.at = map[string]uint64{}
	}
	b.at[key] = turn
}

// turnAtLT is the turn that owns lt in node's log. One record read in the
// common case: every record carries the turn it belongs to. The walk is the
// fallback for a log written before turn ids were stamped, where the id has to
// be counted from the openers.
func (a *Angelus) turnAtLT(node string, lt uint64) uint64 {
	if lt == 0 {
		return 0
	}
	key := node + ":" + fmt.Sprint(lt)
	if t, ok := a.baseTurns.get(key); ok {
		return t
	}
	log := a.openNodeIR(node)
	if log == nil {
		return 0
	}
	turn, sealed := uint64(0), false
	// THE RECORD MUST BE THE ONE ASKED FOR. A read past the end of a log comes
	// back with the last record rather than with nothing, and taking its turn
	// id answers one turn too low for a fork that begins AFTER the end.
	if entries, _ := log.ReadPage(lt, lt+1, 1); len(entries) > 0 &&
		entries[0].LT == lt && entries[0].Payload.TurnID != 0 {
		turn, sealed = entries[0].Payload.TurnID, true
	} else {
		var msgs []message.Message
		for from := uint64(1); ; {
			got := log.ReadFrom(from, 512)
			if len(got) == 0 {
				break
			}
			for _, e := range got {
				m := e.Payload
				m.LogicalTime = e.LT
				msgs = append(msgs, m)
			}
			from = got[len(got)-1].LT + 1
			if from > lt {
				break
			}
		}
		if t, ok := turns.At(msgs, lt); ok {
			turn, sealed = t, true
		} else if len(msgs) > 0 {
			// THE LAST TURN OF A LOG IS NOT SHARED. The coordinate is past the
			// end, so the fork begins where this log stops, and the turn it
			// stops inside is the one turn the two arias can still DISAGREE
			// about: the child repairs a tool call left hanging at the cut
			// (an error result, because the call never returned to it) while
			// the parent goes on to complete the same call successfully. Same
			// turn id, different content, which is the one thing retention may
			// never be wrong about.
			//
			// So the answer is the last turn, not one past it. It costs a head
			// fork a single turn re-read and it is correct whether or not that
			// turn was still running, which is the part a log cannot tell us.
			turn = turns.StampIDs(msgs)
		}
	}
	// ONLY A SEALED COORDINATE IS MEMOIZED. A fork base never moves, but "past
	// the end of the log" is not a coordinate, it is a statement about a log
	// that grows: the parent writes the rest of that turn a second later and
	// the answer changes. Caching it kept the first answer forever.
	if turn != 0 && sealed {
		a.baseTurns.put(key, turn)
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
			base = a.turnAtLT(links[i-1].Node, l.BaseLT)
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
