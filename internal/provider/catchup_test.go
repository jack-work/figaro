package provider

import (
	"encoding/json"
	"errors"
	"strconv"
	"testing"

	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/message"
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

// SYNCHRONISATION IS O(NEW), AND THIS COUNTS RATHER THAN TIMES IT: a second
// pass may visit only the records appended since the first. The count is the
// property; a benchmark over MemLog would measure the fixture's read instead,
// since MemLog has no TailAfter of its own.
//
// The assembly read (Rows) is deliberately O(history) and is NOT this
// property -- see BenchmarkRows.
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

// A SEND THAT CANNOT WRITE ITS ROWS FAILS. Degrading to an in-memory encode
// here means showing the model a conversation that is not the one on disk.
func TestCatchUpWithoutRowsIsAnError(t *testing.T) {
	log := store.NewMemLog[message.Message]()
	appendProjectionMessage(t, log, "body")
	if _, err := CatchUp(catchUpTestConfig(log, nil)); !errors.Is(err, ErrNoTranslator) {
		t.Fatalf("err=%v, want ErrNoTranslator", err)
	}
}

// A ROW THAT DESCRIBES A RECORD THAT IS NOT THERE IS REFUSED, AND THE CHANNEL
// IS RE-DERIVED.
//
// This is the failure row-first ordering can produce and no ordering can
// prevent: a crash between writing a row and writing its record leaves the row
// at a position the NEXT append reissues, so it is adopted by a different
// message. Without the content hash the row reads as a legitimate translation
// of a conversation that never happened.
func TestARowWhoseFigaroHashDoesNotMatchIsRefused(t *testing.T) {
	log := store.NewMemLog[message.Message]()
	rows := store.NewMemLog[[]json.RawMessage]()
	first := appendProjectionMessage(t, log, "the real one")

	// A row for that LT, but describing a DIFFERENT record: exactly the shape
	// a reissued LT produces.
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

// AND A ROW THAT DOES MATCH IS A WATERMARK, so the check costs one comparison
// and does not re-derive a healthy channel.
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
