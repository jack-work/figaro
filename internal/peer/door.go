package peer

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
)

type Verify func(ctx context.Context, bearer string) (context.Context, error)

type Door struct {
	Members   Membership
	Versions  *Versions
	Verify    Verify
	Handshake time.Duration

	mu   sync.Mutex
	open map[string]map[*context.CancelFunc]struct{}
}

const evictGrace = time.Second

func (d *Door) Evict(node string) {
	d.mu.Lock()
	conns := d.open[node]
	delete(d.open, node)
	d.mu.Unlock()
	if len(conns) == 0 {
		return
	}
	time.AfterFunc(evictGrace, func() {
		for cancel := range conns {
			(*cancel)()
		}
	})
}

func (d *Door) track(ctx context.Context, node string) context.Context {
	ctx, cancel := context.WithCancel(ctx)
	key := &cancel
	d.mu.Lock()
	if d.open == nil {
		d.open = map[string]map[*context.CancelFunc]struct{}{}
	}
	if d.open[node] == nil {
		d.open[node] = map[*context.CancelFunc]struct{}{}
	}
	d.open[node][key] = struct{}{}
	d.mu.Unlock()
	context.AfterFunc(ctx, func() {
		d.mu.Lock()
		delete(d.open[node], key)
		if len(d.open[node]) == 0 {
			delete(d.open, node)
		}
		d.mu.Unlock()
	})
	return ctx
}

const defaultHandshake = 10 * time.Second

type viaKey struct{}

func Via(ctx context.Context) (string, bool) {
	node, ok := ctx.Value(viaKey{}).(string)
	return node, ok
}

func (d *Door) Admit(ctx context.Context, conn net.Conn) (context.Context, error) {
	tc, ok := conn.(*tls.Conn)
	if !ok {
		return nil, fmt.Errorf("node door: %s did not arrive over TLS", conn.RemoteAddr())
	}
	wait := d.Handshake
	if wait <= 0 {
		wait = defaultHandshake
	}
	deadline := time.Now().Add(wait)
	if err := tc.SetDeadline(deadline); err != nil {
		return nil, err
	}
	hctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	if err := tc.HandshakeContext(hctx); err != nil {
		return nil, fmt.Errorf("node door: handshake with %s: %w", conn.RemoteAddr(), err)
	}
	ctx, err := d.enter(ctx, tc)
	if aerr := answer(tc, d.Members.Version(), err); aerr != nil && err == nil {
		err = aerr
	}
	if err != nil {
		return nil, fmt.Errorf("node door: %s: %w", conn.RemoteAddr(), err)
	}
	node, _ := Via(ctx)
	return d.track(ctx, node), tc.SetDeadline(time.Time{})
}

func (d *Door) enter(ctx context.Context, tc *tls.Conn) (context.Context, error) {
	node, ok := d.Members.NodeOf(tc.ConnectionState().PeerCertificates[0])
	if !ok {
		return nil, errors.New("the certificate names no peer of this node")
	}
	var p rpc.Passage
	if err := readLine(tc, &p); err != nil {
		return nil, fmt.Errorf("node %s: passage: %w", node, err)
	}
	d.Versions.Saw(node, p.Version, d.Members.Version())
	ctx = authz.WithPrincipal(ctx, rpc.Principal{Kind: rpc.PrincipalNode, ID: node})
	ctx = context.WithValue(ctx, viaKey{}, node)
	switch {
	case p.Forwarded && p.Bearer == "":
		return nil, fmt.Errorf("node %s forwarded a caller without its bearer", node)
	case !p.Forwarded && p.Bearer != "":
		return nil, fmt.Errorf("node %s sent a bearer for no forwarded caller", node)
	case !p.Forwarded:
		return ctx, nil
	case d.Verify == nil:
		return nil, errors.New("this node cannot verify a forwarded caller")
	}
	ctx, err := d.Verify(ctx, p.Bearer)
	if err != nil {
		slog.Warn("node door: forwarded caller refused", "node", node, "err", err)
		return nil, fmt.Errorf("forwarded caller: %w", err)
	}
	return ctx, nil
}

func answer(w io.Writer, version uint64, refused error) error {
	a := rpc.PassageAnswer{Version: version}
	if refused != nil {
		a.Error = refused.Error()
	}
	return writeLine(w, a)
}

func writeLine(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

func readLine(r io.Reader, v any) error {
	buf := make([]byte, 0, 256)
	one := make([]byte, 1)
	for len(buf) < rpc.MaxPassage {
		if _, err := io.ReadFull(r, one); err != nil {
			return err
		}
		if one[0] == '\n' {
			return json.Unmarshal(buf, v)
		}
		buf = append(buf, one[0])
	}
	return fmt.Errorf("longer than %d bytes", rpc.MaxPassage)
}
