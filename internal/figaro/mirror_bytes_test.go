package figaro_test

// THE MIRROR AND THE LOG MUST AGREE BYTE FOR BYTE.
//
// This is entry (3) of the lost-study pattern list, checked rather than
// argued. applyControlPatchVerdict writes the patch through the form channel
// and then publishes THE REQUESTED PATCH into the agent's board -- not the
// `applied` patch the writer handed back. That is the same sentence as the
// lost study (publish what was written), and d921742d named the case where
// it would bite: form.Value keeps the CALLER'S EXACT BYTES, and the writer's
// effectivePatch STRIPS a set whose value is semantically equal to what is
// stored. So re-set a key to an equal value spelled differently and the log
// never hears about it -- while the mirror is handed the new spelling. If the
// mirror took it, the two would be semantically equal and BYTE-DIFFERENT, and
// for object- and array-valued keys the difference reaches the wire, because
// genericBody hands non-strings over raw.
//
// It does not take it, and the reason is worth keeping under test rather than
// in a comment: ptree.Set is ALSO a no-op that keeps the stored spelling when
// the incoming Value is Equal (form/tree.go:105, value.go:143). The two sides
// suppress the same rewrite by two different mechanisms, which is why the
// divergence does not exist today -- and exactly why it would be silent if
// either mechanism changed. This test is the alarm on that.

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
		_, _, err := a.SetAwaiting(ctx, form.Patch{
			Set: map[string]json.RawMessage{"k": json.RawMessage(raw)},
		}, 0, false)
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
