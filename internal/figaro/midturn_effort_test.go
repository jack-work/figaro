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
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/provider/anthropic"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
	"github.com/jack-work/figaro/internal/uiir"
)

// midTurnEffortProvider calls a tool on round one and holds it open until the test
// has changed the effort; round two records the rows Anthropic would be sent,
// derived from the log and the bound form exactly as the translator derives them.
type midTurnEffortProvider struct {
	mu      sync.Mutex
	sends   int
	started chan struct{}
	proceed chan struct{}
	rows    []json.RawMessage
	snap    form.Snapshot
}

func (p *midTurnEffortProvider) Name() string        { return "mock" }
func (p *midTurnEffortProvider) Fingerprint() string { return "mock/v0" }
func (p *midTurnEffortProvider) SetModel(string)     {}
func (p *midTurnEffortProvider) Models(ctx context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}

func (p *midTurnEffortProvider) Send(ctx context.Context, in provider.SendInput, bus provider.Bus) error {
	p.mu.Lock()
	p.sends++
	round := p.sends
	p.mu.Unlock()

	var msg message.Message
	if round == 1 {
		close(p.started)
		select {
		case <-p.proceed:
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Second):
		}
		msg = message.Message{
			Role: message.RoleOutput,
			Content: []message.Content{
				{Type: message.ContentToolInvoke, ToolCallID: "tc_1", ToolName: "bash",
					Arguments: map[string]any{"command": "true"}},
			},
			StopReason: message.StopToolInvoke,
		}
	} else {
		rows, err := sentRows(in)
		if err != nil {
			return err
		}
		p.mu.Lock()
		p.rows, p.snap = rows, in.Snapshot
		p.mu.Unlock()
		msg = message.Message{
			Role:       message.RoleOutput,
			Content:    []message.Content{message.TextContent("done")},
			StopReason: message.StopEnd,
		}
	}
	bus.PushMessageEnd(string(msg.StopReason))
	bus.PushFigaro(msg)
	return nil
}

func sentRows(in provider.SendInput) ([]json.RawMessage, error) {
	enc := &anthropic.Anthropic{}
	d := provider.NewDeriver(in.Form, nil)
	entries, _ := store.TailAfter(in.FigLog, 0)
	var rows []json.RawMessage
	for _, e := range entries {
		msg, snap, ok := d.Next(e)
		if !ok {
			continue
		}
		encoded, err := enc.EncodeMessage(msg, snap)
		if err != nil {
			return nil, err
		}
		d.Commit(e, msg)
		rows = append(rows, encoded...)
	}
	return rows, nil
}

// Gluck sets system.thinking_effort while a turn is mid-tool-call; the very
// next request in that turn must carry the new level, placed after the tool
// results it follows, with the turn's opening still at the old level.
func TestAnEffortSetWhileATurnRunsReachesTheNextRequest(t *testing.T) {
	birth := form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.model":           json.RawMessage(`"claude-opus-5-5"`),
		"system.provider":        json.RawMessage(`"mock"`),
		"system.thinking_effort": json.RawMessage(`"high"`),
	}, nil)
	cb, _ := form.Open("")
	cb.Apply(birth)
	prov := &midTurnEffortProvider{started: make(chan struct{}), proceed: make(chan struct{})}
	testBE, testID := store.NewTestAria(t, "d", birth)
	a := figaro.NewAgent(figaro.Config{
		Backend:   testBE,
		Projector: uiir.New(nil),
		ID:        testID,
		Provider:  prov,
		Tools:     tool.NewRegistry(),
		Form:      cb,
	})
	t.Cleanup(a.Kill)

	ch, unsub := subscribeChan(a)
	defer unsub()
	a.SubmitPrompt(rpc.QuaRequest{Text: "look around"})

	select {
	case <-prov.started:
	case <-time.After(10 * time.Second):
		t.Fatal("round one never started")
	}
	low := form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.thinking_effort": json.RawMessage(`"low"`),
	}, nil)
	if _, _, err := a.SetAwaiting(context.Background(), low, 0, false); err != nil {
		t.Fatal(err)
	}
	close(prov.proceed)

	deadline := time.After(30 * time.Second)
	for done := false; !done; {
		select {
		case n := <-ch:
			done = n.Method == rpc.MethodTurnDone
		case <-deadline:
			t.Fatal("timeout waiting for turn.done")
		}
	}

	prov.mu.Lock()
	defer prov.mu.Unlock()
	if prov.sends != 2 {
		t.Fatalf("the provider ran %d round(s), want 2", prov.sends)
	}
	if v := prov.snap.Lookup("system.thinking_effort"); v == nil || *v != "low" {
		t.Fatalf("round two's bound form has effort %v, want low", v)
	}
	var shape []string
	for _, raw := range prov.rows {
		s := string(raw)
		switch {
		case strings.Contains(s, `"effort":"high"`):
			shape = append(shape, "effort:high")
		case strings.Contains(s, `"effort":"low"`):
			shape = append(shape, "effort:low")
		case strings.Contains(s, `"tool_result"`):
			shape = append(shape, "results")
		case strings.Contains(s, `"tool_use"`):
			shape = append(shape, "call")
		case strings.Contains(s, `"role":"user"`):
			shape = append(shape, "inquiry")
		default:
			shape = append(shape, "other")
		}
	}
	if got := strings.Join(shape, ","); got != "effort:high,inquiry,call,results,effort:low" {
		t.Fatalf("round two's rows = %s, want the turn opened at high and low right after the tool results", got)
	}
}
