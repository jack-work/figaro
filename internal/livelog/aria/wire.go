package aria

import wire "github.com/jack-work/figaro/api/aria"

// The read API's wire types are DEFINED in api/aria -- a client needs them
// and must not need the daemon to get them. These are re-exports, not a
// compatibility shim: one definition, named in the two places that read it.
type (
	Turn           = wire.Turn
	InquirySegment = wire.InquirySegment
	Metrics        = wire.Metrics
	Live           = wire.Live
	NodeDelta      = wire.NodeDelta
	Message        = wire.Message
	Direction      = wire.Direction
	Anchor         = wire.Anchor
	More           = wire.More
	TurnPart       = wire.TurnPart
	Page           = wire.Page
)

const (
	Forward  = wire.Forward
	Backward = wire.Backward
)

// maxNode is the largest representable node ordinal; it belongs with Anchor,
// whose "the whole turn" spelling it is.
const maxNode = ^uint64(0)
