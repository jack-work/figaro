package angelus

import (
	"sync/atomic"
	"testing"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/store"
)

type metadataListBackend struct {
	store.Backend
	chalkReads atomic.Int64
	logReads   atomic.Int64
}

func (b *metadataListBackend) Conversations() []store.NodeView {
	return []store.NodeView{{ID: "dormant", Kind: conversationKind, Trunk: "dormant", Vector: []int{0}}}
}

func (b *metadataListBackend) Meta(string) (*store.AriaMeta, error) {
	return &store.AriaMeta{
		MessageCount:     42,
		TokensIn:         100,
		TokensOut:        20,
		CacheReadTokens:  80,
		CacheWriteTokens: 10,
		Provider:         "provider",
		Model:            "model",
		Mantra:           "essence",
		Cwd:              "work",
		ContextTokens:    120,
		ContextLimit:     1_000,
		ContextExact:     true,
		CreatedAtMS:      10,
		LastActiveMS:     20,
	}, nil
}

func (b *metadataListBackend) ChalkboardState(string) (chalkboard.Snapshot, error) {
	b.chalkReads.Add(1)
	return nil, nil
}

func (b *metadataListBackend) Open(string) (store.Log[message.Message], error) {
	b.logReads.Add(1)
	return store.NewMemLog[message.Message](), nil
}

func TestDormantListUsesMetadataOnly(t *testing.T) {
	backend := &metadataListBackend{}
	h := &handlers{angelus: &Angelus{Registry: NewRegistry(), Backend: backend}}

	response, err := h.list(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	figaros := response.(rpc.ListResponse).Figaros
	if len(figaros) != 1 {
		t.Fatalf("got %d figaros, want 1", len(figaros))
	}
	got := figaros[0]
	if got.MessageCount != 42 || got.Provider != "provider" || got.Model != "model" ||
		got.Mantra != "essence" || got.Cwd != "work" || got.ContextTokens != 120 ||
		got.ContextLimit != 1_000 || !got.ContextExact || got.CreatedAt != 10 ||
		got.LastActive != 20 {
		t.Fatalf("metadata not projected: %#v", got)
	}
	if got := backend.chalkReads.Load(); got != 0 {
		t.Fatalf("dormant list folded chalkboard %d times", got)
	}
	if got := backend.logReads.Load(); got != 0 {
		t.Fatalf("dormant list counted canonical log %d times", got)
	}
}
