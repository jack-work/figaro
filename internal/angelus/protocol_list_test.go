package angelus

import (
	"slices"
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
	return []store.NodeView{{ID: "dormant", Kind: conversationKind, Trunk: "dormant", Vector: []int{0}, Parent: "base@abc123"}}
}

// Nodes is Conversations plus the ceremonial anchors. A listing walks to the
// stump for the outfit label, so it needs them even when it does not show them.
func (b *metadataListBackend) Nodes() []store.NodeView {
	return append([]store.NodeView{
		{ID: "base@abc123", Kind: string(outfitKind), Parent: "null", Outfit: "base", Version: "abc123"},
	}, b.Conversations()...)
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
	return chalkboard.Snapshot{}, nil
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

// A dormant aria still reports the shells attending it. Presence is a
// property of the binding, not of residency: `attend` deliberately does not
// wake anything, so if the dormant branch omits BoundPIDs the caller can
// never draw an attended-but-sleeping aria as "here".
func TestDormantListReportsBoundPIDs(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Bind(4242, "dormant", 0); err != nil {
		t.Fatal(err)
	}
	h := &handlers{angelus: &Angelus{Registry: reg, Backend: &metadataListBackend{}}}

	response, err := h.list(t.Context(), nil)
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

// The outfit column names the STUMP an aria was born under, which is minted
// with the hash and never changes. It used to name a chalkboard key of the
// same name, so `set system.outfit_name x` renamed the aria's outfit in every
// listing — and, since the column is what the version is re-resolved against,
// reported an unchanged outfit as stale in the same breath.
func TestListLabelsFromTheStumpNotTheChalkboard(t *testing.T) {
	backend := &metadataListBackend{}
	h := &handlers{angelus: &Angelus{Registry: NewRegistry(), Backend: backend}}

	resp, err := h.list(t.Context(), nil)
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
