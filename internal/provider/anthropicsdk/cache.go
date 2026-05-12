package anthropicsdk

import (
	"encoding/json"
	"log/slog"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
)

// cacheFor returns the per-aria byte cache, opening lazily. Returns
// nil if caching is unconfigured or the open failed.
func (p *Provider) cacheFor(aria string) store.Stream[[]json.RawMessage] {
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
func (p *Provider) invalidateIfStale(s store.Stream[[]json.RawMessage]) {
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

// catchUp encodes any figStream entries not yet in the cache and
// returns per-message wire bytes plus their logical times.
func (p *Provider) catchUp(figStream store.Stream[message.Message], cache store.Stream[[]json.RawMessage]) ([][]json.RawMessage, []uint64) {
	fp := p.Fingerprint()
	snap := chalkboard.Snapshot{}
	var perMessage [][]json.RawMessage
	var lts []uint64
	for _, e := range figStream.Read() {
		msg := e.Payload
		msg.LogicalTime = e.LT
		var bytes []json.RawMessage
		if cache != nil {
			if existing, ok := cache.Lookup(msg.LogicalTime); ok && len(existing.Payload) > 0 {
				bytes = existing.Payload
			}
		}
		if bytes == nil {
			encoded, err := p.encode(msg, snap)
			if err != nil {
				slog.Error("anthropicsdk encode", "flt", msg.LogicalTime, "err", err)
			} else {
				bytes = encoded
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
	return perMessage, lts
}
