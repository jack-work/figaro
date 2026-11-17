package cli

import (
	"context"
	"github.com/jack-work/figaro/sdk"
	"time"

	"github.com/jack-work/figaro/api/rpc"
)

type forkCallResult struct {
	response *rpc.ForkResponse
	err      error
}

// waitForFork is the one door every fork goes through, so the node coordinate
// is resolved here; `:19`, `.326` and the head go to the wire as typed.
func waitForFork(
	ctx context.Context,
	client *sdk.Angelus,
	ariaID string,
	at forkPoint,
	d dressing,
	studies rpc.ForkStudies,
	prompt string,
) (*rpc.ForkResponse, error) {
	wireAt, note, err := resolveForkPoint(ctx, client, ariaID, at)
	if err != nil {
		return nil, err
	}
	if prompt != "" {
		if err := forkQuotePreflight(ctx, client, ariaID, wireAt, prompt); err != nil {
			return nil, err
		}
	}
	if note != "" {
		report(ctx, "%s", note)
	}
	req := rpc.ForkRequest{FigaroID: ariaID, AtTurn: wireAt.turn, AtLT: wireAt.lt, Outfits: d.names, Patch: d.patch, Studies: studies}
	if err := refuseUnknownFields(req); err != nil {
		return nil, err
	}
	done := make(chan forkCallResult, 1)
	go func() {
		response, err := client.ForkWith(ctx, req)
		done <- forkCallResult{response: response, err: err}
	}()

	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case result := <-done:
		return result.response, result.err
	case <-timer.C:
		report(ctx, "forking %s; waiting for a safe actor/storage boundary...", ariaID)
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	select {
	case result := <-done:
		return result.response, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
