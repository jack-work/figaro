package daemon

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/peer"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/troupe"
)

type estateRoot struct {
	key  ed25519.PrivateKey
	cert *x509.Certificate
}

func newEstateRoot(t *testing.T) estateRoot {
	t.Helper()
	_, k, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	c, err := troupe.RootCert("estate", k)
	require.NoError(t, err)
	return estateRoot{key: k, cert: c}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

type ledgerNode struct {
	t        *testing.T
	root     estateRoot
	storeDir string
	stateDir string
	cfgDir   string
	addr     string
	a        *angelus.Angelus
	backend  *store.XwalBackend
	stop     context.CancelFunc
}

func newLedgerNode(t *testing.T, root estateRoot) *ledgerNode {
	t.Helper()
	n := &ledgerNode{t: t, root: root, storeDir: t.TempDir(), stateDir: t.TempDir(), cfgDir: t.TempDir(), addr: freeAddr(t)}
	body := fmt.Sprintf("[troupe]\nname = \"estate\"\nprovider = \"figaro\"\n[door.tls]\nlisten = %q\n", n.addr)
	require.NoError(t, os.WriteFile(filepath.Join(n.cfgDir, "config.toml"), []byte(body), 0o600))
	n.open()
	_, ik, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	inter, err := troupe.IssueIntermediate(root.key, root.cert, "estate", n.id(), ik.Public(), time.Now(), troupe.IntermediateTTL)
	require.NoError(t, err)
	d := n.dir()
	require.NoError(t, d.WriteRoot(root.cert))
	require.NoError(t, d.WriteIntermediate(inter, ik))
	n.backend.Close()
	return n
}

func (n *ledgerNode) open() {
	b, err := store.NewXwalBackend(n.storeDir, 0)
	require.NoError(n.t, err)
	n.backend = b
}

func (n *ledgerNode) id() string      { return n.backend.NodeID() }
func (n *ledgerNode) dir() troupe.Dir { return troupe.DirFor(n.stateDir, "estate") }

func (n *ledgerNode) start() *peer.Ledger {
	t := n.t
	n.open()
	loaded, err := config.Load(n.cfgDir)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	n.stop = cancel
	n.a = angelus.New(angelus.Config{RuntimeDir: t.TempDir(), Backend: n.backend})
	require.NoError(t, federateIn(ctx, n.a, loaded, n.stateDir))
	n.a.Handlers = angelus.NewHandlers(angelus.ServerConfig{Angelus: n.a, Config: loaded, Ctx: ctx}).Map
	require.NoError(t, serveNodeDoor(ctx, n.a, loaded))
	t.Cleanup(n.halt)
	m, ok := n.a.Peers.(*peer.Ledger)
	require.True(t, ok, "troupe.provider figaro built %T", n.a.Peers)
	return m
}

func (n *ledgerNode) halt() {
	if n.stop == nil {
		return
	}
	n.stop()
	n.stop = nil
	n.backend.Close()
	require.Eventually(n.t, func() bool {
		ln, err := net.Listen("tcp", n.addr)
		if err != nil {
			return false
		}
		ln.Close()
		return true
	}, 2*time.Second, 10*time.Millisecond, "the node door kept its port")
}

func storedEntries(t *testing.T, n *ledgerNode) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(string(n.dir()), "ledger.form"))
	require.NoError(t, err)
	snap, err := n.backend.FormState(strings.TrimSpace(string(raw)))
	require.NoError(t, err)
	got, ok := snap.Get("entries")
	if !ok {
		return nil
	}
	var out map[string]string
	require.NoError(t, json.Unmarshal(got, &out))
	return out
}

// A node holding an intermediate signs its own join at start, with the
// door it advertises, keeps it in a form, and finds it there after a
// restart without signing another.
func TestAFounderSignsItselfInAndKeepsItsLedgerInAForm(t *testing.T) {
	n := newLedgerNode(t, newEstateRoot(t))
	m := n.start()
	me, ok := m.View().Members[n.id()]
	require.True(t, ok)
	require.Equal(t, n.addr, me.Door)
	require.Equal(t, []string{n.id()}, m.View().Signers(time.Now()))
	require.Empty(t, m.Nodes(), "a node is not its own peer")
	require.Len(t, storedEntries(t, n), 1)
	version := m.Version()

	n.halt()
	m = n.start()
	require.Len(t, m.Entries(), 1, "a restart signed a second join")
	require.Equal(t, version, m.Version())
}

// A node moved to another address signs a newer join naming it.
func TestANewDoorIsSignedIn(t *testing.T) {
	n := newLedgerNode(t, newEstateRoot(t))
	n.start()
	n.halt()
	n.addr = freeAddr(t)
	body := fmt.Sprintf("[troupe]\nname = \"estate\"\nprovider = \"figaro\"\n[door.tls]\nlisten = %q\n", n.addr)
	require.NoError(t, os.WriteFile(filepath.Join(n.cfgDir, "config.toml"), []byte(body), 0o600))
	m := n.start()
	require.Len(t, m.Entries(), 2)
	require.Equal(t, n.addr, m.View().Members[n.id()].Door)
}

// Two daemons that know each other trade entries over the real node door
// and keep what they learn; a node the root certified but no ledger names
// is refused at the handshake.
func TestTwoNodesTradeEntriesOverTheNodeDoor(t *testing.T) {
	root := newEstateRoot(t)
	na, nb, stranger := newLedgerNode(t, root), newLedgerNode(t, root), newLedgerNode(t, root)
	ma, mb := na.start(), nb.start()
	_, err := ma.Merge(mb.Entries()...)
	require.NoError(t, err)
	_, err = mb.Merge(ma.Entries()...)
	require.NoError(t, err)

	inter, ik, err := na.dir().Intermediate()
	require.NoError(t, err)
	c, err := troupe.SignJoin(ik, inter, "estate", "node-c", "127.0.0.1:1", time.Now())
	require.NoError(t, err)
	_, err = ma.Merge(c)
	require.NoError(t, err)

	g := &peer.Gossip{Members: ma, Dialer: &peer.Dialer{Members: ma, Versions: &peer.Versions{}, Timeout: 2 * time.Second}}
	require.NoError(t, g.Exchange(context.Background(), nb.id()))
	require.Equal(t, ma.Version(), mb.Version())
	require.Eventually(t, func() bool { _, ok := storedEntries(t, nb)[c.ID()]; return ok }, 3*time.Second, 10*time.Millisecond, "b's form never stored the entry")

	ms := stranger.start()
	sm := peer.NewLedger(stranger.id(), troupe.NewLedger("estate", root.cert), ms.Credentials())
	_, err = sm.Merge(mb.Entries()...)
	require.NoError(t, err)
	_, err = (&peer.Dialer{Members: sm, Versions: &peer.Versions{}, Timeout: 2 * time.Second}).Open(context.Background(), nb.id(), rpc.Passage{}, nil)
	require.Error(t, err, "a node no ledger names was admitted")
}

// The figaro provider needs a door peers can dial and serves it on troupe
// keys alone.
func TestTheFigaroProviderRefusesADoorItCannotAdvertise(t *testing.T) {
	for _, door := range []string{
		"",
		"[door.tls]\nlisten = \"0.0.0.0:7443\"\n",
		"[door.tls]\nlisten = \"127.0.0.1:7443\"\ncert = \"c\"\nkey = \"k\"\nclient_ca = \"ca\"\n",
	} {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[troupe]\nname = \"estate\"\nprovider = \"figaro\"\n"+door), 0o600))
		loaded, err := config.Load(dir)
		require.NoError(t, err)
		a := angelus.New(angelus.Config{RuntimeDir: t.TempDir(), Backend: store.NewTestBackend(t)})
		require.Error(t, federateIn(context.Background(), a, loaded, t.TempDir()), door)
	}
}
