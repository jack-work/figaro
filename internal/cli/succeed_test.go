package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
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
			backend, pred := store.NewTestAria(t, "pred", message.Patch{})
			ir, err := backend.OpenFigIR(pred)
			require.NoError(t, err)
			for i := uint64(1); i <= 3; i++ {
				for _, role := range []message.Role{message.RoleInput, message.RoleOutput} {
					_, err := ir.Append(store.Entry[message.Message]{Payload: message.Message{
						Role: role, TurnID: i, Content: []message.Content{message.TextContent(fmt.Sprintf("%s %d", role, i))}}})
					require.NoError(t, err)
				}
			}
			held, _ := json.Marshal(pred)
			role, _, err := backend.CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{"target-aria": held}, nil))
			require.NoError(t, err)
			acli, dir := serveBackend(t, backend)
			t.Setenv("FIGARO_RUNTIME_DIR", dir)
			t.Setenv("FIGARO_CONFIG_DIR", t.TempDir())
			t.Setenv("FIGARO_TROUPE", "")
			t.Setenv("FIGARO_ARIA", "")

			plan, err := succeedArgs(t, role, "--turn", turn, "--", "you hold the seat")
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			// The fixture runs no provider, so the succession stops at the
			// prompt; the fork before it is what this defends.
			r, err := succeedVerb(ctx, acli, plan, succeedHooks{})
			require.True(t, r.Forked, "no successor was forked: %v", err)

			msgs, err := ariaMessages(ctx, acli, r.Successor)
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
