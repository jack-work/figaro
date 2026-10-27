package angelus

import (
	"slices"
	"sync/atomic"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
)

type metadataListBackend struct {
	store.Backend
	formReads atomic.Int64
	logReads  atomic.Int64
}

func (b *metadataListBackend) LastTS(string) int64 { return 20 }

func (b *metadataListBackend) Conversations() []store.NodeView {
	return []store.NodeView{{
		ID: "dormant", Kind: conversationKind, Trunk: "dormant", Vector: []int{0},
		Parent: "@abc123", Stump: "@abc123", Outfit: "base", Version: "abc123",
	}}
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
		ContextExact:     true,
		CreatedAtMS:      10,
	}, nil
}

func (b *metadataListBackend) FormState(string) (form.Snapshot, error) {
	b.formReads.Add(1)
	return form.Snapshot{}, nil
}

func (b *metadataListBackend) OpenFigIR(string) (store.Log[message.Message], error) {
	b.logReads.Add(1)
	return store.NewMemLog[message.Message](), nil
}

func TestDormantListUsesMetadataOnly(t *testing.T) {
	backend := &metadataListBackend{}
	h := &handlers{angelus: &Angelus{Registry: NewRegistry(), Backend: backend}}

	response, err := h.list(inProcess(t.Context()), nil)
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
		got.ContextLimit != 0 || !got.ContextExact || got.CreatedAt != 10 ||
		got.LastActive != 20 {
		t.Fatalf("metadata not projected: %#v", got)
	}
	if got := backend.formReads.Load(); got != 0 {
		t.Fatalf("dormant list folded form %d times", got)
	}
	if got := backend.logReads.Load(); got != 0 {
		t.Fatalf("dormant list counted canonical log %d times", got)
	}
}

// Presence is a property of the binding, not of residency.
func TestDormantListReportsBoundPIDs(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Bind(4242, "dormant", 0); err != nil {
		t.Fatal(err)
	}
	h := &handlers{angelus: &Angelus{Registry: reg, Backend: &metadataListBackend{}}}

	response, err := h.list(inProcess(t.Context()), nil)
	if err != nil {
		t.Fatal(err)
	}
	figaros := response.(rpc.ListResponse).Figaros
	if len(figaros) != 1 {
		t.Fatalf("got %d figaros, want 1", len(figaros))
	}
	if got := figaros[0]; got.State != "dormant" || !slices.Contains(got.BoundPIDs, 4242) {
		t.Fatalf("dormant entry lost its bound pid: %#v", got)
	}
}

func TestListLabelsFromTheStumpNotTheForm(t *testing.T) {
	backend := &metadataListBackend{}
	h := &handlers{angelus: &Angelus{Registry: NewRegistry(), Backend: backend}}

	resp, err := h.list(inProcess(t.Context()), nil)
	if err != nil {
		t.Fatal(err)
	}
	figaros := resp.(rpc.ListResponse).Figaros
	if len(figaros) != 1 {
		t.Fatalf("got %d figaros, want 1", len(figaros))
	}
	if got := figaros[0].OutfitName; got != "base" {
		t.Errorf("outfit label: got %q, want the stump's %q", got, "base")
	}
}
