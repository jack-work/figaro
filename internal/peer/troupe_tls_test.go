package peer

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/troupe"
)

type troupeClock struct{ t time.Time }

func (c *troupeClock) now() time.Time { return c.t }

func troupeNodes(t *testing.T, start time.Time, interTTL time.Duration, nodes ...string) (map[string]*troupe.Keeper, *troupeClock) {
	t.Helper()
	c := &troupeClock{t: start}
	_, rk, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	root, err := troupe.RootCert("estate", rk)
	require.NoError(t, err)
	out := map[string]*troupe.Keeper{}
	for _, n := range nodes {
		_, ik, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		inter, err := troupe.IssueIntermediate(rk, root, "estate", n, ik.Public(), c.t, interTTL)
		require.NoError(t, err)
		d := troupe.DirFor(t.TempDir(), "estate")
		require.NoError(t, d.WriteRoot(root))
		require.NoError(t, d.WriteIntermediate(inter, ik))
		k := &troupe.Keeper{Dir: d, Troupe: "estate", Node: n, Now: c.now}
		require.NoError(t, k.Ensure())
		out[n] = k
	}
	return out, c
}

func credsOf(k *troupe.Keeper) *Credentials {
	return &Credentials{Certificate: k.Current, Roots: k.Roots}
}

func serveTroupeDoor(t *testing.T, k *troupe.Keeper) (string, chan *x509.Certificate) {
	t.Helper()
	cfg, err := ServerTLS(config.TLSDoor{}, credsOf(k))
	require.NoError(t, err)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
	require.NoError(t, err)
	t.Cleanup(func() { ln.Close() })
	clients := make(chan *x509.Certificate, 8)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			tc := c.(*tls.Conn)
			if tc.Handshake() == nil {
				clients <- tc.ConnectionState().PeerCertificates[0]
			}
			c.Close()
		}
	}()
	return ln.Addr().String(), clients
}

func dialTroupe(t *testing.T, m *Static, node string) (*x509.Certificate, error) {
	t.Helper()
	mem, ok := m.Member(node)
	require.True(t, ok)
	conn, err := (&net.Dialer{Timeout: 2 * time.Second}).Dial("tcp", mem.Address)
	require.NoError(t, err)
	tc := tls.Client(conn, mem.tls)
	defer tc.Close()
	if err := tc.Handshake(); err != nil {
		return nil, err
	}
	return tc.ConnectionState().PeerCertificates[0], nil
}

// With troupe keys a peer needs no certificate files: the dialer presents
// its current node certificate, checks the other end against the troupe
// root and the peer's default identity, and the door does the same.
func TestTroupeKeysDialAndServeWithoutCertificateFiles(t *testing.T) {
	ks, _ := troupeNodes(t, time.Now(), troupe.IntermediateTTL, "node-a", "node-b")
	addr, seenAtB := serveTroupeDoor(t, ks["node-b"])
	m, err := NewStatic(0, []config.Peer{{Node: "node-b", Address: addr, Identity: troupe.Identity("estate", "node-b")}}, credsOf(ks["node-a"]))
	require.NoError(t, err)

	server, err := dialTroupe(t, m, "node-b")
	require.NoError(t, err)
	require.Equal(t, troupe.Identity("estate", "node-b"), server.URIs[0].String())
	client := <-seenAtB
	require.Equal(t, troupe.Identity("estate", "node-a"), client.URIs[0].String())
}

// A renewal takes effect on the next handshake of a live door, without a
// restart, and an expired intermediate closes the door to peers. The
// certificates are issued five days in the past so that renewing at the
// real present is due and verifies against the real clock.
func TestARenewedCertificateIsServedLiveAndAnExpiredOneIsNot(t *testing.T) {
	ks, c := troupeNodes(t, time.Now().Add(-5*24*time.Hour), 10*24*time.Hour, "node-a", "node-b")
	addr, _ := serveTroupeDoor(t, ks["node-b"])
	m, err := NewStatic(0, []config.Peer{{Node: "node-b", Address: addr, Identity: troupe.Identity("estate", "node-b")}}, credsOf(ks["node-a"]))
	require.NoError(t, err)

	before, err := dialTroupe(t, m, "node-b")
	require.NoError(t, err)
	c.t = time.Now()
	require.NoError(t, ks["node-a"].Ensure())
	require.NoError(t, ks["node-b"].Ensure())
	after, err := dialTroupe(t, m, "node-b")
	require.NoError(t, err)
	require.NotEqual(t, before.SerialNumber, after.SerialNumber)

	c.t = c.t.Add(6 * 24 * time.Hour)
	require.Error(t, ks["node-b"].Ensure())
	_, err = dialTroupe(t, m, "node-b")
	require.Error(t, err, "a node whose intermediate lapsed still served its door")
}
