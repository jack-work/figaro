// Package aria is the client half of the live aria protocol: a range store
// and the fold that turns pushed pages into a view.
package aria

import wire "github.com/jack-work/figaro/api/aria"

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

const maxNode = ^uint64(0)
