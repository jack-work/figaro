package methods

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/decide"
)

func bound(t *testing.T, methods string) form.Snapshot {
	t.Helper()
	return form.Snapshot{}.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{FormKey: json.RawMessage(methods)}, nil))
}

const effort = `{
	"enabled": true,
	"reasoning-effort": {
		"key": "system.thinking_effort",
		"event": "inquiry",
		"context": "Earlier: {{inquiry~1}}\nUser input: {{ inquiry }}",
		"options": ["low", "medium", "high", "xhigh"],
		"score": true,
		"default": "high"
	}
}`

func TestTheWorkOrderConfigReads(t *testing.T) {
	ms, errs := Read(bound(t, effort))
	if len(errs) != 0 || len(ms) != 1 {
		t.Fatalf("methods = %+v, errs = %v", ms, errs)
	}
	m := ms[0]
	if m.Name != "reasoning-effort" || m.Key != "system.thinking_effort" || !m.Score || m.Default == nil || *m.Default != "high" {
		t.Fatalf("method = %+v", m)
	}
	if !m.KeepCache {
		t.Fatal("keep_cache must default to true")
	}
	if m.Template.Depth() != 1 {
		t.Fatalf("depth = %d", m.Template.Depth())
	}
	q := m.Question([]string{"prove it in Lean", "hello"})
	if q.Ask != "Earlier: hello\nUser input: prove it in Lean" {
		t.Fatalf("ask = %q", q.Ask)
	}
	if len(q.Options) != 4 || q.Options[3].Label != "xhigh" || !q.Score {
		t.Fatalf("question = %+v", q)
	}
}

func TestTheSwitchesDefaultOnAndTurnOff(t *testing.T) {
	off := strings.Replace(effort, `"enabled": true`, `"enabled": false`, 1)
	if ms, _ := Read(bound(t, off)); len(ms) != 0 {
		t.Fatalf("the whole feature was off and %d methods ran", len(ms))
	}
	offAsString := strings.Replace(effort, `"enabled": true`, `"enabled": "false"`, 1)
	if ms, _ := Read(bound(t, offAsString)); len(ms) != 0 {
		t.Fatal(`a "false" written by figaro set did not turn the feature off`)
	}
	one := strings.Replace(effort, `"score": true`, `"score": true, "enabled": false`, 1)
	if ms, errs := Read(bound(t, one)); len(ms) != 0 || len(errs) != 0 {
		t.Fatalf("a disabled method: %+v %v", ms, errs)
	}
	bare := strings.Replace(effort, `"enabled": true,`, ``, 1)
	if ms, _ := Read(bound(t, bare)); len(ms) != 1 {
		t.Fatal("enabled must default to true")
	}
}

func TestABadMethodDoesNotSilenceItsSiblings(t *testing.T) {
	raw := `{
		"a": {"key": "k", "context": "{{inquiry}}", "options": ["x"]},
		"b": {"key": "k", "context": "{{form.mantra}}", "options": ["x", "y"]},
		"c": {"key": "k", "context": "{{inquiry}}", "options": {"x": "", "y": ""}, "score": true},
		"d": {"key": "k", "context": "{{inquiry}}", "options": ["x", "y"], "default": "z"},
		"e": {"key": "k", "context": "{{inquiry}}", "options": ["x", "y"], "event": "fork"},
		"f": {"key": "k", "context": "{{inquiry}}", "options": ["x", "y"], "typo": 1},
		"g": {"context": "{{inquiry}}", "options": ["x", "y"]},
		"ok": {"key": "k", "context": "{{inquiry}}", "options": ["x", "y"]}
	}`
	ms, errs := Read(bound(t, raw))
	if len(ms) != 1 || ms[0].Name != "ok" || ms[0].Event != EventInquiry {
		t.Fatalf("methods = %+v", ms)
	}
	if len(errs) != 7 {
		t.Fatalf("errs = %v", errs)
	}
}

func TestNamedOptionsCarryTheirDescriptions(t *testing.T) {
	raw := `{"m": {"key": "k", "context": "{{inquiry}}", "options": {"quick": "a one-liner", "deep": "a proof"}}}`
	ms, errs := Read(bound(t, raw))
	if len(errs) != 0 || len(ms) != 1 {
		t.Fatal(errs)
	}
	if o := ms[0].Options; o[0] != (decide.Option{Label: "deep", Criterion: "a proof"}) || o[1].Criterion != "a one-liner" {
		t.Fatalf("options = %+v", o)
	}
}

func TestAMissingEarlierInquiryRendersEmpty(t *testing.T) {
	tmpl, err := Parse("[{{inquiry~2}}] [{{inquiry}}]")
	if err != nil {
		t.Fatal(err)
	}
	if got := tmpl.Render([]string{"now"}); got != "[] [now]" {
		t.Fatalf("render = %q", got)
	}
}

type asker func(ctx context.Context, q decide.Question) (rpc.DecideResponse, error)

func (f asker) Ask(ctx context.Context, _ decide.Source, q decide.Question) (rpc.DecideResponse, error) {
	return f(ctx, q)
}

func chose(label string) asker {
	return func(context.Context, decide.Question) (rpc.DecideResponse, error) {
		return rpc.DecideResponse{Rows: []rpc.DecideRow{{Option: "low"}, {Option: label, Chosen: true}}}, nil
	}
}

func method(t *testing.T, raw string) Method {
	t.Helper()
	ms, errs := Read(bound(t, raw))
	if len(errs) != 0 || len(ms) != 1 {
		t.Fatalf("methods = %+v, errs = %v", ms, errs)
	}
	return ms[0]
}

func TestTheChosenOptionIsTheValue(t *testing.T) {
	o := Run(context.Background(), chose("xhigh"), decide.Source{}, method(t, effort), []string{"q"})
	if o.Verdict != Decided || o.Value != "xhigh" || o.Err != nil {
		t.Fatalf("outcome = %+v", o)
	}
}

func TestASlowAnswerFallsToTheDefaultWithinTheDeadline(t *testing.T) {
	// The asker ignores its context on purpose: the deadline must hold even
	// when the decision client does not.
	slow := asker(func(context.Context, decide.Question) (rpc.DecideResponse, error) {
		time.Sleep(Deadline + time.Second)
		return rpc.DecideResponse{}, nil
	})
	started := time.Now()
	o := Run(context.Background(), slow, decide.Source{}, method(t, effort), []string{"q"})
	if elapsed := time.Since(started); elapsed > Deadline+200*time.Millisecond {
		t.Fatalf("waited %s past a %s deadline", elapsed, Deadline)
	}
	if o.Verdict != Defaulted || o.Value != "high" || o.Err == nil {
		t.Fatalf("outcome = %+v", o)
	}
}

func TestAFailureWithNoDefaultKeepsTheValue(t *testing.T) {
	noDefault := strings.Replace(effort, `,
		"default": "high"`, ``, 1)
	broken := asker(func(context.Context, decide.Question) (rpc.DecideResponse, error) {
		return rpc.DecideResponse{}, errors.New("HTTP 500")
	})
	o := Run(context.Background(), broken, decide.Source{}, method(t, noDefault), []string{"q"})
	if o.Verdict != Kept || o.Value != "" {
		t.Fatalf("outcome = %+v", o)
	}
}

func TestAnAnswerOutsideTheOptionsIsRefused(t *testing.T) {
	o := Run(context.Background(), chose("max"), decide.Source{}, method(t, effort), []string{"q"})
	if o.Verdict != Defaulted || o.Value != "high" {
		t.Fatalf("outcome = %+v", o)
	}
}

func TestTheAriaOverridesTheDefaultOutfitLeafByLeaf(t *testing.T) {
	defaults := bound(t, effort)
	aria := form.Snapshot{}.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.methods.reasoning-effort.default": json.RawMessage(`"low"`),
		"mantra": json.RawMessage(`"unrelated"`),
	}, nil))
	ms, errs := Read(Effective(aria, defaults))
	if len(errs) != 0 || len(ms) != 1 {
		t.Fatalf("methods = %+v, errs = %v", ms, errs)
	}
	if *ms[0].Default != "low" || ms[0].Key != "system.thinking_effort" || len(ms[0].Options) != 4 {
		t.Fatalf("method = %+v", ms[0])
	}
	off := form.Snapshot{}.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.methods.enabled": json.RawMessage(`false`),
	}, nil))
	if ms, _ := Read(Effective(off, defaults)); len(ms) != 0 {
		t.Fatal("the aria's off switch did not override the outfit's on")
	}
}
