package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
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
