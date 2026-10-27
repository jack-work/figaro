package angelus

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/peer"
	"github.com/jack-work/figaro/internal/peer/peertest"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/troupe"
)

// troupe.members is this node's membership as it stands: its version, every
// peer it names, the version each last showed on a handshake, and whether
// that differs from ours. A peer never met has no version, not version 0.
func TestTroupeMembersShowsEachPeersVersionAndDrift(t *testing.T) {
	pki := peertest.NewPKI(t, "fed")
	members, err := peer.NewStatic(7, []config.Peer{
		pki.Peer("self", "agrees", "127.0.0.1:7001"),
		pki.Peer("self", "behind", "127.0.0.1:7002"),
		pki.Peer("self", "unmet", "127.0.0.1:7003"),
	}, nil)
	require.NoError(t, err)
	seen := &peer.Versions{}
	seen.Saw("agrees", 7, 7, 0)
	seen.Saw("behind", 5, 7, 0)

	a := &Angelus{Registry: NewRegistry(), Backend: store.NewTestBackend(t), Hubs: newHubs(), Peers: members, PeerVersions: seen}
	hs := NewHandlers(ServerConfig{Angelus: a, Config: &config.Loaded{}, Ctx: inProcess(context.Background())})
	out, err := hs.Map[rpc.MethodTroupeMembers](inProcess(context.Background()), nil)
	require.NoError(t, err)
	got := out.(rpc.TroupeMembersResponse)

	require.Equal(t, a.Backend.NodeID(), got.Node)
	require.Equal(t, uint64(7), got.Version)
	byNode := map[string]rpc.TroupeMember{}
	for _, m := range got.Peers {
		byNode[m.Node] = m
	}
	require.Len(t, byNode, 3)
	require.Equal(t, "tls://127.0.0.1:7002", byNode["behind"].Endpoint)
	require.True(t, byNode["agrees"].Met)
	require.False(t, byNode["agrees"].Drift)
	require.Equal(t, uint64(5), byNode["behind"].Version)
	require.True(t, byNode["behind"].Drift)
	require.False(t, byNode["unmet"].Met)
	require.False(t, byNode["unmet"].Drift)
}

// A node with no peers answers with an empty membership, not an error.
func TestTroupeMembersWithoutPeers(t *testing.T) {
	a := &Angelus{Registry: NewRegistry(), Backend: store.NewTestBackend(t), Hubs: newHubs()}
	hs := NewHandlers(ServerConfig{Angelus: a, Config: &config.Loaded{}, Ctx: inProcess(context.Background())})
	out, err := hs.Map[rpc.MethodTroupeMembers](inProcess(context.Background()), nil)
	require.NoError(t, err)
	got := out.(rpc.TroupeMembersResponse)
	require.Empty(t, got.Peers)
	require.NotNil(t, got.Peers)
}

// With troupe keys, troupe.members carries when this node's certificate and
// intermediate end, and the keeper's error when there is one, so a lapse
// shows in troupe ls before peers start failing.
func TestTroupeMembersCarriesKeyExpiry(t *testing.T) {
	k := &troupe.Keeper{Dir: troupe.DirFor(t.TempDir(), "estate"), Troupe: "estate", Node: "n"}
	_ = k.Ensure()
	a := &Angelus{Registry: NewRegistry(), Backend: store.NewTestBackend(t), Hubs: newHubs(), Troupe: k}
	hs := NewHandlers(ServerConfig{Angelus: a, Config: &config.Loaded{}, Ctx: inProcess(context.Background())})
	out, err := hs.Map[rpc.MethodTroupeMembers](inProcess(context.Background()), nil)
	require.NoError(t, err)
	got := out.(rpc.TroupeMembersResponse)
	require.NotNil(t, got.Keys)
	require.Equal(t, "estate", got.Keys.Troupe)
	require.Contains(t, got.Keys.Error, "figaro troupe intermediate estate")
}
