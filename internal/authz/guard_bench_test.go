package authz

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/jkrpc"
)

// One guarded call on the owner's connection, as the angelus door serves
// it: a prompt-sized body, with and without the CLI's stamp, and with a
// fence loaded that covers the call but lets it through.
func BenchmarkGuardedOwnerCall(b *testing.B) {
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.Cleanup(func() { slog.SetDefault(old) })
	body := map[string]any{"figaro_id": "aaaa0002", "prompt": strings.Repeat("lorem ipsum ", 200)}
	plain, _ := json.Marshal(body)
	stampedBody, err := rpc.WithCaller(body, "dddd0001", nil)
	if err != nil {
		b.Fatal(err)
	}
	for _, c := range []struct {
		name   string
		policy string
		params json.RawMessage
	}{
		{"unstamped", sandboxPolicy, plain},
		{"stamped", sandboxPolicy, stampedBody},
		{"stamped-fenced", sandboxPolicy + "\n[[fence]]\nverbs = [\"send\"]\non = [\"aria:aaaa0002\"]\nallow = [\"figaro:dddd0001\"]\n", stampedBody},
	} {
		b.Run(c.name, func(b *testing.B) {
			p := NewPolicies(me, facts())
			if err := p.load([]byte(c.policy)); err != nil {
				b.Skip(err)
			}
			ok := func(context.Context, json.RawMessage) (any, error) { return nil, nil }
			call := Authenticated(AriaHeader{}, Guard(map[string]jkrpc.HandlerFunc{rpc.MethodQua: ok}, AriaHeader{}, Enforce(p))[rpc.MethodQua])
			ctx := WithSubject(context.Background(), subject(me))
			b.ReportAllocs()
			for b.Loop() {
				if _, err := call(ctx, c.params); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
