package angelus_test

// The study/cast instruments of the periodic battery: what one casting
// call costs through the whole stack (RPC → actor loop → cross-call to
// the role's writer), and what one role-redirect READ costs (the
// hub-served figaro.Form every role-targeted invocation pays). Run with
// -count=6; the numbers ride scratch/m5-receipts.md.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/transport"
)

func benchPatch(kv map[string]string) *rpc.FormPatch {
	set := map[string]json.RawMessage{}
	for k, v := range kv {
		set[k] = json.RawMessage(v)
	}
	return &rpc.FormPatch{Set: set}
}

// One casting call, end to end, against an already-studied role — the
// steady-state shape (the first call's study registration is one-time).
func BenchmarkCast(b *testing.B) {
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.Cleanup(func() { slog.SetDefault(old) })
	_, acli, ctx := daemonFixture(b)
	created, err := acli.Create(ctx, dress(b, "mock"), nil)
	if err != nil {
		b.Fatal(err)
	}
	role, err := acli.FormCreate(ctx, "", nil, benchPatch(map[string]string{"name": `"bench-role"`}))
	if err != nil {
		b.Fatal(err)
	}
	fc, err := figaro.DialClient(transport.Endpoint{Scheme: created.Endpoint.Scheme, Address: created.Endpoint.Address}, nil)
	if err != nil {
		b.Fatal(err)
	}
	defer fc.Close()
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := fc.Cast(callCtx, rpc.CastRequest{FormID: role.FormID}); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := fc.Cast(callCtx, rpc.CastRequest{FormID: role.FormID}); err != nil {
			b.Fatal(err)
		}
	}
}

// The role-redirect read: every role-targeted send/listen/hup pays one
// hub-served figaro.Form on the role's endpoint before reaching the
// holder. Dial amortized (the CLI dials per invocation; that cost is
// the socket's, not the redirect's).
func BenchmarkRoleRedirectRead(b *testing.B) {
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.Cleanup(func() { slog.SetDefault(old) })
	_, acli, ctx := daemonFixture(b)
	role, err := acli.FormCreate(ctx, "", nil, benchPatch(map[string]string{
		"name": `"bench-role"`, "target-aria": `"abc123"`,
	}))
	if err != nil {
		b.Fatal(err)
	}
	rc, err := figaro.DialClient(transport.Endpoint{Scheme: role.Endpoint.Scheme, Address: role.Endpoint.Address}, nil)
	if err != nil {
		b.Fatal(err)
	}
	defer rc.Close()
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp, err := rc.Form(callCtx)
		if err != nil {
			b.Fatal(err)
		}
		if t := resp.Snapshot.Lookup("target-aria"); t == nil {
			b.Fatal("role lost its target")
		}
	}
}
