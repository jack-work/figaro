package anthropicsdk

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

// cacheFor returns this provider's lineage cache, opening lazily. Returns
// nil if caching is unconfigured or the open failed.
func (p *Provider) cacheFor(aria string) store.Log[[]json.RawMessage] {
	if aria == "" || p.CacheOpen == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cache != nil {
		return p.cache
	}
	s, err := p.CacheOpen(aria)
	if err != nil {
		slog.Warn("anthropicsdk cache open", "aria", aria, "err", err)
		return nil
	}
	if !p.invalidateIfStale(s) {
		return nil
	}
	p.cache = s
	return s
}

// invalidateIfStale clears the cache on fingerprint mismatch.
func (p *Provider) invalidateIfStale(s store.Log[[]json.RawMessage]) bool {
	want := p.Fingerprint()
	stored, cleared, err := provider.ClearStaleTranslationCache(s, want)
	if err != nil {
		slog.Warn("anthropicsdk clear stale cache", "stored", stored, "current", want, "err", err)
		return false
	}
	if cleared {
		slog.Info("anthropicsdk cleared stale cache", "stored", stored, "current", want)
	}
	return true
}

// catchUp encodes any figLog entries not yet in the cache and
// returns per-message wire bytes plus their logical times. Uses an
// in-memory snapshot cache to avoid re-walking the entire conversation
// on every turn: only new entries (since the last call) are processed.
func (p *Provider) catchUp(figLog store.Log[message.Message], cache store.Log[[]json.RawMessage], chalk provider.Chalkboard) ([][]json.RawMessage, []uint64) {
	t0 := time.Now()
	fp := p.Fingerprint()
	p.mu.Lock()
	previous := p.projection
	p.mu.Unlock()

	projection, stats, err := provider.ProjectIncrementally(provider.ProjectionConfig[provider.EncodedMessages]{
		Log:         figLog,
		Cache:       cache,
		Chalkboard:  chalk,
		Previous:    previous,
		Fingerprint: fp,
		Encode:      p.encode,
		Append:      provider.AppendEncodedMessage,
		ReportEncodeError: func(lt uint64, err error) {
			slog.Error("anthropicsdk encode", "flt", lt, "err", err)
		},
	})
	if err != nil {
		slog.Error("anthropicsdk project", "err", err)
		return nil, nil
	}
	p.mu.Lock()
	p.projection = projection
	p.mu.Unlock()

	tTotal := time.Since(t0)
	if tTotal > 200*time.Millisecond {
		slog.Warn("anthropicsdk catchUp slow",
			"total", tTotal,
			"entries", stats.Entries,
			"startIdx", stats.StartIndex,
			"cached", stats.Cached,
			"encoded", stats.Encoded,
		)
	}
	return projection.State.PerMessage, projection.State.LogicalTimes
}
