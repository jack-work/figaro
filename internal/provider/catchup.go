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
		// THE NEWEST ROW IS THE ONLY ONE THAT CAN BE LYING. A row names its
		// record by position, and a position can be reissued: a row written
		// for a record that never landed is adopted by whatever lands there
		// next. Only the tail can be in that state -- everything below it is
		// followed by a record that did land -- so ONE comparison settles it,
		// not a scan.
		if tail.FigaroHash != "" {
			if at, ok := cfg.Log.Lookup(watermark); ok {
				if have, err := store.FigaroHash(at.Payload); err == nil && have != tail.FigaroHash {
					// The row describes a record that is not there. Clear and
					// re-derive: rows are derived state and this is the one
					// canonical moment at which they regenerate.
					if cerr := cfg.Translator.Clear(); cerr != nil {
						return stats, fmt.Errorf("clear misaligned rows at %d: %w", watermark, cerr)
					}
					watermark = 0
				}
			}
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

		encoded, err := cfg.Encode(msg, snap)
		if err != nil {
			if cfg.ReportEncodeError != nil {
				cfg.ReportEncodeError(entry.LT, err)
			}
			return stats, fmt.Errorf("encode at %d: %w", entry.LT, err)
		}
		if len(encoded) == 0 {
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
		stats.Encoded++
	}
	return stats, nil
}

// Rows reads a translator log whole, in order: the messages array, as it
// lies on disk. This is the read that replaced the projection's accumulator.
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

// ClearStaleRows empties a translator log whose stored rows were written under
// a different encoder fingerprint. THIS IS THE ONE CANONICAL MOMENT at which
// derived rows are regenerated: a provider checks it when it opens the log,
// and everything downstream may then assume the rows were written by the
// encoder that is about to read them.
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
