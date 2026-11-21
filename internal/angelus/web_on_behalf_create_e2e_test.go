package angelus_test

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/jkauthz"
)

func (r onBehalfRig) delegatedCreate(t *testing.T, c *edgeClient, params map[string]any) string {
	t.Helper()
	reply := c.call(rpc.MethodCreate, params)
	require.Nil(t, reply.Error)
	var out rpc.CreateResponse
	require.NoError(t, json.Unmarshal(reply.Result, &out))
	require.NotEmpty(t, out.FigaroID)
	return out.FigaroID
}

func TestADelegatedCreateMintsATopLevelFigaroTheOwnerOwnsAsIfMadeInHisTerminal(t *testing.T) {
	r := onBehalfDoor(t)
	c, _, err := r.as(t, "figchat-gateway", "gluck")
	require.NoError(t, err)

	id := r.delegatedCreate(t, c, map[string]any{"outfits": []string{"mock"}, "cwd": ""})

	terminal, err := r.daemon.Backend.Meta(r.aria)
	require.NoError(t, err)
	web, err := r.daemon.Backend.Meta(id)
	require.NoError(t, err)
	require.NotEmpty(t, terminal.MintedBy)
	require.Equal(t, terminal.MintedBy, web.MintedBy, "a web-minted figaro is the owner's, not the gateway's or the web user's")
	require.Equal(t, terminal.MintedAtRoot, web.MintedAtRoot)
	require.True(t, web.MintedAtRoot)

	tl, ok := r.daemon.Backend.NodeLink(r.aria)
	require.True(t, ok)
	wl, ok := r.daemon.Backend.NodeLink(id)
	require.True(t, ok)
	require.Equal(t, tl.Parent, wl.Parent, "born where a terminal create of the same outfit is born")

	listed, err := r.owner.List(r.ctx)
	require.NoError(t, err)
	var ids []string
	for _, f := range listed.Figaros {
		ids = append(ids, f.ID)
	}
	require.Contains(t, ids, id, "the owner's list shows it")

	got := delegatedFormOf(t, c, id)
	cwd := got.Snapshot.Lookup("system.cwd")
	require.NotNil(t, cwd)
	wd, err := os.Getwd()
	require.NoError(t, err)
	require.Equal(t, wd, *cwd, "born in the daemon's default directory")
}

func TestADelegatedCreateAnswersItsFirstPrompt(t *testing.T) {
	r := onBehalfDoor(t)
	c, _, err := r.as(t, "figchat-gateway", "gluck")
	require.NoError(t, err)

	id := r.delegatedCreate(t, c, map[string]any{})
	reply := c.call(rpc.MethodQua, onAria(id, map[string]any{"text": "first words from the web"}))
	require.Nil(t, reply.Error)
	waitIdle(t, r.daemon, id)

	reply = c.call(rpc.MethodRead, onAria(id, nil))
	require.Nil(t, reply.Error)
	require.True(t, bytes.Contains(reply.Result, []byte("first words from the web")))
	require.True(t, bytes.Contains(reply.Result, []byte(`"42"`)), "the mock's answer is on the page: %s", reply.Result)
}

func TestADelegatedCreateIsHeldToTheDelegatedRules(t *testing.T) {
	r := onBehalfDoor(t)
	c, _, err := r.as(t, "figchat-gateway", "gluck")
	require.NoError(t, err)
	before, err := r.owner.List(r.ctx)
	require.NoError(t, err)

	for name, call := range map[string]struct {
		params map[string]any
		want   string
	}{
		"a system.ttl patch":       {map[string]any{"patch": ttlPatch()}, "system.ttl is daemon/owner-held"},
		"a name patch":             {map[string]any{"patch": plainPatch(t, map[string]string{"name": "web"})}, "names the form"},
		"an undeclared outfit":     {map[string]any{"outfits": []string{"nosuch"}}, `"nosuch" is not one`},
		"an outfit path":           {map[string]any{"outfits": []string{"../outfits/mock"}}, "may name declared outfits only"},
		"an absolute outfit path":  {map[string]any{"outfits": []string{r.dir + "/outfits/mock.toml"}}, "may name declared outfits only"},
		"an inline outfit":         {map[string]any{"outfits": []string{`{"system":{"ttl":"1s"}}`}}, "may name declared outfits only"},
		"a chosen cwd":             {map[string]any{"cwd": "/tmp"}, "may not choose a working directory"},
		"an outfit carrying scope": {map[string]any{"outfits": []string{"scoped"}}, "only the owner names an outfit that carries it"},
	} {
		reply := c.call(rpc.MethodCreate, call.params)
		require.NotNil(t, reply.Error, name)
		require.Contains(t, reply.Error.Message, call.want, name)
	}

	after, err := r.owner.List(r.ctx)
	require.NoError(t, err)
	require.Len(t, after.Figaros, len(before.Figaros), "no refused create left a figaro behind")
}

func TestADelegatedCallerIsNotTheOwnerAfterItCreates(t *testing.T) {
	r := onBehalfDoor(t)
	r.daemon.Owner = jkauthz.Principal{Kind: jkauthz.KindUser, ID: "gluck@issuer.test.figaro"}
	c, _, err := r.as(t, "figchat-gateway", "gluck")
	require.NoError(t, err)

	id := r.delegatedCreate(t, c, map[string]any{"outfits": []string{"mock"}})
	web, err := r.daemon.Backend.Meta(id)
	require.NoError(t, err)
	require.Equal(t, "user:gluck@issuer.test.figaro", web.MintedBy)

	reply := c.call(rpc.MethodCreate, map[string]any{"outfits": []string{"scoped"}})
	require.NotNil(t, reply.Error, "a delegated caller whose user is the owner principal still may not name an outfit that carries scopes")
	require.Contains(t, reply.Error.Message, "only the owner names an outfit that carries it")

	reply = c.call(rpc.MethodKill, onAria(id, nil))
	require.NotNil(t, reply.Error)
	require.Contains(t, reply.Error.Message, "acts for user:gluck@issuer.test.figaro")
}

func TestADelegatedCallerReadsTheOutfitList(t *testing.T) {
	r := onBehalfDoor(t)
	c, _, err := r.as(t, "figchat-gateway", "gluck")
	require.NoError(t, err)

	reply := c.call(rpc.MethodOutfits, map[string]any{})
	require.Nil(t, reply.Error)
	var got rpc.OutfitsResponse
	require.NoError(t, json.Unmarshal(reply.Result, &got))
	require.Equal(t, "mock", got.Default)
	require.Contains(t, got.Names, "mock")
	require.Contains(t, got.Names, "scoped")
}
