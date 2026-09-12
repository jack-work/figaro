package cli

// A QUOTE IN A FORK PROMPT IS RESOLVED AGAINST THE CHILD. The child inherits
// its parent's entries up to the cut, so a coordinate means the same passage
// on both, and the daemon refuses one the child does not hold. But by then
// the branch exists, empty, with nothing in it: so the client checks first,
// with the two numbers it already has.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jack-work/figaro/api/quote"
	"github.com/jack-work/figaro/sdk"
)

// forkQuotePreflight refuses a fork-and-send whose prompt quotes a passage at
// or past the cut. wireAt is the point AS THE WIRE TAKES IT (after
// resolveForkPoint), so a node coordinate has already become an LT.
//
// A turn cut is checked against the first node of that turn. The turn's own
// question sits between the previous turn and that node and is not reachable
// from here; a quote of it slips past this check and is refused by the daemon
// after the fork, which is the outcome this exists to avoid but not one a
// reader is likely to ask for.
func forkQuotePreflight(ctx context.Context, acli *sdk.Angelus, ariaID string, wireAt forkPoint, prompt string) error {
	r, _, err := quote.Parse(prompt)
	if errors.Is(err, quote.ErrNone) {
		return nil
	}
	if err != nil {
		return err // the grammar's own refusal; the daemon would say the same
	}
	if wireAt.isHead() {
		return nil // a head fork shares everything
	}
	cut := wireAt.lt
	if cut == 0 {
		nodes, _, err := forkNodesForward(ctx, acli, ariaID, wireAt.turn)
		if err != nil {
			return err
		}
		for _, n := range nodes {
			for _, lt := range n.LTs {
				if cut == 0 || lt < cut {
					cut = lt
				}
			}
		}
		if cut == 0 {
			return nil // nothing to compare against; let the daemon decide
		}
	}
	quoted := r.Start.LT
	if r.Span() && r.End.LT > quoted {
		quoted = r.End.LT
	}
	if quoted >= cut {
		return fmt.Errorf("the region you quoted (lt %d) is at or after the cut (lt %d), so the branch would not contain it", quoted, cut)
	}
	return nil
}
