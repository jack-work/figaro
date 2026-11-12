package angelus_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jack-work/jkauthz"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/figaro/wire"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/turns"
	"github.com/jack-work/figaro/sdk"
)

// successionProvider reports whatever context each aria is told to have, and
// fails or holds a chosen aria from a chosen call on. An aria it was never told
// about is a successor.
type successionProvider struct {
	mu         sync.Mutex
	calls      map[string]int
	usage      map[string]int
	failFrom   map[string]int
	holdFrom   map[string]int
	release    chan struct{}
	known      map[string]bool
	failOthers bool
	holdOthers bool
}

func newSuccessionProvider() *successionProvider {
	return &successionProvider{calls: map[string]int{}, usage: map[string]int{}, failFrom: map[string]int{},
		holdFrom: map[string]int{}, known: map[string]bool{}, release: make(chan struct{})}
}

func (p *successionProvider) Name() string        { return "mock" }
func (p *successionProvider) Fingerprint() string { return "mock/succession" }
func (p *successionProvider) SetModel(string)     {}
func (p *successionProvider) Models(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}

func (p *successionProvider) Send(ctx context.Context, in provider.SendInput, bus provider.Bus) error {
	p.mu.Lock()
	p.calls[in.AriaID]++
	n, used := p.calls[in.AriaID], p.usage[in.AriaID]
	fail := p.failFrom[in.AriaID] > 0 && n >= p.failFrom[in.AriaID]
	hold := p.holdFrom[in.AriaID] > 0 && n >= p.holdFrom[in.AriaID]
	if !p.known[in.AriaID] {
		fail, hold = p.failOthers, p.holdOthers
	}
	release := p.release
	p.mu.Unlock()
	if hold {
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if fail {
		return errors.New("the provider is down")
	}
	msg := message.Message{
		Role:       message.RoleOutput,
		Content:    []message.Content{message.TextContent("ok")},
		StopReason: message.StopEnd,
		Usage:      &message.Usage{InputTokens: 10, CacheReadTokens: used, OutputTokens: 5},
	}
	bus.PushMessageEnd(string(msg.StopReason))
	bus.PushFigaro(msg)
	return nil
}

func (p *successionProvider) set(fn func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fn()
}

type successionRig struct {
	t    *testing.T
	a    *angelus.Angelus
	acli *sdk.Angelus
	ctx  context.Context
	hs   *angelus.Handlers
	p    *successionProvider

	stopped bool
}

func successionDaemon(t *testing.T) *successionRig {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(dir+"/outfits", 0o700))
	require.NoError(t, os.WriteFile(dir+"/outfits/mock.toml", []byte("[system]\nprovider = \"mock\"\nmodel = \"mock-model\"\n"), 0o600))
	require.NoError(t, os.WriteFile(dir+"/outfits/heir.toml", []byte("[system]\nprovider = \"mock\"\nmodel = \"heir-model\"\n"), 0o600))
	require.NoError(t, os.WriteFile(dir+"/outfits/tooled.toml", []byte("[system]\nprovider = \"mock\"\nmodel = \"heir-model\"\ntools = [\"bash\"]\n"), 0o600))
	require.NoError(t, os.WriteFile(dir+"/config.toml", []byte("default_outfit = \"mock\"\n"), 0o600))
	p := newSuccessionProvider()
	tree := t.TempDir()
	a, acli, ctx, hs := daemonWithProvider(t, dir, func(_ *angelus.Angelus, b *store.XwalBackend) {
		require.NoError(t, wire.Install(b.Store(), tree, wire.Capabilities{Trunks: true}))
	}, func() provider.Provider { return p })
	r := &successionRig{t: t, a: a, acli: acli, ctx: ctx, hs: hs, p: p}
	t.Cleanup(func() {
		p.set(func() {
			close(p.release)
			p.release = make(chan struct{})
			p.holdOthers = false
			p.holdFrom = map[string]int{}
		})
		if r.stopped {
			return
		}
		for range 3 {
			for _, id := range r.arias() {
				waitIdle(t, a, id)
			}
			hs.WaitSuccessionsForTest()
		}
	})
	return r
}

func (r *successionRig) aria(retirement map[string]any) string {
	r.t.Helper()
	created, err := r.acli.Create(r.ctx, dress(r.t, "mock"), nil)
	require.NoError(r.t, err)
	r.p.set(func() { r.p.known[created.FigaroID] = true })
	if retirement != nil {
		ownerSets(r.t, r.a, created.FigaroID, map[string]any{"system.retirement": retirement})
	}
	return created.FigaroID
}

func (r *successionRig) role(holder string, keys map[string]any) string {
	r.t.Helper()
	id, _, err := r.a.Backend.CreateForm("", *heldPatch(r.t, keys))
	require.NoError(r.t, err)
	var out rpc.CastResponse
	require.NoError(r.t, dialAria(r.t, r.a, holder, nil).Call(r.ctx, rpc.MethodCast, rpc.CastRequest{FormID: id}, &out))
	require.Equal(r.t, holder, r.holder(id))
	return id
}

func (r *successionRig) key(id, key string) string {
	r.t.Helper()
	snap, err := r.a.Backend.FormState(id)
	require.NoError(r.t, err)
	if v := snap.Lookup(key); v != nil {
		return *v
	}
	return ""
}

func (r *successionRig) has(id, key string) bool {
	r.t.Helper()
	snap, err := r.a.Backend.FormState(id)
	require.NoError(r.t, err)
	return snap.Has(key)
}

func (r *successionRig) holder(role string) string { return r.key(role, "target-aria") }

func (r *successionRig) heard(id string) string {
	r.t.Helper()
	log, err := r.a.Backend.OpenFigIR(id)
	require.NoError(r.t, err)
	var parts []string
	for _, e := range log.Read() {
		if e.Payload.Role == message.RoleInput {
			parts = append(parts, turns.Text(e.Payload))
		}
	}
	return strings.Join(parts, "\n")
}

func (r *successionRig) arias() []string {
	r.t.Helper()
	resp, err := r.acli.ListGlobal(r.ctx)
	require.NoError(r.t, err)
	var out []string
	for _, f := range resp.Figaros {
		if strings.HasPrefix(f.ID, "@") || f.ReapedAt != 0 {
			continue
		}
		snap, err := r.a.Backend.FormState(f.ID)
		if err != nil {
			continue
		}
		if v := snap.Lookup("aria_id"); v != nil && *v == f.ID {
			out = append(out, f.ID)
		}
	}
	return out
}

func (r *successionRig) retired(id string) bool {
	return listed(r.t, r.acli, r.ctx, id).RetiredAt != 0
}

func (r *successionRig) eventually(cond func() bool, msg string) {
	r.t.Helper()
	require.Eventually(r.t, cond, 10*time.Second, 5*time.Millisecond, msg)
}

func (r *successionRig) failed(old string) string {
	return r.key(old, "system.succession_failed")
}

const succeedAt = 75_000

func succeeding(extra map[string]any) map[string]any {
	out := map[string]any{"context_threshold": succeedAt, "ttl": "1h", "action": "succeed"}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func (r *successionRig) cross(old string) {
	r.t.Helper()
	r.p.set(func() { r.p.usage[old] = 100_000 })
	turnOn(r.t, r.a, old, "the work that crosses")
}

func TestSuccessionWarnsOnceAtEightyPercent(t *testing.T) {
	r := successionDaemon(t)
	old := r.aria(succeeding(nil))
	r.p.set(func() { r.p.usage[old] = 50_000 })
	turnOn(t, r.a, old, "below eighty")
	turnOn(t, r.a, old, "still below")
	require.NotContains(t, r.heard(old), "Start handing over", "warned below 80% of the threshold")

	r.p.set(func() { r.p.usage[old] = 61_000 })
	turnOn(t, r.a, old, "reaches eighty")
	require.NotContains(t, r.heard(old), "Start handing over", "the warning must ride the next prompt, not start a turn")
	turnOn(t, r.a, old, "the next prompt")
	turnOn(t, r.a, old, "and another")
	require.Equal(t, 1, strings.Count(r.heard(old), "Start handing over"), "one warning, once")
	require.Contains(t, r.heard(old), "75000")
	require.True(t, r.has(old, "system.succession_warned"))
	require.False(t, r.retired(old))
	require.Empty(t, r.hs.SuccessionPhase(old))
}

func TestSuccessionIsOffForActionRetire(t *testing.T) {
	r := successionDaemon(t)
	old := r.aria(map[string]any{"context_threshold": succeedAt, "action": "retire"})
	role := r.role(old, map[string]any{"name": "warden"})
	r.p.set(func() { r.p.usage[old] = 70_000 })
	turnOn(t, r.a, old, "above eighty")
	turnOn(t, r.a, old, "next")
	require.NotContains(t, r.heard(old), "Start handing over")
	r.cross(old)
	require.True(t, r.retired(old))
	require.Equal(t, old, r.holder(role))
	require.Len(t, r.arias(), 1, "action=retire minted a successor")
}

func TestSuccessionMovesEveryRoleThenRetires(t *testing.T) {
	r := successionDaemon(t)
	old := r.aria(succeeding(map[string]any{"ramp": "/etc/ramp.md", "ramp_turns": 2}))
	ownerSets(t, r.a, old, map[string]any{"system.model": "lane-model", "system.thinking_effort": "high", "mantra": "tend the vines"})
	warden := r.role(old, map[string]any{"name": "warden", "handoff": "/notes/warden.md"})
	scribe := r.role(old, map[string]any{"name": "scribe", "succession-policy": map[string]any{"handoff-key": "ledger"}, "ledger": "/notes/scribe.md"})
	turnOn(t, r.a, old, "early chatter")
	r.p.set(func() { r.p.holdOthers = true })
	r.cross(old)

	r.eventually(func() bool { return r.hs.SuccessionPhase(old) == "ramping" }, "the succession never reached its ramp")
	asked := r.heard(old)
	require.Contains(t, asked, "write your handoff now")
	require.Contains(t, asked, warden+" (warden): handoff at /notes/warden.md")
	require.Contains(t, asked, scribe+" (scribe): handoff at /notes/scribe.md", "the role's own handoff-key was not followed")
	require.Equal(t, 1, strings.Count(asked, "write your handoff now"))
	require.NotContains(t, asked, "Start handing over", "the final prompt restates the warning; it must not also carry it")
	require.True(t, r.has(old, "system.succession_warned"))
	require.Equal(t, "tend the vines", r.key(old, "mantra"), "the warning's bookkeeping must not cost the prompt its other form writes")

	next := r.hs.SuccessorOf(old)
	require.NotEmpty(t, next)
	require.Equal(t, next, r.holder(warden))
	require.Equal(t, next, r.holder(scribe))
	require.False(t, r.retired(old), "the predecessor retired before its successor's first turn ended")
	require.Equal(t, "lane-model", r.key(next, "system.model"), "a roleless outfit loses the model the lane set after birth")
	require.Equal(t, "high", r.key(next, "system.thinking_effort"))
	require.Equal(t, "0", r.key(next, "system.ttl"))
	snap, err := r.a.Backend.FormState(next)
	require.NoError(t, err)
	want, err := r.a.Backend.FormState(old)
	require.NoError(t, err)
	mine, _ := snap.Get("system.retirement")
	theirs, _ := want.Get("system.retirement")
	require.JSONEq(t, string(theirs), string(mine), "the successor must carry the same retirement")

	r.eventually(func() bool { return strings.Contains(r.heard(next), "You succeed figaro "+old) }, "the successor was never briefed")
	brief := r.heard(next)
	require.Contains(t, brief, "Read /etc/ramp.md first")
	require.Contains(t, brief, warden)
	require.Contains(t, brief, "/notes/scribe.md")
	require.Contains(t, brief, "the work that crosses", "the predecessor's last turns are quoted")
	require.NotContains(t, brief, "early chatter", "ramp_turns = 2 bounds the quote")

	r.p.set(func() { close(r.p.release); r.p.release = make(chan struct{}); r.p.holdOthers = false })
	r.eventually(func() bool { return r.retired(old) }, "the predecessor never retired")
	require.Equal(t, "context", retiredKey(t, r.a, old))
	require.Equal(t, "SUCCEEDED: tend the vines", r.key(old, "mantra"))
	r.eventually(func() bool { return r.hs.SuccessionPhase(old) == "" }, "a finished succession was never forgotten")
	require.Empty(t, r.failed(old))
	require.Equal(t, next, r.holder(warden))
	require.False(t, r.retired(next))
	require.ErrorContains(t, promptOn(r.ctx, r.a, old, "after"), "retired")
}

func TestSuccessionKeepsTheSuccessorNarrow(t *testing.T) {
	r := successionDaemon(t)
	old := r.aria(succeeding(nil))
	ownerSets(t, r.a, old, map[string]any{"system.scopes": []string{"send:*"}, "system.model": "lane-model"})
	r.role(old, map[string]any{"name": "warden", "succession-policy": map[string]any{"outfit": "heir"}})
	r.cross(old)
	r.eventually(func() bool { return r.retired(old) }, "the predecessor never retired")
	var next string
	for _, id := range r.arias() {
		if id != old {
			next = id
		}
	}
	require.NotEmpty(t, next)
	require.Equal(t, "heir-model", r.key(next, "system.model"), "the role's succession-policy.outfit dresses the successor")
	snap, err := r.a.Backend.FormState(next)
	require.NoError(t, err)
	scopes, ok := snap.Get("system.scopes")
	require.True(t, ok, "the successor came out wider than its predecessor")
	require.JSONEq(t, `["send:*"]`, string(scopes))
}

func (r *successionRig) heldAtItsRamp(old string) string {
	r.t.Helper()
	r.p.set(func() { r.p.holdOthers = true })
	r.cross(old)
	r.eventually(func() bool { return r.hs.SuccessionPhase(old) == "ramping" }, "the succession never reached its ramp")
	return r.hs.SuccessorOf(old)
}

// Inspector XV, C1: narrowing is the intersection down the ancestry, and the
// successor is born outside the predecessor's. What the ancestors took away
// has to travel in the successor's own key.
func TestSuccessionKeepsInheritedNarrowing(t *testing.T) {
	r := successionDaemon(t)
	parent := r.aria(nil)
	turnOn(t, r.a, parent, "before the fork")
	old := r.forkOf(parent)
	ownerSets(t, r.a, old, map[string]any{"system.retirement": succeeding(nil)})
	ownerSets(t, r.a, parent, map[string]any{"system.scopes": []string{"send:*"}})
	require.False(t, r.has(old, "system.scopes"), "the predecessor is narrowed by its own key, not its parent's")
	before, held := r.a.Policies.Effective(old)
	require.True(t, held)
	require.Equal(t, []string{"send:*"}, before.Strings())

	next := r.heldAtItsRamp(old)
	after, held := r.a.Policies.Effective(next)
	require.True(t, held)
	require.Equal(t, []string{"send:*"}, after.Strings(), "the successor left its predecessor's ancestors and their narrowing behind")
}

func (r *successionRig) forkOf(parent string) string {
	r.t.Helper()
	fr, err := r.acli.Fork(r.ctx, parent, 0, 0, nil, nil)
	require.NoError(r.t, err)
	r.p.set(func() { r.p.known[fr.Alternative] = true })
	return fr.Alternative
}

func (r *successionRig) maySend(figaro, to string) bool {
	owner := jkauthz.Subject{Principal: r.a.Peercred.Self()}
	return r.a.Policies.AllowFigaro(owner, figaro, rpc.VerbSend, authz.Aria(to)).Allowed
}

func (r *successionRig) ownScopes(id string) string {
	r.t.Helper()
	snap, err := r.a.Backend.FormState(id)
	require.NoError(r.t, err)
	raw, _ := snap.Get(form.ScopesKey)
	return string(raw)
}

// Inspector XV's ruling: what the predecessor held over itself the successor
// holds over itself, and nothing over the retired predecessor.
func TestSuccessionGivesTheSuccessorItsOwnSelf(t *testing.T) {
	r := successionDaemon(t)
	parent := r.aria(nil)
	turnOn(t, r.a, parent, "before the fork")
	old := r.forkOf(parent)
	ownerSets(t, r.a, old, map[string]any{"system.retirement": succeeding(nil), "system.scopes": []string{"*:lineage:self"}})
	ownerSets(t, r.a, parent, map[string]any{"system.scopes": []string{"send:*"}})
	stranger := r.aria(nil)
	require.True(t, r.maySend(old, old))
	require.False(t, r.maySend(old, stranger))

	next := r.heldAtItsRamp(old)
	require.JSONEq(t, `["send:lineage:self"]`, r.ownScopes(next))
	require.True(t, r.maySend(next, next), "the successor cannot reach itself as its predecessor could")
	require.False(t, r.maySend(next, old), "the successor kept its predecessor's hold over the predecessor")
	require.False(t, r.maySend(next, stranger))
}

func TestSuccessionLeavesAnAncestorsLineageLiteral(t *testing.T) {
	r := successionDaemon(t)
	grandparent := r.aria(nil)
	turnOn(t, r.a, grandparent, "before the forks")
	parent := r.forkOf(grandparent)
	old := r.forkOf(parent)
	ownerSets(t, r.a, old, map[string]any{"system.retirement": succeeding(nil)})
	ownerSets(t, r.a, parent, map[string]any{"system.scopes": []string{"send:lineage:" + grandparent}})
	require.True(t, r.maySend(old, grandparent))

	next := r.heldAtItsRamp(old)
	require.JSONEq(t, `["send:lineage:`+grandparent+`"]`, r.ownScopes(next))
	require.True(t, r.maySend(next, grandparent))
	require.False(t, r.maySend(next, next), "an ancestor's lineage was rewritten into the successor's own")
}

func TestSuccessionRefusesScopesItCannotRead(t *testing.T) {
	r := successionDaemon(t)
	old := r.aria(succeeding(nil))
	snap, err := r.a.Backend.FormState(old)
	require.NoError(t, err)
	_, err = r.a.Backend.ApplyForm(old, form.Build(snap, map[string]json.RawMessage{
		form.ScopesKey: json.RawMessage(`["send:*"]`), form.RetiredGrantsKey: json.RawMessage(`["*:*"]`),
	}, nil))
	require.NoError(t, err)
	r.cross(old)
	r.requireUnretiredAndPaged(old, "", "read the predecessor's scopes")
	require.Len(t, r.arias(), 1, "a successor was minted for a predecessor whose scopes nobody could read")
}

// Inspector XV, C2: the tool allowlist is owner-held and per figaro. The
// outfit the role names must not hand back what the owner took away.
func TestSuccessionKeepsThePredecessorsTools(t *testing.T) {
	r := successionDaemon(t)
	old := r.aria(succeeding(nil))
	ownerSets(t, r.a, old, map[string]any{"system.tools": []string{}})
	r.role(old, map[string]any{"name": "warden", "succession-policy": map[string]any{"outfit": "tooled"}})
	next := r.heldAtItsRamp(old)
	require.Equal(t, "heir-model", r.key(next, "system.model"))
	snap, err := r.a.Backend.FormState(next)
	require.NoError(t, err)
	tools, ok := snap.Get("system.tools")
	require.True(t, ok)
	require.JSONEq(t, `[]`, string(tools), "the successor took the outfit's tools over its predecessor's allowlist")
}

func TestSuccessionKeepsThePredecessorsSandbox(t *testing.T) {
	r := successionDaemon(t)
	old := r.aria(succeeding(nil))
	ownerSets(t, r.a, old, map[string]any{"system.sandbox": "local"})
	next := r.heldAtItsRamp(old)
	require.Equal(t, "local", r.key(next, "system.sandbox"), "the successor does not carry its predecessor's sandbox key")
}

func TestSuccessionDrawsTheSuccessorBesideIt(t *testing.T) {
	r := successionDaemon(t)
	old := r.aria(succeeding(nil))
	parent := r.aria(nil)
	require.NoError(t, r.a.Backend.HierarchyEngine().WriteDrawn(map[string]string{old: parent}, nil))
	r.p.set(func() { r.p.holdOthers = true })
	r.cross(old)
	r.eventually(func() bool { return r.hs.SuccessionPhase(old) == "ramping" }, "the succession never reached its ramp")
	under, _ := r.a.Backend.HierarchyEngine().Drawn(r.hs.SuccessorOf(old))
	require.Equal(t, parent, under)
}

func (r *successionRig) requireUnretiredAndPaged(old, king, why string, roles ...string) {
	r.t.Helper()
	r.eventually(func() bool { return r.failed(old) != "" }, "the failure was never recorded")
	require.Contains(r.t, r.failed(old), why)
	require.False(r.t, r.retired(old), "a failed succession retired the aria")
	r.eventually(func() bool { return r.hs.SuccessionPhase(old) == "" }, "a failed succession was never forgotten")
	for _, role := range roles {
		require.Equal(r.t, old, r.holder(role), "a failed succession left a seat away from the predecessor")
	}
	if king != "" {
		r.eventually(func() bool { return strings.Contains(r.heard(king), "could not hand over") }, "nobody was paged")
		require.Contains(r.t, r.heard(king), old)
		require.Contains(r.t, r.heard(king), why)
	}
}

func TestSuccessionFailedHandoffTurnPages(t *testing.T) {
	r := successionDaemon(t)
	king := r.aria(nil)
	old := r.aria(succeeding(map[string]any{"page": king}))
	role := r.role(old, map[string]any{"name": "warden"})
	r.p.set(func() { r.p.failFrom[old] = 2 })
	r.cross(old)
	r.requireUnretiredAndPaged(old, king, "the final handoff turn failed", role)
	require.Len(t, r.arias(), 2, "a successor was minted after a failed handoff turn")
}

func TestSuccessionFailureIsStickyUntilCleared(t *testing.T) {
	r := successionDaemon(t)
	old := r.aria(succeeding(nil))
	r.p.set(func() { r.p.failFrom[old] = 2 })
	r.cross(old)
	r.requireUnretiredAndPaged(old, "", "the final handoff turn failed")
	r.p.set(func() { r.p.failFrom[old] = 0 })
	fr, err := r.acli.Fork(r.ctx, old, 0, 0, nil, nil)
	require.NoError(t, err)
	require.Empty(t, r.failed(fr.Alternative), "a fork inherited the failure and would never hand over")
	turnOn(t, r.a, old, "carry on past the threshold")
	waitIdle(t, r.a, old)
	require.Equal(t, 1, strings.Count(r.heard(old), "write your handoff now"), "a failed succession started again on its own")
	require.Len(t, r.arias(), 2)

	var out rpc.SetResponse
	require.NoError(t, dialAria(t, r.a, old, nil).Call(r.ctx, rpc.MethodSet,
		rpc.SetRequest{Patch: form.Build(form.Snapshot{}, nil, []string{"system.succession_failed"})}, &out))
	turnOn(t, r.a, old, "try again")
	r.eventually(func() bool { return r.retired(old) }, "clearing the failure did not let the succession run")
}

func TestSuccessionMintFailurePagesDrawnParent(t *testing.T) {
	r := successionDaemon(t)
	king := r.aria(nil)
	old := r.aria(succeeding(nil))
	court := r.role(king, map[string]any{"name": "court"})
	require.NoError(t, r.a.Backend.HierarchyEngine().WriteDrawn(map[string]string{old: court}, nil))
	role := r.role(old, map[string]any{"name": "warden", "succession-policy": map[string]any{"outfit": "no-such-outfit"}})
	r.cross(old)
	r.requireUnretiredAndPaged(old, king, "mint the successor", role)
	require.Len(t, r.arias(), 2)
}

func TestSuccessionRefusesDisagreeingOutfits(t *testing.T) {
	r := successionDaemon(t)
	king := r.aria(nil)
	old := r.aria(succeeding(map[string]any{"page": king}))
	a := r.role(old, map[string]any{"succession-policy": map[string]any{"outfit": "heir"}})
	b := r.role(old, map[string]any{"succession-policy": map[string]any{"outfit": "mock"}})
	r.cross(old)
	r.requireUnretiredAndPaged(old, king, "roles disagree on the successor's outfit", a, b)
	require.Contains(t, r.failed(old), `"heir"`)
	require.Contains(t, r.failed(old), `"mock"`)
	require.NotContains(t, r.heard(old), "write your handoff now", "the refusal must come before the final turn is spent")
}

func TestSuccessionRefusedCastGivesSeatsBack(t *testing.T) {
	r := successionDaemon(t)
	king := r.aria(nil)
	old := r.aria(succeeding(map[string]any{"page": king}))
	first := r.role(old, map[string]any{"name": "first"})
	second := r.role(old, map[string]any{"name": "second"})
	refused := second
	if second < first {
		refused = first
	}
	r.hs.RefuseSuccessionCastsForTest(func(role string) error {
		if role == refused {
			return errors.New("the owner says no")
		}
		return nil
	})
	r.cross(old)
	r.requireUnretiredAndPaged(old, king, "the owner says no", first, second)
	r.eventually(func() bool { return len(r.arias()) == 2 }, "the unprompted successor was kept")
}

func TestSuccessionFailedRampGivesSeatsBack(t *testing.T) {
	r := successionDaemon(t)
	king := r.aria(nil)
	old := r.aria(succeeding(map[string]any{"page": king}))
	role := r.role(old, map[string]any{"name": "warden"})
	r.p.set(func() { r.p.failOthers = true })
	r.cross(old)
	r.requireUnretiredAndPaged(old, king, "failed", role)
	require.Contains(t, r.failed(old), "the first turn of successor")
	var next string
	for _, id := range r.arias() {
		if id != old && id != king {
			next = id
		}
	}
	require.NotEmpty(t, next, "the failed successor must be kept for the post-mortem")
	require.Equal(t, "24h0m0s", r.key(next, "system.ttl"))
}

func TestSuccessionHandoffTimeoutPages(t *testing.T) {
	r := successionDaemon(t)
	king := r.aria(nil)
	old := r.aria(succeeding(map[string]any{"page": king, "timeout": "10m"}))
	role := r.role(old, map[string]any{"name": "warden"})
	r.p.set(func() { r.p.holdFrom[old] = 2; r.p.usage[old] = 100_000 })
	require.NoError(t, promptOn(r.ctx, r.a, old, "the work that crosses"))
	r.eventually(func() bool { return r.hs.SuccessionPhase(old) == "handoff" }, "the succession never began")
	r.hs.SweepSuccessionsAt(time.Now().Add(9 * time.Minute))
	require.Equal(t, "handoff", r.hs.SuccessionPhase(old), "timed out before its deadline")
	r.hs.SweepSuccessionsAt(time.Now().Add(11 * time.Minute))
	r.requireUnretiredAndPaged(old, king, "the final handoff turn did not end before the timeout", role)
	r.p.set(func() { close(r.p.release); r.p.release = make(chan struct{}) })
	waitIdle(t, r.a, old)
	require.Len(t, r.arias(), 2, "a handoff turn that ended after its timeout still minted a successor")
}

func TestSuccessionRampTimeoutGivesSeatsBack(t *testing.T) {
	r := successionDaemon(t)
	king := r.aria(nil)
	old := r.aria(succeeding(map[string]any{"page": king}))
	role := r.role(old, map[string]any{"name": "warden"})
	r.p.set(func() { r.p.holdOthers = true })
	r.cross(old)
	r.eventually(func() bool { return r.hs.SuccessionPhase(old) == "ramping" }, "the succession never reached its ramp")
	require.NotEqual(t, old, r.holder(role))
	r.hs.SweepSuccessionsAt(time.Now().Add(31 * time.Minute))
	r.requireUnretiredAndPaged(old, king, "the successor's first turn did not end before the timeout", role)
}

func TestSuccessionsInFlightAreInTheCatalog(t *testing.T) {
	r := successionDaemon(t)
	old := r.aria(succeeding(nil))
	r.p.set(func() { r.p.holdOthers = true })
	r.cross(old)
	r.eventually(func() bool { return r.hs.SuccessionPhase(old) == "ramping" }, "the succession never reached its ramp")
	var out rpc.FormResponse
	require.NoError(t, dialAria(t, r.a, rpc.SuccessionsFormID, nil).Call(r.ctx, rpc.MethodForm, rpc.FormRequest{}, &out))
	raw, _ := json.Marshal(out)
	require.Contains(t, string(raw), old)
	require.Contains(t, string(raw), "ramping")
}

// The slot pool mints its own successors: its outfit, its scopes, its bound.
func TestSuccessionMinterMintsWhatItClaims(t *testing.T) {
	r := successionDaemon(t)
	old := r.aria(succeeding(nil))
	other := r.aria(succeeding(nil))
	role := r.role(old, map[string]any{"name": "lease", "succession-policy": map[string]any{"outfit": "mock"}})
	var asked angelus.SuccessorMint
	var pooled string
	r.a.SuccessorMinter = func(ctx context.Context, m angelus.SuccessorMint) (angelus.MintedSuccessor, bool, error) {
		if m.Predecessor != old {
			return angelus.MintedSuccessor{}, false, nil
		}
		asked = m
		created, err := r.acli.Create(ctx, dress(t, "heir"), nil)
		if err != nil {
			return angelus.MintedSuccessor{}, true, err
		}
		pooled = created.FigaroID
		return angelus.MintedSuccessor{ID: pooled}, true, nil
	}
	r.cross(old)
	r.eventually(func() bool { return r.retired(old) }, "the predecessor never retired")
	require.Equal(t, pooled, r.holder(role), "the minter's figaro did not take the seat")
	require.Equal(t, "heir-model", r.key(pooled, "system.model"), "the daemon minted over the minter")
	require.Equal(t, []string{role}, asked.Roles)
	require.Equal(t, []string{"mock"}, asked.Default.Outfits)
	require.Contains(t, r.heard(pooled), "You succeed figaro "+old)
	require.Len(t, r.arias(), 3)

	r.cross(other)
	r.eventually(func() bool { return r.retired(other) }, "an aria the minter does not claim must be minted by the daemon")
	require.Len(t, r.arias(), 4)
}

func TestSuccessionMinterRefusalPages(t *testing.T) {
	r := successionDaemon(t)
	king := r.aria(nil)
	old := r.aria(succeeding(map[string]any{"page": king}))
	role := r.role(old, map[string]any{"name": "lease"})
	r.a.SuccessorMinter = func(context.Context, angelus.SuccessorMint) (angelus.MintedSuccessor, bool, error) {
		return angelus.MintedSuccessor{}, true, errors.New("4 successions a day, all spent")
	}
	r.cross(old)
	r.requireUnretiredAndPaged(old, king, "4 successions a day, all spent", role)
	require.Len(t, r.arias(), 2)
}

// A lease holds at most two figaros: the old one goes when the new one has ramped, not a ttl later.
func TestSuccessionMinterMayPurgePredecessor(t *testing.T) {
	r := successionDaemon(t)
	old := r.aria(succeeding(nil))
	role := r.role(old, map[string]any{"name": "lease"})
	var pooled string
	r.a.SuccessorMinter = func(ctx context.Context, m angelus.SuccessorMint) (angelus.MintedSuccessor, bool, error) {
		created, err := r.acli.Create(ctx, dress(t, "heir"), nil)
		if err != nil {
			return angelus.MintedSuccessor{}, true, err
		}
		pooled = created.FigaroID
		return angelus.MintedSuccessor{ID: pooled, PurgePredecessor: true}, true, nil
	}
	r.p.set(func() { r.p.holdOthers = true })
	r.cross(old)
	r.eventually(func() bool { return r.hs.SuccessionPhase(old) == "ramping" }, "the succession never reached its ramp")
	require.Contains(t, r.arias(), old, "the predecessor was purged before its successor's first turn ended")

	r.p.set(func() { close(r.p.release); r.p.release = make(chan struct{}); r.p.holdOthers = false })
	r.eventually(func() bool { return r.hs.SuccessionPhase(old) == "" }, "the succession never finished")
	r.eventually(func() bool {
		for _, id := range r.arias() {
			if id == old {
				return false
			}
		}
		return true
	}, "the predecessor was kept")
	require.Equal(t, pooled, r.holder(role))
	require.Equal(t, []string{pooled}, r.arias())
}

// A predecessor that cannot be purged (here it has a live branch) must not
// linger unnoticed: it retires with its ttl and the page says the purge failed.
func TestSuccessionFailedPurgeRetiresAndPages(t *testing.T) {
	r := successionDaemon(t)
	king := r.aria(nil)
	old := r.aria(succeeding(map[string]any{"page": king}))
	role := r.role(old, map[string]any{"name": "lease"})
	var pooled string
	r.a.SuccessorMinter = func(ctx context.Context, m angelus.SuccessorMint) (angelus.MintedSuccessor, bool, error) {
		created, err := r.acli.Create(ctx, dress(t, "heir"), nil)
		if err != nil {
			return angelus.MintedSuccessor{}, true, err
		}
		pooled = created.FigaroID
		return angelus.MintedSuccessor{ID: pooled, PurgePredecessor: true}, true, nil
	}
	r.p.set(func() { r.p.holdOthers = true })
	r.cross(old)
	r.eventually(func() bool { return r.hs.SuccessionPhase(old) == "ramping" }, "the succession never reached its ramp")
	_, err := r.acli.Fork(r.ctx, old, 0, 0, nil, nil)
	require.NoError(t, err)

	r.p.set(func() { close(r.p.release); r.p.release = make(chan struct{}); r.p.holdOthers = false })
	r.eventually(func() bool { return r.hs.SuccessionPhase(old) == "" }, "the succession never finished")
	require.Contains(t, r.arias(), old, "a predecessor with a live branch was purged")
	require.True(t, r.retired(old), "an unpurged predecessor was left unretired")
	require.Equal(t, pooled, r.holder(role))
	require.Empty(t, r.failed(old), "the handover itself succeeded")
	r.eventually(func() bool { return strings.Contains(r.heard(king), "could not be purged") }, "nobody was told the purge failed")
	require.Contains(t, r.heard(king), old)
	require.Contains(t, r.heard(king), pooled)
}

// A prompt queued before the threshold was crossed runs first. Its turn ending
// is not the handoff: minting then would give the successor a predecessor that
// has not written anything down yet.
func TestSuccessionWaitsForTheHandoffTurnItself(t *testing.T) {
	r := successionDaemon(t)
	old := r.aria(succeeding(nil))
	calls := func() int {
		r.p.mu.Lock()
		defer r.p.mu.Unlock()
		return r.p.calls[old]
	}
	release := func() { r.p.set(func() { close(r.p.release); r.p.release = make(chan struct{}) }) }
	r.p.set(func() { r.p.holdFrom[old] = 1; r.p.usage[old] = 100_000 })
	require.NoError(t, promptOn(r.ctx, r.a, old, "the work that crosses"))
	r.eventually(func() bool { return calls() == 1 }, "the crossing turn never started")
	require.NoError(t, promptOn(r.ctx, r.a, old, "queued before the threshold"))
	// The succession is held until the queued turn has started: begun sooner,
	// its handoff prompt joins the queued one in a single turn and there is no
	// queued-only turn to tell apart.
	begin := r.hs.HoldSuccessionsForTest()
	release()
	r.eventually(func() bool { return calls() == 2 }, "the queued prompt never ran")
	begin()
	r.eventually(func() bool { return r.hs.SuccessionPhase(old) == "handoff" }, "the succession never began")
	release()
	r.eventually(func() bool { return calls() == 3 }, "the handoff turn never started")
	require.Equal(t, 1, strings.Count(r.heard(old), "write your handoff now"), "the third turn must be the handoff turn")
	time.Sleep(100 * time.Millisecond)
	r.hs.WaitSuccessionsForTest()
	require.Equal(t, "handoff", r.hs.SuccessionPhase(old), "the queued prompt's turn was taken for the handoff turn")
	require.Len(t, r.arias(), 1, "a successor was minted before the handoff turn ended")
	r.p.set(func() { r.p.holdFrom[old] = 0 })
	release()
	r.eventually(func() bool { return r.retired(old) }, "the predecessor never retired")
}

// El Inspector XV: a succession takes no new step (mint, cast, prompt) once
// the stop has begun. Here the handoff turn ends just before the stop and its
// succession reaches the mint step after the seal: it mints nothing, and the
// record stays at the handoff phase for the next start to resume or time out.
func TestASuccessionAtItsMintStepWhenTheStopBeginsMintsNothing(t *testing.T) {
	logs := captureLogs(t)
	r := successionDaemon(t)
	old := r.aria(succeeding(nil))
	r.role(old, map[string]any{"name": "warden"})
	var mints atomic.Int32
	r.a.SuccessorMinter = func(context.Context, angelus.SuccessorMint) (angelus.MintedSuccessor, bool, error) {
		mints.Add(1)
		return angelus.MintedSuccessor{}, false, nil
	}
	r.p.set(func() { r.p.holdFrom[old] = 2; r.p.usage[old] = 100_000 })
	require.NoError(t, promptOn(r.ctx, r.a, old, "the work that crosses"))
	r.eventually(func() bool { return r.hs.SuccessionPhase(old) == "handoff" }, "the succession never began")

	letGo := r.hs.HoldSuccessionsForTest()
	r.p.set(func() { close(r.p.release); r.p.release = make(chan struct{}); r.p.holdFrom[old] = 0 })
	r.eventually(func() bool {
		for _, out := range r.a.OutForTest() {
			if out == "succession" {
				return true
			}
		}
		return false
	}, "canary: the handoff turn's end never started its succession step")
	waitIdle(t, r.a, old)

	r.stopped = true
	stopped := make(chan struct{})
	go func() { r.a.Shutdown(time.Second); close(stopped) }()
	r.eventually(r.a.StoppingForTest, "the stop never sealed")
	letGo()
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("Shutdown never returned")
	}
	require.Zero(t, mints.Load(), "a succession minted a successor after the stop began")
	require.Contains(t, logs.String(), "succession paused by the stop")
	require.Contains(t, logs.String(), "before=mint")
	require.NotContains(t, logs.String(), "successor minted and cast")
	require.Equal(t, "handoff", r.hs.SuccessionPhase(old), "the record was not left for the next start")
}
