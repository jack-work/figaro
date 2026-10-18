package compose

import (
	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

type Namer func(sender string) string

func (n Namer) Segments(segs []aria.InquirySegment) []aria.InquirySegment {
	if n == nil {
		return segs
	}
	for i := range segs {
		if segs[i].Sender != "" {
			segs[i].Name = n(segs[i].Sender)
		}
	}
	return segs
}

func (n Namer) Nodes(nodes []livedoc.Node) []livedoc.Node {
	if n == nil {
		return nodes
	}
	for i := range nodes {
		if nodes[i].Sender != "" {
			nodes[i].SenderName = n(nodes[i].Sender)
		}
	}
	return nodes
}

func (n Namer) Turns(turns []aria.Turn) []aria.Turn {
	if n == nil {
		return turns
	}
	for i := range turns {
		n.Segments(turns[i].InquirySegments)
		n.Nodes(turns[i].Nodes)
	}
	return turns
}
