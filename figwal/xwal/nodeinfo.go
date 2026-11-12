package xwal

// NodeInfo is one node of the fork tree. Treat it as immutable: readers may
// hold it while a mutation runs.
type NodeInfo struct {
	Trunk  string `json:"trunk,omitempty"` // "" for the root and for stumps
	IsRoot bool   `json:"root,omitempty"`

	// Flat lineage. From is the node forked from; empty only for the root.
	From string `json:"from,omitempty"`
	Kind string `json:"kind,omitempty"` // null | loadout | conversation

	// Placed is the node this one was placed under, when that is not From.
	// PlacedTop says it was placed under nothing. From is never rewritten.
	Placed    string `json:"placed,omitempty"`
	PlacedTop bool   `json:"placed_top,omitempty"`
}

// Placement is the node this one sits under: where it was placed, else where
// it was forked.
func (n *NodeInfo) Placement() string {
	switch {
	case n == nil, n.PlacedTop:
		return ""
	case n.Placed != "":
		return n.Placed
	}
	return n.From
}

func (n *NodeInfo) marker() nodeMarker {
	return nodeMarker{from: n.From, kind: n.Kind, trunk: n.Trunk, placed: n.Placed, placedTop: n.PlacedTop}
}

func (m nodeMarker) placedUnder(under string) nodeMarker {
	m.placed, m.placedTop = "", false
	switch {
	case under == m.from:
	case under == "":
		m.placedTop = true
	default:
		m.placed = under
	}
	return m
}

// stumpName is key when the node is a loadout stump, else "". The key is
// the name: a flat node's key IS its directory.
func stumpName(key string, n *NodeInfo) string {
	if n != nil && n.Kind == "loadout" {
		return key
	}
	return ""
}
