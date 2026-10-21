package angelus

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

// list never opens IR or folds form history: it reads sidecars and one tree snapshot.
func (h *handlers) list(ctx context.Context, params json.RawMessage) (interface{}, error) {
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
			AgentSince:       info.AgentSince.UnixMilli(),
			WokeBy:           info.WokeBy,
			Mantra:           info.Mantra,
			Cwd:              info.Cwd,
			BoundPIDs:        boundPIDs[info.ID],
		}
		if h.angelus.Backend != nil {
			if meta, _ := h.angelus.Backend.Meta(info.ID); meta != nil {
				entry.RetiredAt = meta.RetiredAtMS
			}
		}
		result = append(result, entry)
	}

	var nodeList []store.NodeView
	nodeByID := map[string]store.NodeView{}
	var conversationIDs []string
	if h.angelus.Backend != nil {
		switch {
		case req.IDsOnly && !req.Global:
			conversationIDs = h.angelus.Backend.ConversationIDs()
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

	if filtered := h.readable(ctx, result); len(filtered) != len(result) {
		result = filtered
		enrichments = pageEnrichments(result, enrichments)
	}

	var nextBefore string
	if listBounded(req) {
		for i := range result {
			if result[i].State == "dormant" && result[i].LastActive == 0 && h.angelus.Backend != nil {
				result[i].LastActive = h.angelus.Backend.LastTS(result[i].ID)
			}
		}
		var err error
		if result, nextBefore, err = boundPage(result, req); err != nil {
			return nil, err
		}
		enrichments = pageEnrichments(result, enrichments)
	}

	// vers memoizes the re-resolve per outfit; this pass is single-threaded,
	// unlike the metadata fill.
	if !req.IDsOnly {
		h.enrichList(result, enrichments)
		vers := map[string][2]string{}
		for i := range result {
			h.fillFromNode(nodeByID, vers, &result[i])
		}
		h.fillTTL(result)
	}
	h.fillNames(result)

	return rpc.ListResponse{Figaros: result, NextBefore: nextBefore}, nil
}

type ttlLister interface {
	TTLEntries() []store.TTLEntry
}

func (h *handlers) fillTTL(result []rpc.FigaroInfoResponse) {
	lister, ok := h.angelus.Backend.(ttlLister)
	if !ok {
		return
	}
	entries := lister.TTLEntries()
	if len(entries) == 0 {
		return
	}
	byID := make(map[string]store.TTLEntry, len(entries))
	for _, e := range entries {
		byID[e.ID] = e
	}
	def := h.angelus.keepalive()
	for i := range result {
		e, ok := byID[result[i].ID]
		if !ok {
			continue
		}
		result[i].TTLMS = e.TTL.Milliseconds()
		result[i].ExpiresAt = e.DeadlineMS
		result[i].KeepaliveMS = e.KeepaliveOr(def).Milliseconds()
		result[i].ReapsAt = e.ReapsAt(def, h.angelus.Backend.LastTS(e.ID))
	}
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
	entry.ContextLimit = h.angelus.Models.Limit(meta.Provider, meta.Model)
	entry.ContextExact = meta.ContextExact
	entry.Provider = meta.Provider
	entry.Model = meta.Model
	entry.Mantra = meta.Mantra
	entry.Cwd = meta.Cwd
	if meta.CreatedAtMS != 0 {
		entry.CreatedAt = meta.CreatedAtMS
	}
	entry.RetiredAt = meta.RetiredAtMS
}

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
	// The outfit is the stump the aria was born under, not the mutable
	// system.outfit_name and not the presentation parent a promote moves.
	if n.Outfit != "" {
		entry.OutfitName = n.Outfit
		entry.OutfitVer = h.outfitVer(vers, n.Version, n.Outfit)
	}
}
