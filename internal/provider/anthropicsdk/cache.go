package anthropicsdk

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

// cacheFor returns the per-aria byte cache, opening lazily. Returns
// nil if caching is unconfigured or the open failed.
func (p *Provider) cacheFor(aria string) store.Log[[]json.RawMessage] {
	if aria == "" || p.CacheOpen == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if s, ok := p.caches[aria]; ok {
		return s
	}
	s, err := p.CacheOpen(aria)
	if err != nil {
		slog.Warn("anthropicsdk cache open", "aria", aria, "err", err)
		return nil
	}
	p.invalidateIfStale(s)
	p.caches[aria] = s
	return s
}

// invalidateIfStale clears the cache on fingerprint mismatch.
func (p *Provider) invalidateIfStale(s store.Log[[]json.RawMessage]) {
	want := p.Fingerprint()
	for _, e := range s.Read() {
		if e.Fingerprint == "" || e.Fingerprint == want {
			continue
		}
		_ = s.Clear()
		slog.Info("anthropicsdk cleared stale cache", "stored", e.Fingerprint, "current", want)
		return
	}
}

// catchUp encodes any figLog entries not yet in the cache and
// returns per-message wire bytes plus their logical times. Uses an
// in-memory snapshot cache to avoid re-walking the entire conversation
// on every turn: only new entries (since the last call) are processed.
func (p *Provider) catchUp(aria string, figLog store.Log[message.Message], cache store.Log[[]json.RawMessage], chalk provider.Chalkboard) ([][]json.RawMessage, []uint64) {
	t0 := time.Now()
	fp := p.Fingerprint()
	entries := store.Snapshot(figLog)

	// Resume from the snap cache if the log only grew (append-only invariant).
	p.mu.Lock()
	sc := p.snapCache[aria]
	p.mu.Unlock()

	var snap chalkboard.Snapshot
	var perMessage [][]json.RawMessage
	var lts []uint64
	startIdx := 0

	if sc != nil && sc.nEntries <= len(entries) &&
		(sc.nEntries == 0 || entries[sc.nEntries-1].LT == sc.lastLT) {
		// The log only grew: resume from where we left off.
		snap = sc.snap
		perMessage = sc.perMessage
		lts = sc.lts
		startIdx = sc.nEntries
	}

	var nCached, nEncoded int
	for i := startIdx; i < len(entries); i++ {
		e := entries[i]
		msg := e.Payload
		msg.LogicalTime = e.LT
		if msg.Role == message.RoleGenesis {
			continue
		}
		if chalk != nil {
			msg.Patches = chalk.PatchesAt(e.LT)
		}
		var bytes []json.RawMessage
		if cache != nil {
			if existing, ok := cache.Lookup(msg.LogicalTime); ok && len(existing.Payload) > 0 {
				bytes = existing.Payload
				nCached++
			}
		}
		if bytes == nil {
			encoded, err := p.encode(msg, snap)
			if err != nil {
				slog.Error("anthropicsdk encode", "flt", msg.LogicalTime, "err", err)
			} else {
				bytes = encoded
				nEncoded++
				if cache != nil && len(encoded) > 0 {
					_, _ = cache.Append(store.Entry[[]json.RawMessage]{
						FigaroLT: msg.LogicalTime, Payload: bytes, Fingerprint: fp,
					})
				}
			}
		}
		if len(bytes) > 0 {
			perMessage = append(perMessage, bytes)
			lts = append(lts, msg.LogicalTime)
		}
		for _, patch := range msg.Patches {
			snap = snap.Apply(patch)
		}
	}

	// Store the snapshot for next turn.
	if aria != "" {
		var lastLT uint64
		if len(entries) > 0 {
			lastLT = entries[len(entries)-1].LT
		}
		p.mu.Lock()
		if p.snapCache == nil {
			p.snapCache = map[string]*snapCacheEntry{}
		}
		p.snapCache[aria] = &snapCacheEntry{
			snap:       snap,
			perMessage: perMessage,
			lts:        lts,
			nEntries:   len(entries),
			lastLT:     lastLT,
		}
		p.mu.Unlock()
	}

	tTotal := time.Since(t0)
	if tTotal > 200*time.Millisecond {
		slog.Warn("anthropicsdk catchUp slow",
			"total", tTotal,
			"entries", len(entries),
			"startIdx", startIdx,
			"cached", nCached,
			"encoded", nEncoded,
		)
	}
	return perMessage, lts
}
