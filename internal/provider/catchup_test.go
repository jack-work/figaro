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
		Log:  log,
		Rows: rows,
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
	if _, err := CatchUp(catchUpTestConfig(log, nil)); !errors.Is(err, ErrNoRows) {
		t.Fatalf("err=%v, want ErrNoRows", err)
	}
}
