package peer

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/jkrpc"
)

type Unreachable struct {
	Node string
	Err  error
}

func (e *Unreachable) Error() string { return fmt.Sprintf("node %s is unreachable: %v", e.Node, e.Err) }

func (e *Unreachable) Unwrap() error { return e.Err }

type Refused struct {
	Node   string
	Reason string
}

func (e *Refused) Error() string { return fmt.Sprintf("node %s refused: %s", e.Node, e.Reason) }

type Dialer struct {
	Members *Members
	Timeout time.Duration
}

const defaultDialTimeout = 10 * time.Second

type Link struct {
	node string
	cli  *jkrpc.Client
}

func (d *Dialer) Open(ctx context.Context, node string, p rpc.Passage, onNotify jkrpc.NotifyFunc) (*Link, error) {
	m, ok := d.Members.Member(node)
	if !ok {
		return nil, fmt.Errorf("node %s is not a peer of this node", node)
	}
	wait := d.Timeout
	if wait <= 0 {
		wait = defaultDialTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	conn, err := (&tls.Dialer{Config: m.tls}).DialContext(ctx, "tcp", m.Address)
	if err != nil {
		return nil, &Unreachable{Node: node, Err: err}
	}
	if err := passage(ctx, conn, p); err != nil {
		conn.Close()
		var refused *Refused
		if errors.As(err, &refused) {
			refused.Node = node
			return nil, refused
		}
		return nil, &Unreachable{Node: node, Err: err}
	}
	return &Link{node: node, cli: jkrpc.NewClient(jkrpc.NewConn(conn), onNotify)}, nil
}

func passage(ctx context.Context, conn net.Conn, p rpc.Passage) error {
	if dl, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(dl); err != nil {
			return err
		}
	}
	if err := writeLine(conn, p); err != nil {
		return err
	}
	var a rpc.PassageAnswer
	if err := readLine(conn, &a); err != nil {
		return fmt.Errorf("passage: %w", err)
	}
	if a.Error != "" {
		return &Refused{Reason: a.Error}
	}
	return conn.SetDeadline(time.Time{})
}

func (l *Link) Node() string { return l.node }

func (l *Link) Call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	var out json.RawMessage
	err := l.cli.Call(ctx, method, params, &out)
	if err == nil {
		return out, nil
	}
	var answered *jkrpc.Error
	if errors.As(err, &answered) {
		return nil, answered
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return nil, &Unreachable{Node: l.node, Err: err}
}

func (l *Link) Done() <-chan struct{} { return l.cli.Done() }

func (l *Link) Close() error { return l.cli.Close() }

func (l *Link) Resolve(ctx context.Context, refs []string) (map[string]string, error) {
	params, err := json.Marshal(rpc.NodeResolveRequest{Refs: refs})
	if err != nil {
		return nil, err
	}
	raw, err := l.Call(ctx, rpc.MethodNodeResolve, params)
	if err != nil {
		return nil, err
	}
	var resp rpc.NodeResolveResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("node %s: %s: %w", l.node, rpc.MethodNodeResolve, err)
	}
	return resp.IDs, nil
}
