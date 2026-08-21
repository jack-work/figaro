package provider

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
)

// A FAILED ENCODE LEAVES THE WATERMARK WHERE IT WAS. The catch-up writes a row
// per record as it goes, so the watermark is the last row written -- a record
// that failed to encode has no row, and the retry starts exactly there and
// re-attempts it.
//
// Under the deleted memo this needed three assertions about a published
// watermark, a start index and a carried entry count. It needs one now: the
// row log IS the watermark, so "did not advance" is a count of rows.
func TestAFailedEncodeLeavesTheWatermarkWhereItWas(t *testing.T) {
	log := store.NewMemLog[message.Message]()
	rows := store.NewMemLog[[]json.RawMessage]()
	appendProjectionMessage(t, log, "one")
	attempts := map[string]int{}
	cfg := CatchUpConfig{
		Log:         log,
		Translator:  rows,
		Fingerprint: "v1",
		Encode: func(msg message.Message, _ form.Snapshot) ([]json.RawMessage, error) {
			text := msg.Content[0].Text
			attempts[text]++
			if text == "two" && attempts[text] == 1 {
				return nil, errors.New("transient encode failure")
			}
			return []json.RawMessage{json.RawMessage(`"` + text + `"`)}, nil
		},
	}
	if _, err := CatchUp(cfg); err != nil {
		t.Fatal(err)
	}
	if got := len(rows.Read()); got != 1 {
		t.Fatalf("rows=%d after the first pass, want 1", got)
	}

	appendProjectionMessage(t, log, "two")
	appendProjectionMessage(t, log, "three")
	if _, err := CatchUp(cfg); err == nil {
		t.Fatal("expected encode failure")
	}
	if got := len(rows.Read()); got != 1 {
		t.Fatalf("rows=%d after the failed pass: a record that did not encode must not be recorded as translated", got)
	}

	stats, err := CatchUp(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if attempts["two"] != 2 || attempts["three"] != 1 {
		t.Fatalf("attempts=%v -- the retry must re-attempt the failure and no more", attempts)
	}
	if stats.Visited != 2 || stats.Encoded != 2 {
		t.Fatalf("retry visited=%d encoded=%d, want 2/2", stats.Visited, stats.Encoded)
	}
	if got := len(rows.Read()); got != 3 {
		t.Fatalf("rows=%d, want 3", got)
	}
}

func TestClearStaleTranslationCacheChecksTail(t *testing.T) {
	cache := store.NewMemLog[[]json.RawMessage]()
	if _, err := cache.Append(store.Entry[[]json.RawMessage]{
		FigaroLT: 1, Payload: []json.RawMessage{json.RawMessage(`{}`)}, Fingerprint: "old",
	}); err != nil {
		t.Fatal(err)
	}
	stored, cleared, err := ClearStaleTranslationCache(cache, "new")
	if err != nil {
		t.Fatal(err)
	}
	if stored != "old" || !cleared || len(cache.Read()) != 0 {
		t.Fatalf("stored=%q cleared=%v entries=%d", stored, cleared, len(cache.Read()))
	}
}

func appendProjectionMessage(t *testing.T, log *store.MemLog[message.Message], text string) store.Entry[message.Message] {
	t.Helper()
	entry, err := log.Append(store.Entry[message.Message]{Payload: message.Message{
		Role:    message.RoleInput,
		Content: []message.Content{message.TextContent(text)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return entry
}
