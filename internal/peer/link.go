package peer

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
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
	Left   bool
}

func (e *Refused) Error() string { return fmt.Sprintf("node %s refused: %s", e.Node, e.Reason) }

type Dialer struct {
	Members  Membership
	Versions *Versions
	Timeout  time.Duration

	mu    sync.Mutex
	links map[string]map[*Link]struct{}
}

func (d *Dialer) Evict(node string) {
	d.mu.Lock()
	links := d.links[node]
	delete(d.links, node)
	d.mu.Unlock()
	for l := range links {
		l.cli.Close()
	}
}

func (d *Dialer) track(l *Link) {
	d.mu.Lock()
	if d.links == nil {
		d.links = map[string]map[*Link]struct{}{}
	}
	if d.links[l.node] == nil {
		d.links[l.node] = map[*Link]struct{}{}
	}
	d.links[l.node][l] = struct{}{}
	d.mu.Unlock()
}

func (d *Dialer) untrack(l *Link) {
	d.mu.Lock()
	delete(d.links[l.node], l)
	if len(d.links[l.node]) == 0 {
		delete(d.links, l.node)
	}
	d.mu.Unlock()
}

const defaultDialTimeout = 10 * time.Second

type Link struct {
	node   string
	cli    *jkrpc.Client
	dialer *Dialer
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
	p.Version, p.Policy = d.Members.Version(), d.Members.PolicyVersion()
	theirs, err := passage(ctx, conn, p)
	if err != nil {
		conn.Close()
		var refused *Refused
		if errors.As(err, &refused) {
			refused.Node = node
			return nil, refused
		}
		return nil, &Unreachable{Node: node, Err: err}
	}
	d.Versions.Saw(node, theirs.Version, p.Version, theirs.Policy)
	l := &Link{node: node, cli: jkrpc.NewClient(jkrpc.NewConn(conn), onNotify), dialer: d}
	d.track(l)
	return l, nil
}

func passage(ctx context.Context, conn net.Conn, p rpc.Passage) (rpc.PassageAnswer, error) {
	if dl, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(dl); err != nil {
			return rpc.PassageAnswer{}, err
		}
	}
	if err := writeLine(conn, p); err != nil {
		return rpc.PassageAnswer{}, err
	}
	var a rpc.PassageAnswer
	if err := readLine(conn, &a); err != nil {
		return rpc.PassageAnswer{}, fmt.Errorf("passage: %w", err)
	}
	if a.Error != "" {
		return a, &Refused{Reason: a.Error, Left: a.Left}
	}
	return a, conn.SetDeadline(time.Time{})
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

func (l *Link) Close() error {
	l.dialer.untrack(l)
	return l.cli.Close()
}

func (l *Link) Resolve(ctx context.Context, refs []string) (rpc.NodeResolveResponse, error) {
	params, err := json.Marshal(rpc.NodeResolveRequest{Refs: refs})
	if err != nil {
		return rpc.NodeResolveResponse{}, err
	}
	raw, err := l.Call(ctx, rpc.MethodNodeResolve, params)
	if err != nil {
		return rpc.NodeResolveResponse{}, err
	}
	var resp rpc.NodeResolveResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return rpc.NodeResolveResponse{}, fmt.Errorf("node %s: %s: %w", l.node, rpc.MethodNodeResolve, err)
	}
	return resp, nil
}

func (l *Link) TroupeEntries(ctx context.Context, req rpc.TroupeEntriesRequest) (rpc.TroupeEntriesResponse, error) {
	params, err := json.Marshal(req)
	if err != nil {
		return rpc.TroupeEntriesResponse{}, err
	}
	raw, err := l.Call(ctx, rpc.MethodTroupeEntries, params)
	if err != nil {
		return rpc.TroupeEntriesResponse{}, err
	}
	var resp rpc.TroupeEntriesResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return rpc.TroupeEntriesResponse{}, fmt.Errorf("node %s: %s: %w", l.node, rpc.MethodTroupeEntries, err)
	}
	return resp, nil
}

func (l *Link) TroupeSign(ctx context.Context, req rpc.TroupeSignRequest) (rpc.TroupeSignResponse, error) {
	params, err := json.Marshal(req)
	if err != nil {
		return rpc.TroupeSignResponse{}, err
	}
	raw, err := l.Call(ctx, rpc.MethodTroupeSign, params)
	if err != nil {
		return rpc.TroupeSignResponse{}, err
	}
	var resp rpc.TroupeSignResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return rpc.TroupeSignResponse{}, fmt.Errorf("node %s: %s: %w", l.node, rpc.MethodTroupeSign, err)
	}
	return resp, nil
}
