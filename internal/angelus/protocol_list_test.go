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

// LastTS is recency's new source: figwal, not the sidecar. The dormant
// list reads it per row: cheap by figwal's contract (retained atomic
// counter), and wake-free, which is the whole point.
func (b *metadataListBackend) LastTS(string) int64 { return 20 }

// A conversation carries the stump it was born under, and its label, resolved
// by the backend from the stump's own record.
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
		ContextLimit:     1_000,
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
	if got := backend.formReads.Load(); got != 0 {
		t.Fatalf("dormant list folded form %d times", got)
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
// with the hash and never changes. It used to name a form key of the
// same name, so `set system.outfit_name x` renamed the aria's outfit in every
// listing, and, since the column is what the version is re-resolved against,
// reported an unchanged outfit as stale in the same breath.
func TestListLabelsFromTheStumpNotTheForm(t *testing.T) {
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
