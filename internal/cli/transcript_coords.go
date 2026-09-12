package cli

import (
	"strconv"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/internal/config"
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

// coordLabel renders one coordinate: "<turn>.<node>", or the turn alone for the
// question that opened it, plus the time it was written in the configured
// layout. A zero timestamp prints no time rather than 1970: an unstamped node
// is a real state, and lying about it is worse than saying nothing.
func coordLabel(turn, node int, at int64, layout string) string {
	s := strconv.Itoa(turn)
	if node >= 0 {
		s += "." + strconv.Itoa(node)
	}
	if at != 0 {
		if layout == "" {
			layout = config.CoordFormatDefault
		}
		s += coordSep + time.UnixMilli(at).Format(layout)
	}
	return s
}

// verbose is the pager's view of the M-m toggle: whether a block draws its
// address. The transcript does not own the flag, it lives on the shared
// renderSettings the input loop mutates, so this is the one place that reaches
// for it.
func (t *transcript) verbose() bool {
	view, ok := t.view.(*ariaView)
	return ok && view.settings != nil && view.settings.verbose
}

// coordFormat is the layout those addresses date themselves in.
func (t *transcript) coordFormat() string {
	if view, ok := t.view.(*ariaView); ok && view.settings != nil {
		return view.settings.coordFormat
	}
	return config.CoordFormatDefault
}
