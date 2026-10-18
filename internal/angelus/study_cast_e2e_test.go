package angelus_test

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/sdk"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/figaro"
)

type studyClient struct {
	c    *sdk.Aria
	done chan struct{}
}

func dialNode(t *testing.T, a *angelus.Angelus, id string) *studyClient {
	t.Helper()
	sc := &studyClient{done: make(chan struct{}, 4)}
	c, err := sdk.DialAria(ariaAddr(a, id),
		func(method string, _ json.RawMessage) {
			if method == "turn.done" {
				select {
				case sc.done <- struct{}{}:
				default:
				}
			}
		})
	require.NoError(t, err)
	sc.c = c
	t.Cleanup(func() { c.Close() })
	return sc
}

func (sc *studyClient) turn(t *testing.T, ctx context.Context, text string) {
	t.Helper()
	_, _, err := sc.c.Qua(ctx, text, nil)
	require.NoError(t, err)
	select {
	case <-sc.done:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for turn.done")
	}
}

func (sc *studyClient) contextText(t *testing.T, ctx context.Context) string {
	t.Helper()
	resp, err := sc.c.Context(ctx)
	require.NoError(t, err)
	b, err := json.Marshal(resp.Messages)
	require.NoError(t, err)
	return string(b)
}

func (sc *studyClient) formValue(t *testing.T, ctx context.Context, key string) (string, bool) {
	t.Helper()
	resp, err := sc.c.Form(ctx)
	require.NoError(t, err)
	raw, ok := resp.Snapshot.Get(key)
	return string(raw), ok
}

func TestCastPointsRoleAndRegistersStudy(t *testing.T) {
	a, acli, ctx := daemonFixture(t)

	created, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	role, err := acli.FormCreate(ctx, "", nil, rawPatch(map[string]string{"name": `"supervisor"`}))
	require.NoError(t, err)

	fc := dialNode(t, a, created.FigaroID)
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cast, err := fc.c.Cast(callCtx, rpc.CastRequest{FormID: role.FormID})
	require.NoError(t, err)
	require.Equal(t, role.FormID, cast.RoleID)
	require.True(t, cast.Studied, "first cast should register the study")
	require.True(t, cast.Patched)

	rc := dialNode(t, a, role.FormID)
	target, ok := rc.formValue(t, callCtx, "target-aria")
	require.True(t, ok)
	require.Equal(t, `"`+created.FigaroID+`"`, target)

	// The study is on the board, not just in memory.
	studies, ok := fc.formValue(t, callCtx, "system.studies")
	require.True(t, ok, "system.studies missing from the caster's board")
	require.Contains(t, studies, role.FormID)

	// A second cast is idempotent on the study.
	cast, err = fc.c.Cast(callCtx, rpc.CastRequest{FormID: role.FormID})
	require.NoError(t, err)
	require.False(t, cast.Studied, "second cast claims a fresh study")
}

func TestCastMintsRoleBornCast(t *testing.T) {
	a, acli, ctx := daemonFixture(t)
	created, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)

	fc := dialNode(t, a, created.FigaroID)
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cast, err := fc.c.Cast(callCtx, rpc.CastRequest{
		RolePatch: rawPatch(map[string]string{"name": `"minted-role"`}),
	})
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(cast.RoleID, "@"), "minted role id %q", cast.RoleID)
	require.True(t, cast.Patched)
	require.True(t, cast.Studied)

	_, err = acli.Attach(ctx, cast.RoleID)
	require.NoError(t, err)
	rc := dialNode(t, a, cast.RoleID)
	target, ok := rc.formValue(t, callCtx, "target-aria")
	require.True(t, ok, "born-cast role lacks target-aria")
	require.Equal(t, `"`+created.FigaroID+`"`, target)

	m, err := a.Backend.Meta(cast.RoleID)
	require.NoError(t, err)
	require.Equal(t, "user:"+strconv.Itoa(os.Getuid()), m.MintedBy, "a born-cast role records who cast it")
}

func TestCastRefusesBoundTargets(t *testing.T) {
	a, acli, ctx := daemonFixture(t)
	a1, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	a2, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)

	fc := dialNode(t, a, a1.FigaroID)
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err = fc.c.Cast(callCtx, rpc.CastRequest{FormID: a2.FigaroID})
	require.Error(t, err)
	require.Contains(t, err.Error(), "figaro, not an unbound form")
}

// The study mark and position stamps are in the IR; none of the studied form's content is.
func TestStudyStampsIRAndBakesNothing(t *testing.T) {
	a, acli, ctx := daemonFixture(t)
	created, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	role, err := acli.FormCreate(ctx, "", nil, rawPatch(map[string]string{"name": `"watched"`}))
	require.NoError(t, err)

	fc := dialNode(t, a, created.FigaroID)
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	study, err := fc.c.Study(callCtx, role.FormID)
	require.NoError(t, err)
	require.Contains(t, study.Studies, role.FormID)

	// Through the hub: the agentless write path.
	rc := dialNode(t, a, role.FormID)
	_, err = rc.c.Set(callCtx, *rawPatch(map[string]string{"phase": `"canary"`}), 0)
	require.NoError(t, err)

	fc.turn(t, callCtx, "hello")
	text := fc.contextText(t, callCtx)
	require.Contains(t, text, `"study"`, "study mark missing from the IR")
	require.Contains(t, text, role.FormID)
	// "canary" lives only in the studied form's channel.
	require.NotContains(t, text, "system-reminder", "studied content baked into the IR")
	require.NotContains(t, text, "canary", "studied VALUE copied into the IR")
}

func TestDropEndsStampsAndStatesItself(t *testing.T) {
	a, acli, ctx := daemonFixture(t)
	created, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	role, err := acli.FormCreate(ctx, "", nil, rawPatch(map[string]string{"name": `"brief"`}))
	require.NoError(t, err)

	fc := dialNode(t, a, created.FigaroID)
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	_, err = fc.c.Study(callCtx, role.FormID)
	require.NoError(t, err)
	after, err := fc.c.Drop(callCtx, role.FormID)
	require.NoError(t, err)
	require.NotContains(t, after.Studies, role.FormID)

	rc := dialNode(t, a, role.FormID)
	_, err = rc.c.Set(callCtx, *rawPatch(map[string]string{"after": `"drop"`}), 0)
	require.NoError(t, err)

	fc.turn(t, callCtx, "hello")
	text := fc.contextText(t, callCtx)
	require.NotContains(t, text, `"drop"`, "a dropped study's value reached the IR")
	require.Contains(t, text, `"began":true`)
	require.Contains(t, text, `"began":false`)
}

// A naked figaro cannot wake; cast must be served from the hub with no agent.
func TestCastServesADormantFigaroWithoutWaking(t *testing.T) {
	a, acli, ctx := daemonFixture(t)

	naked, err := acli.FormBind(ctx, "null", nil, nil)
	if err != nil {
		t.Fatalf("bind null: %v", err)
	}
	role, err := acli.FormCreate(ctx, "", nil, rawPatch(map[string]string{"name": `"understudy"`}))
	if err != nil {
		t.Fatalf("form create: %v", err)
	}

	fc, err := sdk.DialAria(ariaAddr(a, naked.FigaroID), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer fc.Close()

	res, err := fc.Cast(ctx, rpc.CastRequest{FormID: role.FormID})
	if err != nil {
		t.Fatalf("cast a dormant naked figaro: %v", err)
	}
	if !res.Studied || !res.Patched {
		t.Fatalf("cast reported studied=%v patched=%v", res.Studied, res.Patched)
	}

	form, err := fc.Form(ctx)
	if err != nil {
		t.Fatalf("form: %v", err)
	}
	if studies := figaro.StudiesFromSnapshot(form.Snapshot); len(studies) != 1 || studies[0] != role.FormID {
		t.Fatalf("studies on the board: %v", studies)
	}

	if _, err := fc.Drop(ctx, role.FormID); err != nil {
		t.Fatalf("drop: %v", err)
	}
	form, err = fc.Form(ctx)
	if err != nil {
		t.Fatalf("form after drop: %v", err)
	}
	if studies := figaro.StudiesFromSnapshot(form.Snapshot); len(studies) != 0 {
		t.Fatalf("studies after drop: %v", studies)
	}
}
