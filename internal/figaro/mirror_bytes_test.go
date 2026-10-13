package figaro_test

// The board mirror and the form log must agree byte for byte: a re-set with
// an equal value spelled differently must not leave the mirror holding bytes
// the log never received.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/stretchr/testify/require"
)

func TestMirrorAndLogAgreeOnBytes(t *testing.T) {
	entered, gate := newGate()
	prov := &gateProvider{name: "gate", entered: entered, gate: gate}
	a, backend, ariaID := fuzzAgent(t, prov, nil)

	set := func(t *testing.T, raw string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		_, _, err := a.SetAwaiting(ctx, form.Build(form.Snapshot{}, map[string]json.RawMessage{"k": json.RawMessage(raw)}, nil), 0, false)
		require.NoError(t, err)
	}

	const first = `[1,2]`
	const equalButRespelled = `[ 1 , 2 ]` // semantically Equal, different bytes

	set(t, first)
	set(t, equalButRespelled)

	mirror, ok := a.Snapshot().Get("k")
	require.True(t, ok, "the mirror lost the key")
	durable, err := backend.FormState(ariaID)
	require.NoError(t, err)
	logged, ok := durable.Get("k")
	require.True(t, ok, "the log lost the key")

	require.Equal(t, string(logged), string(mirror),
		"the mirror and the durable log hold the same value spelled differently: "+
			"the mirror published the REQUESTED patch where the writer stripped it as a no-op. "+
			"Publish what was written.")
	require.Equal(t, first, string(mirror),
		"a semantically equal re-set changed the stored spelling: the no-op suppression that "+
			"keeps these two sides identical has moved, and the byte identity above now depends on luck")
}
