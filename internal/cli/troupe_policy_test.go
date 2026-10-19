package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
)

type fakePolicyDoor struct {
	show    rpc.TroupePolicyResponse
	setBody string
}

func (f *fakePolicyDoor) TroupePolicy(context.Context, string) (*rpc.TroupePolicyResponse, error) {
	return &f.show, nil
}

func (f *fakePolicyDoor) TroupePolicySet(_ context.Context, _ string, body string) (*rpc.TroupePolicySetResponse, error) {
	f.setBody = body
	return &rpc.TroupePolicySetResponse{Version: 4, Signer: "aaaa", Reached: []string{"bbbb"}, Unreachable: []rpc.NodeUnreachable{{Node: "cccc", Error: "refused"}}}, nil
}

// Show says which version is in force and who signed it, what every
// member last showed, any write that lost a tie, and the rights the
// troupe can never grant.
func TestTroupePolicyShowMakesDriftAndTiesVisible(t *testing.T) {
	now := time.Now()
	d := &fakePolicyDoor{show: rpc.TroupePolicyResponse{
		Troupe: "estate", Provider: "figaro",
		InForce:   &rpc.TroupePolicyVersion{Version: 3, Signer: "aaaa", By: "user:jack@auth.kelliher.info", At: now.UnixMilli()},
		Rivals:    []rpc.TroupePolicyVersion{{Version: 3, Root: true, By: "root:estate", At: now.UnixMilli()}},
		Refused:   []rpc.TroupePolicyVersion{{Version: 4, Signer: "eeee", By: "user:eve@auth.kelliher.info", At: now.UnixMilli()}},
		Nodes:     []rpc.TroupePolicySighting{{Node: "aaaa", Self: true, Policy: 3}, {Node: "bbbb", Policy: 2, SeenAt: now.Add(-3 * time.Hour).UnixMilli()}, {Node: "cccc"}},
		LocalOnly: []string{"use on sandbox:local", "admin on the troupe"},
		Body:      "[roles.r]\n",
	}}
	var out bytes.Buffer
	require.NoError(t, troupePolicyShow(context.Background(), d, &out, "estate", false))
	got := out.String()
	for _, want := range []string{
		"policy version 3, signed by node aaaa for user:jack@auth.kelliher.info",
		"lost the tie at version 3: signed by the root for root:estate",
		"not adopted, node eeee is not a setter of the version before it: version 4",
		"never granted by the troupe, only by each node's own policy: use on sandbox:local; admin on the troupe",
		"aaaa  3       this node",
		"bbbb  2       3h ago",
		"cccc  ?       not since this daemon started",
		"[roles.r]",
	} {
		require.Contains(t, got, want)
	}
}

func TestTroupePolicySetSendsTheFileAndSaysWhoHeard(t *testing.T) {
	path := filepath.Join(t.TempDir(), "troupe.toml")
	require.NoError(t, os.WriteFile(path, []byte("[roles.r]\n"), 0o600))
	d := &fakePolicyDoor{}
	var out bytes.Buffer
	require.NoError(t, troupePolicySet(context.Background(), d, nil, &out, "estate", path))
	require.Equal(t, "[roles.r]\n", d.setBody)
	require.Contains(t, out.String(), "troupe estate policy version 4, signed by aaaa")
	require.Contains(t, out.String(), "cccc  not reached: refused")

	require.NoError(t, troupePolicySet(context.Background(), d, strings.NewReader("[roles.s]\n"), &out, "estate", "-"))
	require.Equal(t, "[roles.s]\n", d.setBody)
}
