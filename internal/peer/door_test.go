package peer_test

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/peer"
	"github.com/jack-work/figaro/internal/peer/peertest"
	"github.com/jack-work/jkrpc"
)

type callerKey struct{}

type seen struct {
	Principal rpc.Principal `json:"principal"`
	Via       string        `json:"via"`
	Caller    string        `json:"caller"`
}

// node b serves a door that admits node a; it answers whoami, echoes,
// pushes on watch, and owns the ids it is given.
type served struct {
	addr     string
	admitted chan error
	ln       net.Listener
}

func serve(t *testing.T, cfg config.TLSDoor, d *peer.Door, owned ...string) *served {
	t.Helper()
	tlsCfg, err := peer.ServerTLS(cfg, nil)
	require.NoError(t, err)
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ln := tls.NewListener(raw, tlsCfg)
	t.Cleanup(func() { ln.Close() })
	s := &served{addr: raw.Addr().String(), admitted: make(chan error, 16), ln: ln}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				ctx, err := d.Admit(context.Background(), conn)
				s.admitted <- err
				if err != nil {
					conn.Close()
					return
				}
				var srv *jkrpc.Server
				srv = jkrpc.NewServer(jkrpc.NewConn(conn), map[string]jkrpc.HandlerFunc{
					"whoami": func(ctx context.Context, _ json.RawMessage) (any, error) {
						via, _ := peer.Via(ctx)
						caller, _ := ctx.Value(callerKey{}).(string)
						return seen{Principal: authz.PrincipalOf(ctx), Via: via, Caller: caller}, nil
					},
					"echo": func(_ context.Context, p json.RawMessage) (any, error) { return p, nil },
					"fail": func(context.Context, json.RawMessage) (any, error) {
						return nil, &jkrpc.Error{Code: -32020, Message: "denied by the owner"}
					},
					"watch": func(_ context.Context, p json.RawMessage) (any, error) {
						go srv.Notify("figaro.aria", p)
						return map[string]bool{"ok": true}, nil
					},
					"hang": func(context.Context, json.RawMessage) (any, error) {
						conn.Close()
						return nil, nil
					},
					rpc.MethodNodeResolve: func(_ context.Context, p json.RawMessage) (any, error) {
						var req rpc.NodeResolveRequest
						if err := json.Unmarshal(p, &req); err != nil {
							return nil, err
						}
						out := rpc.NodeResolveResponse{IDs: map[string]string{}}
						for _, ref := range req.Refs {
							for _, o := range owned {
								if strings.HasPrefix(o, ref) {
									out.IDs[ref] = o
								}
							}
						}
						return out, nil
					},
				})
				srv.Serve(ctx)
			}()
		}
	}()
	return s
}

func members(t *testing.T, peers ...config.Peer) *peer.Static {
	t.Helper()
	m, err := peer.NewStatic(0, peers, nil)
	require.NoError(t, err)
	return m
}

func verifyAs(want string) peer.Verify {
	return func(ctx context.Context, bearer string) (context.Context, error) {
		if bearer != want {
			return nil, errors.New("token rejected")
		}
		return context.WithValue(ctx, callerKey{}, "user:gluck"), nil
	}
}

func whoami(t *testing.T, l *peer.Link) seen {
	t.Helper()
	raw, err := l.Call(context.Background(), "whoami", nil)
	require.NoError(t, err)
	var s seen
	require.NoError(t, json.Unmarshal(raw, &s))
	return s
}

// A configured peer is admitted as node:<id>, and the connection remembers
// which node carried it.
func TestDoorStampsTheNode(t *testing.T) {
	p := peertest.NewPKI(t, "fed")
	b := serve(t, p.Door("b", ""), &peer.Door{Members: members(t, p.Peer("b", "a", "127.0.0.1:1"))})
	a := &peer.Dialer{Members: members(t, p.Peer("a", "b", b.addr))}

	l, err := a.Open(context.Background(), "b", rpc.Passage{}, nil)
	require.NoError(t, err)
	defer l.Close()
	got := whoami(t, l)
	require.Equal(t, rpc.Principal{Kind: rpc.PrincipalNode, ID: "a"}, got.Principal)
	require.Equal(t, "a", got.Via)
	require.Empty(t, got.Caller)
}

// A forwarded caller's bearer reaches the owner's verifier, and the owner
// serves the call as that caller, with the carrying node kept beside it.
func TestForwardedCallerIsVerifiedByTheOwner(t *testing.T) {
	p := peertest.NewPKI(t, "fed")
	b := serve(t, p.Door("b", ""), &peer.Door{Members: members(t, p.Peer("b", "a", "127.0.0.1:1")), Verify: verifyAs("tok")})
	a := &peer.Dialer{Members: members(t, p.Peer("a", "b", b.addr))}

	l, err := a.Open(context.Background(), "b", rpc.Passage{Forwarded: true, Bearer: "tok"}, nil)
	require.NoError(t, err)
	defer l.Close()
	got := whoami(t, l)
	require.Equal(t, "user:gluck", got.Caller)
	require.Equal(t, "a", got.Via)

	_, err = a.Open(context.Background(), "b", rpc.Passage{Forwarded: true, Bearer: "forged"}, nil)
	var refused *peer.Refused
	require.ErrorAs(t, err, &refused)
	require.Equal(t, "b", refused.Node)
	require.Contains(t, err.Error(), "token rejected")
}

// Nodes never vouch: until the owner can verify a forwarded token, a
// forwarded caller is refused, not served as the node.
func TestForwardedCallerIsRefusedWithoutAVerifier(t *testing.T) {
	p := peertest.NewPKI(t, "fed")
	b := serve(t, p.Door("b", ""), &peer.Door{Members: members(t, p.Peer("b", "a", "127.0.0.1:1"))})
	a := &peer.Dialer{Members: members(t, p.Peer("a", "b", b.addr))}

	_, err := a.Open(context.Background(), "b", rpc.Passage{Forwarded: true, Bearer: "tok"}, nil)
	var refused *peer.Refused
	require.ErrorAs(t, err, &refused)
	require.Contains(t, err.Error(), "node b refused")
	require.Contains(t, err.Error(), "cannot verify a forwarded caller")
}

// The forwarded flag and the bearer travel together or not at all.
func TestPassageMustBeWhole(t *testing.T) {
	p := peertest.NewPKI(t, "fed")
	b := serve(t, p.Door("b", ""), &peer.Door{Members: members(t, p.Peer("b", "a", "127.0.0.1:1")), Verify: verifyAs("tok")})
	a := &peer.Dialer{Members: members(t, p.Peer("a", "b", b.addr))}
	for _, pass := range []rpc.Passage{{Forwarded: true}, {Bearer: "tok"}} {
		_, err := a.Open(context.Background(), "b", pass, nil)
		var refused *peer.Refused
		require.ErrorAs(t, err, &refused, "%+v", pass)
	}
}

// A certificate from the federation CA that names no configured peer is
// refused with a reason; one from another CA never completes the handshake.
func TestDoorRefusesStrangers(t *testing.T) {
	p := peertest.NewPKI(t, "fed")
	b := serve(t, p.Door("b", ""), &peer.Door{Members: members(t, p.Peer("b", "a", "127.0.0.1:1"))})

	stranger := &peer.Dialer{Members: members(t, p.Peer("c", "b", b.addr))}
	_, err := stranger.Open(context.Background(), "b", rpc.Passage{}, nil)
	var refused *peer.Refused
	require.ErrorAs(t, err, &refused)
	require.Contains(t, err.Error(), "names no peer")
	require.ErrorContains(t, <-b.admitted, "names no peer")

	rogue := peertest.NewPKI(t, "rogue")
	cfg := rogue.Peer("a", "b", b.addr)
	cfg.CA = p.CA
	impostor := &peer.Dialer{Members: members(t, cfg)}
	_, err = impostor.Open(context.Background(), "b", rpc.Passage{}, nil)
	var unreachable *peer.Unreachable
	require.ErrorAs(t, err, &unreachable)
	require.ErrorContains(t, <-b.admitted, "handshake")
}

// The dialler checks the owner's identity, not a hostname: a door holding
// another node's certificate is not the peer.
func TestDialerChecksThePeersIdentity(t *testing.T) {
	p := peertest.NewPKI(t, "fed")
	b := serve(t, p.Door("c", ""), &peer.Door{Members: members(t, p.Peer("b", "a", "127.0.0.1:1"))})
	a := &peer.Dialer{Members: members(t, p.Peer("a", "b", b.addr))}
	_, err := a.Open(context.Background(), "b", rpc.Passage{}, nil)
	var unreachable *peer.Unreachable
	require.ErrorAs(t, err, &unreachable)
	require.Contains(t, err.Error(), peertest.Identity("b"))
}

// An unreachable owner is an error naming it, whether the door is down or
// dies during a call; an owner's own refusal passes through untouched.
func TestUnreachableNamesTheNode(t *testing.T) {
	p := peertest.NewPKI(t, "fed")
	a := &peer.Dialer{Members: members(t, p.Peer("a", "b", "127.0.0.1:1"))}
	_, err := a.Open(context.Background(), "b", rpc.Passage{}, nil)
	var unreachable *peer.Unreachable
	require.ErrorAs(t, err, &unreachable)
	require.Equal(t, "b", unreachable.Node)
	require.True(t, strings.HasPrefix(err.Error(), "node b is unreachable"), err.Error())

	_, err = a.Open(context.Background(), "nowhere", rpc.Passage{}, nil)
	require.ErrorContains(t, err, "node nowhere is not a peer")

	b := serve(t, p.Door("b", ""), &peer.Door{Members: members(t, p.Peer("b", "a", "127.0.0.1:1"))})
	a = &peer.Dialer{Members: members(t, p.Peer("a", "b", b.addr))}
	l, err := a.Open(context.Background(), "b", rpc.Passage{}, nil)
	require.NoError(t, err)
	defer l.Close()

	_, err = l.Call(context.Background(), "fail", nil)
	var answered *jkrpc.Error
	require.ErrorAs(t, err, &answered)
	require.Equal(t, "denied by the owner", answered.Message)

	_, err = l.Call(context.Background(), "hang", nil)
	require.ErrorAs(t, err, &unreachable)
	require.Equal(t, "b", unreachable.Node)
}

// A peer that accepts TCP and never finishes the handshake is unreachable
// within the dial timeout, and a door gives up on such a dialler too.
func TestHandshakesAreBounded(t *testing.T) {
	p := peertest.NewPKI(t, "fed")
	silent, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer silent.Close()
	a := &peer.Dialer{Members: members(t, p.Peer("a", "b", silent.Addr().String())), Timeout: 200 * time.Millisecond}
	start := time.Now()
	_, err = a.Open(context.Background(), "b", rpc.Passage{}, nil)
	var unreachable *peer.Unreachable
	require.ErrorAs(t, err, &unreachable)
	require.Less(t, time.Since(start), 5*time.Second)

	b := serve(t, p.Door("b", ""), &peer.Door{Members: members(t, p.Peer("b", "a", "127.0.0.1:1")), Handshake: 200 * time.Millisecond})
	mute, err := net.Dial("tcp", b.addr)
	require.NoError(t, err)
	defer mute.Close()
	select {
	case err := <-b.admitted:
		require.ErrorContains(t, err, "handshake")
	case <-time.After(5 * time.Second):
		t.Fatal("the door waited on a silent dialler")
	}
}

// A listen stream rides the same link: the owner's pushes reach the
// entry node's notify handler in order.
func TestLinkCarriesPushes(t *testing.T) {
	p := peertest.NewPKI(t, "fed")
	b := serve(t, p.Door("b", ""), &peer.Door{Members: members(t, p.Peer("b", "a", "127.0.0.1:1"))})
	a := &peer.Dialer{Members: members(t, p.Peer("a", "b", b.addr))}
	pushes := make(chan string, 4)
	l, err := a.Open(context.Background(), "b", rpc.Passage{}, func(method string, params json.RawMessage) {
		pushes <- method + " " + string(params)
	})
	require.NoError(t, err)
	defer l.Close()
	for i := range 3 {
		_, err := l.Call(context.Background(), "watch", json.RawMessage(`{"n":`+string(rune('0'+i))+`}`))
		require.NoError(t, err)
		select {
		case got := <-pushes:
			require.Equal(t, `figaro.aria {"n":`+string(rune('0'+i))+`}`, got)
		case <-time.After(5 * time.Second):
			t.Fatal("no push")
		}
	}
}

// node.resolve answers, for the refs the owner holds, the ids they name.
func TestResolve(t *testing.T) {
	p := peertest.NewPKI(t, "fed")
	b := serve(t, p.Door("b", ""), &peer.Door{Members: members(t, p.Peer("b", "a", "127.0.0.1:1"))}, "x1aaaa", "x2bbbb")
	a := &peer.Dialer{Members: members(t, p.Peer("a", "b", b.addr))}
	l, err := a.Open(context.Background(), "b", rpc.Passage{}, nil)
	require.NoError(t, err)
	defer l.Close()
	resp, err := l.Resolve(context.Background(), []string{"x1", "y", "x2bbbb"})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"x1": "x1aaaa", "x2bbbb": "x2bbbb"}, resp.IDs)
}

// The admitter stamps nothing a non-TLS connection could reach.
func TestDoorRefusesPlainTCP(t *testing.T) {
	d := &peer.Door{}
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	_, err := d.Admit(context.Background(), c1)
	require.ErrorContains(t, err, "did not arrive over TLS")
}

// A peer's passage is one bounded JSON line: an endless one or a garbled
// one is refused, and the door does not wait for more.
func TestPassageIsBoundedAndParsed(t *testing.T) {
	p := peertest.NewPKI(t, "fed")
	b := serve(t, p.Door("b", ""), &peer.Door{Members: members(t, p.Peer("b", "a", "127.0.0.1:1")), Handshake: time.Minute})
	me := p.Peer("a", "b", b.addr)
	cert, err := tls.LoadX509KeyPair(me.Cert, me.Key)
	require.NoError(t, err)
	for name, c := range map[string]struct {
		passage []byte
		refusal string
	}{
		"endless": {[]byte(strings.Repeat("x", rpc.MaxPassage+1)), "longer than"},
		"garbled": {[]byte("not json\n"), "invalid character"},
	} {
		conn, err := tls.Dial("tcp", b.addr, &tls.Config{Certificates: []tls.Certificate{cert}, InsecureSkipVerify: true})
		require.NoError(t, err, name)
		_, _ = conn.Write(c.passage)
		select {
		case err := <-b.admitted:
			require.ErrorContains(t, err, c.refusal, name)
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: the door kept reading", name)
		}
		conn.Close()
	}
}
