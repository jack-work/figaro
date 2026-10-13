package cli

// A node is finer than a fork point: a fork cuts between whole messages, so a node
// coordinate resolves to the message boundary at or before it, and the difference
// is reported. Nodes come from the daemon over the read wire so the numbering
// matches the screen.

import (
	"context"
	"fmt"

	"github.com/jack-work/figaro/api/aria"
	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/sdk"
)

// Small: the walk wants the head of one turn.
const forkNodePage = 64 << 10

const forkNodePages = 32

const forkNodeSettle = 4 << 20

// resolveForkPoint returns the wire coordinate and a note when it differs from the
// one typed.
func resolveForkPoint(ctx context.Context, acli *sdk.Angelus, ariaID string, at forkPoint) (forkPoint, string, error) {
	if !at.hasNode {
		return at, "", nil
	}
	// `:19.-1` addresses the inquiry, which is the turn coordinate.
	if at.node == inquiryNode {
		return forkPoint{turn: at.turn}, "", nil
	}
	nodes, exact, err := forkNodesOf(ctx, acli, ariaID, at.turn, at.node)
	if err != nil {
		return forkPoint{}, "", err
	}
	if at.node >= len(nodes) {
		// A count clipped by the page budget must say so.
		has := "it has " + nodeRangeOf(len(nodes))
		if !exact {
			has = "it has at least " + nodeRangeOf(len(nodes))
		}
		return forkPoint{}, "", fmt.Errorf("turn %d has no node %d (%s)", at.turn, at.node, has)
	}
	cut, landed := forkCutBefore(nodes, at.node)
	note := forkNodeNote(at, landed, int(cut))
	if cut == 0 {
		return forkPoint{turn: at.turn}, note, nil
	}
	return forkPoint{lt: cut}, note, nil
}

func nodeRangeOf(n int) string {
	switch n {
	case 0:
		return "none"
	case 1:
		return "node 0"
	}
	return fmt.Sprintf("nodes 0..%d", n-1)
}

// Landing on node 0 keeps the question and drops the answers; only a zero cut
// takes the question too.
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

// forkCutBefore returns the LT to fork at so node idx and after are replaced, and
// the node the cut lands before. The cut retreats past a whole message, and past a
// tool call whose result it would strand: a tool_use without its tool_result is
// rejected by the provider. A zero cut means nothing of the turn survives.
func forkCutBefore(nodes []livedoc.Node, idx int) (cut uint64, landed int) {
	// idx 0 keeps the question; `:19` drops it too.
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
			return 0, 0
		}
		cut = next - 1
	}
	return cut, landingFor(nodes, cut)
}

// A tool node carries two LTs (invoke, result); every other node one.
func firstLT(n livedoc.Node) uint64 {
	if len(n.LTs) == 0 {
		return 0
	}
	return n.LTs[0]
}

func lastLT(n livedoc.Node) uint64 {
	if len(n.LTs) == 0 {
		return 0
	}
	return n.LTs[len(n.LTs)-1]
}

// Only a tool node spans two messages.
func straddlingNode(nodes []livedoc.Node, cut uint64) int {
	for i := range nodes {
		if firstLT(nodes[i]) <= cut && lastLT(nodes[i]) > cut {
			return i
		}
	}
	return -1
}

func landingFor(nodes []livedoc.Node, cut uint64) int {
	for i := range nodes {
		if lt := firstLT(nodes[i]); lt > cut {
			return i
		}
	}
	return len(nodes)
}

// A backward read anchored past the turn's end clamps to its last node and reads
// exclusively before it, so it can never see the final node; a forward read sees
// node 0 onward but stops on budget. Walk backward; when the walk comes back
// exactly one short, settle forward.
func forkNodesOf(ctx context.Context, acli *sdk.Angelus, ariaID string, turn uint64, idx int) (nodes []livedoc.Node, exact bool, err error) {
	held, err := forkNodesBack(ctx, acli, ariaID, turn, idx)
	if err != nil {
		return nil, false, err
	}
	if len(held) > idx {
		return held, true, nil
	}
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

// Indices are only meaningful counted from node 0.
func forkNodesBack(ctx context.Context, acli *sdk.Angelus, ariaID string, turn uint64, idx int) ([]livedoc.Node, error) {
	at := aria.Anchor{Turn: turn, Node: uint64(idx + 1)}
	var held []livedoc.Node
	for page := 0; page < forkNodePages; page++ {
		resp, err := acli.Read(ctx, rpc.ReadRequest{
			FigaroID: ariaID, At: at, Backward: true, Limit: forkNodePage,
		})
		if err != nil {
			return nil, err
		}
		part, ok := partOf(resp, turn)
		if !ok {
			if len(held) == 0 {
				return nil, fmt.Errorf("aria %s has no turn %d", ariaID, turn)
			}
			break
		}
		held = append(append([]livedoc.Node(nil), part.Nodes...), held...)
		if part.From == 0 {
			return held, nil
		}
		if !resp.More.Before || resp.Prev == nil {
			break
		}
		at = *resp.Prev
	}
	// Without node 0 the indices in hand are not the ones the user named.
	return nil, fmt.Errorf("could not reach the head of turn %d to count its nodes (read %d pages)", turn, forkNodePages)
}

// One page: a tie-breaker, not a walk.
func forkNodesForward(ctx context.Context, acli *sdk.Angelus, ariaID string, turn uint64) ([]livedoc.Node, bool, error) {
	resp, err := acli.Read(ctx, rpc.ReadRequest{
		FigaroID: ariaID, At: aria.Anchor{Turn: turn}, Limit: forkNodeSettle,
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

func partOf(p aria.Page, turn uint64) (aria.TurnPart, bool) {
	for i := range p.Parts {
		if p.Parts[i].ID == turn {
			return p.Parts[i], true
		}
	}
	return aria.TurnPart{}, false
}

// A pointer: `"node": 0` and no node are different answers, and omitempty cannot
// tell them apart.
func (f forkPoint) nodeJSON() *int {
	if !f.hasNode {
		return nil
	}
	n := f.node
	return &n
}
