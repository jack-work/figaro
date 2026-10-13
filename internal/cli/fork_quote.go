package cli

// A quote in a fork prompt is resolved against the child, which holds the
// parent's entries up to the cut; the client checks first so the branch is not
// left empty by a refused prompt.
import (
	"context"
	"errors"
	"fmt"

	"github.com/jack-work/figaro/api/quote"
	"github.com/jack-work/figaro/sdk"
)

// wireAt is the point as the wire takes it (after resolveForkPoint). A turn cut
// is checked against that turn's first node; a quote of the turn's own question
// slips past and is refused by the daemon.
func forkQuotePreflight(ctx context.Context, acli *sdk.Angelus, ariaID string, wireAt forkPoint, prompt string) error {
	r, _, err := quote.Parse(prompt)
	if errors.Is(err, quote.ErrNone) {
		return nil
	}
	if err != nil {
		return err
	}
	if wireAt.isHead() {
		return nil
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
			return nil
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
