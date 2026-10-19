package peer

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/troupe"
	"github.com/jack-work/jkrpc"
)

type ledgerTroupe struct {
	t    testing.TB
	rk   ed25519.PrivateKey
	root *x509.Certificate
	keep map[string]*troupe.Keeper
	ln   map[string]net.Listener
}

func newLedgerTroupe(t testing.TB, nodes ...string) *ledgerTroupe {
	t.Helper()
	_, rk, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	root, err := troupe.RootCert("estate", rk)
	require.NoError(t, err)
	lt := &ledgerTroupe{t: t, rk: rk, root: root, keep: map[string]*troupe.Keeper{}, ln: map[string]net.Listener{}}
	for _, n := range nodes {
		_, ik, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		inter, err := troupe.IssueIntermediate(rk, root, "estate", n, ik.Public(), time.Now(), troupe.IntermediateTTL)
		require.NoError(t, err)
		d := troupe.DirFor(t.TempDir(), "estate")
		require.NoError(t, d.WriteRoot(root))
		require.NoError(t, d.WriteIntermediate(inter, ik))
		k := &troupe.Keeper{Dir: d, Troupe: "estate", Node: n}
		require.NoError(t, k.Ensure())
		lt.keep[n] = k
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		t.Cleanup(func() { ln.Close() })
		lt.ln[n] = ln
	}
	return lt
}

func (lt *ledgerTroupe) selfJoin(node string) troupe.Entry {
	lt.t.Helper()
	inter, ik, err := lt.keep[node].Dir.Intermediate()
	require.NoError(lt.t, err)
	e, err := troupe.SignJoin(ik, inter, "estate", node, lt.ln[node].Addr().String(), "user:owner", time.Now())
	require.NoError(lt.t, err)
	return e
}

func (lt *ledgerTroupe) leave(node string) troupe.Entry {
	lt.t.Helper()
	cur, err := lt.keep[node].Current()
	require.NoError(lt.t, err)
	inter, err := x509.ParseCertificate(cur.Certificate[1])
	require.NoError(lt.t, err)
	e, err := troupe.SignLeave(cur.PrivateKey.(ed25519.PrivateKey), cur.Leaf, inter, "estate", node, time.Now())
	require.NoError(lt.t, err)
	return e
}

func (lt *ledgerTroupe) revoke(node string) troupe.Entry {
	lt.t.Helper()
	e, err := troupe.SignRevoke(lt.rk, "estate", node, time.Now().Add(time.Second))
	require.NoError(lt.t, err)
	return e
}

func (lt *ledgerTroupe) membership(node string, es ...troupe.Entry) *Ledger {
	lt.t.Helper()
	l := troupe.NewLedger("estate", lt.root)
	_, err := l.Merge(es...)
	require.NoError(lt.t, err)
	k := lt.keep[node]
	return NewLedger(node, l, &Credentials{Certificate: k.Current, Roots: k.Roots})
}

// serveLedgerDoor runs node's door the way the daemon does: TLS from the
// membership's credentials, the passage through Door.Admit, then an rpc
// server that stops when the admitted context ends.
func (lt *ledgerTroupe) serveLedgerDoor(node string, m *Ledger) *Door {
	lt.t.Helper()
	cfg, err := ServerTLS(config.TLSDoor{}, m.Credentials())
	require.NoError(lt.t, err)
	d := &Door{Members: m, Versions: &Versions{}}
	m.OnEvict(d.Evict)
	ln := tls.NewListener(lt.ln[node], cfg)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				cctx, err := d.Admit(ctx, conn)
				if err != nil {
					conn.Close()
					return
				}
				srv := jkrpc.NewServer(jkrpc.NewConn(conn), map[string]jkrpc.HandlerFunc{
					"echo": func(_ context.Context, p json.RawMessage) (any, error) { return p, nil },
					rpc.MethodTroupeEntries: func(_ context.Context, p json.RawMessage) (any, error) {
						var req rpc.TroupeEntriesRequest
						if err := json.Unmarshal(p, &req); err != nil {
							return nil, err
						}
						es, err := troupe.Decode(req.Entries)
						if err != nil {
							return nil, err
						}
						m.Merge(es...)
						return rpc.TroupeEntriesResponse{Entries: troupe.Encode(m.Entries())}, nil
					},
				})
				done := make(chan struct{})
				go func() { srv.Serve(cctx); close(done) }()
				select {
				case <-done:
				case <-cctx.Done():
					srv.Stop()
				}
			}()
		}
	}()
	return d
}

func open(t *testing.T, m *Ledger, node string) (*Link, error) {
	t.Helper()
	d := &Dialer{Members: m, Versions: &Versions{}, Timeout: 2 * time.Second}
	m.OnEvict(d.Evict)
	return d.Open(context.Background(), node, rpc.Passage{}, nil)
}

func echo(l *Link) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := l.Call(ctx, "echo", json.RawMessage(`{}`))
	return err
}

func closedSoon(t *testing.T, l *Link) {
	t.Helper()
	select {
	case <-l.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the link stayed open")
	}
}

// Under the figaro provider the members come from the ledger: no config
// names the peers, and both ends verify each other on troupe keys.
func TestLedgerMembersDialAndServeEachOther(t *testing.T) {
	lt := newLedgerTroupe(t, "a", "b")
	es := []troupe.Entry{lt.selfJoin("a"), lt.selfJoin("b")}
	ma, mb := lt.membership("a", es...), lt.membership("b", es...)
	require.Equal(t, []string{"b"}, ma.Nodes())
	require.Equal(t, mb.Version(), ma.Version())
	lt.serveLedgerDoor("b", mb)

	l, err := open(t, ma, "b")
	require.NoError(t, err)
	defer l.Close()
	require.NoError(t, echo(l))
}

// A node that is not in the ledger is refused by the door at the TLS
// handshake, even holding a certificate from the troupe's root.
func TestADoorRefusesACertifiedNodeTheLedgerDoesNotName(t *testing.T) {
	lt := newLedgerTroupe(t, "a", "b")
	mb := lt.membership("b", lt.selfJoin("b"))
	lt.serveLedgerDoor("b", mb)
	ma := lt.membership("a", lt.selfJoin("a"), lt.selfJoin("b"))

	_, err := open(t, ma, "b")
	require.Error(t, err)
}

// A revoke arriving while a link is open takes effect on both sides: the
// door that learns it drops the revoked node's connections, the dialer
// that learns it drops its links to that node, and the next handshake is
// refused.
func TestARevokeMidConnectionDropsTheLinkAndRefusesTheNextHandshake(t *testing.T) {
	t.Run("the door learns it", func(t *testing.T) {
		lt := newLedgerTroupe(t, "a", "b")
		es := []troupe.Entry{lt.selfJoin("a"), lt.selfJoin("b")}
		ma, mb := lt.membership("a", es...), lt.membership("b", es...)
		lt.serveLedgerDoor("b", mb)
		l, err := open(t, ma, "b")
		require.NoError(t, err)
		require.NoError(t, echo(l))

		_, err = mb.Merge(lt.revoke("a"))
		require.NoError(t, err)
		closedSoon(t, l)
		require.Error(t, echo(l))
		_, err = open(t, ma, "b")
		require.Error(t, err, "a revoked node completed a new handshake")
	})
	t.Run("the dialer learns it", func(t *testing.T) {
		lt := newLedgerTroupe(t, "a", "b")
		es := []troupe.Entry{lt.selfJoin("a"), lt.selfJoin("b")}
		ma, mb := lt.membership("a", es...), lt.membership("b", es...)
		lt.serveLedgerDoor("b", mb)
		l, err := open(t, ma, "b")
		require.NoError(t, err)
		stale, ok := ma.Member("b")
		require.True(t, ok)

		_, err = ma.Merge(lt.leave("b"))
		require.NoError(t, err)
		closedSoon(t, l)
		_, ok = ma.Member("b")
		require.False(t, ok)

		conn, err := net.Dial("tcp", stale.Address)
		require.NoError(t, err)
		tc := tls.Client(conn, stale.tls)
		defer tc.Close()
		require.Error(t, tc.Handshake(), "a config taken before the leave still verified the departed node")
	})
}

// A root revoke of a node voids every certificate its intermediate issued:
// a member whose current leaf came from it is refused until a signer that
// is still trusted renews it.
func TestACertificateFromARevokedIntermediateIsRefused(t *testing.T) {
	lt := newLedgerTroupe(t, "a", "c", "d")
	ainter, aik, err := lt.keep["a"].Dir.Intermediate()
	require.NoError(t, err)
	_, ck, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	leaf, err := troupe.IssueNode(aik, ainter, "estate", "c", ck.Public(), time.Now(), troupe.NodeTTL)
	require.NoError(t, err)
	dinter, dik, err := lt.keep["d"].Dir.Intermediate()
	require.NoError(t, err)
	cjoin, err := troupe.SignJoin(dik, dinter, "estate", "c", "c:1", "user:owner", time.Now())
	require.NoError(t, err)

	md := lt.membership("d", lt.selfJoin("a"), lt.selfJoin("d"), cjoin)
	chain := []*x509.Certificate{leaf, ainter, lt.root}
	require.NoError(t, md.Admit(chain))
	_, err = md.Merge(lt.revoke("a"))
	require.NoError(t, err)
	require.Contains(t, md.Nodes(), "c", "c was admitted by d, so it stays a member")
	require.ErrorContains(t, md.Admit(chain), "revoked")
}

// The door forgets a connection when it ends, so a node that is never
// evicted does not accumulate one entry per call it ever made.
func TestTheDoorForgetsAClosedConnection(t *testing.T) {
	lt := newLedgerTroupe(t, "a", "b")
	es := []troupe.Entry{lt.selfJoin("a"), lt.selfJoin("b")}
	ma, mb := lt.membership("a", es...), lt.membership("b", es...)
	d := lt.serveLedgerDoor("b", mb)
	for range 3 {
		l, err := open(t, ma, "b")
		require.NoError(t, err)
		require.NoError(t, echo(l))
		require.NoError(t, l.Close())
	}
	require.Eventually(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return len(d.open) == 0
	}, 2*time.Second, 10*time.Millisecond)
}
