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
