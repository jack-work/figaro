package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/rpc"
)

type forkCallResult struct {
	response *rpc.ForkResponse
	err      error
}

func waitForFork(
	ctx context.Context,
	client *angelus.Client,
	ariaID string,
	atMainLT uint64,
) (*rpc.ForkResponse, error) {
	done := make(chan forkCallResult, 1)
	go func() {
		response, err := client.Fork(ctx, ariaID, atMainLT)
		done <- forkCallResult{response: response, err: err}
	}()

	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case result := <-done:
		return result.response, result.err
	case <-timer.C:
		fmt.Fprintf(os.Stderr, "forking %s; waiting for a safe actor/storage boundary...\n", ariaID)
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
