package authz

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

func TestADelegatedSetRefusesTheBareRuntimeRowsBeforeTheDaemon(t *testing.T) {
	guarded, ctx, calls := delegatedWriter(t)
	for _, k := range []string{"model", "token_budget", "truncation"} {
		require.False(t, form.HarnessKey(k), k)
		_, owner := form.OwnerHeld(k)
		_, lineage := form.Protected(k)
		require.False(t, owner || lineage, k)
		before := *calls
		_, err := guarded[rpc.MethodSet](ctx, writeBody(t, "aaaa0002", map[string]any{k: "forged"}))
		require.Error(t, err, k)
		require.Equal(t, before, *calls, "%s reached the handler", k)
	}
}
