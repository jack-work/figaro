package rpc

// A fork point is sealed and snapped to a turn boundary, so the shared prefix
// is coordinate-identical in both arias and a client can keep every turn
// below the divergence across a subject switch.

type LineageRequest struct {
	FigaroID string `json:"figaro_id"`
	// Against is the other aria; with it the answer carries the divergence.
	Against string `json:"against,omitempty"`
}

// LineageLink.Base is the first turn this link owns; turns below it belong to
// the link before it. A root's base is zero.
type LineageLink struct {
	Node string `json:"node"`
	Base uint64 `json:"base"`
}

type LineageResponse struct {
	// Chain is root first, ending in FigaroID. The genesis root and the
	// outfit version are not in it.
	Chain   []LineageLink `json:"chain,omitempty"`
	Against []LineageLink `json:"against,omitempty"`
	// Ancestor is empty when the two share nothing.
	Ancestor string `json:"ancestor,omitempty"`
	// Divergence is the first turn not shared: keep turn < Divergence. Zero
	// means nothing is shared; one aria against itself diverges at the
	// maximum turn.
	Divergence uint64 `json:"divergence,omitempty"`
	// Epoch is the topology form's revision; a client re-asks when it moves.
	Epoch uint64 `json:"epoch"`
}
