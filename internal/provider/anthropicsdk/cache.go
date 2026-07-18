package anthropicsdk

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

type projectedMessages struct {
	Messages     []anthropic.MessageParam
	LogicalTimes []uint64
	err          error
}

func appendProjectedMessages(state projectedMessages, encoded []json.RawMessage, lt uint64) projectedMessages {
	if state.err != nil {
		return state
	}
	for _, raw := range encoded {
		if len(raw) == 0 {
			continue
		}
		var msg anthropic.MessageParam
		if err := json.Unmarshal(raw, &msg); err != nil {
			state.err = fmt.Errorf("unmarshal cached message: %w", err)
			return state
		}
		state.Messages = append(state.Messages, msg)
		state.LogicalTimes = append(state.LogicalTimes, lt)
	}
	return state
}

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

// catchUp projects untranslated entries into immutable typed messages.
// Cached raw bytes are parsed only when their entry first joins the projection.
func (p *Provider) catchUp(figLog store.Log[message.Message], cache store.Log[[]json.RawMessage], chalk provider.Chalkboard) (projectedMessages, error) {
	t0 := time.Now()
	fp := p.Fingerprint()
	p.mu.Lock()
	previous := p.projection
	p.mu.Unlock()

	projection, stats, err := provider.ProjectIncrementally(provider.ProjectionConfig[projectedMessages]{
		Log:         figLog,
		Cache:       cache,
		Chalkboard:  chalk,
		Previous:    previous,
		Fingerprint: fp,
		Encode:      p.encode,
		Append:      appendProjectedMessages,
		ReportEncodeError: func(lt uint64, err error) {
			slog.Error("anthropicsdk encode", "flt", lt, "err", err)
		},
	})
	if err != nil {
		return projectedMessages{}, fmt.Errorf("project messages: %w", err)
	}
	if projection.State.err != nil {
		return projectedMessages{}, projection.State.err
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
	return projection.State, nil
}
