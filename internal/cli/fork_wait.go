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

// waitForFork is THE ONE DOOR every fork goes through, which is why the node
// coordinate is resolved here rather than at each of the three call sites: a
// translation armed on two doors out of three is armed on neither, and this
// program has paid that bill before (see command.go's setCommandRunner).
//
// The resolution costs one read of the addressed turn, and only when a node
// was named; `:19`, `.326` and the head go straight to the wire as typed.
func waitForFork(
	ctx context.Context,
	client *sdk.Angelus,
	ariaID string,
	at forkPoint,
	d dressing,
) (*rpc.ForkResponse, error) {
	wireAt, note, err := resolveForkPoint(ctx, client, ariaID, at)
	if err != nil {
		return nil, err
	}
	if note != "" {
		fmt.Fprintf(stderrw, "%s\n", note)
	}
	done := make(chan forkCallResult, 1)
	go func() {
		response, err := client.Fork(ctx, ariaID, wireAt.turn, wireAt.lt, d.names, d.patch)
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
