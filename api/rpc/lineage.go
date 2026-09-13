package rpc

// The lineage read: where two arias part company.
//
// A fork point is SEALED. Everything below it can never change, and because
// the base is snapped down to a turn boundary the shared prefix is
// coordinate-identical in both arias: same turn ids, same node ordinals, same
// bytes. A client that is told where two arias diverge can therefore keep
// every turn below that point across a subject switch instead of reading it
// again.
//
// This is NOT figaro.read with a different name, and it is NOT a field on a
// page. A page field would be uncacheable (it rides every read of every aria)
// and would say nothing about the aria a client is LEAVING, which is half the
// question. A chain is immutable for the life of a node, so a reader hopping
// around a fork tree asks once per aria and never again.

// LineageRequest asks for one aria's ancestry, and optionally for where that
// ancestry parts company with another aria's.
type LineageRequest struct {
	FigaroID string `json:"figaro_id"`
	// Against is the other aria. With it the answer carries the divergence,
	// which is the whole reason a client asks.
	Against string `json:"against,omitempty"`
}

// LineageLink is one conversation in an ancestry, root first. Base is the
// first TURN this link owns; turns below it belong to the link before it,
// byte for byte. A root conversation owns everything, so its base is zero.
//
// The coordinate is a TURN and not an LT deliberately: LT is the model's
// clock, the client's caches and its window are keyed by turn, and the
// conversion needs the log, which is the server's to read.
type LineageLink struct {
	Node string `json:"node"`
	Base uint64 `json:"base"`
}

// LineageResponse is the chain, and the answer about the pair when one was
// asked for.
type LineageResponse struct {
	// Chain is the conversation ancestry of FigaroID, root first, ending in
	// FigaroID itself. The genesis root and the outfit stump are not in it:
	// they carry no turns a reader can see.
	Chain []LineageLink `json:"chain,omitempty"`
	// Against is the same for the other aria, when one was named.
	Against []LineageLink `json:"against,omitempty"`
	// Ancestor is the deepest conversation the two share, empty when they
	// share none.
	Ancestor string `json:"ancestor,omitempty"`
	// Divergence is the first turn the two arias do NOT share: keep turn <
	// Divergence, drop at or above. Zero means they share nothing. Two names
	// for one aria diverge at the maximum turn, because everything is shared,
	// which is what makes "keep turn < Divergence" the whole client rule with
	// no case analysis around it.
	Divergence uint64 `json:"divergence,omitempty"`
	// Epoch is the topology form's revision: the handle for "the past has
	// changed shape". A client holding a retained prefix re-asks when it
	// moves rather than trusting a cached chain.
	Epoch uint64 `json:"epoch"`
}
