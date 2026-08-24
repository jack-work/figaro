package cli

import (
	"context"
	"fmt"
	"github.com/jack-work/figaro/sdk"
	"time"

	"github.com/jack-work/figaro/api/rpc"
)

type forkCallResult struct {
	response *rpc.ForkResponse
	err      error
}

func waitForFork(
	ctx context.Context,
	client *sdk.Angelus,
	ariaID string,
	at forkPoint,
	d dressing,
) (*rpc.ForkResponse, error) {
	done := make(chan forkCallResult, 1)
	go func() {
		response, err := client.Fork(ctx, ariaID, at.turn, at.lt, d.names, d.patch)
		done <- forkCallResult{response: response, err: err}
	}()

	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case result := <-done:
		return result.response, result.err
	case <-timer.C:
		fmt.Fprintf(stderrw, "forking %s; waiting for a safe actor/storage boundary...\n", ariaID)
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
