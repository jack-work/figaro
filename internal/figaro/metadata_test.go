package figaro

import (
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

// UpdateMeta is how the agent publishes: read-modify-write, because the
// sidecar has a second writer (a board commit mirroring system.ttl) whose
// field a whole-record write would erase.
func (b *metadataCaptureBackend) UpdateMeta(_ string, mutate func(*store.AriaMeta)) error {
	cur := &store.AriaMeta{}
	if b.meta != nil {
		copy := *b.meta
		cur = &copy
	}
	mutate(cur)
	b.meta = cur
	return nil
}

func TestPublishMetadataUsesIncrementalActorState(t *testing.T) {
	backend := &metadataCaptureBackend{}
	a := &Agent{
		id:            "aria",
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

// The agent owns its counts and NOT the whole record: a lifetime mirrored
// from the board must survive every turn the aria takes.
func TestPublishMetadataPreservesTheLifetime(t *testing.T) {
	backend := &metadataCaptureBackend{meta: &store.AriaMeta{TTLMS: 3600_000, CreatedAtMS: 42}}
	a := &Agent{id: "aria", backend: backend, messageCount: 7}
	a.publishMetadata()
	if backend.meta.TTLMS != 3600_000 {
		t.Fatalf("ttl_ms = %d after a turn, want it untouched: publishing erased the lifetime",
			backend.meta.TTLMS)
	}
	if backend.meta.MessageCount != 7 {
		t.Fatalf("message_count = %d, want the agent's 7", backend.meta.MessageCount)
	}
}
