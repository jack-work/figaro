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

// rowsToMessageParams turns stored rows into the SDK's typed messages.
//
// THE ROWS ARE ALREADY THE SDK'S OWN MARSHALLING: this provider writes what
// MessageParam serializes to, and the round trip is byte-identical (see
// TestMessageParamRoundTripsThroughJSONWithoutLoss). So the read is a parse,
// never a re-encode, and what reaches the wire is what is on disk.
func rowsToMessageParams(perMessage [][]json.RawMessage, lts []uint64) ([]anthropic.MessageParam, []uint64, error) {
	msgs := make([]anthropic.MessageParam, 0, len(perMessage))
	out := make([]uint64, 0, len(perMessage))
	for i, row := range perMessage {
		for _, raw := range row {
			if len(raw) == 0 {
				continue
			}
			var msg anthropic.MessageParam
			if err := json.Unmarshal(raw, &msg); err != nil {
				return nil, nil, fmt.Errorf("unmarshal stored message at %d: %w", lts[i], err)
			}
			msgs = append(msgs, msg)
			out = append(out, lts[i])
		}
	}
	return msgs, out, nil
}

// cacheFor returns this provider's lineage cache, opening lazily.
func (p *Provider) cacheFor(aria string) (store.Log[[]json.RawMessage], error) {
	if aria == "" || p.CacheOpen == nil {
		return nil, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cache != nil {
		return p.cache, nil
	}
	s, err := p.CacheOpen(aria)
	if err != nil {
		slog.Warn("anthropicsdk cache open failed; running uncached", "aria", aria, "err", err)
		return nil, nil
	}
	if !p.invalidateIfStale(s) {
		slog.Warn("anthropicsdk cache invalidation failed; running uncached", "aria", aria)
		return nil, nil
	}
	p.cache = s
	return s, nil
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

// catchUp translates whatever the row log has not translated yet, writes those
// rows, and hands back the SDK's typed messages parsed from them. It keeps
// nothing between calls: the watermark is the row log's tail.
func (p *Provider) catchUp(figLog store.Log[message.Message], rows store.Log[[]json.RawMessage], form provider.Form, studies map[string]provider.Form) ([]anthropic.MessageParam, []uint64, error) {
	t0 := time.Now()
	stats, err := provider.CatchUp(provider.CatchUpConfig{
		Log:         figLog,
		Translator:  rows,
		Form:        form,
		Studies:     studies,
		Fingerprint: p.Fingerprint(),
		Encode:      p.encode,
		ReportEncodeError: func(lt uint64, err error) {
			slog.Error("anthropicsdk encode", "flt", lt, "err", err)
		},
		ReportWriteError: func(lt uint64, err error) {
			slog.Error("anthropicsdk write row", "flt", lt, "err", err)
		},
	})
	if err != nil {
		// A SEND THAT CANNOT WRITE ITS ROWS MUST NOT PROCEED. The history it
		// would assemble is not the one on disk.
		return nil, nil, fmt.Errorf("anthropicsdk catch up: %w", err)
	}

	perMessage, lts := provider.Translations(rows)
	msgs, msgLTs, perr := rowsToMessageParams(perMessage, lts)
	if perr != nil {
		return nil, nil, perr
	}

	if total := time.Since(t0); total > 200*time.Millisecond {
		slog.Warn("anthropicsdk catchUp slow",
			"total", total,
			"entries", stats.Entries,
			"visited", stats.Visited,
			"encoded", stats.Encoded,
			"messages", len(msgs),
		)
	}
	return msgs, msgLTs, nil
}
