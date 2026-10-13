package cli

import (
	"github.com/jack-work/figaro/api/rpc"
)

// expired: lifetime spent and nobody holding it (no bound shell, no turn in
// flight); residency is not holding. The daemon's sweep deletes such nodes
// later, once dormant; `ls --json` and `doctor ttl` still report them.
func expired(f rpc.FigaroInfoResponse, nowMS int64) bool {
	return f.ExpiresAt > 0 &&
		f.ExpiresAt <= nowMS &&
		f.State != "active" &&
		len(f.BoundPIDs) == 0
}

// A withheld row takes its subtree with it, except an expired ancestor of a
// surviving row, which is drawn uncounted so the survivor keeps its path to a
// root.
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
	// The seen set bounds the walk on a cycle.
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
