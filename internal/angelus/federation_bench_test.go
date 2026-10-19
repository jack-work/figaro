package angelus

import (
	"context"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/peer"
	"github.com/jack-work/figaro/internal/peer/peertest"
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
