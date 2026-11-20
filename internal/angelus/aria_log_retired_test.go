package angelus_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
)

func TestARetiredLogTakesNoEmptyEntryEither(t *testing.T) {
	r := logRig(t, defaultPolicy, nil)
	id := mintLog(t, r, asOwner())
	require.NoError(t, r.backend.UpdateMeta(id, func(m *store.AriaMeta) { m.RetiredAtMS = 1 }))
	_, err := r.h[rpc.MethodQua](asOwner(), withAria(t, id, rpc.QuaRequest{Text: "x", Exact: true}))
	require.Error(t, err, "a retired log refuses an entry")
	_, err = r.h[rpc.MethodQua](asOwner(), withAria(t, id, rpc.QuaRequest{Text: "", Exact: true}))
	require.Error(t, err, "a retired log refuses an empty entry too")
}
