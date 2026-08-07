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
	// LastChalkVersion is how far the board had advanced at the last entry
	// projected. It MUST survive a warm start: without it the next pass asks
	// for patches from 0 and re-renders the whole board onto the first new
	// message, which the per-LT cache then makes permanent.
	LastChalkVersion uint64
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
//
// It READS only the suffix too, which it did not used to: the whole log was
// materialized and then sliced, so a warm pass that touched three messages
// still required all N to be decoded and resident. Since the decoded fig IR
// runs 4-5x its wire bytes and is the largest thing a live aria holds, that
// slice was the single biggest reason an agent could not be made cheap.
//
// What the wire needs in full is the ENCODED projection, carried in
// Previous.State — bytes, not structs, and unavoidable because it is the
// request body. The decoded prefix was never needed for anything.
func ProjectIncrementally[T any](config ProjectionConfig[T]) (*IncrementalProjection[T], ProjectionStats, error) {
	state := config.Initial
	snap := chalkboard.Snapshot{}
	var lastChalk uint64

	// A warm start reads from the watermark; a cold one reads everything.
	// TailAfter(0) is the whole log, so the two are one call.
	var watermark uint64
	if previous := config.Previous; previous != nil && previous.Fingerprint == config.Fingerprint {
		watermark = previous.LastLT
	}
	entries, total := store.TailAfter(config.Log, watermark)
	stats := ProjectionStats{Entries: total}
	prefix := total - len(entries)

	// The watermark is only trustworthy if the prefix is exactly as long as it
	// was when the watermark was taken. Anything else — a Clear, a fork
	// rewrite, a fingerprint change that raced us — means the cached state
	// describes a log that no longer exists, so fall back to a cold walk.
	if previous := config.Previous; previous != nil &&
		previous.Fingerprint == config.Fingerprint &&
		previous.Entries == prefix {
		state = previous.State
		snap = previous.Chalkboard
		lastChalk = previous.LastChalkVersion
		stats.StartIndex = prefix
	} else if prefix > 0 {
		// Watermark rejected but we only read the suffix. Re-read cold.
		entries, total = store.TailAfter(config.Log, 0)
		stats = ProjectionStats{Entries: total}
	}

	for _, entry := range entries {
		msg := entry.Payload
		msg.LogicalTime = entry.LT
		if msg.Role == message.RoleGenesis {
			continue
		}
		if config.Chalkboard != nil {
			// (after, upTo]: the previous entry's mark and this one's. Absolute,
			// so a warm start renders exactly the same patches a cold walk would.
			msg.Patches = config.Chalkboard.PatchesBetween(lastChalk, entry.ChalkVersion)
		}
		if entry.ChalkVersion > lastChalk {
			lastChalk = entry.ChalkVersion
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

	// lastLT must be the tail of the WHOLE log, not of the suffix we walked:
	// on a warm pass with nothing new the suffix is empty and the watermark
	// has to stay where it was, or the next pass re-reads from zero.
	lastLT := watermark
	if len(entries) > 0 {
		lastLT = entries[len(entries)-1].LT
	}
	return &IncrementalProjection[T]{
		State:            state,
		Chalkboard:       snap,
		Fingerprint:      config.Fingerprint,
		Entries:          stats.Entries,
		LastLT:           lastLT,
		LastChalkVersion: lastChalk,
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
