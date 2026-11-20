package angelus

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/jkrpc"
	"github.com/stretchr/testify/require"
)

func TestADormantAriaRefusesItsIntrinsicsWithTheirOwnCode(t *testing.T) {
	backend, id := benchStore(t, 1)
	h := &handlers{angelus: &Angelus{Backend: backend}}
	for _, name := range []string{"runtime", "queue"} {
		raw, err := json.Marshal(rpc.FormRequest{Intrinsic: name})
		require.NoError(t, err)
		_, handled, err := h.readFromStore(context.Background(), id, rpc.MethodForm, raw)
		require.True(t, handled)
		var refusal *jkrpc.Error
		require.True(t, errors.As(err, &refusal), "refusal %v", err)
		require.Equal(t, rpc.ErrCodeIntrinsicDormant, refusal.Code)
	}
}
