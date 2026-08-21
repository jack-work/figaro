package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/message"
)

// seedSchemaStore builds a store with one conversation carrying IR messages
// and a derived translation-cache entry, then closes it. Returns the root and
// the conversation id.
func seedSchemaStore(tb testing.TB) (string, string) {
	tb.Helper()
	root := tb.TempDir()
	be, err := NewXwalBackend(root, 0)
	if err != nil {
		tb.Fatal(err)
	}
	lo, err := be.CreateOutfit("schema", message.Patch{})
	if err != nil {
		tb.Fatal(err)
	}
	conv, err := be.CreateConversation(lo)
	if err != nil {
		tb.Fatal(err)
	}
	lg, err := be.OpenFigIR(conv)
	if err != nil {
		tb.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		m := message.Message{Role: message.RoleInput, Content: []message.Content{
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
	s, err := OpenXwalStore(root, 0)
	if err != nil {
		tb.Fatal(err)
	}
	defer s.trunks.Close()
	x, err := s.openNode(conv)
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

	_, err := OpenXwalStore(root, 0)
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
	s, err := OpenXwalStore(root, 0)
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
	s, err := OpenXwalStore(root, 0)
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
		if got.Channels[key] != want.version {
			t.Errorf("schema[%q] = %d, want %d", key, got.Channels[key], want.version)
		}
	}
	if got.StoreVersion != storeVersion {
		t.Errorf("store-version = %d, want %d", got.StoreVersion, storeVersion)
	}
}

// A store written by a NEWER build must be refused, not silently
// misread. This is the gate that covers the form becoming unkeyed:
// an older binary reads its main LT of 0 as real and loses every inline
// transition, so the IR bump has to stop it at the door.
func TestSchemaRefusesANewerStore(t *testing.T) {
	dir := t.TempDir()
	b, err := NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	l, _ := b.CreateOutfit("d", message.Patch{})
	if _, err := b.CreateConversation(l); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	// Pretend the store was written by a build one version ahead.
	stored, err := readSchema(dir)
	if err != nil {
		t.Fatal(err)
	}
	stored.Channels[chanIR] = channelSchemas[chanIR].version + 1
	if err := writeSchema(dir, stored); err != nil {
		t.Fatal(err)
	}
	if _, err := NewXwalBackend(dir, 0); err == nil {
		t.Fatal("opened a store written by a newer build")
	} else if !strings.Contains(err.Error(), "newer build") {
		t.Fatalf("error = %v, want a refusal naming the newer build", err)
	}
}

// The generation is a statement, not a probe: a store minted now carries it,
// and one written by a newer build is refused rather than half-understood.
func TestStoreGenerationIsStampedAndGated(t *testing.T) {
	dir := t.TempDir()
	b, err := NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	b.Close()
	if disk, known, err := StoreGeneration(dir); err != nil || disk != storeVersion || known != storeVersion {
		t.Fatalf("generation on disk %d / known %d (err %v)", disk, known, err)
	}

	f, _ := readSchema(dir)
	f.StoreVersion = storeVersion + 1
	if err := writeSchema(dir, f); err != nil {
		t.Fatal(err)
	}
	if _, err := NewXwalBackend(dir, 0); err == nil || !strings.Contains(err.Error(), "newer build") {
		t.Fatalf("opened a store from a newer generation: %v", err)
	}
}
