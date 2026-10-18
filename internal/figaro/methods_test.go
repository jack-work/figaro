package figaro_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/decide"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/uiir"
)

type effortProvider struct {
	mu          sync.Mutex
	seen        []string
	breaksCache bool
}

func (p *effortProvider) KeepsCacheOnChange(key string, _ form.Snapshot) bool {
	return key == "system.thinking_effort" && !p.breaksCache
}

func (p *effortProvider) Name() string        { return "mock" }
func (p *effortProvider) Fingerprint() string { return "mock/v0" }
func (p *effortProvider) SetModel(string)     {}
func (p *effortProvider) Models(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}

func (p *effortProvider) Send(_ context.Context, in provider.SendInput, bus provider.Bus) error {
	got := ""
	if v := in.Snapshot.Lookup("system.thinking_effort"); v != nil {
		got = *v
	}
	p.mu.Lock()
	p.seen = append(p.seen, got)
	p.mu.Unlock()
	mockPushAssistant(in.FigLog, nil, bus, func(_ message.Message, _ form.Snapshot) ([]json.RawMessage, error) {
		return []json.RawMessage{json.RawMessage(`{"role":"assistant","content":[]}`)}, nil
	}, p.Fingerprint(), "ok")
	return nil
}

func (p *effortProvider) sends() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.seen...)
}

type scriptedAsker struct {
	mu    sync.Mutex
	asked []string
	pick  func(ask string) (string, error)
}

func (s *scriptedAsker) Ask(ctx context.Context, _ decide.Source, q decide.Question) (rpc.DecideResponse, error) {
	s.mu.Lock()
	s.asked = append(s.asked, q.Ask)
	s.mu.Unlock()
	label, err := s.pick(q.Ask)
	if err != nil {
		return rpc.DecideResponse{}, err
	}
	if label == "" {
		<-ctx.Done()
		return rpc.DecideResponse{}, ctx.Err()
	}
	return rpc.DecideResponse{Rows: []rpc.DecideRow{{Option: label, Chosen: true}}}, nil
}

const effortMethod = `{
	"reasoning-effort": {
		"key": "system.thinking_effort",
		"event": "inquiry",
		"context": "before: {{inquiry~1}} | now: {{inquiry}}",
		"options": ["low", "medium", "high", "xhigh"],
		"score": true,
		"default": "high"
	}
}`

func agentWithMethods(t *testing.T, asker *scriptedAsker) (*figaro.Agent, *effortProvider) {
	t.Helper()
	return agentOn(t, asker, &effortProvider{}, effortMethod)
}

func agentOn(t *testing.T, asker *scriptedAsker, p *effortProvider, methods string) (*figaro.Agent, *effortProvider) {
	t.Helper()
	return agentWith(t, asker, p, map[string]json.RawMessage{"system.methods": json.RawMessage(methods)}, nil)
}

func agentWith(t *testing.T, asker *scriptedAsker, p *effortProvider, extra map[string]json.RawMessage, defaults *form.Snapshot) (*figaro.Agent, *effortProvider) {
	t.Helper()
	board := map[string]json.RawMessage{
		"system.model":           json.RawMessage(`"mock-model-v1"`),
		"system.provider":        json.RawMessage(`"mock"`),
		"system.thinking_effort": json.RawMessage(`"medium"`),
	}
	for k, v := range extra {
		board[k] = v
	}
	cb, _ := form.Open("")
	cb.Apply(form.Build(form.Snapshot{}, board, nil))
	be, id := store.NewTestAria(t, "m", form.Build(form.Snapshot{}, board, nil))
	cfg := figaro.Config{
		Projector: uiir.New(nil), ID: id,
		Provider: p, Backend: be, Form: cb, Decider: asker,
	}
	if defaults != nil {
		cfg.DefaultForm = func() form.Snapshot { return *defaults }
	}
	a := figaro.NewAgent(cfg)
	t.Cleanup(a.Kill)
	return a, p
}

func waitSends(t *testing.T, p *effortProvider, n int) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s := p.sends(); len(s) >= n {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("only %d of %d sends arrived", len(p.sends()), n)
	return nil
}

// The work order's scenario: every inquiry is judged before the provider
// sees it, the verdict lands on the configured key, and the template reads
// earlier inquiries relative to this one.
func TestAnInquirySetsItsEffortBeforeTheSend(t *testing.T) {
	asker := &scriptedAsker{pick: func(ask string) (string, error) {
		if strings.Contains(ask, "now: prove") {
			return "xhigh", nil
		}
		return "low", nil
	}}
	a, p := agentWithMethods(t, asker)

	submitPrompt(a, "hello")
	waitSends(t, p, 1)
	submitPrompt(a, "prove the halting problem undecidable")
	sends := waitSends(t, p, 2)

	if sends[0] != "low" || sends[1] != "xhigh" {
		t.Fatalf("the provider saw efforts %v, want [low xhigh]", sends)
	}
	asker.mu.Lock()
	defer asker.mu.Unlock()
	if asker.asked[0] != "before:  | now: hello" {
		t.Fatalf("first ask = %q", asker.asked[0])
	}
	if asker.asked[1] != "before: hello | now: prove the halting problem undecidable" {
		t.Fatalf("second ask = %q", asker.asked[1])
	}
}

func TestASilentDeciderFallsToTheDefaultInTime(t *testing.T) {
	asker := &scriptedAsker{pick: func(string) (string, error) { return "", nil }}
	a, p := agentWithMethods(t, asker)
	started := time.Now()
	submitPrompt(a, "hello")
	sends := waitSends(t, p, 1)
	if sends[0] != "high" {
		t.Fatalf("the provider saw %q, want the default", sends[0])
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("the turn waited %s on a decider that never answered", elapsed)
	}
}

func TestTurningMethodsOffLeavesTheKeyAlone(t *testing.T) {
	asker := &scriptedAsker{pick: func(string) (string, error) { return "xhigh", nil }}
	a, p := agentWithMethods(t, asker)
	if _, _, err := a.Set(form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.methods.enabled": json.RawMessage(`false`),
	}, nil), 0); err != nil {
		t.Fatal(err)
	}
	submitPrompt(a, "hello")
	if sends := waitSends(t, p, 1); sends[0] != "medium" {
		t.Fatalf("the provider saw %q, want the untouched medium", sends[0])
	}
	asker.mu.Lock()
	defer asker.mu.Unlock()
	if len(asker.asked) != 0 {
		t.Fatalf("a disabled feature asked %d questions", len(asker.asked))
	}
}

// Gluck, 2026-10-18: "the cache is more important than this setting." Where
// the route cannot change effort per message, the method does not run.
func TestAMethodWhoseKeyWouldBreakTheCacheIsHeld(t *testing.T) {
	asker := &scriptedAsker{pick: func(string) (string, error) { return "xhigh", nil }}
	a, p := agentOn(t, asker, &effortProvider{breaksCache: true}, effortMethod)
	submitPrompt(a, "hello")
	if sends := waitSends(t, p, 1); sends[0] != "medium" {
		t.Fatalf("the provider saw %q, want the untouched medium", sends[0])
	}
	asker.mu.Lock()
	defer asker.mu.Unlock()
	if len(asker.asked) != 0 {
		t.Fatalf("a held method still asked %d questions", len(asker.asked))
	}
}

func TestKeepCacheFalseRunsAnyway(t *testing.T) {
	asker := &scriptedAsker{pick: func(string) (string, error) { return "xhigh", nil }}
	spend := strings.Replace(effortMethod, `"score": true,`, `"score": true, "keep_cache": false,`, 1)
	a, p := agentOn(t, asker, &effortProvider{breaksCache: true}, spend)
	submitPrompt(a, "hello")
	if sends := waitSends(t, p, 1); sends[0] != "xhigh" {
		t.Fatalf("the provider saw %q, want xhigh", sends[0])
	}
}

// Gluck tested on an aria born before core.toml gained the method: its form
// had no system.methods, nothing ran, and the M-e toggle flipped a switch
// with nothing behind it. Methods fall back to the default outfit, as the
// decide settings do, and the aria's own keys override it leaf by leaf.
func TestAnAriaOlderThanItsOutfitRunsTheDefaultMethods(t *testing.T) {
	defaults := form.Snapshot{}.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.methods": json.RawMessage(effortMethod),
	}, nil))
	asker := &scriptedAsker{pick: func(string) (string, error) { return "low", nil }}
	a, p := agentWith(t, asker, &effortProvider{}, nil, &defaults)
	submitPrompt(a, "no thinking, just say hi")
	if sends := waitSends(t, p, 1); sends[0] != "low" {
		t.Fatalf("the provider saw %q, want the default outfit's method to pick low", sends[0])
	}

	asker2 := &scriptedAsker{pick: func(string) (string, error) { return "xhigh", nil }}
	off := map[string]json.RawMessage{"system.methods.enabled": json.RawMessage(`false`)}
	b, q := agentWith(t, asker2, &effortProvider{}, off, &defaults)
	submitPrompt(b, "hello")
	if sends := waitSends(t, q, 1); sends[0] != "medium" {
		t.Fatalf("the aria switched methods off and the provider saw %q", sends[0])
	}
}
