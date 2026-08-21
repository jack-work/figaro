package cli

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/turns"
)

// ariaMessages reads an aria's whole log and returns it as messages with their
// logical times attached. The LT lives on the store entry, not in the payload
// (it is the frame index, populated on read), so it has to be stitched back on
// here: every caller that forgets produces messages that silently claim LT 0.
func ariaMessages(ctx context.Context, acli *angelus.Client, ariaID string) ([]message.Message, error) {
	resp, err := acli.IR(ctx, ariaID, 0, 0)
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
func resolveTurn(ctx context.Context, acli *angelus.Client, ariaID string, turn uint64) (uint64, error) {
	msgs, err := ariaMessages(ctx, acli, ariaID)
	if err != nil {
		return 0, err
	}
	first, _, ok := turns.Span(msgs, turn)
	if !ok {
		last := turns.StampIDs(msgs)
		if last == 0 {
			return 0, fmt.Errorf("aria %s has no turns yet", ariaID)
		}
		return 0, fmt.Errorf("aria %s has no turn %d (it has turns 1..%d)", ariaID, turn, last)
	}
	return first - 1, nil
}
