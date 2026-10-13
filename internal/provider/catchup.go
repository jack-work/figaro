package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/store"
	"go.opentelemetry.io/otel/attribute"
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
	Provider    string
	Encode      func(message.Message, form.Snapshot) ([]json.RawMessage, error)

	ReportEncodeError func(uint64, error)
	ReportWriteError  func(uint64, error)
}

type CatchUpStats struct {
	Entries   int // fig IR records in the log
	Visited   int // records at or after the watermark
	Encoded   int // records translated and written
	Unwritten int // records that encoded to nothing; each owns an empty row
	// RepairedAt is the LT of a hole found below the watermark; zero when none.
	RepairedAt uint64
}

// ErrNoTranslator fails the send: encoding a second copy in memory would show
// the model a conversation other than the one on disk.
var ErrNoTranslator = errors.New("provider: no translator log")

func CatchUp(cfg CatchUpConfig) (stats CatchUpStats, err error) {
	defer func(start time.Time) {
		figOtel.RecordCatchUp(context.Background(), time.Since(start), stats.Visited, stats.Encoded,
			attribute.String("provider", cfg.Provider))
	}(time.Now())
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
	entries, total := store.TailAfter(cfg.Log, watermark)
	if watermark > 0 && store.VerifyOnce(cfg.Translator) {
		if owed := total - len(entries) - genesisBelow(cfg.Log, watermark); cfg.Translator.Len() != owed {
			if gap, _, found := firstGap(cfg, watermark); found {
				slog.Warn("translator rows have a hole; re-deriving from the fig IR",
					"figaro_lt", gap, "watermark", watermark)
				if cerr := cfg.Translator.Clear(); cerr != nil {
					return stats, fmt.Errorf("clear rows with a hole at %d: %w", gap, cerr)
				}
				stats.RepairedAt = gap
				watermark = 0
				entries, total = store.TailAfter(cfg.Log, watermark)
			}
		}
	}
	stats.Entries = total
	stats.Visited = len(entries)
	if len(entries) == 0 {
		return stats, nil
	}

	d := NewDeriver(cfg.Form, cfg.Studies)
	d.SeedAt(cfg.Log, SeedLT(cfg.Translator))

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
		// An empty row commits no cursor: its window rides the next entry
		// that carries bytes, and SeedLT resumes from there.
		empty := len(encoded) == 0
		if empty {
			stats.Unwritten++
			encoded = []json.RawMessage{}
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
		if empty {
			continue
		}
		d.Commit(entry, msg)
		stats.Encoded++
	}
	return stats, nil
}

// SeedLT is the FigaroLT the deriver resumes from: the newest row that
// carries bytes, zero when none does.
func SeedLT(rows store.Log[[]json.RawMessage]) uint64 {
	tail, ok := rows.PeekTail()
	if !ok {
		return 0
	}
	if len(tail.Payload) > 0 {
		return tail.FigaroLT
	}
	before := tail.LT
	for {
		page, _ := rows.ReadPage(0, before, 32)
		if len(page) == 0 {
			return 0
		}
		for i := len(page) - 1; i >= 0; i-- {
			if len(page[i].Payload) > 0 {
				return page[i].FigaroLT
			}
		}
		before = page[0].LT
	}
}

// genesisBelow counts the untranslatable records at or below the watermark.
// Genesis is written once, at LT 1 of the root, so the count is a lookup.
func genesisBelow(log store.Log[message.Message], watermark uint64) int {
	if watermark < 1 {
		return 0
	}
	if at, ok := log.Lookup(1); ok && at.Payload.Role == message.RoleGenesis {
		return 1
	}
	return 0
}

// firstGap is the earliest translatable record at or below the watermark
// with no row, and the channel LT of the first row at or after it. It
// decodes both logs once, in order, and stops at the gap.
func firstGap(cfg CatchUpConfig, watermark uint64) (gap, cut uint64, found bool) {
	type rowKey struct{ lt, figaroLT uint64 }
	var rows []rowKey
	store.ScanAll(cfg.Translator, func(r store.Entry[[]json.RawMessage]) bool {
		rows = append(rows, rowKey{r.LT, r.FigaroLT})
		return true
	})
	i := 0
	store.ScanAll(cfg.Log, func(entry store.Entry[message.Message]) bool {
		if entry.LT > watermark {
			return false
		}
		if entry.Payload.Role == message.RoleGenesis {
			return true
		}
		for i < len(rows) && rows[i].figaroLT < entry.LT {
			i++
		}
		if i < len(rows) && rows[i].figaroLT == entry.LT {
			return true
		}
		gap, found = entry.LT, true
		if i < len(rows) {
			cut = rows[i].lt
		} else if len(rows) > 0 {
			cut = rows[len(rows)-1].lt + 1
		}
		return false
	})
	return gap, cut, found
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
