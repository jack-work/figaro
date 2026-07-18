package provider

import (
	"encoding/json"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
)

type IncrementalProjection[T any] struct {
	State       T
	Chalkboard  chalkboard.Snapshot
	Fingerprint string
	Entries     int
	LastLT      uint64
}

type ProjectionStats struct {
	Entries    int
	StartIndex int
	Cached     int
	Encoded    int
}

type ProjectionConfig[T any] struct {
	Log         store.Log[message.Message]
	Cache       store.Log[[]json.RawMessage]
	Chalkboard  Chalkboard
	Previous    *IncrementalProjection[T]
	Fingerprint string
	Initial     T
	Encode      func(message.Message, chalkboard.Snapshot) ([]json.RawMessage, error)
	Append      func(T, []json.RawMessage, uint64) T

	ReportEncodeError func(uint64, error)
	HandleCacheError  func(uint64, error)
}

// ProjectIncrementally validates one append-only watermark, then visits only
// the untranslated suffix. The retained state is in-memory and derivable.
func ProjectIncrementally[T any](config ProjectionConfig[T]) (*IncrementalProjection[T], ProjectionStats, error) {
	entries := store.Snapshot(config.Log)
	stats := ProjectionStats{Entries: len(entries)}
	state := config.Initial
	snap := chalkboard.Snapshot{}

	if previous := config.Previous; previous != nil &&
		previous.Fingerprint == config.Fingerprint &&
		previous.Entries <= len(entries) &&
		(previous.Entries == 0 || entries[previous.Entries-1].LT == previous.LastLT) {
		state = previous.State
		snap = previous.Chalkboard
		stats.StartIndex = previous.Entries
	}

	for _, entry := range entries[stats.StartIndex:] {
		msg := entry.Payload
		msg.LogicalTime = entry.LT
		if msg.Role == message.RoleGenesis {
			continue
		}
		if config.Chalkboard != nil {
			msg.Patches = config.Chalkboard.PatchesAt(entry.LT)
		}

		var encoded []json.RawMessage
		if config.Cache != nil {
			if cached, ok := config.Cache.Lookup(entry.LT); ok &&
				(cached.Fingerprint == "" || cached.Fingerprint == config.Fingerprint) &&
				len(cached.Payload) > 0 {
				encoded = cached.Payload
				stats.Cached++
			}
		}
		if encoded == nil {
			var err error
			encoded, err = config.Encode(msg, snap)
			if err != nil {
				if config.ReportEncodeError != nil {
					config.ReportEncodeError(entry.LT, err)
				}
				return nil, stats, err
			} else {
				stats.Encoded++
				if config.Cache != nil && len(encoded) > 0 {
					_, err = config.Cache.Append(store.Entry[[]json.RawMessage]{
						FigaroLT:    entry.LT,
						Payload:     encoded,
						Fingerprint: config.Fingerprint,
					})
					if err != nil && config.HandleCacheError != nil {
						config.HandleCacheError(entry.LT, err)
					}
				}
			}
		}
		if len(encoded) > 0 {
			state = config.Append(state, encoded, entry.LT)
		}
		for _, patch := range msg.Patches {
			snap = snap.Apply(patch)
		}
	}

	var lastLT uint64
	if len(entries) > 0 {
		lastLT = entries[len(entries)-1].LT
	}
	return &IncrementalProjection[T]{
		State:       state,
		Chalkboard:  snap,
		Fingerprint: config.Fingerprint,
		Entries:     len(entries),
		LastLT:      lastLT,
	}, stats, nil
}

type EncodedMessages struct {
	PerMessage   [][]json.RawMessage
	LogicalTimes []uint64
}

func AppendEncodedMessage(state EncodedMessages, encoded []json.RawMessage, lt uint64) EncodedMessages {
	state.PerMessage = append(state.PerMessage, encoded)
	state.LogicalTimes = append(state.LogicalTimes, lt)
	return state
}

func ClearStaleTranslationCache(cache store.Log[[]json.RawMessage], fingerprint string) (string, bool, error) {
	entry, ok := cache.PeekTail()
	if !ok || entry.Fingerprint == "" || entry.Fingerprint == fingerprint {
		return "", false, nil
	}
	return entry.Fingerprint, true, cache.Clear()
}
