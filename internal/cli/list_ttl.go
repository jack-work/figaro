package cli

import (
	"github.com/jack-work/figaro/api/rpc"
)

// The listing withholds nodes whose stated lifetime has run out.
//
// A node states its own lifetime on its board (system.ttl); the daemon's
// reclamation sweep deletes it once that lifetime is spent, on the interval
// set by [memory] sweep_interval_seconds (two minutes by default), and only
// once the node is dormant -- an aria holding a resident agent keeps its
// deadline and is taken on a later pass. Between a node's deadline and its
// deletion there is therefore a window of one sweep at minimum and, for an
// aria the daemon still has in memory, of however long it takes to hibernate.
//
// dropExpired is what the window looks like in `figaro ls`: nothing. The rows
// are still on the wire, and `ls --json` (the whole store, verbatim) and
// `figaro doctor ttl` (straight from the sidecars) both still report them.

// expired reports whether a row's lifetime is spent and nobody is holding it:
// no shell is bound to it and no turn is in flight. Those two are the only
// reasons to keep drawing a node that is on its way out -- a shell bound to it
// is somebody's attention, and a turn in flight is output being produced.
// Residency is not one of them: an idle agent in the daemon's memory delays
// the deletion without making the node any more wanted.
func expired(f rpc.FigaroInfoResponse, nowMS int64) bool {
	return f.ExpiresAt > 0 &&
		f.ExpiresAt <= nowMS &&
		f.State != "active" &&
		len(f.BoundPIDs) == 0
}

// dropExpired returns the rows to draw and the count it withheld.
//
// A withheld row takes its subtree with it: both tree renderers descend from a
// root through parent links, so a hidden node's children are unreachable and
// would vanish without a glyph. That matches the deletion, which is recursive
// over the presentation hierarchy. The exception is an expired ancestor of a
// row that survives -- a branch still in use, or one promoted out from under
// it. Those ancestors are drawn so the surviving row keeps its path to a root,
// and they are not counted as withheld.
func dropExpired(figs []rpc.FigaroInfoResponse, nowMS int64) ([]rpc.FigaroInfoResponse, int) {
	doomed := map[string]bool{}
	for _, f := range figs {
		if expired(f, nowMS) {
			doomed[f.ID] = true
		}
	}
	if len(doomed) == 0 {
		return figs, 0
	}
	byID := make(map[string]rpc.FigaroInfoResponse, len(figs))
	for _, f := range figs {
		byID[f.ID] = f
	}
	// Walk up from every survivor, reprieving the doomed ancestors it is
	// drawn beneath. The seen set bounds the walk on a cycle.
	for _, f := range figs {
		if doomed[f.ID] {
			continue
		}
		seen := map[string]bool{f.ID: true}
		for id := drawnUnder(f); id != "" && !seen[id]; {
			seen[id] = true
			delete(doomed, id)
			parent, ok := byID[id]
			if !ok {
				break
			}
			id = drawnUnder(parent)
		}
	}
	if len(doomed) == 0 {
		return figs, 0
	}
	kept := make([]rpc.FigaroInfoResponse, 0, len(figs)-len(doomed))
	for _, f := range figs {
		if doomed[f.ID] {
			continue
		}
		kept = append(kept, f)
	}
	return kept, len(doomed)
}
