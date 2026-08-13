package provider

import (
	"encoding/json"

	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
)

type IncrementalProjection[T any] struct {
	State       T
	Form        form.Snapshot
	Fingerprint string
	Entries     int
	LastLT      uint64
	// LastFormVersion is how far the board had advanced at the last entry
	// projected. It MUST survive a warm start: without it the next pass asks
	// for patches from 0 and re-renders the whole board onto the first new
	// message, which the per-LT cache then makes permanent.
	LastFormVersion uint64
	// LastStudyVersions is the same fact for every OBSERVED form (the
	// study half of the cursor stamp), and must survive a warm start for
	// the same reason.
	LastStudyVersions map[string]uint64
	// FormVersionOfSnapshot is where Form actually stands, which is not
	// LastFormVersion once a run of cached entries has been skipped. Both
	// must survive a warm start or the next pass folds the wrong span.
	FormVersionOfSnapshot uint64
}

type ProjectionStats struct {
	Entries    int
	StartIndex int
	Cached     int
	Encoded    int
}

type ProjectionConfig[T any] struct {
	Log   store.Log[message.Message]
	Cache store.Log[[]json.RawMessage]
	Form  Form
	// Studies are the observed forms' patch accessors, keyed by form id.
	// A stamped id with no accessor renders a tombstone: the form ceased
	// to exist while observed, and that is a fact the model should see.
	Studies     map[string]Form
	Previous    *IncrementalProjection[T]
	Fingerprint string
	Initial     T
	Encode      func(message.Message, form.Snapshot) ([]json.RawMessage, error)
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
// Previous.State: bytes, not structs, and unavoidable because it is the
// request body. The decoded prefix was never needed for anything.
func ProjectIncrementally[T any](config ProjectionConfig[T]) (*IncrementalProjection[T], ProjectionStats, error) {
	state := config.Initial
	snap := form.Snapshot{}
	var lastForm, snapAt uint64
	lastStudy := map[string]uint64{}

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
	// was when the watermark was taken. Anything else, a Clear, a fork
	// rewrite, a fingerprint change that raced us: means the cached state
	// describes a log that no longer exists, so fall back to a cold walk.
	if previous := config.Previous; previous != nil &&
		previous.Fingerprint == config.Fingerprint &&
		previous.Entries == prefix {
		state = previous.State
		snap = previous.Form
		lastForm = previous.LastFormVersion
		snapAt = previous.FormVersionOfSnapshot
		for k, v := range previous.LastStudyVersions {
			lastStudy[k] = v
		}
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

		// THE CACHE FIRST. A record whose bytes are already encoded needs no
		// patches: they exist only to be rendered, and the rendering is what
		// was cached. Deriving them and then discarding them cost one board
		// read plus one read per observed form, per record, on every cold
		// pass over a warm cache -- which is what a fingerprint bump or a
		// rejected watermark produces.
		//
		// The cursors still advance, from the entry's own stamps. A stamp is
		// where a form stood, and knowing that never required reading it.
		if cached, ok := lookupCached(config, entry); ok {
			stats.Cached++
			lastForm = maxU64(lastForm, entry.FormChannelVersion)
			for fid, upTo := range entry.StudyVersions {
				lastStudy[fid] = maxU64(lastStudy[fid], upTo)
			}
			// An EPHEMERAL aria has no accessor and carries its patches on
			// the record itself, so there is nothing to catch up from later.
			// They are already decoded here; applying them costs no read.
			if config.Form == nil {
				for _, patch := range msg.Patches {
					snap = snap.Apply(patch)
				}
			}
			if len(cached) > 0 {
				state = config.Append(state, cached, entry.LT)
			}
			continue
		}

		// A MISS, so the board must be real. Skipping the derivation above
		// left `snap` behind wherever the last encoded record put it, and
		// `snap` is what Encode renders transitions against. Catch it up in
		// ONE range read spanning everything skipped, rather than the N reads
		// the per-record derivation used to do.
		if config.Form != nil {
			// Only when something was actually skipped. The guard is not an
			// optimisation of a nil result: it is one read per record that a
			// segment-backed accessor would otherwise perform to be told
			// nothing, and this loop runs once per record forever.
			if snapAt < lastForm {
				for _, p := range config.Form.PatchesBetween(snapAt, lastForm) {
					snap = snap.Apply(p)
				}
				snapAt = lastForm
			}
			// (after, upTo]: the previous entry's mark and this one's. Absolute,
			// so a warm start renders exactly the same patches a cold walk would.
			msg.Patches = config.Form.PatchesBetween(lastForm, entry.FormChannelVersion)
		}
		if entry.FormChannelVersion > lastForm {
			lastForm = entry.FormChannelVersion
		}
		// The observed set: the same derivation per member, from the same
		// stamp. The bound board above is member zero of this pattern; the
		// studied forms are the shared members, and their transitions fold
		// into the provider IR identically: re-derived on retranslate.
		for fid, upTo := range entry.StudyVersions {
			acc := config.Studies[fid]
			if acc == nil {
				if msg.StudyNotes == nil {
					msg.StudyNotes = map[string]string{}
				}
				msg.StudyNotes[fid] = "the observed form no longer exists (removed while studied)"
				continue
			}
			if ps := acc.PatchesBetween(lastStudy[fid], upTo); len(ps) > 0 {
				if msg.StudyPatches == nil {
					msg.StudyPatches = map[string][]message.Patch{}
				}
				msg.StudyPatches[fid] = ps
				if msg.StudyAt == nil {
					msg.StudyAt = map[string]uint64{}
				}
				msg.StudyAt[fid] = upTo
			}
			if upTo > lastStudy[fid] {
				lastStudy[fid] = upTo
			}
		}

		encoded, err := config.Encode(msg, snap)
		if err != nil {
			if config.ReportEncodeError != nil {
				config.ReportEncodeError(entry.LT, err)
			}
			return nil, stats, err
		}
		stats.Encoded++
		if config.Cache != nil && len(encoded) > 0 {
			if _, cerr := config.Cache.Append(store.Entry[[]json.RawMessage]{
				FigaroLT:    entry.LT,
				Payload:     encoded,
				Fingerprint: config.Fingerprint,
			}); cerr != nil && config.HandleCacheError != nil {
				config.HandleCacheError(entry.LT, cerr)
			}
		}
		if len(encoded) > 0 {
			state = config.Append(state, encoded, entry.LT)
		}
		for _, patch := range msg.Patches {
			snap = snap.Apply(patch)
		}
		snapAt = maxU64(snapAt, entry.FormChannelVersion)
	}

	// lastLT must be the tail of the WHOLE log, not of the suffix we walked:
	// on a warm pass with nothing new the suffix is empty and the watermark
	// has to stay where it was, or the next pass re-reads from zero.
	lastLT := watermark
	if len(entries) > 0 {
		lastLT = entries[len(entries)-1].LT
	}
	return &IncrementalProjection[T]{
		State:             state,
		Form:              snap,
		Fingerprint:       config.Fingerprint,
		Entries:           stats.Entries,
		LastLT:            lastLT,
		LastFormVersion:   lastForm,
		LastStudyVersions: lastStudy,

		FormVersionOfSnapshot: snapAt,
	}, stats, nil
}

// lookupCached answers whether this entry is already encoded under the
// current fingerprint.
func lookupCached[T any](config ProjectionConfig[T], entry store.Entry[message.Message]) ([]json.RawMessage, bool) {
	if config.Cache == nil {
		return nil, false
	}
	cached, ok := config.Cache.Lookup(entry.LT)
	if !ok || len(cached.Payload) == 0 {
		return nil, false
	}
	if cached.Fingerprint != "" && cached.Fingerprint != config.Fingerprint {
		return nil, false
	}
	return cached.Payload, true
}

func maxU64(a, b uint64) uint64 {
	if b > a {
		return b
	}
	return a
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
