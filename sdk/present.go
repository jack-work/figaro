package sdk

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/jkrpc"
)

const (
	presentAt       = 0.8
	presentRetryMax = 30 * time.Second
	presentTimeout  = 10 * time.Second
)

func keepPresented(cli *jkrpc.Client, b transport.Bearer) {
	token, err := b.Resolve()
	if err != nil {
		return
	}
	for {
		exp, ok := tokenExpiry(token)
		if !ok {
			return
		}
		if !sleepOrClosed(cli, time.Duration(float64(time.Until(exp))*presentAt)) {
			return
		}
		fresh, err := renewed(b, token)
		if err == nil {
			err = present(cli, fresh)
			if err == nil {
				token = fresh
				continue
			}
			if rpcRefused(err) {
				return
			}
		}
		left := time.Until(exp)
		if left <= 0 {
			return
		}
		if !sleepOrClosed(cli, min(left/2, presentRetryMax)) {
			return
		}
	}
}

func renewed(b transport.Bearer, token string) (string, error) {
	if err := b.Invalidate(token); err != nil {
		return "", err
	}
	fresh, err := b.Resolve()
	if err != nil {
		return "", err
	}
	if fresh == token {
		return "", errNoFreshToken
	}
	return fresh, nil
}

var errNoFreshToken = errors.New("the credential source returned the token it had")

const jsonRPCMethodNotFound = -32601

func present(cli *jkrpc.Client, token string) error {
	ctx, cancel := context.WithTimeout(context.Background(), presentTimeout)
	defer cancel()
	var res rpc.PresentResponse
	return cli.Call(ctx, rpc.MethodEndpointPresent, rpc.PresentRequest{Token: token}, &res)
}

func rpcRefused(err error) bool {
	var e *jkrpc.Error
	return errors.As(err, &e) && e.Code == jsonRPCMethodNotFound
}

func sleepOrClosed(cli *jkrpc.Client, d time.Duration) bool {
	t := time.NewTimer(max(d, 0))
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-cli.Done():
		return false
	}
}

func tokenExpiry(token string) (time.Time, bool) {
	_, rest, ok := strings.Cut(token, ".")
	if !ok {
		return time.Time{}, false
	}
	payload, _, ok := strings.Cut(rest, ".")
	if !ok {
		return time.Time{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return time.Time{}, false
	}
	var c struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(raw, &c) != nil || c.Exp == 0 {
		return time.Time{}, false
	}
	return time.Unix(c.Exp, 0), true
}
