package cli

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
)

// ls through a troupe door asks the door to skip this node, so this node's
// rows come from its own daemon, stamped with its node the way the door
// stamps its peers' rows. gluck listing through spain once lost gluck's rows
// and warned that gluck refused spain.
func TestListThroughADoorAddsThisNodesRows(t *testing.T) {
	doorBackend, there := store.NewTestAria(t, "door", message.Patch{})
	homeBackend, here := store.NewTestAria(t, "home", message.Patch{})
	require.NotEqual(t, doorBackend.NodeID(), homeBackend.NodeID())
	_, doorDir := serveBackend(t, doorBackend)
	_, homeDir := serveBackend(t, homeBackend)

	cfg := t.TempDir()
	t.Setenv("FIGARO_RUNTIME_DIR", homeDir)
	t.Setenv("FIGARO_CONFIG_DIR", cfg)
	t.Setenv("FIGARO_TROUPE", "unix://"+filepath.Join(doorDir, "angelus.sock"))
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

// The same daemon behind the door and the local socket (a box) is one
// listing, never merged with itself.
func TestListThroughADoorToThisNodeIsOneListing(t *testing.T) {
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

// A door from before except_nodes still asks this node; whatever it brings
// back about this node gives way to this node's own answer.
func TestMergeHomeDropsTheDoorsWordOnThisNode(t *testing.T) {
	away := &rpc.ListResponse{
		Figaros: []rpc.FigaroInfoResponse{
			{ID: "on-door"},
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
		Unreachable: []rpc.NodeUnreachable{{Node: "door", Error: "no bearer"}},
	}
	got := mergeHome(away, home, "home", "door", nodesAnswering(away, "door"))

	ids := map[string]string{}
	for _, r := range got.Figaros {
		_, dup := ids[r.ID]
		require.False(t, dup, r.ID)
		ids[r.ID] = r.Node
	}
	require.Equal(t, map[string]string{"on-door": "", "on-peer": "peer", "mine": "home", "only-home-sees": "lone"}, ids)
	require.Equal(t, []rpc.NodeUnreachable{{Node: "far", Error: "down (as seen from node door; this node did not try it)"}}, got.Unreachable)
}
