package provider

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
)

// CatchUp translates the fig IR records that have no translation yet and
// WRITES THEM TO THE LOG. It keeps no representation of its own: what it
// produces lives in the log, and the caller reads the log.
//
// It replaces ProjectIncrementally and the four per-provider wrappers that
// each carried a different accumulator. Those existed to hand the assembler
// an in-memory messages array; the assembler now splices the rows.
//
// THE WATERMARK IS THE LOG'S, NOT A MEMO'S: the newest row names the last
// fig IR record that has a translation. And THE RECORD AT THE WATERMARK
// CARRIES THE CURSORS -- its FormChannelVersion and StudyVersions are where
// the boards stood when it was written -- so the five version fields the old
// projection carried between calls are recoverable from the logs themselves.
type CatchUpConfig struct {
	// Log is the fig IR.
	Log store.Log[message.Message]
	// Rows is the translator log for this provider.
	Rows store.Log[[]json.RawMessage]
	// Form is the bound board's patch accessor; nil for a log with none.
	Form Form
	// Studies are the observed forms' accessors, keyed by form id.
	Studies     map[string]Form
	Fingerprint string
	Encode      func(message.Message, form.Snapshot) ([]json.RawMessage, error)

	ReportEncodeError func(uint64, error)
	ReportWriteError  func(uint64, error)
}

// CatchUpStats reports what one pass did. Counts, not times: the question is
// how many records had to be translated.
type CatchUpStats struct {
	Entries   int // fig IR records in the log
	Visited   int // records at or after the watermark
	Encoded   int // records translated and written
	Unwritten int // records that encoded to nothing (genesis, empty)
}

// ErrNoRows is returned when there is no translator log to write to. A send
// that cannot read its own conversation back must fail rather than encode a
// second copy in memory and send that: "degrade to a miss" here means showing
// the model a different conversation than the one on disk.
var ErrNoRows = errors.New("provider: no translator log")

func CatchUp(cfg CatchUpConfig) (CatchUpStats, error) {
	var stats CatchUpStats
	if cfg.Rows == nil {
		return stats, ErrNoRows
	}

	var watermark uint64
	if tail, ok := cfg.Rows.PeekTail(); ok {
		watermark = tail.FigaroLT
	}
	entries, total := store.TailAfter(cfg.Log, watermark)
	stats.Entries = total
	stats.Visited = len(entries)
	if len(entries) == 0 {
		return stats, nil
	}

	// THE CURSORS COME OFF THE WATERMARK RECORD. Where the boards stood when
	// the last translated record was written is stamped on that record; a
	// memo carried between calls was a copy of it.
	lastForm := uint64(0)
	lastStudy := map[string]uint64{}
	if watermark > 0 {
		if at, ok := cfg.Log.Lookup(watermark); ok {
			lastForm = at.FormChannelVersion
			for fid, v := range at.StudyVersions {
				lastStudy[fid] = v
			}
		}
	}

	// The board as it stood at the watermark. Folded from zero, which costs
	// one patch application per form patch: measured across a real store at
	// p50=5, p99=74, max=371 -- the fold the five carried version fields
	// existed to avoid.
	snap := form.Snapshot{}
	if cfg.Form != nil && lastForm > 0 {
		snap = form.Fold(snap, cfg.Form.PatchesBetween(0, lastForm))
	}

	for _, entry := range entries {
		msg := entry.Payload
		msg.LogicalTime = entry.LT
		if msg.Role == message.RoleGenesis {
			// Genesis is furniture: it advances the cursors and translates
			// to nothing.
			lastForm = maxVersion(lastForm, entry.FormChannelVersion)
			for fid, upTo := range entry.StudyVersions {
				lastStudy[fid] = maxVersion(lastStudy[fid], upTo)
			}
			continue
		}

		if cfg.Form != nil {
			// (after, upTo]: the previous record's mark and this one's.
			msg.Patches = cfg.Form.PatchesBetween(lastForm, entry.FormChannelVersion)
		}
		lastForm = maxVersion(lastForm, entry.FormChannelVersion)

		// A WINDOW MAY ONLY CLOSE ON A RECORD THAT CAN CARRY THE BLOCK. A
		// studied form's transitions ride a USER message: every encoder
		// renders them under RoleInput and nowhere else. An assistant record
		// that consumed its window would compute a block, drop it, and leave
		// the next user record asking for (v, v] -- so the change would never
		// be shown to anyone, ever.
		if carriesStudy(msg) {
			for fid, upTo := range entry.StudyVersions {
				// ADVANCE FIRST, whatever happens below: the cursor is where
				// the form STOOD at this stamp, which is true of a form that
				// has since been deleted too.
				prev := lastStudy[fid]
				lastStudy[fid] = maxVersion(prev, upTo)
				acc := cfg.Studies[fid]
				if acc == nil {
					continue
				}
				if ps := acc.PatchesBetween(prev, upTo); len(ps) > 0 {
					if msg.StudyPatches == nil {
						msg.StudyPatches = map[string][]message.Patch{}
					}
					msg.StudyPatches[fid] = ps
					if msg.StudyAt == nil {
						msg.StudyAt = map[string]uint64{}
					}
					msg.StudyAt[fid] = upTo
				}
			}
		}

		encoded, err := cfg.Encode(msg, snap)
		if err != nil {
			if cfg.ReportEncodeError != nil {
				cfg.ReportEncodeError(entry.LT, err)
			}
			return stats, fmt.Errorf("encode at %d: %w", entry.LT, err)
		}
		snap = form.Fold(snap, msg.Patches)
		if len(encoded) == 0 {
			stats.Unwritten++
			continue
		}
		if _, werr := cfg.Rows.Append(store.Entry[[]json.RawMessage]{
			FigaroLT:    entry.LT,
			Payload:     encoded,
			Fingerprint: cfg.Fingerprint,
		}); werr != nil {
			if cfg.ReportWriteError != nil {
				cfg.ReportWriteError(entry.LT, werr)
			}
			return stats, fmt.Errorf("write row at %d: %w", entry.LT, werr)
		}
		stats.Encoded++
	}
	return stats, nil
}

// Rows reads a translator log whole, in order: the messages array, as it
// lies on disk. This is the read that replaced the projection's accumulator.
func Rows(rows store.Log[[]json.RawMessage]) (perMessage [][]json.RawMessage, lts []uint64) {
	if rows == nil {
		return nil, nil
	}
	entries := rows.Read()
	perMessage = make([][]json.RawMessage, 0, len(entries))
	lts = make([]uint64, 0, len(entries))
	for _, e := range entries {
		if len(e.Payload) == 0 {
			continue
		}
		perMessage = append(perMessage, e.Payload)
		lts = append(lts, e.FigaroLT)
	}
	return perMessage, lts
}

func maxVersion(a, b uint64) uint64 {
	if b > a {
		return b
	}
	return a
}
