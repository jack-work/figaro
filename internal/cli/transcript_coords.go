package cli

import (
	"strconv"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
)

// COORDINATES: the address of a thing, drawn on the thing.
//
// The pager can now be told to go somewhere (`:12.3`, see transcript_jump.go).
// A gesture that names a coordinate is useless if no coordinate is ever shown,
// so Ctrl-O — the verbosity toggle, which already reveals tool timings and
// argument dumps — additionally draws one dim row above every rendered node:
//
//	 12.3 · 01:23:45
//
// turn id, node id, and when the node was written. The inquiry gets the same
// row with its VIRTUAL node id (inquiryNode, -1), because the question is
// addressable exactly like a node is — it selects, copies and highlights
// through the same nodeRef — and hiding its id would make the one thing at the
// top of every turn the one thing you cannot jump to.
//
// Three constraints the shape has to satisfy, all of them load-bearing:
//
//   - ONE PHYSICAL ROW. It goes through plainNodeRow/clipToWidth like every
//     other row, so a narrow pane truncates it instead of wrapping and
//     desynchronising the painter's one-row-per-line cursor math.
//   - IT MUST NOT SEPARATE A VOICE HEADER FROM ITS RULE. The rule is the
//     header's overline (TestVoiceHeaderHugsItsRule); so the coordinate sits
//     BELOW the header, on the node's own side of the chrome — it labels the
//     node, not the voice.
//   - THE DEFAULT PATH MUST NOT PAY. The row is minted inside renderMsgBase,
//     which is memoized in rowCache, and only when verbose is on. With Ctrl-O
//     off the cost is one bool read per message render, and no frame, no
//     golden and no row count moves at all.
//
// It carries the NODE'S ref, deliberately: the coordinate is part of the
// node's span, so selection washes over it, ^N scrolls it into view with the
// node, and nodeSpanOf/ensureSelectionVisible stay in agreement without
// either of them learning that a label exists.

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
// 1970 — an unstamped node is a real state (an empty prose block minted so ids
// cannot shift) and lying about it is worse than saying nothing.
func coordLabel(turn, node int, at int64) string {
	s := strconv.Itoa(turn) + "." + strconv.Itoa(node)
	if at != 0 {
		s += coordSep + time.UnixMilli(at).Format("15:04:05")
	}
	return s
}

// verbose is the pager's view of the Ctrl-O toggle. The transcript does not own
// the flag — it lives on the shared renderSettings the input loop mutates — so
// this is the one place that reaches for it, rather than three sites each
// repeating the type assertion.
func (t *transcript) verbose() bool {
	view, ok := t.view.(*ariaView)
	return ok && view.settings != nil && view.settings.verbose
}
