package cli

// FORKING AT A NODE: `<id>:<turn>.<node>`.
//
// The pager numbers every node of a turn and draws that number under ^O
// (`19.10 · 01:23:45`). This file is what makes that number a fork coordinate,
// so the address you READ is the address you can BRANCH AT.
//
// ONE FACT DECIDES THE WHOLE DESIGN: a node is finer than a fork point. A
// node is a content BLOCK of a message -- an assistant message that says a
// paragraph and then calls a tool is one message and two nodes -- while a fork
// cuts the message log between whole messages, because that is what the branch
// shares with its parent. So a node coordinate is resolved to the message
// boundary at or before it, and when that boundary is earlier than the node
// named, the difference is REPORTED rather than quietly taken.
//
// The nodes come from the daemon over the ordinary read wire: the same
// composer, the same numbering the reader saw on screen. Re-deriving them from
// the IR here would be a second composer, and two composers that disagree by
// one node would fork one node away from where the user pointed.

import (
	"context"
	"fmt"

	"github.com/jack-work/figaro/api/aria"
	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/sdk"
)

// forkNodePage is the byte budget of one backward read while resolving a node
// coordinate. Small on purpose: the walk wants the HEAD of one turn, and a
// page big enough to also drag in the turns before it costs bytes nobody reads.
const forkNodePage = 64 << 10

// forkNodePages bounds the walk. A turn whose head cannot be reached in this
// many pages is not a turn anyone is addressing node 10 of.
const forkNodePages = 32

// forkNodeSettle is the budget of the ONE forward read that settles "is this
// the turn's last node, or no node at all". Generous, because it is asked once
// and only when the answer is otherwise unknowable.
const forkNodeSettle = 4 << 20

// resolveForkPoint turns the coordinate the user typed into the one the wire
// takes, and returns a note when the two are not the same place.
//
// Only the node form does any work: `:19` goes to the daemon as a turn (it
// owns that translation, and has since before this file), `.326` is already an
// LT, and the head is the head.
func resolveForkPoint(ctx context.Context, acli *sdk.Angelus, ariaID string, at forkPoint) (forkPoint, string, error) {
	if !at.hasNode {
		return at, "", nil
	}
	// THE QUESTION IS THE TURN. `:19.-1` addresses the inquiry, which opens
	// turn 19 -- so it is the turn coordinate, spelled the way the pager
	// spells that row, and it goes down the path that already exists.
	if at.node == inquiryNode {
		return forkPoint{turn: at.turn}, "", nil
	}
	nodes, exact, err := forkNodesOf(ctx, acli, ariaID, at.turn, at.node)
	if err != nil {
		return forkPoint{}, "", err
	}
	if at.node >= len(nodes) {
		// A COUNT THAT IS NOT EXACT SAYS SO. A turn too large to read whole
		// can still be addressed by node, and "it has nodes 0..137" would be
		// a lie told by the page budget rather than by the conversation.
		has := "it has " + nodeRangeOf(len(nodes))
		if !exact {
			has = "it has at least " + nodeRangeOf(len(nodes))
		}
		return forkPoint{}, "", fmt.Errorf("turn %d has no node %d (%s)", at.turn, at.node, has)
	}
	cut, landed := forkCutBefore(nodes, at.node)
	note := forkNodeNote(at, landed, int(cut))
	if cut == 0 {
		// Nothing of the turn survives the cut, so this is the turn
		// coordinate: let the daemon compute it, exactly as `:19` does.
		return forkPoint{turn: at.turn}, note, nil
	}
	return forkPoint{lt: cut}, note, nil
}

// nodeRangeOf spells a turn's node range for an error.
func nodeRangeOf(n int) string {
	switch n {
	case 0:
		return "none"
	case 1:
		return "node 0"
	}
	return fmt.Sprintf("nodes 0..%d", n-1)
}

// forkNodeNote explains a cut that landed earlier than the node named. An
// exact landing says nothing: the command's own report already names the
// coordinate.
//
// LANDING ON NODE 0 IS NOT "the whole turn". It is the fork that keeps the
// turn's question and drops every answer to it, which is a place a reader
// means; only a cut of zero takes the question too, and the caller turns that
// into the turn coordinate before this is asked.
func forkNodeNote(at forkPoint, landed, cut int) string {
	if landed == at.node {
		return ""
	}
	if cut == 0 {
		return fmt.Sprintf("node %d is inside the message that opens turn %d, so the fork takes the whole turn: a fork cuts whole messages, never inside one",
			at.node, at.turn)
	}
	return fmt.Sprintf("node %d cannot be cut at: forking before node %d instead (a fork cuts whole messages, and node %d is not where one begins)",
		at.node, landed, at.node)
}

// ---------------------------------------------------------------------------
// The cut. Pure: it is the part worth testing, and it needs no daemon.
// ---------------------------------------------------------------------------

// forkCutBefore reports the LT to fork at so that node `idx` and everything
// after it are replaced, plus the node the cut ACTUALLY lands before.
//
// Two reasons the landing can be earlier than the node asked for, and they are
// the same reason wearing two hats -- a message is atomic:
//
//  1. Node idx is not the first block of its message. Cutting before it would
//     have to cut inside a message, so the cut goes before the whole message,
//     taking its earlier blocks (a paragraph, some thinking) with it.
//  2. The cut would land BETWEEN a tool call and its result. That is the
//     stranded `tool_use` the LT form has always been able to produce, and it
//     is fatal at the provider, not cosmetic: Anthropic rejects a conversation
//     whose tool_use has no tool_result. A tool NODE carries both coordinates
//     (LTs = [invoke, result]), so the straddle is visible here and the cut
//     retreats past the whole call.
//
// A zero cut means "nothing of this turn survives": the caller turns that back
// into the turn coordinate rather than into a head fork, which is a different
// thing entirely.
func forkCutBefore(nodes []livedoc.Node, idx int) (cut uint64, landed int) {
	// idx 0 IS ADDRESSABLE, and it is not the same place as `:19`. Cutting
	// before the agent's first node keeps the turn's QUESTION and drops the
	// answer; cutting at the turn drops the question too. Both are things a
	// reader means, so both have a coordinate.
	if idx < 0 || idx >= len(nodes) {
		return 0, 0
	}
	first := firstLT(nodes[idx])
	if first == 0 {
		return 0, 0
	}
	cut = first - 1
	for {
		straddled := straddlingNode(nodes[:idx], cut)
		if straddled < 0 {
			break
		}
		next := firstLT(nodes[straddled])
		if next == 0 || next-1 >= cut {
			return 0, 0 // no progress to make: take the whole turn
		}
		cut = next - 1
	}
	return cut, landingFor(nodes, cut)
}

// firstLT is the coordinate a node BEGINS at. A tool node carries two (its
// invoke and its result); every other node carries one.
func firstLT(n livedoc.Node) uint64 {
	if len(n.LTs) == 0 {
		return 0
	}
	return n.LTs[0]
}

// lastLT is where a node ENDS: for a tool, the LT of its result.
func lastLT(n livedoc.Node) uint64 {
	if len(n.LTs) == 0 {
		return 0
	}
	return n.LTs[len(n.LTs)-1]
}

// straddlingNode finds a node the cut would split: one that begins at or
// before the cut and ends after it. Only a tool node can do that, because only
// a tool node spans two messages.
func straddlingNode(nodes []livedoc.Node, cut uint64) int {
	for i := range nodes {
		if firstLT(nodes[i]) <= cut && lastLT(nodes[i]) > cut {
			return i
		}
	}
	return -1
}

// landingFor names the first node the cut drops: the one the fork really
// happens before.
func landingFor(nodes []livedoc.Node, cut uint64) int {
	for i := range nodes {
		if lt := firstLT(nodes[i]); lt > cut {
			return i
		}
	}
	return len(nodes)
}

// ---------------------------------------------------------------------------
// The read.
// ---------------------------------------------------------------------------

// forkNodesOf returns turn's nodes from 0 through at least idx, as the DAEMON
// composed them.
//
// TWO READS, because the wire has two ends and neither alone can answer:
//
//   - A BACKWARD read anchored on (turn, idx+1) walks the head of the turn,
//     however large the turn is. It is the primary. But an anchor past the
//     turn's end CLAMPS to its last node and then reads exclusively before it,
//     so this end can never see a turn's final node. Measured: a 6-node turn
//     answers a (turn, 6) anchor with nodes 0..4, and a (turn, 7) anchor with
//     the same five.
//   - A FORWARD read from the turn starts at node 0 and stops on a budget. It
//     is the only read that CAN see the last node, and it cannot see very far:
//     a 240-node turn answers with 138 whatever budget it is given.
//
// So: walk backward, and when the walk comes back exactly one node short --
// the one case where "the node is last" and "the node does not exist" look
// identical -- settle it forward.
func forkNodesOf(ctx context.Context, acli *sdk.Angelus, ariaID string, turn uint64, idx int) (nodes []livedoc.Node, exact bool, err error) {
	held, err := forkNodesBack(ctx, acli, ariaID, turn, idx)
	if err != nil {
		return nil, false, err
	}
	if len(held) > idx {
		return held, true, nil // the node asked for is in hand
	}
	// The walk stopped at or before the node named, which is the one case the
	// backward end cannot report on: its clamp hides a turn's last node, so
	// "the node is last" and "the node does not exist" arrive identical.
	whole, clipped, ferr := forkNodesForward(ctx, acli, ariaID, turn)
	if ferr != nil {
		return nil, false, ferr
	}
	if len(whole) > idx || !clipped {
		return whole, true, nil
	}
	if len(whole) > len(held) {
		return whole, false, nil
	}
	return held, false, nil
}

// forkNodesBack is the backward walk: pages toward the head of the turn until
// node 0 is in hand, because indices are only meaningful counted from there.
func forkNodesBack(ctx context.Context, acli *sdk.Angelus, ariaID string, turn uint64, idx int) ([]livedoc.Node, error) {
	before, beforeNode := int(turn), idx+1
	var held []livedoc.Node
	for page := 0; page < forkNodePages; page++ {
		resp, err := acli.Read(ctx, rpc.ReadRequest{
			FigaroID: ariaID, Before: before, BeforeNode: beforeNode,
			Backward: true, Limit: forkNodePage,
		})
		if err != nil {
			return nil, err
		}
		part, ok := partOf(resp, turn)
		if !ok {
			if len(held) == 0 {
				return nil, fmt.Errorf("aria %s has no turn %d", ariaID, turn)
			}
			break // the turn fell off the page before its head did
		}
		held = append(append([]livedoc.Node(nil), part.Nodes...), held...)
		if part.From == 0 {
			return held, nil
		}
		if !resp.More.Before {
			break
		}
		before, beforeNode = int(part.ID), int(part.From)
	}
	// Node 0 never came back, so the indices in hand are not the indices the
	// user is naming. Refusing is the only honest answer: an off-by-N fork
	// point is worse than no fork at all.
	return nil, fmt.Errorf("could not reach the head of turn %d to count its nodes (read %d pages)", turn, forkNodePages)
}

// forkNodesForward reads the turn from its head, and reports whether the page
// stopped short. One page: this is a tie-breaker, not a walk.
func forkNodesForward(ctx context.Context, acli *sdk.Angelus, ariaID string, turn uint64) ([]livedoc.Node, bool, error) {
	resp, err := acli.Read(ctx, rpc.ReadRequest{
		FigaroID: ariaID, SinceLT: int(turn), Limit: forkNodeSettle,
	})
	if err != nil {
		return nil, false, err
	}
	part, ok := partOf(resp, turn)
	if !ok || part.From != 0 {
		return nil, true, nil
	}
	return part.Nodes, part.ClippedTail, nil
}

// partOf finds the requested turn's slice on a page.
func partOf(p aria.Page, turn uint64) (aria.TurnPart, bool) {
	for i := range p.Parts {
		if p.Parts[i].ID == turn {
			return p.Parts[i], true
		}
	}
	return aria.TurnPart{}, false
}

// nodeJSON is the node a -j caller asked for, or nil when none was named. A
// POINTER because node 0 is an address: `"node": 0` and no node at all are
// different answers, and omitempty cannot tell them apart.
func (f forkPoint) nodeJSON() *int {
	if !f.hasNode {
		return nil
	}
	n := f.node
	return &n
}
