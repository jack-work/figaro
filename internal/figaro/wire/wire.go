// Package wire is the ONLY place that knows the trunk capability exists.
package wire

import (
	"github.com/jack-work/figaro/internal/store"
)

// Capabilities are the optional behaviours a server is built with. They are
// negotiated with clients so a CLI can hide a verb the server cannot serve.
type Capabilities struct {
	// Trunks enables the presentation hierarchy: promote, and a delete that
	// follows presentation rather than history.
	Trunks bool
}

// Install gives the store its presentation hierarchy.
func Install(s *store.XwalStore, root string, caps Capabilities) error {
	if !caps.Trunks {
		return nil
	}
	// The presentation hierarchy is a FORM on a reserved stump, and it folds
	// a legacy trunks.json in on first open. See store/topology_form.go.
	t, err := store.OpenTopologyTree(s, root)
	if err != nil {
		return err
	}
	s.SetTree(t)
	return nil
}
