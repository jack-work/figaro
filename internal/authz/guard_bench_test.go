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
	granted := func(own map[string]string) Facts {
		f := lineageFakes(own)
		f.chains["dddd0001"] = []string{"top", "kid", "dddd0001"}
		return f
	}
	for _, c := range []struct {
		name   string
		policy string
		params json.RawMessage
		facts  Facts
	}{
		{"unstamped", sandboxPolicy, plain, nil},
		{"stamped", sandboxPolicy, stampedBody, nil},
		{"stamped-no-grants-depth-3", sandboxPolicy, stampedBody, granted(nil)},
		{"stamped-grants-depth-3", sandboxPolicy, stampedBody, granted(map[string]string{"top": `["*:*", "egress:*"]`, "kid": `["read:*", "send:*", "kill:lineage:self"]`, "dddd0001": `["send:aria:aaaa0002", "read:*"]`})},
		{"stamped-fenced", sandboxPolicy + "\n[[fence]]\ngrants = [\"send:aria:aaaa0002\"]\nallow = [\"figaro:dddd0001\"]\n", stampedBody, nil},
	} {
		b.Run(c.name, func(b *testing.B) {
			var f Facts = facts()
			if c.facts != nil {
				f = c.facts
			}
			p := NewPolicies(me, f)
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
