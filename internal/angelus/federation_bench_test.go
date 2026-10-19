package angelus

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/peer"
	"github.com/jack-work/figaro/internal/peer/peertest"
	"github.com/jack-work/figaro/internal/troupe"
)

// A peered daemon serves a local aria exactly as a lone one does: the
// configured peer here is unreachable, so any trip to it would show.
func BenchmarkDoorCallUnixFederated(b *testing.B) {
	quiet(b)
	pki := peertest.NewPKI(b, "fed")
	members, err := peer.NewStatic(0, []config.Peer{pki.Peer("self", "elsewhere", "127.0.0.1:1")}, nil)
	if err != nil {
		b.Fatal(err)
	}
	hb := newAriaHub("abcd1234")
	defer hb.bind(newDrainAgent())()
	h := &handlers{angelus: &Angelus{Hubs: newHubs(), Peers: members}}
	h.angelus.Hubs.putIfAbsent(hb.id, hb)
	h.federate()
	client, _ := dialDoor(b, serveDoor(b, h, authz.AllowAll()), hb.id)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var out map[string]any
		if err := client.Call(ctx, rpc.MethodQua, rpc.QuaRequest{Text: "hi"}, &out); err != nil {
			b.Fatal(err)
		}
	}
}

// The same local call when the members come from the troupe ledger, with a
// member that is not running: the local path must not consult it either.
func BenchmarkDoorCallUnixLedger(b *testing.B) {
	quiet(b)
	_, rk, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		b.Fatal(err)
	}
	root, err := troupe.RootCert("estate", rk)
	if err != nil {
		b.Fatal(err)
	}
	_, ik, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		b.Fatal(err)
	}
	inter, err := troupe.IssueIntermediate(rk, root, "estate", "elsewhere", ik.Public(), time.Now(), troupe.IntermediateTTL)
	if err != nil {
		b.Fatal(err)
	}
	join, err := troupe.SignJoin(ik, inter, "estate", "elsewhere", "127.0.0.1:1", time.Now())
	if err != nil {
		b.Fatal(err)
	}
	l := troupe.NewLedger("estate", root)
	if _, err := l.Merge(join); err != nil {
		b.Fatal(err)
	}
	members := peer.NewLedger("self", l, &peer.Credentials{})
	hb := newAriaHub("abcd1234")
	defer hb.bind(newDrainAgent())()
	h := &handlers{angelus: &Angelus{Hubs: newHubs(), Peers: members}}
	h.angelus.Hubs.putIfAbsent(hb.id, hb)
	h.federate()
	client, _ := dialDoor(b, serveDoor(b, h, authz.AllowAll()), hb.id)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var out map[string]any
		if err := client.Call(ctx, rpc.MethodQua, rpc.QuaRequest{Text: "hi"}, &out); err != nil {
			b.Fatal(err)
		}
	}
}
