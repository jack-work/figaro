package angelus_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/transport"
)

// studyClient wraps a node endpoint: calls, plus a turn.done latch for
// the aria case.
type studyClient struct {
	c    *figaro.Client
	done chan struct{}
}

func dialNode(t *testing.T, ep rpc.Endpoint) *studyClient {
	t.Helper()
	sc := &studyClient{done: make(chan struct{}, 4)}
	c, err := figaro.DialClient(transport.Endpoint{Scheme: ep.Scheme, Address: ep.Address},
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

// A cast: the figaro studies the role and the role's target-aria points
// at it — one call, serialized in the actor loop, durable on the board.
func TestCastPointsRoleAndRegistersStudy(t *testing.T) {
	_, acli, ctx := daemonFixture(t)

	created, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	role, err := acli.FormCreate(ctx, "", nil, rawPatch(map[string]string{"name": `"supervisor"`}))
	require.NoError(t, err)

	fc := dialNode(t, created.Endpoint)
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cast, err := fc.c.Cast(callCtx, rpc.CastRequest{FormID: role.FormID})
	require.NoError(t, err)
	require.Equal(t, role.FormID, cast.RoleID)
	require.True(t, cast.Studied, "first cast should register the study")
	require.True(t, cast.Patched)

	// The role points here, durably (read through the role's own hub).
	rc := dialNode(t, role.Endpoint)
	target, ok := rc.formValue(t, callCtx, "target-aria")
	require.True(t, ok)
	require.Equal(t, `"`+created.FigaroID+`"`, target)

	// The study is on the BOARD (system.studies), not just in memory.
	studies, ok := fc.formValue(t, callCtx, "system.studies")
	require.True(t, ok, "system.studies missing from the caster's board")
	require.Contains(t, studies, role.FormID)

	// A second cast is idempotent on the study.
	cast, err = fc.c.Cast(callCtx, rpc.CastRequest{FormID: role.FormID})
	require.NoError(t, err)
	require.False(t, cast.Studied, "second cast claims a fresh study")
}

// cast with a role patch: the role is BORN cast — target-aria rides the
// birth patch, so there is no window where the role exists unpointed.
func TestCastMintsRoleBornCast(t *testing.T) {
	_, acli, ctx := daemonFixture(t)
	created, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)

	fc := dialNode(t, created.Endpoint)
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cast, err := fc.c.Cast(callCtx, rpc.CastRequest{
		RolePatch: rawPatch(map[string]string{"name": `"minted-role"`}),
	})
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(cast.RoleID, "@"), "minted role id %q", cast.RoleID)
	require.True(t, cast.Patched)
	require.True(t, cast.Studied)

	att, err := acli.Attach(ctx, cast.RoleID)
	require.NoError(t, err)
	rc := dialNode(t, rpc.Endpoint{Scheme: att.Endpoint.Scheme, Address: att.Endpoint.Address})
	target, ok := rc.formValue(t, callCtx, "target-aria")
	require.True(t, ok, "born-cast role lacks target-aria")
	require.Equal(t, `"`+created.FigaroID+`"`, target)
}

// A cast at a BOUND board is refused by name: roles are unbound only.
func TestCastRefusesBoundTargets(t *testing.T) {
	_, acli, ctx := daemonFixture(t)
	a1, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	a2, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)

	fc := dialNode(t, a1.Endpoint)
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err = fc.c.Cast(callCtx, rpc.CastRequest{FormID: a2.FigaroID})
	require.Error(t, err)
	require.Contains(t, err.Error(), "figaro, not an unbound form")
}

// The observation, pull-at-the-stamp: studying STATES itself in the IR
// (the StudyMark record), every subsequent IR record stamps the studied
// form's position, and NOTHING of the studied form's content is baked
// into this aria's records — the provider derives the fold from the
// stamps at translation time (pinned in provider's projection tests).
func TestStudyStampsIRAndBakesNothing(t *testing.T) {
	_, acli, ctx := daemonFixture(t)
	created, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	role, err := acli.FormCreate(ctx, "", nil, rawPatch(map[string]string{"name": `"watched"`}))
	require.NoError(t, err)

	fc := dialNode(t, created.Endpoint)
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	study, err := fc.c.Study(callCtx, role.FormID)
	require.NoError(t, err)
	require.Contains(t, study.Studies, role.FormID)

	// Patch the studied form THROUGH THE HUB (agentless write path).
	rc := dialNode(t, role.Endpoint)
	_, err = rc.c.Set(callCtx, *rawPatch(map[string]string{"phase": `"canary"`}), 0)
	require.NoError(t, err)

	fc.turn(t, callCtx, "hello")
	text := fc.contextText(t, callCtx)
	// The MARK is in the timeline: began-observing is a stated fact.
	require.Contains(t, text, `"study"`, "study mark missing from the IR")
	require.Contains(t, text, role.FormID)
	// The CONTENT is not: no baked reminder text, no copied value. The
	// studied form's channel is the only place "canary" lives.
	require.NotContains(t, text, "system-reminder", "studied content baked into the IR")
	require.NotContains(t, text, "canary", "studied VALUE copied into the IR")
}

// Drop ends the stamps and states itself: the observed set no longer
// includes the form, so later records carry no position for it, and the
// stopped-observing mark is in the timeline.
func TestDropEndsStampsAndStatesItself(t *testing.T) {
	_, acli, ctx := daemonFixture(t)
	created, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	role, err := acli.FormCreate(ctx, "", nil, rawPatch(map[string]string{"name": `"brief"`}))
	require.NoError(t, err)

	fc := dialNode(t, created.Endpoint)
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	_, err = fc.c.Study(callCtx, role.FormID)
	require.NoError(t, err)
	after, err := fc.c.Drop(callCtx, role.FormID)
	require.NoError(t, err)
	require.NotContains(t, after.Studies, role.FormID)

	rc := dialNode(t, role.Endpoint)
	_, err = rc.c.Set(callCtx, *rawPatch(map[string]string{"after": `"drop"`}), 0)
	require.NoError(t, err)

	fc.turn(t, callCtx, "hello")
	text := fc.contextText(t, callCtx)
	require.NotContains(t, text, `"drop"`, "a dropped study's value reached the IR")
	// Both marks stand: began and stopped.
	require.Contains(t, text, `"began":true`)
	require.Contains(t, text, `"began":false`)
}
