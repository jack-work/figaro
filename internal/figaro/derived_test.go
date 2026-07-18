package figaro

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jack-work/figaro/internal/store"
)

type metadataCaptureBackend struct {
	store.Backend
	meta *store.AriaMeta
}

func (b *metadataCaptureBackend) SetMeta(_ string, meta *store.AriaMeta) error {
	copy := *meta
	b.meta = &copy
	return nil
}

func TestPublishMetadataUsesIncrementalActorState(t *testing.T) {
	backend := &metadataCaptureBackend{}
	a := &Agent{
		id:            "aria",
		prov:          perfProvider{},
		backend:       backend,
		messageCount:  10_000,
		turnCount:     5_000,
		tokensIn:      100,
		tokensOut:     20,
		contextTokens: 120,
		contextExact:  true,
		metricsLT:     10_000,
	}
	a.publishMetadata()
	if backend.meta == nil || backend.meta.MessageCount != 10_000 ||
		backend.meta.TurnCount != 5_000 || backend.meta.TokensIn != 100 ||
		backend.meta.TokensOut != 20 || backend.meta.ContextTokens != 120 ||
		!backend.meta.ContextExact || backend.meta.LastFigaroLT != 10_000 {
		t.Fatalf("actor metadata not published: %#v", backend.meta)
	}
}

func TestMetaDerivationPublishesActorSnapshot(t *testing.T) {
	d := &metaDerivation{ariaID: "test-aria", providerName: "fallback"}
	evt := DerivationEvent{
		Metadata: store.AriaMeta{
			Provider:         "anthropic",
			Model:            "claude-sonnet-4-5",
			MessageCount:     2,
			TokensIn:         1234,
			TokensOut:        56,
			CacheReadTokens:  7,
			CacheWriteTokens: 3,
			ContextTokens:    1290,
			ContextExact:     true,
			LastFigaroLT:     2,
		},
		LastUpdateMS: 99,
	}
	var buf bytes.Buffer
	if err := d.OnTick(&buf, evt); err != nil {
		t.Fatalf("OnTick: %v", err)
	}
	var got MetaSnapshot
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, buf.String())
	}
	if got.AriaID != "test-aria" || got.Provider != "anthropic" ||
		got.Model != "claude-sonnet-4-5" || got.MessageCount != 2 ||
		got.TokensIn != 1234 || got.TokensOut != 56 ||
		got.CacheReadTokens != 7 || got.CacheWriteTokens != 3 ||
		got.ContextTokens != 1290 || !got.ContextExact ||
		got.LastFigaroLT != 2 || got.LastUpdateMS != 99 {
		t.Fatalf("metadata snapshot not preserved: %#v", got)
	}
}

func TestSnapshotDerivationsPreserveFileShapes(t *testing.T) {
	evt := DerivationEvent{
		Metadata: store.AriaMeta{
			MessageCount:     4,
			TurnCount:        2,
			TokensIn:         100,
			TokensOut:        20,
			CacheReadTokens:  80,
			CacheWriteTokens: 10,
			LastActiveMS:     50,
			LastFigaroLT:     4,
			Provider:         "provider",
			Model:            "model",
			Mantra:           "must not leak into summary",
			ContextTokens:    120,
			ContextExact:     true,
		},
		LastUpdateMS: 60,
	}

	var summary bytes.Buffer
	if err := (&summaryDerivation{}).OnTick(&summary, evt); err != nil {
		t.Fatal(err)
	}
	var summaryJSON map[string]any
	if err := json.Unmarshal(summary.Bytes(), &summaryJSON); err != nil {
		t.Fatal(err)
	}
	if _, ok := summaryJSON["mantra"]; ok {
		t.Fatalf("summary format gained list-only fields: %s", summary.String())
	}
	if summaryJSON["message_count"] != float64(4) || summaryJSON["last_figaro_lt"] != float64(4) {
		t.Fatalf("summary format lost metrics: %s", summary.String())
	}
	if summaryJSON["last_active_ms"] != float64(60) {
		t.Fatalf("summary update time changed: %s", summary.String())
	}

	var usageJSON bytes.Buffer
	if err := (&usageDerivation{ariaID: "aria", providerName: "fallback"}).OnTick(&usageJSON, evt); err != nil {
		t.Fatal(err)
	}
	var usage Usage
	if err := json.Unmarshal(usageJSON.Bytes(), &usage); err != nil {
		t.Fatal(err)
	}
	if usage.AriaID != "aria" || usage.Provider != "provider" ||
		usage.MessageCount != 4 || usage.TurnCount != 2 ||
		usage.TokensIn != 100 || usage.TokensOut != 20 ||
		usage.CacheReadTokens != 80 || usage.CacheWriteTokens != 10 ||
		usage.LastFigaroLT != 4 || usage.LastUpdateMS != 60 {
		t.Fatalf("usage format not preserved: %#v", usage)
	}
}

func TestMetaDerivationEmptySnapshot(t *testing.T) {
	d := &metaDerivation{ariaID: "empty"}
	var buf bytes.Buffer
	if err := d.OnTick(&buf, DerivationEvent{
		Metadata:     store.AriaMeta{ContextExact: true},
		LastUpdateMS: 1,
	}); err != nil {
		t.Fatalf("OnTick: %v", err)
	}
	var got MetaSnapshot
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.MessageCount != 0 || got.ContextTokens != 0 || !got.ContextExact {
		t.Fatalf("empty snapshot changed: %#v", got)
	}
}

func TestDerivationLoopCoalescesToLatestSnapshot(t *testing.T) {
	loop := &derivationLoop{inbox: make(chan DerivationEvent, 1)}
	loop.tick(DerivationEvent{Metadata: store.AriaMeta{MessageCount: 1}})
	loop.tick(DerivationEvent{Metadata: store.AriaMeta{MessageCount: 2}})
	got := <-loop.inbox
	if got.Metadata.MessageCount != 2 {
		t.Fatalf("coalesced message count = %d, want 2", got.Metadata.MessageCount)
	}
}

func TestDerivedCloseFlushesLatestSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "meta.json")
	loop := startLoop("summary", path, &summaryDerivation{})
	fanout := &derivedFanout{loops: []*derivationLoop{loop}}
	fanout.Tick(store.AriaMeta{MessageCount: 1})
	fanout.Tick(store.AriaMeta{MessageCount: 2})
	fanout.Close()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got store.AriaMeta
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.MessageCount != 2 {
		t.Fatalf("flushed message count = %d, want 2", got.MessageCount)
	}
}
