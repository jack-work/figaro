package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/message"
)

// seedSchemaStore builds a store with one conversation carrying IR messages
// and a derived translation-cache entry, then closes it. Returns the root and
// the conversation id.
func seedSchemaStore(tb testing.TB) (string, string) {
	tb.Helper()
	root := tb.TempDir()
	be, err := NewXwalBackend(root)
	if err != nil {
		tb.Fatal(err)
	}
	lo, err := be.CreateLoadout("schema", message.Patch{})
	if err != nil {
		tb.Fatal(err)
	}
	conv, err := be.CreateConversation(lo)
	if err != nil {
		tb.Fatal(err)
	}
	lg, err := be.Open(conv)
	if err != nil {
		tb.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		m := message.Message{Role: message.RoleUser, Content: []message.Content{
			{Type: message.ContentProse, Text: "canonical"},
		}}
		if _, err := lg.Append(Entry[message.Message]{Payload: m}); err != nil {
			tb.Fatal(err)
		}
	}
	if _, err := be.store.trunks.Append(conv, transChannel("anthropic"), 0,
		[]byte(`{"cached":true}`), nil); err != nil {
		tb.Fatal(err)
	}
	// Release the writer flock; every assertion below reopens the store.
	if err := be.Close(); err != nil {
		tb.Fatal(err)
	}
	return root, conv
}

// channelLast reports a channel's last index, and whether it exists at all.
func channelLast(tb testing.TB, root, conv, name string) (uint64, bool) {
	tb.Helper()
	s, err := OpenXwalStore(root)
	if err != nil {
		tb.Fatal(err)
	}
	defer s.trunks.Close()
	x, err := s.OpenNode(conv)
	if err != nil {
		tb.Fatal(err)
	}
	defer x.Close()
	for _, ch := range x.Channels() {
		if ch.Name == name {
			return ch.Last, true
		}
	}
	return 0, false
}

func putSchema(tb testing.TB, root string, m map[string]int) {
	tb.Helper()
	raw, err := json.Marshal(schemaFile{Channels: m})
	if err != nil {
		tb.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, schemaFileName), raw, 0o600); err != nil {
		tb.Fatal(err)
	}
}

// A store written by a NEWER figaro must be refused, not silently misread.
// This is the failure mode nothing covered before the schema sidecar.
func TestSchemaRefusesNewerStore(t *testing.T) {
	root, _ := seedSchemaStore(t)
	putSchema(t, root, map[string]int{chanIR: 999})

	_, err := OpenXwalStore(root)
	if err == nil {
		t.Fatal("expected refusal opening a store written by a newer build")
	}
	for _, want := range []string{"ir", "v999", "newer build"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal should mention %q, got: %v", want, err)
		}
	}
}

// A derived cache whose schema moved on is cleared at open and regenerates
// lazily. The canonical record beside it must survive untouched.
func TestSchemaBustsDerivedCacheOnly(t *testing.T) {
	root, conv := seedSchemaStore(t)
	trans := transChannel("anthropic")

	if last, ok := channelLast(t, root, conv, trans); !ok || last == 0 {
		t.Fatalf("fixture: translation cache should hold an entry (last=%d ok=%v)", last, ok)
	}
	irBefore, _ := channelLast(t, root, conv, chanIR)

	// Pretend the store was written before the derived schema bumped.
	putSchema(t, root, map[string]int{"translations-v2/": 0})
	s, err := OpenXwalStore(root)
	if err != nil {
		t.Fatalf("open after derived bump: %v", err)
	}
	s.trunks.Close()

	if last, _ := channelLast(t, root, conv, trans); last != 0 {
		t.Errorf("derived cache should have been cleared, last=%d", last)
	}
	if irAfter, _ := channelLast(t, root, conv, chanIR); irAfter != irBefore {
		t.Errorf("canonical ir must not be touched: before=%d after=%d", irBefore, irAfter)
	}
}

// The canonical channel is never cleared, however far behind it is: the record
// migrates by derivation on read, never by deletion.
func TestSchemaNeverClearsCanonical(t *testing.T) {
	root, conv := seedSchemaStore(t)
	irBefore, _ := channelLast(t, root, conv, chanIR)
	if irBefore == 0 {
		t.Fatal("fixture: ir should hold entries")
	}

	putSchema(t, root, map[string]int{chanIR: 0})
	s, err := OpenXwalStore(root)
	if err != nil {
		t.Fatalf("open with stale canonical schema: %v", err)
	}
	s.trunks.Close()

	if irAfter, _ := channelLast(t, root, conv, chanIR); irAfter != irBefore {
		t.Errorf("canonical ir was modified: before=%d after=%d", irBefore, irAfter)
	}
}

// First open of a store with no sidecar records every current version, so the
// next upgrade has something to compare against.
func TestSchemaWrittenOnFirstOpen(t *testing.T) {
	root, _ := seedSchemaStore(t)
	got, err := readSchema(root)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range channelSchemas {
		if got[key] != want.version {
			t.Errorf("schema[%q] = %d, want %d", key, got[key], want.version)
		}
	}
}
