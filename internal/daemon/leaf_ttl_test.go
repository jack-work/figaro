package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jack-work/jkrpc"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
)

const day = 24 * time.Hour

func (n *ledgerNode) troupeLines(lines string) {
	body := fmt.Sprintf("[troupe]\nname = \"estate\"\nprovider = \"figaro\"\n%s\n[door.tls]\nlisten = %q\n", lines, n.addr)
	require.NoError(n.t, os.WriteFile(filepath.Join(n.cfgDir, "config.toml"), []byte(body), 0o600))
}

// preTTLSignRequest is troupe.sign's request as v0.53.2 and older decode it.
type preTTLSignRequest struct {
	Troupe    string            `json:"troupe"`
	Node      string            `json:"node"`
	PublicKey []byte            `json:"public_key"`
	Door      string            `json:"door"`
	Rejoin    *rpc.TroupeRejoin `json:"rejoin,omitempty"`
}

// beforeTTL makes a node's troupe.sign decode its request as a renewer from
// before ttl_ms did, so the field never reaches the issuer.
func beforeTTL(hs map[string]jkrpc.HandlerFunc) {
	sign := hs[rpc.MethodTroupeSign]
	hs[rpc.MethodTroupeSign] = func(ctx context.Context, params json.RawMessage) (any, error) {
		var old preTTLSignRequest
		if err := json.Unmarshal(params, &old); err != nil {
			return nil, err
		}
		params, err := json.Marshal(old)
		if err != nil {
			return nil, err
		}
		return sign(ctx, params)
	}
}

func renewedOverTheDoor(t *testing.T, signer func(*ledgerNode), leafLines, signerLines string) time.Duration {
	t.Helper()
	root := newEstateRoot(t)
	na := newLedgerNode(t, root)
	na.troupeLines(signerLines)
	signer(na)
	nb := newLeafNode(t, root, na, time.Now().Add(-6*day))
	nb.troupeLines(leafLines)
	ma := na.start()
	_, err := ma.Merge(nb.joinedBy(na)...)
	require.NoError(t, err)
	mb := nb.start()
	_, err = mb.Merge(ma.Entries()...)
	require.NoError(t, err)

	require.NoError(t, nb.a.Troupe.Ensure())
	cur, err := nb.a.Troupe.Current()
	require.NoError(t, err)
	return time.Until(cur.Leaf.NotAfter)
}

// A leaf-only node asks its renewer for its leaf_ttl over the node door and
// gets it, under the renewer's max_leaf_ttl.
func TestALeafOnlyNodeIsRenewedForItsLeafTTL(t *testing.T) {
	none := func(*ledgerNode) {}
	got := renewedOverTheDoor(t, none, `leaf_ttl = "30d"`, "")
	require.InDelta(t, float64(30*day), float64(got), float64(time.Minute))

	got = renewedOverTheDoor(t, none, `leaf_ttl = "30d"`, `max_leaf_ttl = "14d"`)
	require.InDelta(t, float64(14*day), float64(got), float64(time.Minute), "the renewer granted past its max_leaf_ttl")
}

// A node asking for 30d from a renewer that predates ttl_ms still gets a
// leaf: the renewer ignores the field and grants its seven days.
func TestANewRequesterRenewsFromARenewerThatPredatesTheField(t *testing.T) {
	old := func(n *ledgerNode) { n.handlers = beforeTTL }
	got := renewedOverTheDoor(t, old, `leaf_ttl = "30d"`, "")
	require.InDelta(t, float64(7*day), float64(got), float64(time.Minute))
}
