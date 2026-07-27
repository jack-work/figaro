package cli

import (
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/render"
	"github.com/jack-work/figaro/internal/term"
)

// THE LOCAL ECHO: a prompt that has been SUBMITTED and has no coordinate yet.
//
// Between `figaro send` returning and the drain classifying the prompt there
// is a state with no address. It is not a message (no turn, no node), it is
// not the open turn's content, and it is not history — so nothing in the store
// could hold it and nothing on either surface could draw it. That gap is what
// made a steer INVISIBLE for as long as the tool round it landed in: accepted,
// in the inbox, on no screen, for forty seconds. The user could not tell
// "accepted, waiting" from "dropped".
//
// This is the one place an echo's rows are built, for BOTH surfaces — incipit
// draws them in the live trailer above the footer, the pager as entries after
// the open turn. Two renderers for one representation is the defect
// renderSteeringNode's comment already names; an echo is not exempt.
//
// THE SHAPE IS THE STEER'S, with one word changed. A submitted prompt is most
// often about to become a NodeSteering (that is precisely the case that was
// invisible), so it is drawn as one — the inline "↳" marker and the dim
// blockquote gutter — with "queued" where a placed steer says "input". When
// the ack lands, the row is replaced by the real node in the same repaint and
// the only thing that moves is the label. The other resolution (our prompt
// opened a turn) is a bigger change on screen, and it is also the one that
// takes ~40 ms rather than the length of a tool call.
//
// "queued" and not "sent", "saved" or "committed", deliberately: the daemon's
// copy lives in a VOLATILE inbox that is never persisted. If the agent dies,
// the prompt dies with it. The word must not imply durability we do not have.
const pendingMarker = "↳ queued"

// pendingBody is one echo's prose rows at the given width, without the marker.
func pendingBody(p aria.Pending, width int) []string {
	if width <= 0 {
		width = 80
	}
	return render.Prose(blockquote(p.Text), width)
}

// pendingRows is one echo, marker included — the incipit's row shape.
func pendingRows(p aria.Pending, width int) []string {
	return append([]string{term.Dim(pendingMarker)}, pendingBody(p, width)...)
}

// pendingChrome is the incipit's Pending provider: every outstanding echo,
// oldest first, one blank row between them. Empty (and allocation-free) when
// nothing is pending, which is the steady state.
func pendingChrome(c *aria.Client) func(width int) []string {
	return func(width int) []string {
		var rows []string
		c.ForEachPending(func(p aria.Pending) bool {
			if len(rows) > 0 {
				rows = append(rows, "")
			}
			rows = append(rows, pendingRows(p, width)...)
			return true
		})
		return rows
	}
}
