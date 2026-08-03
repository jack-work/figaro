// Package wire is the ONLY place that knows the trunk capability exists.
//
// Everything else depends on internal/topo.Tree. This package decides, once,
// whether that Tree is the topology itself or a pstate-backed presentation
// hierarchy — and it is the single import that a trunkless build would drop.
//
// internal/trunk/isolation_test.go enforces that; if another package starts
// importing internal/trunk, that test fails and names it.
package wire

import (
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/trunk"
)

// Capabilities are the optional behaviours a server is built with. They are
// negotiated with clients so a CLI can hide a verb the server cannot serve.
type Capabilities struct {
	// Trunks enables the presentation hierarchy: promote, and a delete that
	// follows presentation rather than history.
	Trunks bool
}

// Names are the capability names as they go over the wire.
func (c Capabilities) Names() []string {
	var out []string
	if c.Trunks {
		out = append(out, "trunks")
	}
	return out
}

// Install gives the store its presentation hierarchy.
//
// Without the trunk capability the store keeps topo.FromTopology: the
// hierarchy IS the history, promote reports ErrNoPromote, and a delete can
// never orphan anyone because the two trees agree by construction.
func Install(s *store.XwalStore, root string, caps Capabilities) error {
	if !caps.Trunks {
		return nil
	}
	t, err := trunk.Open(root, s.TopologyAdjacency())
	if err != nil {
		return err
	}
	s.SetTree(t)
	return nil
}
