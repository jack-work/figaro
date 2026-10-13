// Package wire is the only place that knows the trunk capability exists.
package wire

import (
	"github.com/jack-work/figaro/internal/store"
)

// Capabilities are negotiated with clients so a CLI can hide a verb the server cannot serve.
type Capabilities struct {
	// Trunks enables the presentation hierarchy: promote, and delete by presentation.
	Trunks bool
}

func Install(s *store.XwalStore, root string, caps Capabilities) error {
	if !caps.Trunks {
		return nil
	}
	// The hierarchy is a form on a reserved stump; it folds a legacy trunks.json in on first open.
	t, err := store.OpenTopologyTree(s, root)
	if err != nil {
		return err
	}
	s.SetTree(t)
	return nil
}
