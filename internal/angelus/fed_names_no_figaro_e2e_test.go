package angelus_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/sdk"
)

// El Inspector XVI, D7: a principal whose grants read no figaro is refused
// figaro.list before the fan-out, so its bearer never reaches a peer and no
// peer's node id or link error comes back to it.
func TestAListByWhoReadsNoFigaroAsksNoPeer(t *testing.T) {
	a, b := newFedPair(t)
	here, there := a.mint(), b.mint()
	ctx := callCtx(t)

	decider, token := a.asHolding("decider")
	resp, err := decider.List(ctx)
	require.ErrorContains(t, err, "holds no grant that reads a figaro", "listed: %+v", resp)
	require.Zero(t, b.admitted(token), "the decider's bearer reached the peer")

	probe, probeToken := a.asHolding("probe")
	resp, err = probe.List(ctx)
	require.NoError(t, err)
	ids := map[string]bool{}
	for _, r := range resp.Figaros {
		ids[r.ID] = true
	}
	require.True(t, ids[here] && ids[there], "the owner's list lost a row: %v", ids)
	require.NotZero(t, b.admitted(probeToken), "the owner's list never asked the peer, so the count above proves nothing")
}

// El Inspector XVI, D6: a principal whose grants name no figaro is refused by
// policy before an id is resolved: the same code and words for an id this
// node holds, one a peer holds and one nobody holds, and no peer is asked
// where an id lives.
func TestAnIDFromWhoNamesNoFigaroIsRefusedFirst(t *testing.T) {
	a, b := newFedPair(t)
	here, there := a.mint(), b.mint()
	never := "0123456789abcdef0123456789abcdef"
	ctx := callCtx(t)

	set := func(token, ref string) string {
		c, err := sdk.DialAria(sdk.Addr{Endpoint: a.callerEndpoint(token), ID: ref}, nil)
		require.NoError(t, err)
		defer c.Close()
		_, err = c.Set(ctx, *rawPatch(map[string]string{"mantra": `"probe"`}), 0)
		require.Error(t, err, ref)
		return strings.ReplaceAll(err.Error(), ref, "<id>")
	}
	token := a.issuer.Mint("decider", time.Hour)
	want := set(token, never)
	require.Contains(t, want, "holds no grant that names a figaro")
	for _, ref := range []string{here, there, here[:8], there[:8], never[:8]} {
		require.Equal(t, want, set(token, ref), ref)
	}
	require.Zero(t, b.admitted(token), "the decider's bearer reached the peer")

	stranger := a.issuer.Mint("stranger", time.Hour)
	set(stranger, never)
	require.NotZero(t, b.admitted(stranger), "an unknown id was never looked for on the peer, so the count above proves nothing")
}
