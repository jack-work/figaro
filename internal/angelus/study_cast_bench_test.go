package angelus_test

import (
	"context"
	"encoding/json"
	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/sdk"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"
)

func benchPatch(kv map[string]string) *rpc.FormPatch {
	set := map[string]json.RawMessage{}
	for k, v := range kv {
		set[k] = json.RawMessage(v)
	}
	p := form.Build(form.Snapshot{}, set, nil)
	return &p
}

func BenchmarkCast(b *testing.B) {
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.Cleanup(func() { slog.SetDefault(old) })
	a, acli, ctx := daemonFixture(b)
	created, err := acli.Create(ctx, dress(b, "mock"), nil)
	if err != nil {
		b.Fatal(err)
	}
	role, err := acli.FormCreate(ctx, "", nil, benchPatch(map[string]string{"name": `"bench-role"`}))
	if err != nil {
		b.Fatal(err)
	}
	fc, err := sdk.DialAria(ariaAddr(a, created.FigaroID), nil)
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

// Every role-targeted invocation pays one hub-served figaro.Form on the role's endpoint.
func BenchmarkRoleRedirectRead(b *testing.B) {
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.Cleanup(func() { slog.SetDefault(old) })
	a, acli, ctx := daemonFixture(b)
	role, err := acli.FormCreate(ctx, "", nil, benchPatch(map[string]string{
		"name": `"bench-role"`, "target-aria": `"abc123"`,
	}))
	if err != nil {
		b.Fatal(err)
	}
	rc, err := sdk.DialAria(ariaAddr(a, role.FormID), nil)
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
