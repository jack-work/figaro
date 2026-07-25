package cli

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/compose"
	"github.com/jack-work/figaro/internal/message"
)

// ariaMessages reads an aria's whole log and returns it as messages with their
// logical times attached. The LT lives on the store entry, not in the payload
// (it is the frame index, populated on read), so it has to be stitched back on
// here — every caller that forgets produces messages that silently claim LT 0.
func ariaMessages(ctx context.Context, acli *angelus.Client, ariaID string) ([]message.Message, error) {
	resp, err := acli.AriaRead(ctx, ariaID, 0, 0)
	if err != nil {
		return nil, err
	}
	msgs := make([]message.Message, len(resp.Entries))
	for i, e := range resp.Entries {
		if err := json.Unmarshal(e.Payload, &msgs[i]); err != nil {
			return nil, fmt.Errorf("parse LT=%d: %w", e.LT, err)
		}
		msgs[i].LogicalTime = e.LT
	}
	return msgs, nil
}

// resolveTurn turns a user-facing turn id into the atMainLT a fork takes.
//
// MEASURED, not assumed: figwal's doc reads "atIdx must be in (FirstIndex,
// LastIndex+1]", which sounds like the prefix is [First,atMainLT). It is not.
// Forking an aria at atMainLT=5 produced a branch that still contained LT 5, so
// the retained prefix is [First, atMainLT] — INCLUSIVE — and the branch begins
// at atMainLT+1.
//
// So the coordinate that lets you REPLACE turn N is the LT just before its
// prompt: the branch then retains everything through the end of turn N-1, and
// your new prompt becomes the new turn N. That boundary is the tail of a
// completed exchange, so no tool_invoke is ever left without its result and
// interrupted-tool synthesis is unreachable for a user-initiated fork.
//
// compose.TurnSpan reports the honest span; the -1 is fork policy and lives
// here, in one place, shared by send, fork and attend.
func resolveTurn(ctx context.Context, acli *angelus.Client, ariaID string, turn uint64) (uint64, error) {
	msgs, err := ariaMessages(ctx, acli, ariaID)
	if err != nil {
		return 0, err
	}
	first, _, ok := compose.TurnSpan(msgs, turn)
	if !ok {
		last := compose.StampTurnIDs(msgs)
		if last == 0 {
			return 0, fmt.Errorf("aria %s has no turns yet", ariaID)
		}
		return 0, fmt.Errorf("aria %s has no turn %d (it has turns 1..%d)", ariaID, turn, last)
	}
	return first - 1, nil
}
