package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/arialog"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/figaro/wire"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/sdk"
)

func succeedArgs(t *testing.T, argv ...string) (succeedPlan, error) {
	t.Helper()
	rc, err := buildRouter("figaro", nil).Parse("succeed", argv)
	if err != nil {
		return succeedPlan{}, err
	}
	return succeedPlanFrom(rc)
}

func TestSucceedGrammar(t *testing.T) {
	p, err := succeedArgs(t, "@warden", "--title", "warden II", "--", "take", "over")
	if err != nil {
		t.Fatal(err)
	}
	if p.role != "@warden" || p.prompt != "take over" || p.title != "warden II" {
		t.Fatalf("plan = %+v", p)
	}
	if p.turn != 2 || p.ttl != 10*time.Minute || p.abdication != abdicateRetire {
		t.Fatalf("defaults: turn %d ttl %s abdication %q", p.turn, p.ttl, p.abdication)
	}
}

// The prompt is everything past --, including words that look like flags or roles.
func TestSucceedPromptKeepsFlagsAndRolesAfterTheSeparator(t *testing.T) {
	p, err := succeedArgs(t, "--ttl", "0", "@warden", "--", "--turn", "@other")
	if err != nil {
		t.Fatal(err)
	}
	if p.role != "@warden" || p.prompt != "--turn @other" || p.ttl != 0 {
		t.Fatalf("plan = %+v", p)
	}
}

func TestSucceedRefusals(t *testing.T) {
	for name, argv := range map[string][]string{
		"no prompt":              {"@warden"},
		"no role":                {"--", "go"},
		"two roles":              {"@a", "@b", "--", "go"},
		"turn zero":              {"@a", "--turn", "0", "--", "go"},
		"turn not a number":      {"@a", "--turn", "two", "--", "go"},
		"turn with a node":       {"@a", "--turn", "3.2", "--", "go"},
		"head with a node":       {"@a", "--turn", "head.1", "--", "go"},
		"turn and successor":     {"@a", "--turn", "3", "--successor", "x", "--", "go"},
		"negative ttl":           {"@a", "--ttl", "-5m", "--", "go"},
		"unknown abdication":     {"@a", "--abdication", "flee", "--", "go"},
		"retained is not a flag": {"@a", "--retained", "x", "--", "go"},
	} {
		t.Run(name, func(t *testing.T) {
			if p, err := succeedArgs(t, argv...); err == nil {
				t.Fatalf("accepted: %+v", p)
			}
		})
	}
}

func patchKeys(t *testing.T, p form.Patch) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, e := range p.Entries() {
		if e.IsRemoval() {
			continue
		}
		var s string
		if json.Unmarshal(e.New, &s) != nil {
			s = string(e.New)
		}
		out[e.Key] = s
	}
	return out
}

func TestAbdicationRetireStampsAndCountsTheTTLFromNow(t *testing.T) {
	now := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	born := now.Add(-time.Hour).UnixMilli()
	got := patchKeys(t, abdicationPatch(succeedPlan{ttl: 10 * time.Minute, abdication: abdicateRetire}, "deploy warden", born, now))
	if got["mantra"] != "SUCCEEDED: deploy warden" {
		t.Errorf("mantra = %q", got["mantra"])
	}
	if got[store.SystemTTLKey] != "1h10m0s" {
		t.Errorf("ttl = %q, want the aria's age plus ten minutes", got[store.SystemTTLKey])
	}
	if got[store.SystemRetiredKey] == "" {
		t.Error("retire abdication did not stamp system.retired")
	}
}

func TestAbdicationLingerLeavesTheAriaPromptable(t *testing.T) {
	got := patchKeys(t, abdicationPatch(succeedPlan{ttl: time.Minute, abdication: abdicateLinger}, "x", 0, time.Now()))
	if _, ok := got[store.SystemRetiredKey]; ok {
		t.Error("linger stamped system.retired")
	}
	if got["mantra"] != "SUCCEEDED: x" {
		t.Errorf("mantra = %q", got["mantra"])
	}
}

// A second succession of the same aria must not stack prefixes.
func TestSucceededMantraIsNotPrefixedTwice(t *testing.T) {
	if got := succeededMantra("SUCCEEDED: x"); got != "SUCCEEDED: x" {
		t.Fatalf("got %q", got)
	}
}

func TestRecoveryNamesWhatIsLeft(t *testing.T) {
	r := succeedReport{Role: "@r", Predecessor: "old", Successor: "new", Mantra: "m", Prompted: true, FailedAt: "cast"}
	steps := strings.Join(r.recovery(succeedPlan{ttl: time.Minute, abdication: abdicateRetire}), "\n")
	for _, want := range []string{"figaro cast new @r", "figaro drop old @r", "figaro retire old --ttl"} {
		if !strings.Contains(steps, want) {
			t.Errorf("recovery lacks %q:\n%s", want, steps)
		}
	}
	if strings.Contains(steps, "send") {
		t.Errorf("the prompt landed; recovery must not resend it:\n%s", steps)
	}
}

// Fixes #50: a successor forked from an old turn comes up on that turn's
// model; -O/-S/-D dress it in the fork itself, as fork does.
func TestSucceedDressesTheFork(t *testing.T) {
	p, err := succeedArgs(t, "@warden", "-O", "default", "-S", "system.model=claude-opus-5.5", "--title", "warden II", "--", "go")
	if err != nil {
		t.Fatal(err)
	}
	outfits, patch := p.birth()
	if strings.Join(outfits, ",") != "default" {
		t.Fatalf("outfits = %v", outfits)
	}
	if patch == nil {
		t.Fatal("no birth patch")
	}
	got := form.Snapshot{}.Apply(*patch)
	if v, ok := got.Get("system.model"); !ok || string(v) != `"claude-opus-5.5"` {
		t.Fatalf("system.model = %s %v", v, ok)
	}
	if v, ok := got.Get("mantra"); !ok || string(v) != `"warden II"` {
		t.Fatalf("mantra = %s %v (the title must survive the dress)", v, ok)
	}
}

func TestSucceedRefusesToDressAnExistingSuccessor(t *testing.T) {
	if p, err := succeedArgs(t, "@warden", "--successor", "abc", "-O", "default", "--", "go"); err == nil {
		t.Fatalf("accepted: %+v", p)
	}
}

// gen4 take 29: a succeed that keeps the predecessor's whole history had no
// spelling. --turn head forks at the head, and --turn N+1 on an N-turn aria
// is the same fork; both successors carry all three turns.
func TestSucceedAtTheHeadKeepsEveryTurn(t *testing.T) {
	for _, turn := range []string{"head", "4"} {
		t.Run(turn, func(t *testing.T) {
			s := seatedPredecessor(t, nil)
			plan, err := succeedArgs(t, s.role, "--turn", turn, "--", "you hold the seat")
			require.NoError(t, err)
			r := s.succeed(t, plan)

			msgs, err := ariaMessages(s.ctx, s.acli, r.Successor)
			require.NoError(t, err)
			var inputs []string
			for _, m := range msgs {
				if m.Role == message.RoleInput && len(m.Content) > 0 {
					inputs = append(inputs, m.Content[0].Text)
				}
			}
			require.Equal(t, []string{"input 1", "input 2", "input 3"}, inputs[:min(3, len(inputs))],
				"the successor of --turn %s must carry the predecessor's three turns", turn)
		})
	}
}

type seated struct {
	backend *store.XwalBackend
	acli    *sdk.Angelus
	pred    string
	role    string
	ctx     context.Context
}

// seatedPredecessor holds a role and has three turns; afterTurn runs before
// the first, as turn 0, and once each turn is written.
func seatedPredecessor(t *testing.T, afterTurn func(backend *store.XwalBackend, pred string, turn uint64)) seated {
	t.Helper()
	return seatedPredecessorWith(t, afterTurn, func(string, provider.Knobs) (provider.Provider, error) {
		return nil, errors.New("no provider: this aria is read, never run")
	})
}

func seatedPredecessorWith(t *testing.T, afterTurn func(backend *store.XwalBackend, pred string, turn uint64), providers func(string, provider.Knobs) (provider.Provider, error)) seated {
	t.Helper()
	return seatedPredecessorAt(t, afterTurn, providers, t.TempDir())
}

func seatedPredecessorAt(t *testing.T, afterTurn func(backend *store.XwalBackend, pred string, turn uint64), providers func(string, provider.Knobs) (provider.Provider, error), configDir string) seated {
	t.Helper()
	backend, pred := store.NewTestAria(t, "pred", message.Patch{})
	ir, err := backend.OpenFigIR(pred)
	require.NoError(t, err)
	if afterTurn != nil {
		afterTurn(backend, pred, 0)
	}
	for i := uint64(1); i <= 3; i++ {
		for _, role := range []message.Role{message.RoleInput, message.RoleOutput} {
			_, err := ir.Append(arialog.Entry[message.Message]{Payload: message.Message{
				Role: role, TurnID: i, Content: []message.Content{message.TextContent(fmt.Sprintf("%s %d", role, i))}}})
			require.NoError(t, err)
		}
		if afterTurn != nil {
			afterTurn(backend, pred, i)
		}
	}
	held, _ := json.Marshal(pred)
	role, _, err := backend.CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{"target-aria": held}, nil))
	require.NoError(t, err)
	acli, dir := serveBackendAt(t, backend, providers, configDir)
	t.Setenv("FIGARO_RUNTIME_DIR", dir)
	t.Setenv("FIGARO_CONFIG_DIR", t.TempDir())
	t.Setenv("FIGARO_TROUPE", "")
	t.Setenv("FIGARO_ARIA", "")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return seated{backend: backend, acli: acli, pred: pred, role: role, ctx: ctx}
}

// The fixture runs no provider, so the succession stops at the prompt; the
// fork before it is what these tests defend.
func (s seated) succeed(t *testing.T, plan succeedPlan) succeedReport {
	t.Helper()
	r, err := succeedVerb(s.ctx, s.acli, plan, succeedHooks{})
	require.True(t, r.Forked, "no successor was forked: %v", err)
	return r
}

// #135: the successor of a default succeed is cut at turn 2, before the
// predecessor studied the board after turn two; it must study the board
// anyway, with its keys.
func TestTheSuccessorStudiesWhatThePredecessorStudiesNow(t *testing.T) {
	var early, board string
	s := seatedPredecessor(t, func(b *store.XwalBackend, pred string, turn uint64) {
		var err error
		switch turn {
		case 0:
			early, _, err = b.CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{"name": json.RawMessage(`"early"`)}, nil))
			require.NoError(t, err)
			_, err = b.StudyForm(pred, early, nil)
		case 2:
			board, _, err = b.CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{"issue": json.RawMessage(`"135"`)}, nil))
			require.NoError(t, err)
			_, err = b.StudyForm(pred, board, []string{"issue", "tree"})
		}
		require.NoError(t, err)
	})

	for name, tc := range map[string]struct {
		argv    []string
		studies []string
		keys    []string
	}{
		"live":       {nil, []string{early, board}, []string{"issue", "tree"}},
		"no-studies": {[]string{"--no-studies"}, []string{early}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			plan, err := succeedArgs(t, append(append([]string{s.role}, tc.argv...), "--", "you hold the seat")...)
			require.NoError(t, err)
			r := s.succeed(t, plan)
			f, err := s.acli.Form(s.ctx, r.Successor)
			require.NoError(t, err)
			require.ElementsMatch(t, tc.studies, figaro.StudiesFromSnapshot(f.Snapshot))
			require.Equal(t, tc.keys, form.StudyKeys(f.Snapshot, board))
		})
	}
}

func TestSucceedRefusesNoStudiesForAnExistingSuccessor(t *testing.T) {
	if p, err := succeedArgs(t, "@warden", "--successor", "abc", "--no-studies", "--", "go"); err == nil {
		t.Fatalf("accepted: %+v", p)
	}
}

// A daemon older than fork.studies would refuse the live request outright:
// succeed falls back to the studies at the cut rather than fail, and asks
// for live studies whenever the daemon reports them.
func TestSucceedFallsBackToTheCutOnADaemonWithoutForkStudies(t *testing.T) {
	resetDaemonSeen(t)
	recordDaemon("figaro daemon", daemonIdentity{build: "v0.72.0", caps: []rpc.Capability{rpc.CapFormApply, rpc.CapStudyKeys}}, "this-build")
	got, atCut := succeedStudies(rpc.ForkStudiesLive)
	require.Equal(t, rpc.ForkStudiesAtCut, got)
	require.True(t, atCut)
	require.NoError(t, refuseUnknownFields(rpc.ForkRequest{FigaroID: "x", AtTurn: 2, Studies: got}))
	got, atCut = succeedStudies(rpc.ForkStudiesAtCut)
	require.Equal(t, rpc.ForkStudiesAtCut, got)
	require.False(t, atCut)

	recordDaemon("figaro daemon", daemonIdentity{build: "v0.73.0", caps: []rpc.Capability{rpc.CapForkStudies}}, "this-build")
	got, atCut = succeedStudies(rpc.ForkStudiesLive)
	require.Equal(t, rpc.ForkStudiesLive, got)
	require.False(t, atCut)
}

type answersOK struct{}

func (answersOK) Name() string                                         { return "ok" }
func (answersOK) Fingerprint() string                                  { return "ok/v1" }
func (answersOK) SetModel(string)                                      {}
func (answersOK) Models(context.Context) ([]provider.ModelInfo, error) { return nil, nil }
func (answersOK) Send(_ context.Context, _ provider.SendInput, bus provider.Bus) error {
	bus.PushDelta(message.TextContent("ok"))
	bus.PushMessageEnd(string(message.StopEnd))
	bus.PushFigaro(message.Message{Role: message.RoleOutput, Content: []message.Content{message.TextContent("ok")}, StopReason: message.StopEnd})
	return nil
}

// #96: a predecessor holding @a and @b hands both to the successor of
// succeed @a; @b no longer points at a figaro on its way out.
func TestSucceedRecastsEveryRoleThePredecessorHolds(t *testing.T) {
	var other string
	s := seatedPredecessorWith(t, func(b *store.XwalBackend, pred string, turn uint64) {
		if turn != 3 {
			return
		}
		held, _ := json.Marshal(pred)
		var err error
		other, _, err = b.CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{"target-aria": held}, nil))
		require.NoError(t, err)
	}, func(string, provider.Knobs) (provider.Provider, error) { return answersOK{}, nil })

	plan, err := succeedArgs(t, s.role, "--", "you hold both seats")
	require.NoError(t, err)
	r, err := succeedVerb(s.ctx, s.acli, plan, succeedHooks{})
	require.NoError(t, err)
	require.Equal(t, []string{other}, r.Recast)
	for _, role := range []string{s.role, other} {
		f, err := s.acli.Form(s.ctx, role)
		require.NoError(t, err)
		v, _ := f.Snapshot.Get("target-aria")
		require.JSONEq(t, fmt.Sprintf("%q", r.Successor), string(v), "%s still targets the predecessor", role)
	}
	require.Contains(t, r.summary(), ariaName(other))
}

func TestRecoveryIsSilentWhenTheSeatMovedNothing(t *testing.T) {
	r := succeedReport{Role: "@r", Predecessor: "old", Successor: "new", FailedAt: "demote",
		Error: "nothing moved yet: the demote waits on p; the successor is back where it was, so nothing moved"}
	require.Empty(t, r.recovery(succeedPlan{ttl: time.Minute, abdication: abdicateRetire}))
}

func TestRecoveryKillsAForkThatWasNeverSeated(t *testing.T) {
	for _, step := range []string{"place", "demote"} {
		r := succeedReport{Role: "@r", Predecessor: "old", Successor: "new", Forked: true, FailedAt: step}
		require.Equal(t, []string{"figaro kill new"}, r.recovery(succeedPlan{ttl: time.Minute, abdication: abdicateRetire}), step)
	}
}

func TestSucceedRefusesADaemonWithoutSeatBeforeItForks(t *testing.T) {
	s := seatedPredecessor(t, nil)
	daemonSeen.Lock()
	known, same, who, d := daemonSeen.known, daemonSeen.same, daemonSeen.who, daemonSeen.d
	daemonSeen.Unlock()
	t.Cleanup(func() {
		daemonSeen.Lock()
		daemonSeen.known, daemonSeen.same, daemonSeen.who, daemonSeen.d = known, same, who, d
		daemonSeen.Unlock()
	})
	recordDaemon("the running angelus", daemonIdentity{build: "0ld0ld0ld0ld0ld0", release: "0.74.4"}, boot.BuildRevision())
	before := len(s.backend.EveryNode())

	plan, err := succeedArgs(t, s.role, "--", "you hold the seat")
	require.NoError(t, err)
	r, err := succeedVerb(s.ctx, s.acli, plan, succeedHooks{})
	require.Error(t, err)
	require.Contains(t, r.Error, string(rpc.CapSeat))
	require.Empty(t, r.Successor)
	require.Len(t, s.backend.EveryNode(), before, "succeed forked before it knew the daemon could seat")
}

func TestRecoveryCastsTheOtherRolesNotYetRecast(t *testing.T) {
	r := succeedReport{Role: "@r", Predecessor: "old", Successor: "new", Prompted: true, Cast: true,
		OtherRoles: []string{"@b", "@c"}, Recast: []string{"@b"}, FailedAt: "cast @c"}
	steps := strings.Join(r.recovery(succeedPlan{ttl: time.Minute, abdication: abdicateRetire}), "\n")
	require.Contains(t, steps, "figaro cast new @c")
	require.NotContains(t, steps, "figaro cast new @b")
	require.NotContains(t, steps, "figaro cast new @r")
}

// A role another figaro took between succeed's listing and its recast stays
// with that figaro: the recast reads the role again and leaves it alone.
func TestSucceedLeavesARoleTakenMidSuccession(t *testing.T) {
	var other, third string
	s := seatedPredecessorWith(t, func(b *store.XwalBackend, pred string, turn uint64) {
		if turn != 3 {
			return
		}
		held, _ := json.Marshal(pred)
		var err error
		other, _, err = b.CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{"target-aria": held}, nil))
		require.NoError(t, err)
		third, _, err = b.ForkWith(pred, 0, form.Build(form.Snapshot{}, map[string]json.RawMessage{"title": json.RawMessage(`"third"`)}, nil))
		require.NoError(t, err)
	}, func(string, provider.Knobs) (provider.Provider, error) { return answersOK{}, nil })

	plan, err := succeedArgs(t, s.role, "--", "you hold the seat")
	require.NoError(t, err)
	took := succeedHooks{progress: func(msg string) {
		if !strings.HasPrefix(msg, "prompting") {
			return
		}
		raw, _ := json.Marshal(third)
		_, err := s.backend.ApplyForm(other, form.Build(form.Snapshot{}, map[string]json.RawMessage{"target-aria": raw}, nil))
		require.NoError(t, err)
	}}
	r, err := succeedVerb(s.ctx, s.acli, plan, took)
	require.NoError(t, err)
	f, err := s.acli.Form(s.ctx, other)
	require.NoError(t, err)
	v, _ := f.Snapshot.Get("target-aria")
	require.JSONEq(t, fmt.Sprintf("%q", third), string(v), "succeed took %s from the figaro that held it", other)
	require.Equal(t, []string{other}, r.LeftAlone)
	require.Empty(t, r.Recast)
	require.NotContains(t, strings.Join(r.recovery(plan), "\n"), other)
}

func strKeys(t *testing.T, keys map[string]string) form.Patch {
	t.Helper()
	set := map[string]json.RawMessage{}
	for k, v := range keys {
		raw, err := json.Marshal(v)
		require.NoError(t, err)
		set[k] = raw
	}
	return form.Build(form.Snapshot{}, set, nil)
}

// The King, 11-17: El Pregonero's successor came up on the chatgpt route its
// predecessor's turn was recorded on, while its role says anthropic/opus.
// The successor of a seat takes the seat's route; the caller's -S/-D and -O
// still win, and a role naming no route leaves the recorded one. A role
// naming a provider alone must not pair it with another provider's model.
func TestSucceedSeatsTheSuccessorOnTheRolesRoute(t *testing.T) {
	provider.Register(&provider.Registration{Name: "seat-defaulted", DefaultModel: "seat-default-model"})
	recorded := map[string]string{"system.provider": "chatgpt-personal", "system.model": "gpt-5.5", "system.thinking_effort": "low"}
	seat := map[string]string{"system.provider": "anthropic", "system.model": "claude-opus-5", "system.thinking_effort": "high"}
	for name, tc := range map[string]struct {
		role map[string]string
		argv []string
		want map[string]string
	}{
		"role route":       {seat, nil, seat},
		"caller -S wins":   {seat, []string{"-S", "system.model=claude-sonnet-5"}, map[string]string{"system.provider": "anthropic", "system.model": "claude-sonnet-5", "system.thinking_effort": "high"}},
		"caller -D wins":   {seat, []string{"-D", "system.thinking_effort"}, map[string]string{"system.provider": "anthropic", "system.model": "claude-opus-5", "system.thinking_effort": ""}},
		"caller -O wins":   {seat, []string{"-O", "sonnet"}, map[string]string{"system.provider": "anthropic", "system.model": "claude-sonnet-5", "system.thinking_effort": "high"}},
		"no route on role": {nil, nil, recorded},
		"provider alone, another provider": {map[string]string{"system.provider": "seat-defaulted"}, nil,
			map[string]string{"system.provider": "seat-defaulted", "system.model": "", "system.thinking_effort": "low"}},
		"provider alone, the same provider": {map[string]string{"system.provider": "chatgpt-personal"}, nil, recorded},
	} {
		t.Run(name, func(t *testing.T) {
			s := seatWithRoute(t, recorded, tc.role)
			plan, err := succeedArgs(t, append(append([]string{s.role}, tc.argv...), "--", "you hold the seat")...)
			require.NoError(t, err)
			r := s.succeed(t, plan)
			f, err := s.acli.Form(s.ctx, r.Successor)
			require.NoError(t, err)
			for k, want := range tc.want {
				got := ""
				if v := f.Snapshot.Lookup(k); v != nil {
					got = *v
				}
				require.Equal(t, want, got, k)
			}
		})
	}
}

func TestSucceedRefusesAProviderAloneWithNoDefaultModel(t *testing.T) {
	provider.Register(&provider.Registration{Name: "seat-bare"})
	s := seatWithRoute(t, map[string]string{"system.provider": "chatgpt-personal", "system.model": "gpt-5.5"}, map[string]string{"system.provider": "seat-bare"})
	plan, err := succeedArgs(t, s.role, "--", "you hold the seat")
	require.NoError(t, err)
	r, err := succeedVerb(s.ctx, s.acli, plan, succeedHooks{})
	require.ErrorContains(t, err, "seat-bare has no default model")
	require.False(t, r.Forked)
	require.Equal(t, s.pred, roleHolder(s.ctx, s.acli, s.role))
}

func seatWithRoute(t *testing.T, recorded, role map[string]string) seated {
	t.Helper()
	cfg := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(cfg, "outfits"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(cfg, "outfits", "sonnet.toml"), []byte("[system]\nmodel = \"claude-sonnet-5\"\n"), 0o600))
	s := seatedPredecessorAt(t, func(b *store.XwalBackend, pred string, turn uint64) {
		if turn == 0 {
			_, err := b.ApplyForm(pred, strKeys(t, recorded))
			require.NoError(t, err)
		}
	}, func(string, provider.Knobs) (provider.Provider, error) {
		return nil, errors.New("no provider: this aria is read, never run")
	}, cfg)
	if role != nil {
		_, err := s.backend.ApplyForm(s.role, strKeys(t, role))
		require.NoError(t, err)
	}
	return s
}

// figaro succeed and the daemon's succession share one seat: a named
// successor, a forked one and (in angelus) a daemon-minted one all end in the
// predecessor's place with the predecessor and its branch beneath them.
func TestANamedSuccessorIsDrawnWhereAForkedOneGoes(t *testing.T) {
	shape := func(t *testing.T, named bool) map[string]string {
		s := seatedPredecessor(t, nil)
		require.NoError(t, wire.Install(s.backend.Store(), t.TempDir(), wire.Capabilities{Trunks: true}))
		_, kid, err := s.backend.Fork(s.pred)
		require.NoError(t, err)
		was, _ := s.backend.Node(s.pred)
		var plan succeedPlan
		var r succeedReport
		if named {
			other, err := s.backend.CreateConversation(was.Parent)
			require.NoError(t, err)
			_, succ, err := s.backend.Fork(other)
			require.NoError(t, err)
			plan, err = succeedArgs(t, s.role, "--successor", succ, "--", "you hold the seat")
			require.NoError(t, err)
			r, err = succeedVerb(s.ctx, s.acli, plan, succeedHooks{})
			require.Equal(t, "prompt", r.FailedAt, "the named succession stopped early: %v", err)
		} else {
			plan, err = succeedArgs(t, s.role, "--", "you hold the seat")
			require.NoError(t, err)
			r = s.succeed(t, plan)
		}
		require.Equal(t, r.Successor, r.DemotedUnder)
		names := map[string]string{was.Present: "top", s.pred: "pred", r.Successor: "succ", kid: "kid"}
		got := map[string]string{}
		for id, name := range map[string]string{s.pred: "pred", r.Successor: "succ", kid: "kid"} {
			n, _ := s.backend.Node(id)
			got[name] = names[n.Present]
		}
		return got
	}
	forked := shape(t, false)
	require.Equal(t, map[string]string{"succ": "top", "pred": "succ", "kid": "succ"}, forked)
	require.Equal(t, forked, shape(t, true))
}

func TestANamedSuccessorTheGuardRefusesStopsTheSuccession(t *testing.T) {
	s := seatedPredecessor(t, nil)
	require.NoError(t, wire.Install(s.backend.Store(), t.TempDir(), wire.Capabilities{Trunks: true}))
	_, _, err := s.backend.Fork(s.pred)
	require.NoError(t, err)
	was, _ := s.backend.Node(s.pred)
	other, err := s.backend.CreateConversation(was.Parent)
	require.NoError(t, err)
	_, succ, err := s.backend.Fork(other)
	require.NoError(t, err)
	snap, err := s.backend.FormState(s.pred)
	require.NoError(t, err)
	one, _ := json.Marshal(1)
	_, err = s.backend.ApplyForm(s.pred, form.Build(snap, map[string]json.RawMessage{form.FanoutMaxChildrenKey: one}, nil))
	require.NoError(t, err)

	plan, err := succeedArgs(t, s.role, "--successor", succ, "--", "you hold the seat")
	require.NoError(t, err)
	r, err := succeedVerb(s.ctx, s.acli, plan, succeedHooks{})
	require.Error(t, err)
	require.Equal(t, "place", r.FailedAt)
	require.Contains(t, r.Error, "fanout")
	require.Empty(t, r.recovery(plan))
	n, _ := s.backend.Node(succ)
	require.Equal(t, other, n.Present, "a refused successor moved")
	p, _ := s.backend.Node(s.pred)
	require.Equal(t, was.Present, p.Present, "the predecessor moved after the refusal")
}
