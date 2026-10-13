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

// CatchUp translates the untranslated fig IR records into the translator
// log. The watermark and cursors are read from the log's newest row; nothing
// is held between calls.
type CatchUpConfig struct {
	Log         store.Log[message.Message]
	Translator  store.Log[[]json.RawMessage]
	Form        Form
	Studies     map[string]Form
	Fingerprint string
	Encode      func(message.Message, form.Snapshot) ([]json.RawMessage, error)

	ReportEncodeError func(uint64, error)
	ReportWriteError  func(uint64, error)
}

type CatchUpStats struct {
	Entries   int // fig IR records in the log
	Visited   int // records at or after the watermark
	Encoded   int // records translated and written
	Unwritten int // records that encoded to nothing (genesis, empty)
	// RepairedAt is the LT of a hole found below the watermark; zero when none.
	RepairedAt uint64
}

// ErrNoTranslator fails the send: encoding a second copy in memory would show
// the model a conversation other than the one on disk.
var ErrNoTranslator = errors.New("provider: no translator log")

func CatchUp(cfg CatchUpConfig) (CatchUpStats, error) {
	var stats CatchUpStats
	if cfg.Translator == nil {
		return stats, ErrNoTranslator
	}

	var watermark uint64
	if tail, ok := cfg.Translator.PeekTail(); ok {
		watermark = tail.FigaroLT
		// Only the newest row can describe a record that never landed; a
		// reissued position adopts it.
		if tail.FigaroHash != "" {
			if at, ok := cfg.Log.Lookup(watermark); ok {
				if have, err := store.FigaroHash(at.Payload); err == nil && have != tail.FigaroHash {
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
			// No row, no commit: the patches ride the next entry that writes one.
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

// The hole check runs once per log handle; the flag lives on the handle so
// an evicted handle is not pinned by it.

// firstGap is the earliest record at or below the watermark that owed a row
// and has none. It walks both logs and materializes neither.
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
		// A missing row is a hole only where one was owed.
		sendable, _ := SendableImages(msg)
		if encoded, err := cfg.Encode(sendable, snap); err == nil && len(encoded) > 0 {
			gap, found = entry.LT, true
			return false
		}
		return true
	})
	return gap, found
}

// TranslationRows streams the translator log one row resident at a time; a
// whole-history slice on the send path is the design failure of invariant
// #14. to bounds the read so an http replay sends the same conversation;
// zero means the tail.
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

// TranslationTail is the bound for TranslationRows; false when the log is empty.
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

// carriesStudy: only a user message carries a studied-form block, so only
// one may consume a window.
func carriesStudy(msg message.Message) bool { return msg.Role == message.RoleInput }

// ClearStaleTranslationCache empties a log written under another fingerprint.
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
