package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

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
		if store.VerifyOnce(cfg.Translator) {
			if gap, found := firstGap(cfg, watermark); found {
				slog.Warn("translator rows have a hole; re-deriving from the fig IR",
					"figaro_lt", gap, "watermark", watermark)
				if cerr := cfg.Translator.Clear(); cerr != nil {
					return stats, fmt.Errorf("clear rows with a hole at %d: %w", gap, cerr)
				}
				stats.RepairedAt = gap
				watermark = 0
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

// The hole check runs ONCE PER LOG HANDLE, which is once per aria per daemon
// lifetime -- and that is when a hole can appear, since making one takes a
// crash between the canonical append and the derived one. The flag lives on
// the handle (store.VerifyOnce): it used to live in a process-global sync.Map
// keyed by the log itself, which never forgot, so it pinned every translator
// handle the backend had already evicted and grew for the life of the daemon.

// firstGap is the earliest fig IR record at or below the watermark that would
// have written a row and has none. The watermark alone cannot see one: it is
// the newest row's LT, so a record whose row was lost while later records got
// theirs sits below it forever.
// IT WALKS BOTH LOGS AND HOLDS NEITHER. The check is O(total history) by
// nature -- a hole is defined against the whole prefix -- but it used to be
// O(total history) IN THE HEAP as well: two Read() calls, the translations and
// the fig IR, both materialized inside a send. What it needs from the
// translator is a set of logical times, and what it needs from the fig IR is
// one record at a time.
func firstGap(cfg CatchUpConfig, watermark uint64) (uint64, bool) {
	have := make(map[uint64]struct{})
	store.ScanAll(cfg.Translator, func(r store.Entry[[]json.RawMessage]) bool {
		have[r.FigaroLT] = struct{}{}
		return true
	})
	d := NewDeriver(cfg.Form, cfg.Studies)
	var (
		gap   uint64
		found bool
	)
	store.ScanAll(cfg.Log, func(entry store.Entry[message.Message]) bool {
		if entry.LT > watermark {
			return false
		}
		msg, snap, translatable := d.Next(entry)
		if !translatable {
			return true
		}
		if _, ok := have[entry.LT]; ok {
			d.Commit(entry, msg)
			return true
		}
		// No row is a hole only where a row was owed: an entry that encodes
		// to nothing never had one, and its patches ride the next entry.
		sendable, _ := SendableImages(msg)
		if encoded, err := cfg.Encode(sendable, snap); err == nil && len(encoded) > 0 {
			gap, found = entry.LT, true
			return false
		}
		return true
	})
	return gap, found
}

// TranslationRows streams a translator log as wire rows, in order, as they lie
// on disk: the messages array, one row resident at a time.
//
// It replaces Translations(), which returned the whole log as a slice. The
// slice was the memory bug: 87% of a 461MB live heap, in one in-flight send,
// was the conversation decoded and held for the length of a request. Nothing
// about the WALK changed -- eeec2bc0 ruled that the walk is the design -- only
// that the walk no longer builds an array on its way past.
//
// to bounds the read at a coordinate captured before the request, so a body
// REPLAYED by net/http (a GOAWAY, a 307) sends the same conversation the first
// attempt did rather than one that grew underneath it. Zero means the tail.
func TranslationRows(rows store.Log[[]json.RawMessage], to uint64) RowSeq {
	return func(yield func(json.RawMessage, uint64) bool) {
		if rows == nil {
			return
		}
		store.Scan(rows, 0, to, func(e store.Entry[[]json.RawMessage]) bool {
			for _, raw := range e.Payload {
				if len(raw) == 0 {
					continue
				}
				if !yield(raw, e.FigaroLT) {
					return false
				}
			}
			return true
		})
	}
}

// TranslationTail is the coordinate TranslationRows should be bounded at, and
// reports whether the log has anything in it at all. A send with no rows is an
// empty context, which is an error its caller raises by name.
func TranslationTail(rows store.Log[[]json.RawMessage]) (uint64, bool) {
	if rows == nil {
		return 0, false
	}
	tail, ok := rows.PeekTail()
	if !ok {
		return 0, false
	}
	return tail.LT, true
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
