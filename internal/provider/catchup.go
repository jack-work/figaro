package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
)

// CatchUp translates the fig IR records that have no translation yet and
// WRITES THEM TO THE LOG. It keeps no representation of its own: what it
// produces lives in the log, and the caller reads the log.
//
// The watermark is the log's -- the newest row names the last fig IR record
// that has a translation -- and the record at the watermark carries the
// cursors, so nothing is held between calls.
type CatchUpConfig struct {
	// Log is the fig IR.
	Log store.Log[message.Message]
	// Rows is the translator log for this provider.
	Translator store.Log[[]json.RawMessage]
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
	// RepairedAt is the fig IR LT of a hole found BELOW the watermark, which
	// clears the rows and re-derives them whole. Zero when there was none.
	RepairedAt uint64
}

// ErrNoTranslator is returned when there is no translator log to write to. A send
// that cannot read its own conversation back must fail rather than encode a
// second copy in memory and send that: "degrade to a miss" here means showing
// the model a different conversation than the one on disk.
var ErrNoTranslator = errors.New("provider: no translator log")

func CatchUp(cfg CatchUpConfig) (CatchUpStats, error) {
	var stats CatchUpStats
	if cfg.Translator == nil {
		return stats, ErrNoTranslator
	}

	var watermark uint64
	if tail, ok := cfg.Translator.PeekTail(); ok {
		watermark = tail.FigaroLT
		// THE NEWEST ROW IS THE ONLY ONE THAT CAN BE LYING: a position can be
		// reissued, so a row written for a record that never landed is adopted
		// by whatever lands there next. Everything below the tail is followed
		// by a record that did land, so one comparison settles it.
		if tail.FigaroHash != "" {
			if at, ok := cfg.Log.Lookup(watermark); ok {
				if have, err := store.FigaroHash(at.Payload); err == nil && have != tail.FigaroHash {
					// The row describes a record that is not there: clear and
					// re-derive.
					if cerr := cfg.Translator.Clear(); cerr != nil {
						return stats, fmt.Errorf("clear misaligned rows at %d: %w", watermark, cerr)
					}
					watermark = 0
				}
			}
		}
	}
	if watermark > 0 {
		if _, done := verifiedLogs.Load(cfg.Translator); !done {
			if gap, found := firstGap(cfg, watermark); found {
				slog.Warn("translator rows have a hole; re-deriving from the fig IR",
					"figaro_lt", gap, "watermark", watermark)
				if cerr := cfg.Translator.Clear(); cerr != nil {
					return stats, fmt.Errorf("clear rows with a hole at %d: %w", gap, cerr)
				}
				stats.RepairedAt = gap
				watermark = 0
			}
			verifiedLogs.Store(cfg.Translator, struct{}{})
		}
	}

	entries, total := store.TailAfter(cfg.Log, watermark)
	stats.Entries = total
	stats.Visited = len(entries)
	if len(entries) == 0 {
		return stats, nil
	}

	d := NewDeriver(cfg.Form, cfg.Studies)
	d.SeedAt(cfg.Log, watermark)

	for _, entry := range entries {
		msg, snap, translatable := d.Next(entry)
		if !translatable {
			continue
		}

		msg, _ = SendableImages(msg)
		encoded, err := cfg.Encode(msg, snap)
		if err != nil {
			if cfg.ReportEncodeError != nil {
				cfg.ReportEncodeError(entry.LT, err)
			}
			return stats, fmt.Errorf("encode at %d: %w", entry.LT, err)
		}
		if len(encoded) == 0 {
			// NO ROW, NO COMMIT: the window stays open and its patches ride
			// the next entry that writes one.
			stats.Unwritten++
			continue
		}
		hash, herr := store.FigaroHash(entry.Payload)
		if herr != nil {
			return stats, fmt.Errorf("hash record at %d: %w", entry.LT, herr)
		}
		if _, werr := cfg.Translator.Append(store.Entry[[]json.RawMessage]{
			FigaroLT:    entry.LT,
			Payload:     encoded,
			Fingerprint: cfg.Fingerprint,
			FigaroHash:  hash,
		}); werr != nil {
			if cfg.ReportWriteError != nil {
				cfg.ReportWriteError(entry.LT, werr)
			}
			return stats, fmt.Errorf("write row at %d: %w", entry.LT, werr)
		}
		d.Commit(entry, msg)
		stats.Encoded++
	}
	return stats, nil
}

// verifiedLogs remembers which translator logs this process has checked for a
// hole below the watermark. The handle is cached per aria, so the check runs
// once per aria per daemon lifetime -- which is when a hole can appear, since
// making one takes a crash between the canonical append and the derived one.
var verifiedLogs sync.Map

// firstGap is the earliest fig IR record at or below the watermark that would
// have written a row and has none. The watermark alone cannot see one: it is
// the newest row's LT, so a record whose row was lost while later records got
// theirs sits below it forever.
func firstGap(cfg CatchUpConfig, watermark uint64) (uint64, bool) {
	have := make(map[uint64]struct{})
	for _, r := range cfg.Translator.Read() {
		have[r.FigaroLT] = struct{}{}
	}
	d := NewDeriver(cfg.Form, cfg.Studies)
	for _, entry := range cfg.Log.Read() {
		if entry.LT > watermark {
			break
		}
		msg, snap, translatable := d.Next(entry)
		if !translatable {
			continue
		}
		if _, ok := have[entry.LT]; ok {
			d.Commit(entry, msg)
			continue
		}
		// No row is a hole only where a row was owed: an entry that encodes
		// to nothing never had one, and its patches ride the next entry.
		sendable, _ := SendableImages(msg)
		if encoded, err := cfg.Encode(sendable, snap); err == nil && len(encoded) > 0 {
			return entry.LT, true
		}
	}
	return 0, false
}

// Translations reads a translator log whole, in order: the messages array, as
// it lies on disk.
func Translations(rows store.Log[[]json.RawMessage]) (perMessage [][]json.RawMessage, lts []uint64) {
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

// carriesStudy reports whether a record can carry a studied-form block. Only
// a user message can: every encoder renders StudyReminderTexts under
// RoleInput. A record that cannot carry one must not consume a window.
func carriesStudy(msg message.Message) bool { return msg.Role == message.RoleInput }

// ClearStaleTranslationCache empties a translator log whose stored rows were
// written under a different encoder fingerprint. A provider checks it when it
// opens the log, so everything downstream may assume the rows were written by
// the encoder about to read them.
func ClearStaleTranslationCache(rows store.Log[[]json.RawMessage], fingerprint string) (string, bool, error) {
	entry, ok := rows.PeekTail()
	if !ok || entry.Fingerprint == "" || entry.Fingerprint == fingerprint {
		return "", false, nil
	}
	return entry.Fingerprint, true, rows.Clear()
}

func maxVersion(a, b uint64) uint64 {
	if b > a {
		return b
	}
	return a
}
