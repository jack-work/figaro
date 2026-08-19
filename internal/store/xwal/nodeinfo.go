package xwal

// NodeInfo is one node of the fork forest. Treat it as immutable: readers may
// hold it while a mutation runs.
type NodeInfo struct {
	Trunk  string `json:"trunk,omitempty"` // "" for the root and for stumps
	IsRoot bool   `json:"root,omitempty"`

	// Flat lineage. From is the node forked from; empty only for the root.
	From string `json:"from,omitempty"`
	Kind string `json:"kind,omitempty"` // null | loadout | conversation
}

// stumpName is key when the node is a loadout stump, else "". The key is
// the name: a flat node's key IS its directory.
func stumpName(key string, n *NodeInfo) string {
	if n != nil && n.Kind == "loadout" {
		return key
	}
	return ""
}
