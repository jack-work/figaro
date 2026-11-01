package figaro_test

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
	"github.com/jack-work/figaro/internal/uiir"
)

// forgingProvider records the tools each request offers and, on the first
// round, calls bash whether or not it was offered: a model is free to name
// any tool, so the refusal has to happen where the tool runs.
type forgingProvider struct {
	mu      sync.Mutex
	offered [][]string
	results []message.Content
	calls   atomic.Int32
}

func (p *forgingProvider) Name() string        { return "forging" }
func (p *forgingProvider) Fingerprint() string { return "forging/v0" }
func (p *forgingProvider) SetModel(string)     {}
func (p *forgingProvider) Models(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}

func (p *forgingProvider) Send(_ context.Context, in provider.SendInput, bus provider.Bus) error {
	names := make([]string, 0, len(in.Tools))
	for _, t := range in.Tools {
		names = append(names, t.Name)
	}
	sort.Strings(names)
	p.mu.Lock()
	p.offered = append(p.offered, names)
	p.mu.Unlock()
	if p.calls.Add(1) > 1 {
		p.mu.Lock()
		for _, e := range in.FigLog.Read() {
			m := e.Payload
			for _, c := range m.Content {
				if c.Type == message.ContentToolResult {
					p.results = append(p.results, c)
				}
			}
		}
		p.mu.Unlock()
		msg := message.Message{Role: message.RoleOutput, Content: []message.Content{message.TextContent("done")}, StopReason: message.StopEnd}
		bus.PushMessageEnd(string(msg.StopReason))
		bus.PushFigaro(msg)
		return nil
	}
	call := message.Content{Type: message.ContentToolInvoke, ToolCallID: "forged", ToolName: "bash", Arguments: map[string]any{"command": "figaro new -- escape"}}
	bus.PushToolReady(call)
	msg := message.Message{Role: message.RoleOutput, Content: []message.Content{call}, StopReason: message.StopToolInvoke}
	bus.PushMessageEnd(string(msg.StopReason))
	bus.PushFigaro(msg)
	return nil
}

type namedTool struct {
	name string
	ran  atomic.Int32
}

func (n *namedTool) Name() string        { return n.name }
func (n *namedTool) Description() string { return n.name }
func (n *namedTool) Parameters() any     { return map[string]any{} }
func (n *namedTool) Execute(context.Context, map[string]any, tool.OnOutput) ([]message.Content, error) {
	n.ran.Add(1)
	return []message.Content{message.TextContent("ran " + n.name)}, nil
}

func runForged(t *testing.T, tools json.RawMessage) (*forgingProvider, map[string]*namedTool) {
	t.Helper()
	ts := map[string]*namedTool{}
	reg := tool.NewRegistry()
	for _, name := range []string{"bash", "process", "read", "write", "edit"} {
		ts[name] = &namedTool{name: name}
		if err := reg.Register(ts[name]); err != nil {
			t.Fatal(err)
		}
	}
	keys := map[string]json.RawMessage{
		"system.model":    json.RawMessage(`"mock"`),
		"system.provider": json.RawMessage(`"forging"`),
	}
	if tools != nil {
		keys["system.tools"] = tools
	}
	cb, _ := form.Open("")
	cb.Apply(form.Build(form.Snapshot{}, keys, nil))
	prov := &forgingProvider{}
	be, id := store.NewTestAria(t, "d", message.Patch{})
	a := figaro.NewAgent(figaro.Config{Backend: be, Projector: uiir.New(nil), ID: id, Provider: prov, Tools: reg, Form: cb})
	defer a.Kill()
	ch, _ := subscribeChan(a)
	submitPrompt(a, "go")
	timeout := time.After(5 * time.Second)
	for {
		select {
		case n := <-ch:
			if n.Method == rpc.MethodTurnDone {
				return prov, ts
			}
		case <-timeout:
			t.Fatal("timeout waiting for turn.done")
		}
	}
}

func TestAnAriaWithNoToolsIsOfferedNoneAndRunsNone(t *testing.T) {
	prov, ts := runForged(t, json.RawMessage(`[]`))
	for i, offered := range prov.offered {
		if len(offered) != 0 {
			t.Errorf("round %d offered %v to an aria with system.tools = []", i, offered)
		}
	}
	if n := ts["bash"].ran.Load(); n != 0 {
		t.Fatalf("a forged bash call ran %d times", n)
	}
	if len(prov.results) != 1 || !prov.results[0].IsError {
		t.Fatalf("the forged call's result: %+v", prov.results)
	}
}

func TestAnAllowlistOffersAndRunsOnlyWhatItNames(t *testing.T) {
	prov, ts := runForged(t, json.RawMessage(`["read"]`))
	if got := prov.offered[0]; len(got) != 1 || got[0] != "read" {
		t.Errorf("offered %v, want [read]", got)
	}
	if n := ts["bash"].ran.Load(); n != 0 {
		t.Fatalf("bash ran %d times outside the allowlist", n)
	}
}

func TestAnAllowlistThatIsNotAListAllowsNothing(t *testing.T) {
	prov, ts := runForged(t, json.RawMessage(`"bash"`))
	if got := prov.offered[0]; len(got) != 0 {
		t.Errorf("offered %v under a malformed allowlist", got)
	}
	if n := ts["bash"].ran.Load(); n != 0 {
		t.Fatalf("bash ran %d times under a malformed allowlist", n)
	}
}

// The default stays as it was: an aria without system.tools has every tool.
func TestNoAllowlistMeansEveryTool(t *testing.T) {
	prov, ts := runForged(t, nil)
	if got := prov.offered[0]; len(got) != 5 {
		t.Errorf("offered %v, want all five", got)
	}
	if n := ts["bash"].ran.Load(); n != 1 {
		t.Fatalf("bash ran %d times, want 1", n)
	}
}
