package provider

import (
	"encoding/json"
	"errors"
	"strconv"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
)

func catchUpTestConfig(log store.Log[message.Message], rows store.Log[[]json.RawMessage]) CatchUpConfig {
	return CatchUpConfig{
		Log:        log,
		Translator: rows,
		Encode: func(msg message.Message, _ form.Snapshot) ([]json.RawMessage, error) {
			body, err := json.Marshal(map[string]any{"role": string(msg.Role), "lt": msg.LogicalTime})
			if err != nil {
				return nil, err
			}
			return []json.RawMessage{body}, nil
		},
	}
}

// Synchronisation is O(new): a second pass visits only the records appended
// since the first.
func TestCatchUpVisitsOnlyWhatIsNew(t *testing.T) {
	log := store.NewMemLog[message.Message]()
	rows := store.NewMemLog[[]json.RawMessage]()
	for i := 0; i < 20; i++ {
		appendProjectionMessage(t, log, "body "+strconv.Itoa(i))
	}
	cfg := catchUpTestConfig(log, rows)

	first, err := CatchUp(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if first.Visited != 20 || first.Encoded != 20 {
		t.Fatalf("cold pass visited=%d encoded=%d, want 20/20", first.Visited, first.Encoded)
	}

	appendProjectionMessage(t, log, "new one")
	appendProjectionMessage(t, log, "new two")

	second, err := CatchUp(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if second.Entries != 22 {
		t.Fatalf("entries=%d, want 22", second.Entries)
	}
	if second.Visited != 2 || second.Encoded != 2 {
		t.Fatalf("warm pass visited=%d encoded=%d, want 2/2", second.Visited, second.Encoded)
	}

	steady, err := CatchUp(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if steady.Visited != 0 || steady.Encoded != 0 {
		t.Fatalf("steady pass visited=%d encoded=%d, want 0/0", steady.Visited, steady.Encoded)
	}
	if got := len(rows.Read()); got != 22 {
		t.Fatalf("rows=%d, want 22 -- one per record, written once", got)
	}
}

func TestCatchUpWithoutRowsIsAnError(t *testing.T) {
	log := store.NewMemLog[message.Message]()
	appendProjectionMessage(t, log, "body")
	if _, err := CatchUp(catchUpTestConfig(log, nil)); !errors.Is(err, ErrNoTranslator) {
		t.Fatalf("err=%v, want ErrNoTranslator", err)
	}
}

// A row whose record hash does not match was adopted by a reissued LT; the
// channel is re-derived.
func TestARowWhoseFigaroHashDoesNotMatchIsRefused(t *testing.T) {
	log := store.NewMemLog[message.Message]()
	rows := store.NewMemLog[[]json.RawMessage]()
	first := appendProjectionMessage(t, log, "the real one")

	wrongHash, err := store.FigaroHash(message.Message{
		Role: message.RoleInput, Content: []message.Content{message.TextContent("a message that never landed")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rows.Append(store.Entry[[]json.RawMessage]{
		FigaroLT:   first.LT,
		Payload:    []json.RawMessage{json.RawMessage(`{"orphan":true}`)},
		FigaroHash: wrongHash,
	}); err != nil {
		t.Fatal(err)
	}

	stats, err := CatchUp(catchUpTestConfig(log, rows))
	if err != nil {
		t.Fatal(err)
	}
	if stats.Visited != 1 || stats.Encoded != 1 {
		t.Fatalf("visited=%d encoded=%d: the misaligned row must not be trusted as a watermark",
			stats.Visited, stats.Encoded)
	}
	served := rows.Read()
	if len(served) != 1 {
		t.Fatalf("rows=%d, want 1 -- the channel is cleared and re-derived, not appended to", len(served))
	}
	if string(served[0].Payload[0]) == `{"orphan":true}` {
		t.Fatal("the orphan row survived and would reach the wire")
	}
	if served[0].FigaroHash == "" {
		t.Fatal("the re-derived row carries no record hash, so the next pass cannot check it")
	}
}

func TestAMatchingFigaroHashLeavesTheChannelAlone(t *testing.T) {
	log := store.NewMemLog[message.Message]()
	rows := store.NewMemLog[[]json.RawMessage]()
	appendProjectionMessage(t, log, "one")
	cfg := catchUpTestConfig(log, rows)
	if _, err := CatchUp(cfg); err != nil {
		t.Fatal(err)
	}
	before := rows.Read()[0]

	appendProjectionMessage(t, log, "two")
	stats, err := CatchUp(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Visited != 1 || stats.Encoded != 1 {
		t.Fatalf("visited=%d encoded=%d, want 1/1: a healthy channel must not be re-derived",
			stats.Visited, stats.Encoded)
	}
	after := rows.Read()
	if len(after) != 2 || after[0].LT != before.LT {
		t.Fatalf("the first row moved: rows=%d", len(after))
	}
}

// A record with no row below the watermark (a crash between the canonical
// and derived appends) is found and repaired.
func TestCatchUpFillsAHoleBelowTheWatermark(t *testing.T) {
	log := store.NewMemLog[message.Message]()
	rows := store.NewMemLog[[]json.RawMessage]()
	for i := range 6 {
		appendProjectionMessage(t, log, "body "+strconv.Itoa(i))
	}
	cfg := catchUpTestConfig(log, rows)
	if _, err := CatchUp(cfg); err != nil {
		t.Fatal(err)
	}

	all := rows.Read()
	if len(all) < 6 {
		t.Fatalf("fixture wrote %d rows", len(all))
	}
	missing := all[3].FigaroLT
	// A fresh handle stands in for a restart: the once flag lives on it.
	rows = store.NewMemLog[[]json.RawMessage]()
	cfg = catchUpTestConfig(log, rows)
	for _, r := range all {
		if r.FigaroLT == missing {
			continue
		}
		if _, err := rows.Append(store.Entry[[]json.RawMessage]{
			FigaroLT: r.FigaroLT, Payload: r.Payload,
			Fingerprint: r.Fingerprint, FigaroHash: r.FigaroHash,
		}); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := CatchUp(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if stats.RepairedAt != missing {
		t.Fatalf("RepairedAt=%d, want the hole at %d", stats.RepairedAt, missing)
	}
	got := map[uint64]bool{}
	for _, r := range rows.Read() {
		got[r.FigaroLT] = true
	}
	for _, e := range log.Read() {
		if !got[e.LT] {
			t.Fatalf("fig IR %d still has no row after catch-up", e.LT)
		}
	}
}

// store.VerifyOnce answers true once per handle; asking consumes it, so each
// half of this test gets its own log.
func TestCatchUpChecksForHolesOncePerLog(t *testing.T) {
	t.Run("an empty translator is not checked", func(t *testing.T) {
		log := store.NewMemLog[message.Message]()
		rows := store.NewMemLog[[]json.RawMessage]()
		for i := range 4 {
			appendProjectionMessage(t, log, "body "+strconv.Itoa(i))
		}
		cfg := catchUpTestConfig(log, rows)
		if _, err := CatchUp(cfg); err != nil {
			t.Fatal(err)
		}
		if !store.VerifyOnce(cfg.Translator) {
			t.Fatal("an empty translator was checked for holes")
		}
	})

	t.Run("a translator with rows is checked once", func(t *testing.T) {
		log := store.NewMemLog[message.Message]()
		rows := store.NewMemLog[[]json.RawMessage]()
		for i := range 4 {
			appendProjectionMessage(t, log, "body "+strconv.Itoa(i))
		}
		cfg := catchUpTestConfig(log, rows)
		if _, err := CatchUp(cfg); err != nil {
			t.Fatal(err)
		}
		if _, err := CatchUp(cfg); err != nil {
			t.Fatal(err)
		}
		if store.VerifyOnce(cfg.Translator) {
			t.Fatal("the log was not marked checked")
		}
		appendProjectionMessage(t, log, "another")
		stats, err := CatchUp(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if stats.Visited != 1 {
			t.Fatalf("visited=%d after the check, want 1: the watermark path is the hot one", stats.Visited)
		}
	})
}
