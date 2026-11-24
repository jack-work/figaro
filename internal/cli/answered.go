package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/figwal/xwal"
	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/sdk"
	"github.com/jack-work/jkrpc"
)

var (
	commandTimeout = 10 * time.Second
	answerBound    = 60 * time.Second
)

var errNotListening = errors.New("nothing is listening")

func commandContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), commandTimeout)
}

func dialAnswered(ep transport.Endpoint) (*sdk.Angelus, *rpc.StatusResponse, error) {
	cli, err := sdk.DialAngelus(ep)
	if err != nil {
		return nil, nil, fmt.Errorf("%w on %s: %v", errNotListening, ep.Address, err)
	}
	st, err := awaitAnswer(cli, storeBusy)
	var werr *jkrpc.Error
	switch {
	case err == nil:
		return cli, st, nil
	case errors.As(err, &werr):
		return cli, nil, nil
	}
	cli.Close()
	if errors.Is(err, context.DeadlineExceeded) {
		return nil, nil, fmt.Errorf("the angelus on %s took the connection but has not answered in %s%s",
			ep.Address, answerBound, startupDiagnosis())
	}
	return nil, nil, fmt.Errorf("the angelus on %s hung up before answering: %v%s", ep.Address, err, startupDiagnosis())
}

func awaitAnswer(cli *sdk.Angelus, busy func() bool) (*rpc.StatusResponse, error) {
	start := time.Now()
	for {
		ctx, cancel := context.WithTimeout(context.Background(), answerBound)
		st, err := cli.Status(ctx)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) || !busy() || time.Since(start) > startupHardCap {
			return st, err
		}
	}
}

func storeBusy() bool {
	if need, err := xwal.NeedsFlatten(boot.AriaRoot()); err == nil && need {
		return true
	}
	_, _, held := boot.StoreLockHolder()
	return held
}
