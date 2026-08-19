package cli

import (
	"strconv"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
)

// COORDINATES: the address of a thing, drawn on the thing.

// coordSep is the middot between the address and the time. Kept here so the
// search prefilter (messageMayRenderQuery) and the row builder cannot disagree
// about the text a coordinate row holds.
const coordSep = " · "

// nodeCoordAt is the timestamp a node reports on its coordinate row. Prose and
// thinking blocks carry At (when the block was written); a tool carries
// StartedAt instead, because a tool's At is not set until it is folded and the
// interesting instant is when it began. FinishedAt is deliberately not used:
// a running tool has none, and a coordinate row must not change shape when the
// spinner stops.
func nodeCoordAt(n livedoc.Node) int64 {
	if n.At != 0 {
		return n.At
	}
	return n.StartedAt
}

// coordLabel renders one coordinate: "<turn>.<node>", plus " · hh:mm:ss" when
// there is a timestamp to show. A zero timestamp prints no time rather than
// 1970, an unstamped node is a real state (an empty prose block minted so ids
// cannot shift) and lying about it is worse than saying nothing.
func coordLabel(turn, node int, at int64) string {
	s := strconv.Itoa(turn) + "." + strconv.Itoa(node)
	if at != 0 {
		s += coordSep + time.UnixMilli(at).Format("15:04:05")
	}
	return s
}

// verbose is the pager's view of the Ctrl-O toggle. The transcript does not own
// the flag: it lives on the shared renderSettings the input loop mutates: so
// this is the one place that reaches for it, rather than three sites each
// repeating the type assertion.
func (t *transcript) verbose() bool {
	view, ok := t.view.(*ariaView)
	return ok && view.settings != nil && view.settings.verbose
}
