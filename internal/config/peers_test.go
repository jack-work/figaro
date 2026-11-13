package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const twoPeers = `[door.tls]
listen = "10.77.0.1:7443"
cert = "node.crt"
key = "/run/credentials/figaro/node.key"
client_ca = "/run/credentials/figaro/ca.crt"

[peers.b2c1d7a0-0000-4000-8000-000000000002]
endpoint = "tls://10.77.0.2:7443"
identity = "spiffe://kelliher.info/figaro/node/b2c1d7a0-0000-4000-8000-000000000002"
ca = "/run/credentials/figaro/ca.crt"
cert = "/run/credentials/figaro/node.crt"
key = "/run/credentials/figaro/node.key"

[peers.a1]
endpoint = "tls://[::1]:7444"
identity = "spiffe://kelliher.info/figaro/node/a1"
ca = "ca.crt"
cert = "node.crt"
key = "node.key"
`

// Membership is static and parses as written, in a stable order, with
// relative credential paths anchored to the config dir.
func TestPeersConfig(t *testing.T) {
	l := loadToml(t, twoPeers)
	endpoint, ok, err := l.TLSEndpoint()
	if err != nil || !ok {
		t.Fatalf("door.tls: ok=%v err=%v", ok, err)
	}
	if endpoint.Cert != filepath.Join(l.ConfigDir, "node.crt") || endpoint.Key != "/run/credentials/figaro/node.key" {
		t.Fatalf("door.tls paths: %+v", endpoint)
	}
	peers, err := l.Peers()
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 2 || peers[0].Node != "a1" || peers[1].Node != "b2c1d7a0-0000-4000-8000-000000000002" {
		t.Fatalf("peers: %+v", peers)
	}
	if peers[0].Address != "[::1]:7444" || peers[0].CA != filepath.Join(l.ConfigDir, "ca.crt") {
		t.Fatalf("a1: %+v", peers[0])
	}
}

// No federation unless configured.
func TestPeersOffByDefault(t *testing.T) {
	l := loadToml(t, "")
	if _, ok, err := l.TLSEndpoint(); ok || err != nil {
		t.Fatalf("door.tls: ok=%v err=%v", ok, err)
	}
	if p, err := l.Peers(); err != nil || len(p) != 0 {
		t.Fatalf("peers: %v %v", p, err)
	}
}

// A peer that cannot be dialled and checked exactly as written stops the daemon.
func TestPeersConfigRefusesWhatItCannotHonour(t *testing.T) {
	good := "endpoint = \"tls://h:1\"\nidentity = \"spiffe://x/n\"\nca = \"c\"\ncert = \"c\"\nkey = \"k\"\n"
	for _, body := range []string{
		"[peers.a]\n" + strings.Replace(good, "tls://h:1", "wss://h:1", 1),
		"[peers.a]\n" + strings.Replace(good, "tls://h:1", "tls://h", 1),
		"[peers.a]\n" + strings.Replace(good, "spiffe://x/n", "", 1),
		"[peers.a]\n" + strings.Replace(good, "spiffe://x/n", "node-a", 1),
		"[peers.a]\n" + strings.Replace(good, "key = \"k\"\n", "", 1),
		"[peers.\"a/b\"]\n" + good,
		"[peers.a]\n" + good + "[peers.b]\n" + good,
		"[door.tls]\nlisten = \"h\"\ncert = \"c\"\nkey = \"k\"\nclient_ca = \"c\"\n",
		"[door.tls]\nlisten = \"h:1\"\ncert = \"c\"\nkey = \"k\"\n",
	} {
		l, err := loadWith(t, body)
		if err != nil {
			continue
		}
		_, _, derr := l.TLSEndpoint()
		_, perr := l.Peers()
		if derr == nil && perr == nil {
			t.Errorf("accepted %q", body)
		}
	}
}

// The directory of remote owners is bounded, by default and as configured.
func TestDirectorySize(t *testing.T) {
	if n := loadToml(t, "").DirectorySize(); n != defaultDirectorySize {
		t.Fatalf("default: %d", n)
	}
	if n := loadToml(t, "[federation]\ndirectory_size = 500\n").DirectorySize(); n != 500 {
		t.Fatalf("configured: %d", n)
	}
	if n := loadToml(t, "[federation]\ndirectory_size = 0\n").DirectorySize(); n != defaultDirectorySize {
		t.Fatalf("zero: %d", n)
	}
}

// The static troupe carries a version, so nodes whose config files were
// written apart can tell. Unset is 0; a negative one is refused.
func TestTroupeVersion(t *testing.T) {
	if v, err := loadToml(t, twoPeers).TroupeVersion(); err != nil || v != 0 {
		t.Fatalf("unset: %d %v", v, err)
	}
	if v, err := loadToml(t, "[troupe]\nversion = 7\n"+twoPeers).TroupeVersion(); err != nil || v != 7 {
		t.Fatalf("set: %d %v", v, err)
	}
	if _, err := loadToml(t, "[troupe]\nversion = -1\n").TroupeVersion(); err == nil {
		t.Fatal("a negative version was accepted")
	}
}

// With [troupe] name set, peers and the node endpoint may leave their
// certificate files out: the troupe's keys stand in, and a peer's identity
// defaults to its spiffe id in the troupe. Half a set of files is refused.
func TestATroupeNameLetsPeersAndTheEndpointUseTroupeKeys(t *testing.T) {
	body := "[troupe]\nname = \"estate\"\n[door.tls]\nlisten = \"127.0.0.1:7443\"\n[peers.b1]\nendpoint = \"tls://127.0.0.1:7444\"\n"
	l := loadToml(t, body)
	name, err := l.TroupeName()
	require.NoError(t, err)
	require.Equal(t, "estate", name)
	endpoint, ok, err := l.TLSEndpoint()
	require.NoError(t, err)
	require.True(t, ok)
	require.Empty(t, endpoint.Cert)
	peers, err := l.Peers()
	require.NoError(t, err)
	require.Equal(t, "spiffe://estate/node/b1", peers[0].Identity)
	require.Empty(t, peers[0].Cert)

	for _, bad := range []string{
		"[door.tls]\nlisten = \"127.0.0.1:7443\"\n",
		"[peers.b1]\nendpoint = \"tls://127.0.0.1:7444\"\n",
		"[troupe]\nname = \"estate\"\n[peers.b1]\nendpoint = \"tls://127.0.0.1:7444\"\ncert = \"c\"\n",
		"[troupe]\nname = \"Not A Name\"\n",
	} {
		l, err := loadWith(t, bad)
		if err != nil {
			continue
		}
		_, _, derr := l.TLSEndpoint()
		_, perr := l.Peers()
		_, nerr := l.TroupeName()
		require.True(t, derr != nil || perr != nil || nerr != nil, "accepted %q", bad)
	}
}

// The figaro provider takes its members from the troupe: it needs a troupe
// name, and refuses the static provider's [peers] and version.
func TestTroupeProvider(t *testing.T) {
	p, err := loadToml(t, twoPeers).TroupeProvider()
	require.NoError(t, err)
	require.Equal(t, ProviderStatic, p)
	p, err = loadToml(t, "[troupe]\nname = \"estate\"\nprovider = \"figaro\"\n").TroupeProvider()
	require.NoError(t, err)
	require.Equal(t, ProviderFigaro, p)
	for _, bad := range []string{
		"[troupe]\nprovider = \"figaro\"\n",
		"[troupe]\nname = \"estate\"\nprovider = \"gossip\"\n",
		"[troupe]\nname = \"estate\"\nprovider = \"figaro\"\nversion = 3\n",
		"[troupe]\nname = \"estate\"\nprovider = \"figaro\"\n[peers.b1]\nendpoint = \"tls://127.0.0.1:7444\"\n",
	} {
		_, err := loadToml(t, bad).TroupeProvider()
		require.Error(t, err, bad)
	}
}

// A join records the endpoint other nodes dial. It is the listen address
// unless that is a wildcard, in which case advertise must name it.
func TestTheAdvertisedEndpoint(t *testing.T) {
	endpoint := func(body string) (TLSEndpoint, error) {
		d, _, err := loadToml(t, "[troupe]\nname = \"estate\"\n[door.tls]\n"+body).TLSEndpoint()
		return d, err
	}
	d, err := endpoint("listen = \"10.0.0.2:7443\"\n")
	require.NoError(t, err)
	require.Equal(t, "10.0.0.2:7443", d.Advertise)
	d, err = endpoint("listen = \"0.0.0.0:7443\"\n")
	require.NoError(t, err)
	require.Empty(t, d.Advertise, "a wildcard is no address to dial")
	d, err = endpoint("listen = \"[::]:7443\"\nadvertise = \"gluck.tail:7443\"\n")
	require.NoError(t, err)
	require.Equal(t, "gluck.tail:7443", d.Advertise)
	_, err = endpoint("listen = \"0.0.0.0:7443\"\nadvertise = \"0.0.0.0:7443\"\n")
	require.Error(t, err)
}

// A node asks for a seven-day leaf and issues at most thirty unless
// [troupe] says otherwise; a lifetime it cannot honour is refused by name.
func TestTroupeLeafLifetimes(t *testing.T) {
	const day = 24 * time.Hour
	unset := loadToml(t, "[troupe]\nname = \"estate\"\n")
	if d, err := unset.TroupeLeafTTL(); err != nil || d != 7*day {
		t.Fatalf("leaf_ttl unset: %v %v", d, err)
	}
	if d, err := unset.TroupeMaxLeafTTL(); err != nil || d != 30*day {
		t.Fatalf("max_leaf_ttl unset: %v %v", d, err)
	}
	set := loadToml(t, "[troupe]\nname = \"estate\"\nleaf_ttl = \"30d\"\nmax_leaf_ttl = \"60d\"\n")
	if d, err := set.TroupeLeafTTL(); err != nil || d != 30*day {
		t.Fatalf("leaf_ttl 30d: %v %v", d, err)
	}
	if d, err := set.TroupeMaxLeafTTL(); err != nil || d != 60*day {
		t.Fatalf("max_leaf_ttl 60d: %v %v", d, err)
	}
	_, err := loadToml(t, "[troupe]\nleaf_ttl = \"2h\"\n").TroupeLeafTTL()
	require.ErrorContains(t, err, "troupe.leaf_ttl")
	_, err = loadToml(t, "[troupe]\nmax_leaf_ttl = \"a month\"\n").TroupeMaxLeafTTL()
	require.ErrorContains(t, err, "troupe.max_leaf_ttl")
}
