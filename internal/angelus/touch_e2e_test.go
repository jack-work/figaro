package angelus_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/sdk"
)

type accessReader interface{ LastAccess(id string) int64 }

func lastAccess(t *testing.T, a *angelus.Angelus, id string) int64 {
	t.Helper()
	r, ok := a.Backend.(accessReader)
	require.True(t, ok, "the backend keeps no access stamps")
	return r.LastAccess(id)
}

// The keepalive is only as good as what stamps it. Every call that means
// somebody used the aria must stamp it, and nothing that merely looks at the
// roster may, or `fig ls` in a loop would keep every aria alive forever.
func TestAccessStampsMatrix(t *testing.T) {
	stamps := map[string]func(ctx context.Context, a *angelus.Angelus, acli *sdk.Angelus, id string) error{
		"prompt": func(ctx context.Context, a *angelus.Angelus, _ *sdk.Angelus, id string) error {
			var out rpc.QuaResponse
			return dialAria(t, a, id, nil).Call(ctx, rpc.MethodQua, rpc.QuaRequest{Text: "hi"}, &out)
		},
		"set": func(ctx context.Context, a *angelus.Angelus, _ *sdk.Angelus, id string) error {
			var out rpc.SetResponse
			return dialAria(t, a, id, nil).Call(ctx, rpc.MethodSet, setKeys(t, map[string]string{"mantra": "x"}), &out)
		},
		"read": func(ctx context.Context, a *angelus.Angelus, _ *sdk.Angelus, id string) error {
			var out map[string]any
			return dialAria(t, a, id, nil).Call(ctx, rpc.MethodRead, rpc.ReadRequest{}, &out)
		},
		"form": func(ctx context.Context, a *angelus.Angelus, _ *sdk.Angelus, id string) error {
			var out rpc.FormResponse
			return dialAria(t, a, id, nil).Call(ctx, rpc.MethodForm, rpc.FormRequest{}, &out)
		},
		"queued": func(ctx context.Context, a *angelus.Angelus, _ *sdk.Angelus, id string) error {
			var out rpc.QueuedResponse
			return dialAria(t, a, id, nil).Call(ctx, rpc.MethodQueued, struct{}{}, &out)
		},
		"follow": func(ctx context.Context, a *angelus.Angelus, _ *sdk.Angelus, id string) error {
			var out rpc.FollowResponse
			return dialAria(t, a, id, nil).Call(ctx, rpc.MethodFollow, struct{}{}, &out)
		},
		"attach": func(ctx context.Context, _ *angelus.Angelus, acli *sdk.Angelus, id string) error {
			_, err := acli.Attach(ctx, id)
			return err
		},
		"bind": func(ctx context.Context, _ *angelus.Angelus, acli *sdk.Angelus, id string) error {
			return acli.Bind(ctx, os.Getpid(), id, 0)
		},
		"ir": func(ctx context.Context, _ *angelus.Angelus, acli *sdk.Angelus, id string) error {
			_, err := acli.IR(ctx, id, 0, 10)
			return err
		},
		"fork from it": func(ctx context.Context, _ *angelus.Angelus, acli *sdk.Angelus, id string) error {
			_, err := acli.Fork(ctx, id, 0, 0, nil, nil)
			return err
		},
	}
	for name, call := range stamps {
		t.Run("stamps/"+name, func(t *testing.T) {
			a, acli, ctx := daemonFixture(t)
			created, err := acli.Create(ctx, dress(t, "mock"), nil)
			require.NoError(t, err)
			id := created.FigaroID
			require.Zero(t, lastAccess(t, a, id), "creation alone stamped the aria")
			before := time.Now().UnixMilli()
			require.NoError(t, call(ctx, a, acli, id))
			require.GreaterOrEqual(t, lastAccess(t, a, id), before, "%s left no access stamp", name)
		})
	}

	silent := map[string]func(ctx context.Context, a *angelus.Angelus, acli *sdk.Angelus, id string) error{
		"ls": func(ctx context.Context, _ *angelus.Angelus, acli *sdk.Angelus, _ string) error {
			_, err := acli.List(ctx)
			return err
		},
		"ls -g": func(ctx context.Context, _ *angelus.Angelus, acli *sdk.Angelus, _ string) error {
			_, err := acli.ListGlobal(ctx)
			return err
		},
		"status": func(ctx context.Context, _ *angelus.Angelus, acli *sdk.Angelus, _ string) error {
			_, err := acli.Status(ctx)
			return err
		},
		"a refused prompt": func(ctx context.Context, a *angelus.Angelus, _ *sdk.Angelus, id string) error {
			retired := a.Backend.(interface {
				UpdateMeta(string, func(*store.AriaMeta)) error
			})
			require.NoError(t, retired.UpdateMeta(id, func(m *store.AriaMeta) { m.RetiredAtMS = 1 }))
			var out rpc.QuaResponse
			err := dialAria(t, a, id, nil).Call(ctx, rpc.MethodQua, rpc.QuaRequest{Text: "hi"}, &out)
			require.Error(t, err)
			return nil
		},
	}
	for name, call := range silent {
		t.Run("silent/"+name, func(t *testing.T) {
			a, acli, ctx := daemonFixture(t)
			created, err := acli.Create(ctx, dress(t, "mock"), nil)
			require.NoError(t, err)
			id := created.FigaroID
			require.NoError(t, call(ctx, a, acli, id))
			require.Zero(t, lastAccess(t, a, id), "%s stamped the aria; looking at the roster is not using it", name)
		})
	}
}
