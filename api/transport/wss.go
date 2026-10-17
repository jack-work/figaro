package transport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
)

const (
	wssDialTimeout = 15 * time.Second
	wssReadLimit   = 64 << 20
)

func dialWSS(ep Endpoint) (net.Conn, error) {
	if !strings.HasPrefix(ep.Address, "wss://") {
		return nil, fmt.Errorf("wss endpoint %q is not a wss:// url", ep.Address)
	}
	c, err := upgrade(ep, false)
	if err != nil {
		return nil, err
	}
	c.SetReadLimit(wssReadLimit)
	return websocket.NetConn(context.Background(), c, websocket.MessageText), nil
}

func upgrade(ep Endpoint, retried bool) (*websocket.Conn, error) {
	opts := &websocket.DialOptions{HTTPClient: ep.HTTPClient}
	token := ""
	if ep.Bearer != nil {
		var err error
		if token, err = ep.Bearer.Resolve(); err != nil {
			return nil, fmt.Errorf("%s: credential: %w", ep.Address, err)
		}
		opts.HTTPHeader = http.Header{"Authorization": {"Bearer " + token}}
	}
	ctx, cancel := context.WithTimeout(context.Background(), wssDialTimeout)
	defer cancel()
	c, resp, err := websocket.Dial(ctx, ep.Address, opts)
	if err == nil {
		return c, nil
	}
	if resp != nil && resp.StatusCode == http.StatusUnauthorized && ep.Bearer != nil && !retried {
		if ierr := ep.Bearer.Invalidate(token); ierr != nil {
			return nil, errors.Join(fmt.Errorf("%s: refused the credential (401)", ep.Address), ierr)
		}
		return upgrade(ep, true)
	}
	if resp != nil {
		return nil, fmt.Errorf("%s: upgrade refused: HTTP %d", ep.Address, resp.StatusCode)
	}
	return nil, fmt.Errorf("%s: %w", ep.Address, err)
}
