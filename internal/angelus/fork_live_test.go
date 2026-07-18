package angelus

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/store"
	"github.com/stretchr/testify/require"
)

type liveForkFigaro struct {
	id     string
	killed bool
}

func (f *liveForkFigaro) ID() string                 { return f.id }
func (f *liveForkFigaro) SocketPath() string         { return "" }
func (f *liveForkFigaro) Prompt(string)              {}
func (f *liveForkFigaro) Interrupt()                 {}
func (f *liveForkFigaro) Context() []message.Message { return nil }
func (f *liveForkFigaro) Info() figaro.FigaroInfo {
	return figaro.FigaroInfo{ID: f.id, State: "active", MessageCount: 12, Provider: "provider"}
}
func (f *liveForkFigaro) Kill() { f.killed = true }

type liveForkBackend struct {
	store.Backend
	forked     bool
	parentMeta *store.AriaMeta
	childMeta  *store.AriaMeta
	owner      store.OwnerInfo
}

func (b *liveForkBackend) Fork(id string) (string, string, error) {
	b.forked = true
	return id, "alternative", nil
}

func (b *liveForkBackend) ForkAt(id string, _ uint64) (string, string, error) {
	b.forked = true
	return id, "alternative", nil
}

func (b *liveForkBackend) OwnerResolution(string, uint64) (store.OwnerInfo, error) {
	return b.owner, nil
}

func (b *liveForkBackend) Meta(string) (*store.AriaMeta, error) {
	return b.parentMeta, nil
}

func (b *liveForkBackend) SetMeta(_ string, meta *store.AriaMeta) error {
	b.childMeta = meta
	return nil
}

func TestForkKeepsLiveAgentRunning(t *testing.T) {
	registry := NewRegistry()
	live := &liveForkFigaro{id: "live"}
	require.NoError(t, registry.Register(live))
	backend := &liveForkBackend{parentMeta: &store.AriaMeta{MessageCount: 12, Provider: "provider"}}
	h := &handlers{angelus: &Angelus{Registry: registry, Backend: backend}}
	params, err := json.Marshal(rpc.ForkRequest{FigaroID: live.id})
	require.NoError(t, err)

	_, err = h.fork(t.Context(), params)
	require.NoError(t, err)
	require.True(t, backend.forked)
	require.False(t, live.killed)
	require.Same(t, live, registry.Get(live.id))
	require.Equal(t, 12, backend.childMeta.MessageCount)
	require.Equal(t, "provider", backend.childMeta.Provider)
}

func TestInteriorForkAtRootDoesNotCopyConversationState(t *testing.T) {
	backend := &liveForkBackend{
		parentMeta: &store.AriaMeta{
			MessageCount:   12,
			Provider:       "provider",
			Model:          "model",
			Mantra:         "mantra",
			Cwd:            "work",
			LoadoutName:    "loadout",
			LoadoutVersion: "version",
		},
		owner: store.OwnerInfo{IsRoot: true},
	}
	h := &handlers{angelus: &Angelus{Registry: NewRegistry(), Backend: backend}}
	params, err := json.Marshal(rpc.ForkRequest{FigaroID: "parent", AtMainLT: 1})
	require.NoError(t, err)

	_, err = h.fork(t.Context(), params)
	require.NoError(t, err)
	require.Zero(t, backend.childMeta.MessageCount)
	require.Empty(t, backend.childMeta.Provider)
	require.Empty(t, backend.childMeta.Model)
	require.Empty(t, backend.childMeta.Mantra)
	require.Empty(t, backend.childMeta.Cwd)
	require.Empty(t, backend.childMeta.LoadoutName)
	require.Empty(t, backend.childMeta.LoadoutVersion)
}

type blockingInfoFigaro struct {
	liveForkFigaro
	entered chan struct{}
	release chan struct{}
}

func (f *blockingInfoFigaro) Info() figaro.FigaroInfo {
	close(f.entered)
	<-f.release
	return f.liveForkFigaro.Info()
}

func TestRegistryListDoesNotHoldRegistryLockDuringInfo(t *testing.T) {
	registry := NewRegistry()
	f := &blockingInfoFigaro{
		liveForkFigaro: liveForkFigaro{id: "live"},
		entered:        make(chan struct{}),
		release:        make(chan struct{}),
	}
	require.NoError(t, registry.Register(f))
	listed := make(chan struct{})
	go func() {
		registry.List()
		close(listed)
	}()
	<-f.entered

	bound := make(chan error, 1)
	go func() { bound <- registry.Bind(42, f.id, 0) }()
	select {
	case err := <-bound:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("Bind blocked behind Figaro.Info")
	}
	close(f.release)
	<-listed
}
