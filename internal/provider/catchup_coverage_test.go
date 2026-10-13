package provider

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
)

// freshHandle is a reopened log: the once-per-handle hole check fires again
// over the same rows.
type freshHandle struct {
	*store.MemLog[[]json.RawMessage]
	verified atomic.Bool
}

func (l *freshHandle) VerifyOnce() bool { return l.verified.CompareAndSwap(false, true) }

func countingConfig(log store.Log[message.Message], rows store.Log[[]json.RawMessage], calls *int) CatchUpConfig {
	cfg := catchUpTestConfig(log, rows)
	inner := cfg.Encode
	cfg.Encode = func(msg message.Message, snap form.Snapshot) ([]json.RawMessage, error) {
		*calls++
		return inner(msg, snap)
	}
	return cfg
}

// Coverage is a count, not a derivation: a complete channel opened afresh
// costs no encode.
func TestACleanCatchUpEncodesNothing(t *testing.T) {
	log := store.NewMemLog[message.Message]()
	rows := store.NewMemLog[[]json.RawMessage]()
	if _, err := log.Append(store.Entry[message.Message]{Payload: message.Message{Role: message.RoleGenesis}}); err != nil {
		t.Fatal(err)
	}
	for i := range 50 {
		appendProjectionMessage(t, log, "body "+strconv.Itoa(i))
	}
	var calls int
	if _, err := CatchUp(countingConfig(log, rows, &calls)); err != nil {
		t.Fatal(err)
	}
	if calls != 50 {
		t.Fatalf("cold pass encoded %d, want 50", calls)
	}

	calls = 0
	stats, err := CatchUp(countingConfig(log, &freshHandle{MemLog: rows}, &calls))
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("a clean catch-up called Encode %d times, want 0", calls)
	}
	if stats.RepairedAt != 0 || stats.Visited != 0 {
		t.Fatalf("repaired=%d visited=%d, want 0/0", stats.RepairedAt, stats.Visited)
	}
}

// A record that translates to nothing owns an empty row, so the next pass
// has nothing to revisit and the send path has nothing to send.
func TestEmptyEncodesOwnEmptyRows(t *testing.T) {
	log := store.NewMemLog[message.Message]()
	rows := store.NewMemLog[[]json.RawMessage]()
	for i := range 8 {
		appendProjectionMessage(t, log, "body "+strconv.Itoa(i))
	}
	cfg := catchUpTestConfig(log, rows)
	cfg.Encode = func(message.Message, form.Snapshot) ([]json.RawMessage, error) { return nil, nil }

	first, err := CatchUp(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if first.Unwritten != 8 || first.Encoded != 0 {
		t.Fatalf("unwritten=%d encoded=%d, want 8/0", first.Unwritten, first.Encoded)
	}
	all := rows.Read()
	if len(all) != 8 {
		t.Fatalf("rows=%d, want 8 empty rows", len(all))
	}
	for _, r := range all {
		if len(r.Payload) != 0 || r.FigaroHash == "" {
			t.Fatalf("row %d: payload=%d hash=%q, want an empty stamped row", r.FigaroLT, len(r.Payload), r.FigaroHash)
		}
	}
	for range TranslationRows(rows, 0) {
		t.Fatal("an empty row reached the send path")
	}

	second, err := CatchUp(countingConfig(log, &freshHandle{MemLog: rows}, new(int)))
	if err != nil {
		t.Fatal(err)
	}
	if second.Visited != 0 || second.RepairedAt != 0 {
		t.Fatalf("visited=%d repaired=%d, want 0/0", second.Visited, second.RepairedAt)
	}
	if SeedLT(rows) != 0 {
		t.Fatalf("SeedLT=%d, want 0: no row carries bytes", SeedLT(rows))
	}
}

// The window of an entry that encodes to nothing rides the next entry that
// carries bytes, across a reseed from the log.
func TestAnEmptyRowCommitsNoCursor(t *testing.T) {
	log := store.NewMemLog[message.Message]()
	rows := store.NewMemLog[[]json.RawMessage]()
	stamp(t, log, "one", 2)
	stamp(t, log, "two", 4)
	stamp(t, log, "three", 6)

	var rendered [][]string
	cfg := catchUpTestConfig(log, rows)
	cfg.Form = newFakeBoard(2, 3, 4, 5, 6)
	cfg.Encode = func(msg message.Message, _ form.Snapshot) ([]json.RawMessage, error) {
		var keys []string
		for _, p := range msg.Patches {
			for _, ent := range p.Entries() {
				keys = append(keys, ent.Key)
			}
		}
		rendered = append(rendered, keys)
		if msg.LogicalTime == 2 {
			return nil, nil
		}
		return []json.RawMessage{json.RawMessage(`"x"`)}, nil
	}
	entries := log.Read()
	one := store.NewMemLog[message.Message]()
	for _, e := range entries[:2] {
		if _, err := one.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	cfg.Log = one
	if _, err := CatchUp(cfg); err != nil {
		t.Fatal(err)
	}
	if SeedLT(rows) != entries[0].LT {
		t.Fatalf("SeedLT=%d, want %d: the empty row is not a seed", SeedLT(rows), entries[0].LT)
	}
	cfg.Log = log
	cfg.Form = newFakeBoard(2, 3, 4, 5, 6)
	if _, err := CatchUp(cfg); err != nil {
		t.Fatal(err)
	}
	if len(rendered) != 3 {
		t.Fatalf("rendered %d entries, want 3", len(rendered))
	}
	if got := strings.Join(rendered[2], " "); got != "k3 k4 k5 k6" {
		t.Fatalf("the empty entry's window did not ride the next row: got %q, all %v", got, rendered)
	}
}
