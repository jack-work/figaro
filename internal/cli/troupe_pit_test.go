package cli

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/config"
)

type stubBearer struct {
	token string
	err   error
}

func (b stubBearer) Resolve() (string, error) { return b.token, b.err }
func (b stubBearer) Invalidate(string) error  { return nil }

// A peer's form is reached through the door of the profile that answers for
// the peer's node, with that profile's own token; with no login the mount
// names the command that makes one, and a node no profile reaches is refused,
// not sent to this node's own troupe door.
func TestAPeerMountUsesTheDoorThatAnswersForItsNode(t *testing.T) {
	r := newRemoteRig(t, "[troupe]\nname = \"kelliher\"\n", map[string]string{"kelliher": "wss://fig.example"})
	r.own(nodeGluck)
	loaded, err := config.Load(r.cfg)
	require.NoError(t, err)
	in := &interactiveInput{loaded: loaded}
	opened := []transport.Endpoint{}
	probe := probeTroupeNode
	probeTroupeNode = func(ep transport.Endpoint) (string, error) {
		opened = append(opened, ep)
		return probe(ep)
	}

	r.signed["kelliher"] = false
	_, _, _, _, err = in.mountForm(nodeSpain + "/@arias")
	require.EqualError(t, err, "figaro troupe login kelliher")
	require.Empty(t, opened, "nothing is dialled without a login")

	r.signed["kelliher"] = true
	r.nodes["wss://fig.example"] = nodeSpain
	forgetDoors()
	_, _, _, _, err = in.mountForm(nodeThird + "/@arias")
	require.EqualError(t, err, "no troupe profile reaches node "+nodeThird)

	a, err := doorFor(nodeSpain + "/@arias")
	require.NoError(t, err)
	require.Equal(t, "wss://fig.example", a.Door.Address)
	require.Equal(t, "kelliher", bearerOf(t, a.Door))
}
