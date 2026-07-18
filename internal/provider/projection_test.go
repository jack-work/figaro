package provider

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
)

func TestProjectIncrementallyVisitsOnlySuffix(t *testing.T) {
	log := store.NewMemLog[message.Message]()
	appendProjectionMessage(t, log, "one")
	appendProjectionMessage(t, log, "two")

	encoded := 0
	config := ProjectionConfig[EncodedMessages]{
		Log:         log,
		Fingerprint: "v1",
		Encode: func(msg message.Message, _ chalkboard.Snapshot) ([]json.RawMessage, error) {
			encoded++
			return []json.RawMessage{json.RawMessage(`"` + msg.Content[0].Text + `"`)}, nil
		},
		Append: AppendEncodedMessage,
	}
	first, stats, err := ProjectIncrementally(config)
	if err != nil {
		t.Fatal(err)
	}
	if stats.StartIndex != 0 || encoded != 2 {
		t.Fatalf("cold projection start=%d encoded=%d", stats.StartIndex, encoded)
	}

	appendProjectionMessage(t, log, "three")
	config.Previous = first
	second, stats, err := ProjectIncrementally(config)
	if err != nil {
		t.Fatal(err)
	}
	if stats.StartIndex != 2 || encoded != 3 {
		t.Fatalf("warm projection start=%d total encoded=%d", stats.StartIndex, encoded)
	}
	if len(second.State.PerMessage) != 3 {
		t.Fatalf("messages=%d, want 3", len(second.State.PerMessage))
	}
}

func TestProjectIncrementallyInvalidatesFingerprint(t *testing.T) {
	log := store.NewMemLog[message.Message]()
	appendProjectionMessage(t, log, "one")
	previous := &IncrementalProjection[int]{
		State:       1,
		Fingerprint: "old",
		Entries:     1,
		LastLT:      1,
	}
	encoded := 0
	projection, stats, err := ProjectIncrementally(ProjectionConfig[int]{
		Log:         log,
		Previous:    previous,
		Fingerprint: "new",
		Encode: func(message.Message, chalkboard.Snapshot) ([]json.RawMessage, error) {
			encoded++
			return []json.RawMessage{json.RawMessage(`{}`)}, nil
		},
		Append: func(state int, _ []json.RawMessage, _ uint64) int { return state + 1 },
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats.StartIndex != 0 || encoded != 1 || projection.State != 1 {
		t.Fatalf("start=%d encoded=%d state=%d", stats.StartIndex, encoded, projection.State)
	}
}

func TestProjectIncrementallyDoesNotAdvancePastEncodeFailure(t *testing.T) {
	log := store.NewMemLog[message.Message]()
	appendProjectionMessage(t, log, "one")
	attempts := map[string]int{}
	config := ProjectionConfig[EncodedMessages]{
		Log:         log,
		Fingerprint: "v1",
		Encode: func(msg message.Message, _ chalkboard.Snapshot) ([]json.RawMessage, error) {
			text := msg.Content[0].Text
			attempts[text]++
			if text == "two" && attempts[text] == 1 {
				return nil, errors.New("transient encode failure")
			}
			return []json.RawMessage{json.RawMessage(`"` + text + `"`)}, nil
		},
		Append: AppendEncodedMessage,
	}
	first, _, err := ProjectIncrementally(config)
	if err != nil {
		t.Fatal(err)
	}

	appendProjectionMessage(t, log, "two")
	appendProjectionMessage(t, log, "three")
	config.Previous = first
	failed, stats, err := ProjectIncrementally(config)
	if err == nil {
		t.Fatal("expected encode failure")
	}
	if failed != nil {
		t.Fatal("failed projection must not publish a later watermark")
	}
	if stats.StartIndex != 1 || first.Entries != 1 {
		t.Fatalf("start=%d prior entries=%d", stats.StartIndex, first.Entries)
	}

	retried, stats, err := ProjectIncrementally(config)
	if err != nil {
		t.Fatal(err)
	}
	if stats.StartIndex != 1 || attempts["two"] != 2 || attempts["three"] != 1 {
		t.Fatalf("start=%d attempts=%v", stats.StartIndex, attempts)
	}
	if retried.Entries != 3 || len(retried.State.PerMessage) != 3 {
		t.Fatalf("entries=%d messages=%d", retried.Entries, len(retried.State.PerMessage))
	}
}

func TestProjectIncrementallyUsesInputReadyCache(t *testing.T) {
	log := store.NewMemLog[message.Message]()
	entry := appendProjectionMessage(t, log, "one")
	cache := store.NewMemLog[[]json.RawMessage]()
	want := []json.RawMessage{json.RawMessage(`{"cached":true}`)}
	if _, err := cache.Append(store.Entry[[]json.RawMessage]{FigaroLT: entry.LT, Payload: want}); err != nil {
		t.Fatal(err)
	}

	projection, stats, err := ProjectIncrementally(ProjectionConfig[[]json.RawMessage]{
		Log:         log,
		Cache:       cache,
		Fingerprint: "v1",
		Encode: func(message.Message, chalkboard.Snapshot) ([]json.RawMessage, error) {
			return nil, errors.New("cache miss")
		},
		Append: func(state, encoded []json.RawMessage, _ uint64) []json.RawMessage {
			return append(state, encoded...)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Cached != 1 || len(projection.State) != 1 || string(projection.State[0]) != string(want[0]) {
		t.Fatalf("cached=%d state=%s", stats.Cached, projection.State)
	}
}

func TestProjectIncrementallyRejectsStaleCacheEntry(t *testing.T) {
	log := store.NewMemLog[message.Message]()
	entry := appendProjectionMessage(t, log, "one")
	cache := store.NewMemLog[[]json.RawMessage]()
	if _, err := cache.Append(store.Entry[[]json.RawMessage]{
		FigaroLT: entry.LT, Payload: []json.RawMessage{json.RawMessage(`{"stale":true}`)}, Fingerprint: "old",
	}); err != nil {
		t.Fatal(err)
	}

	encoded := 0
	_, stats, err := ProjectIncrementally(ProjectionConfig[int]{
		Log:         log,
		Cache:       cache,
		Fingerprint: "new",
		Encode: func(message.Message, chalkboard.Snapshot) ([]json.RawMessage, error) {
			encoded++
			return []json.RawMessage{json.RawMessage(`{"fresh":true}`)}, nil
		},
		Append: func(state int, _ []json.RawMessage, _ uint64) int { return state + 1 },
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Cached != 0 || stats.Encoded != 1 || encoded != 1 {
		t.Fatalf("cached=%d encoded=%d calls=%d", stats.Cached, stats.Encoded, encoded)
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
		Role:    message.RoleUser,
		Content: []message.Content{message.TextContent(text)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return entry
}
