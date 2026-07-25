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
// This is the only place the CLI performs that translation. compose.TurnSpan
// owns the rule; this function owns fetching the log to feed it. Turn ids are
// what `figaro show` prints and what `<aria>:<n>` means everywhere.
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
	return first, nil
}
