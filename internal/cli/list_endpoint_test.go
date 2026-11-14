package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
)

// ls through a troupe endpoint asks the endpoint to skip this node, so this node's
// rows come from its own daemon, stamped with its node the way the endpoint
// stamps its peers' rows. gluck listing through spain once lost gluck's rows
// and warned that gluck refused spain.
func TestListThroughAEndpointAddsThisNodesRows(t *testing.T) {
	endpointBackend, there := store.NewTestAria(t, "endpoint", message.Patch{})
	homeBackend, here := store.NewTestAria(t, "home", message.Patch{})
	require.NotEqual(t, endpointBackend.NodeID(), homeBackend.NodeID())
	_, endpointDir := serveBackend(t, endpointBackend)
	_, homeDir := serveBackend(t, homeBackend)

	cfg := t.TempDir()
	t.Setenv("FIGARO_RUNTIME_DIR", homeDir)
	t.Setenv("FIGARO_CONFIG_DIR", cfg)
	t.Setenv("FIGARO_TROUPE", "unix://"+filepath.Join(endpointDir, "angelus.sock"))
	t.Setenv("FIGARO_NO_BIND", "1")
	loaded, err := config.Load(cfg)
	require.NoError(t, err)

	out := stdoutOf(t, func() { runList(loaded, lsOpts{jsonOut: true}) })
	var rows []rpc.FigaroInfoResponse
	require.NoError(t, json.Unmarshal([]byte(out), &rows), out)
	nodes := map[string]string{}
	for _, r := range rows {
		nodes[r.ID] = r.Node
	}
	require.Contains(t, nodes, there)
	require.Equal(t, "", nodes[there])
	require.Contains(t, nodes, here, "this node's rows are missing")
	require.Equal(t, homeBackend.NodeID(), nodes[here])
}

// The same daemon behind the endpoint and the local socket (a box) is one
// listing, never merged with itself.
func TestListThroughAEndpointToThisNodeIsOneListing(t *testing.T) {
	backend, id := store.NewTestAria(t, "home", message.Patch{})
	_, dir := serveBackend(t, backend)

	cfg := t.TempDir()
	t.Setenv("FIGARO_RUNTIME_DIR", dir)
	t.Setenv("FIGARO_CONFIG_DIR", cfg)
	t.Setenv("FIGARO_TROUPE", "unix://"+filepath.Join(dir, "angelus.sock"))
	t.Setenv("FIGARO_NO_BIND", "1")
	loaded, err := config.Load(cfg)
	require.NoError(t, err)

	out := stdoutOf(t, func() { runList(loaded, lsOpts{jsonOut: true}) })
	var rows []rpc.FigaroInfoResponse
	require.NoError(t, json.Unmarshal([]byte(out), &rows), out)
	seen := 0
	for _, r := range rows {
		if r.ID == id {
			seen++
			require.Equal(t, "", r.Node)
		}
	}
	require.Equal(t, 1, seen)
}

// An endpoint from before except_nodes still asks this node; whatever it brings
// back about this node gives way to this node's own answer.
func TestMergeHomeDropsTheEndpointsWordOnThisNode(t *testing.T) {
	away := &rpc.ListResponse{
		Figaros: []rpc.FigaroInfoResponse{
			{ID: "on-endpoint"},
			{ID: "stale", Node: "home"},
			{ID: "on-peer", Node: "peer"},
		},
		Unreachable: []rpc.NodeUnreachable{{Node: "home", Error: "refused"}, {Node: "far", Error: "down"}},
	}
	home := &rpc.ListResponse{
		Figaros: []rpc.FigaroInfoResponse{
			{ID: "mine"},
			{ID: "on-peer", Node: "peer"},
			{ID: "only-home-sees", Node: "lone"},
		},
		Unreachable: []rpc.NodeUnreachable{{Node: "endpoint", Error: "no bearer"}},
	}
	got := mergeHome(away, home, "home", "endpoint", nodesAnswering(away, "endpoint"))

	ids := map[string]string{}
	for _, r := range got.Figaros {
		_, dup := ids[r.ID]
		require.False(t, dup, r.ID)
		ids[r.ID] = r.Node
	}
	require.Equal(t, map[string]string{"on-endpoint": "", "on-peer": "peer", "mine": "home", "only-home-sees": "lone"}, ids)
	require.Equal(t, []rpc.NodeUnreachable{{Node: "far", Error: "down (as seen from node endpoint; this node did not try it)"}}, got.Unreachable)
}

// A local listing names the peers it could not ask in one line, never one
// warning per peer, and names the troupe when this config has only one.
func TestLocalListSaysOnceWhatItDidNotAsk(t *testing.T) {
	cfg := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(cfg, "troupes"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(cfg, "troupes", "kelliher.toml"), []byte("url = \"wss://x\"\n"), 0o600))
	t.Setenv("FIGARO_CONFIG_DIR", cfg)
	t.Setenv("FIGARO_TROUPE", "")
	loaded, err := config.Load(cfg)
	require.NoError(t, err)

	var errs bytes.Buffer
	prev := stderrw
	stderrw = &errs
	t.Cleanup(func() { stderrw = prev })

	printPeersUnasked(loaded, 0)
	require.Empty(t, errs.String())
	printPeersUnasked(loaded, 2)
	require.Equal(t, "\nthis node only: 2 peer(s) not asked (FIGARO_TROUPE=kelliher figaro ls asks the troupe)\n", errs.String())
}

func TestMergeHomeCountsBothSidesUnaskedPeers(t *testing.T) {
	got := mergeHome(&rpc.ListResponse{PeersUnasked: 1}, &rpc.ListResponse{PeersUnasked: 2}, "home", "endpoint", map[string]bool{"endpoint": true})
	require.Equal(t, 3, got.PeersUnasked)
}
