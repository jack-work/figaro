package peer

import (
	"context"
	"crypto/tls"
	"net"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/troupe"
)

func serveBenchDoor(b *testing.B, ln net.Listener, members Membership, creds *Credentials) {
	cfg, err := ServerTLS(config.TLSDoor{}, creds)
	if err != nil {
		b.Fatal(err)
	}
	d := &Door{Members: members, Versions: &Versions{}}
	tl := tls.NewListener(ln, cfg)
	go func() {
		for {
			conn, err := tl.Accept()
			if err != nil {
				return
			}
			go func() {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if _, err := d.Admit(ctx, conn); err != nil {
					conn.Close()
					return
				}
				buf := make([]byte, 1)
				conn.Read(buf)
				conn.Close()
			}()
		}
	}()
}

func benchOpen(b *testing.B, d *Dialer, node string) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		l, err := d.Open(context.Background(), node, rpc.Passage{}, nil)
		if err != nil {
			b.Fatal(err)
		}
		l.Close()
	}
}

// A full node-door handshake (TCP, mutual TLS on troupe keys, passage),
// with the members named in config and with the members read from the
// ledger, which adds the membership and revocation checks.
func BenchmarkNodeDoorHandshake(b *testing.B) {
	b.Run("static", func(b *testing.B) {
		ks, _ := troupeNodes(b, time.Now(), troupe.IntermediateTTL, "node-a", "node-b")
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			b.Fatal(err)
		}
		defer ln.Close()
		mb, _ := NewStatic(0, []config.Peer{{Node: "node-a", Address: "127.0.0.1:1", Identity: troupe.Identity("estate", "node-a")}}, credsOf(ks["node-b"]))
		serveBenchDoor(b, ln, mb, credsOf(ks["node-b"]))
		ma, _ := NewStatic(0, []config.Peer{{Node: "node-b", Address: ln.Addr().String(), Identity: troupe.Identity("estate", "node-b")}}, credsOf(ks["node-a"]))
		benchOpen(b, &Dialer{Members: ma, Versions: &Versions{}}, "node-b")
	})
	b.Run("figaro", func(b *testing.B) {
		lt := newLedgerTroupe(b, "a", "b")
		es := []troupe.Entry{lt.selfJoin("a"), lt.selfJoin("b")}
		ma, mb := lt.membership("a", es...), lt.membership("b", es...)
		serveBenchDoor(b, lt.ln["b"], mb, mb.Credentials())
		benchOpen(b, &Dialer{Members: ma, Versions: &Versions{}}, "b")
	})
}
