package config

import (
	"path/filepath"
	"strings"
	"testing"
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
	door, ok, err := l.TLSDoor()
	if err != nil || !ok {
		t.Fatalf("door.tls: ok=%v err=%v", ok, err)
	}
	if door.Cert != filepath.Join(l.ConfigDir, "node.crt") || door.Key != "/run/credentials/figaro/node.key" {
		t.Fatalf("door.tls paths: %+v", door)
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
	if _, ok, err := l.TLSDoor(); ok || err != nil {
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
		_, _, derr := l.TLSDoor()
		_, perr := l.Peers()
		if derr == nil && perr == nil {
			t.Errorf("accepted %q", body)
		}
	}
}
