package angelus_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/aria"
	"github.com/jack-work/figaro/api/handle"
	"github.com/jack-work/figaro/api/rpc"
)

// A prompt one aria sends another arrives named on both read paths: the live
// frame that opens the turn and the page a later read returns carry the
// sender's handle beside its id.
func TestAnAriaSentPromptCarriesTheSendersName(t *testing.T) {
	a, acli, ctx := daemonFixture(t)
	from, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	to, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	sender := from.FigaroID
	named := func(name string) bool {
		return name == handle.Words(sender, 2) || name == handle.Words(sender, 3)
	}

	live := make(chan aria.Page, 64)
	done := make(chan struct{}, 1)
	conn := dialAria(t, a, to.FigaroID, func(m string, p json.RawMessage) {
		switch m {
		case rpc.MethodAriaFrame:
			var pg aria.Page
			if json.Unmarshal(p, &pg) == nil {
				live <- pg
			}
		case rpc.MethodTurnDone:
			select {
			case done <- struct{}{}:
			default:
			}
		}
	})
	params, err := rpc.WithCaller(rpc.QuaRequest{Text: "hello from another aria"}, sender, nil)
	require.NoError(t, err)
	var out rpc.QuaResponse
	require.NoError(t, conn.Call(ctx, rpc.MethodQua, params, &out))
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the turn never ended")
	}

	sawLive := false
	for len(live) > 0 {
		for _, part := range (<-live).Parts {
			for _, s := range part.InquirySegments {
				if s.Sender == sender {
					require.True(t, named(s.Name), "live segment %+v", s)
					sawLive = true
				}
			}
		}
	}
	require.True(t, sawLive, "no live frame carried the sender")

	read := func(label string) {
		t.Helper()
		var page aria.Page
		require.NoError(t, dialAria(t, a, to.FigaroID, nil).Call(ctx, rpc.MethodRead, rpc.ReadRequest{}, &page))
		saw := false
		for _, part := range page.Parts {
			for _, s := range part.InquirySegments {
				if s.Sender == sender {
					require.True(t, named(s.Name), "%s: read segment %+v", label, s)
					require.False(t, strings.Contains(s.Name, sender[:8]))
					saw = true
				}
			}
		}
		require.True(t, saw, "%s: the read page lost the sender", label)
	}
	read("awake")
	require.NoError(t, a.Registry.Hibernate(to.FigaroID))
	read("dormant")
}
