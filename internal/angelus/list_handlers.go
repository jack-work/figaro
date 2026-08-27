package angelus

// The listing: one snapshot of the tree per request, enriched per aria.
//
// Split out of protocol.go, which had grown to 2,011 lines and answered every
// question at once. Same package, same behaviour: only the reader's job
// changes. plans/api-coherence.md step 5.

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
)

type listEnrichment struct {
	index  int
	ariaID string
}

// list merges live and dormant arias.
func (h *handlers) list(ctx context.Context, params json.RawMessage) (interface{}, error) {
	// IDsOnly skips the per-aria form + node fills (the slow part): used
	// by completion, which only needs the ids. Tolerant of nil/empty params.
	var req rpc.ListRequest
	_ = json.Unmarshal(params, &req)

	live := h.angelus.Registry.List()
	boundPIDs := h.angelus.Registry.BoundPIDsByFigaro()
	result := make([]rpc.FigaroInfoResponse, 0, len(live))
	enrichments := make([]listEnrichment, 0, len(live))
	seen := make(map[string]struct{}, len(live))
	for _, info := range live {
		seen[info.ID] = struct{}{}
		entry := rpc.FigaroInfoResponse{
			ID:               info.ID,
			State:            info.State,
			Provider:         info.Provider,
			Model:            info.Model,
			MessageCount:     info.MessageCount,
			TokensIn:         info.TokensIn,
			TokensOut:        info.TokensOut,
			CacheReadTokens:  info.CacheReadTokens,
			CacheWriteTokens: info.CacheWriteTokens,
			ContextTokens:    info.ContextTokens,
			ContextLimit:     info.ContextLimit,
			ContextExact:     info.ContextExact,
			CreatedAt:        info.CreatedAt.UnixMilli(),
			LastActive:       info.LastActive.UnixMilli(),
			Mantra:           info.Mantra,
			Cwd:              info.Cwd,
			BoundPIDs:        boundPIDs[info.ID],
		}
		result = append(result, entry)
	}

	// Snapshot the tree once per request. Ordinary lists need conversation
	// nodes only; global listings also need the ceremonial anchors. ID-only
	// completion skips vectors and anchors entirely.
	var nodeList []store.NodeView
	nodeByID := map[string]store.NodeView{}
	var conversationIDs []string
	if h.angelus.Backend != nil {
		switch {
		case req.IDsOnly && !req.Global:
			conversationIDs = h.angelus.Backend.ConversationIDs()
			// Forms complete everywhere an id does: attend, kill, set --id.
			for _, f := range h.angelus.Backend.Forms() {
				conversationIDs = append(conversationIDs, f.ID)
			}
		case req.Global:
			nodeList = h.angelus.Backend.Nodes()
		default:
			nodeList = h.angelus.Backend.Conversations()
		}
		for _, n := range nodeList {
			if n.Kind == conversationKind {
				conversationIDs = append(conversationIDs, n.ID)
			}
			if !req.IDsOnly {
				nodeByID[n.ID] = n
			}
		}
	}

	// Dormant conversation trunks (not currently registered/live).
	for _, id := range conversationIDs {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		entry := rpc.FigaroInfoResponse{ID: id, State: "dormant", BoundPIDs: boundPIDs[id]}
		if req.IDsOnly {
			if meta, _ := h.angelus.Backend.Meta(id); meta != nil {
				entry.MessageCount = meta.MessageCount
				entry.TokensIn = meta.TokensIn
				entry.TokensOut = meta.TokensOut
				entry.CacheReadTokens = meta.CacheReadTokens
				entry.CacheWriteTokens = meta.CacheWriteTokens
			}
			// Recency comes from figwal, not the sidecar: the newest record
			// timestamp anywhere in the node, read from the store WITHOUT
			// waking anything.
			entry.LastActive = h.angelus.Backend.LastTS(id)
		}
		result = append(result, entry)
		if !req.IDsOnly {
			enrichments = append(enrichments, listEnrichment{
				index:  len(result) - 1,
				ariaID: id,
			})
		}
	}

	// Global: also surface the ceremonial anchors: the null genesis trunk and
	// every versioned outfit: that the conversation filter above skips.
	// fillFromNode below stamps their Kind/Outfit/Version/Parent.
	if req.Global {
		for _, n := range nodeList {
			if n.Kind == conversationKind {
				continue
			}
			if _, ok := seen[n.ID]; ok {
				continue
			}
			seen[n.ID] = struct{}{}
			entry := rpc.FigaroInfoResponse{ID: n.ID, State: "anchor", BoundPIDs: boundPIDs[n.ID]}
			if n.Kind == string(store.KindForm) {
				// A form is a live row, not a ceremonial anchor: it has
				// recency (figwal LastTS, wake-free, a form has nothing to
				// wake) and possibly a name and a casting (role) target,
				// both read from its folded state. Forms are few; the fold
				// is the same one `fig form <id>` performs.
				entry.State = "form"
				entry.LastActive = h.angelus.Backend.LastTS(n.ID)
				if snap, err := h.angelus.Backend.FormState(n.ID); err == nil {
					if v := snap.Lookup("name"); v != nil {
						entry.Name = *v
					}
					if v := snap.Lookup("target-aria"); v != nil {
						entry.TargetAria = *v
					}
				}
			}
			result = append(result, entry)
		}
	}

	// Tree position for every entry (live + dormant), from the snapshot -
	// and the outfit columns, which come from the stump and from nowhere else.
	// vers memoizes the re-resolve per outfit; it lives here because this pass
	// is single-threaded, unlike the metadata fill above.
	if !req.IDsOnly {
		h.enrichList(result, enrichments)
		vers := map[string][2]string{}
		for i := range result {
			h.fillFromNode(nodeByID, vers, &result[i])
		}
	}

	// FEDERATE. The local rows are always answered even when a peer is
	// unreachable: a listing that blocks on the worst node is worse than one
	// that names it, and a listing that silently omits it is worse still --
	// it tells the reader those arias are gone.
	if h.angelus.Peers != nil {
		remote, errs := h.angelus.Peers.federate(ctx, req)
		result = append(result, remote...)
		return rpc.ListResponse{Figaros: result, PeerErrors: errs}, nil
	}
	return rpc.ListResponse{Figaros: result}, nil
}

func (h *handlers) enrichList(result []rpc.FigaroInfoResponse, tasks []listEnrichment) {
	if len(tasks) == 0 {
		return
	}
	workers := min(8, len(tasks))
	queue := make(chan listEnrichment)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for task := range queue {
				entry := &result[task.index]
				meta, _ := h.angelus.Backend.Meta(task.ariaID)
				if meta != nil {
					h.fillFromMeta(meta, entry)
				}
				// Recency from figwal, sidecar-free, wake-free.
				entry.LastActive = h.angelus.Backend.LastTS(task.ariaID)
			}
		}()
	}
	for _, task := range tasks {
		queue <- task
	}
	close(queue)
	wg.Wait()
}

func (h *handlers) fillFromMeta(meta *store.AriaMeta, entry *rpc.FigaroInfoResponse) {
	entry.MessageCount = meta.MessageCount
	entry.TokensIn = meta.TokensIn
	entry.TokensOut = meta.TokensOut
	entry.CacheReadTokens = meta.CacheReadTokens
	entry.CacheWriteTokens = meta.CacheWriteTokens
	entry.ContextTokens = meta.ContextTokens
	entry.ContextLimit = meta.ContextLimit
	entry.ContextExact = meta.ContextExact
	entry.Provider = meta.Provider
	entry.Model = meta.Model
	entry.Mantra = meta.Mantra
	entry.Cwd = meta.Cwd
	if meta.CreatedAtMS != 0 {
		entry.CreatedAt = meta.CreatedAtMS
	}
}

// fillFromNode adds the fork-tree position (vector/trunk/parent/branched-at)
// from the tree. The tree is snapshotted by the caller (once per request)
// and indexed by id, so this is a map lookup.
func (h *handlers) fillFromNode(nodes map[string]store.NodeView, vers map[string][2]string, entry *rpc.FigaroInfoResponse) {
	n, ok := nodes[entry.ID]
	if !ok {
		return
	}
	entry.Vector = n.Vector
	entry.Trunk = n.Trunk
	entry.Parent = n.Parent
	entry.Present = n.Present
	entry.BranchedLT = n.BranchedLT
	entry.Kind = n.Kind
	if n.Kind == string(outfitKind) {
		entry.OutfitName = n.Outfit
		entry.OutfitVer = h.outfitVer(vers, n.Version, n.Outfit)
		return
	}
	// A conversation's outfit is the stump it was BORN under, carried down the
	// lineage by the topology walk -- not `system.outfit_name`, which is the
	// agent's own mutable copy, and not the presentation parent, which a
	// promote moves. Both were live lies: one `set` renamed an aria's outfit in
	// every listing and, because this column is what the version is
	// re-resolved against, reported an unchanged outfit as stale in the same
	// breath.
	if n.Outfit != "" {
		entry.OutfitName = n.Outfit
		entry.OutfitVer = h.outfitVer(vers, n.Version, n.Outfit)
	}
}
