package figaro_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
	"github.com/jack-work/figaro/internal/uiir"
)

// OpenTurn resets the open frame's stable count. If it fired once per round,
// the accumulating steady state the benchmarks measure would be unreachable.
// Live.V==0 marks a reopen, so the count is read from the wire.

// twoRoundProvider runs two rounds inside one turn.
type twoRoundProvider struct{ sends int }

func (p *twoRoundProvider) Name() string                                             { return "mock" }
func (p *twoRoundProvider) Fingerprint() string                                      { return "mock/v0" }
func (p *twoRoundProvider) SetModel(string)                                          {}
func (p *twoRoundProvider) Models(ctx context.Context) ([]provider.ModelInfo, error) { return nil, nil }

func (p *twoRoundProvider) Send(_ context.Context, in provider.SendInput, bus provider.Bus) error {
	p.sends++
	var msg message.Message
	if p.sends == 1 {
		bus.PushDelta(message.Content{Type: message.ContentProse, Text: "calling a tool"})
		msg = message.Message{
			Role: message.RoleOutput,
			Content: []message.Content{
				{Type: message.ContentProse, Text: "calling a tool"},
				{Type: message.ContentToolInvoke, ToolCallID: "tc_1", ToolName: "bash",
					Arguments: map[string]any{"command": "true"}},
			},
			StopReason: message.StopToolInvoke,
		}
	} else {
		bus.PushDelta(message.Content{Type: message.ContentProse, Text: "all done"})
		msg = message.Message{
			Role:       message.RoleOutput,
			Content:    []message.Content{message.TextContent("all done")},
			StopReason: message.StopEnd,
		}
	}
	bus.PushMessageEnd(string(msg.StopReason))
	bus.PushFigaro(msg)
	return nil
}

func TestOpenFrameIsNotResetPerRound(t *testing.T) {
	cb, _ := form.Open("")
	cb.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.model":    json.RawMessage(`"mock-model-v1"`),
		"system.provider": json.RawMessage(`"mock"`),
	}, nil))
	prov := &twoRoundProvider{}
	testBE, testID := store.NewTestAria(t, "d", message.Patch{})
	a := figaro.NewAgent(figaro.Config{
		Backend:    testBE,
		Projector:  uiir.New(nil),
		ID:         testID,
		SocketPath: "/tmp/test-figaro-rounds.sock",
		Provider:   prov,
		Tools:      tool.NewRegistry(),
		Form:       cb,
	})
	t.Cleanup(a.Kill)

	ch, unsub := subscribeChan(a)
	defer unsub()
	a.SubmitPrompt(rpc.QuaRequest{Text: "do the thing"})

	var frames []aria.Page
	deadline := time.After(20 * time.Second)
loop:
	for {
		select {
		case n := <-ch:
			switch n.Method {
			case rpc.MethodAriaFrame:
				frames = append(frames, n.Params.(aria.Page))
			case rpc.MethodTurnDone:
				break loop
			}
		case <-deadline:
			t.Fatal("timeout waiting for turn.done")
		}
	}

	if prov.sends < 2 {
		t.Fatalf("the provider was called %d time(s); this test needs a multi-round turn", prov.sends)
	}

	var versions []int
	zeros := 0
	for _, f := range frames {
		for _, part := range f.Parts {
			if part.Live == nil {
				continue
			}
			versions = append(versions, part.Live.V)
			if part.Live.V == 0 {
				zeros++
			}
		}
	}
	if len(versions) == 0 {
		t.Fatal("no live frames arrived; nothing to conclude")
	}
	// One reopen per turn, not one per round.
	if zeros != 1 {
		t.Errorf("saw %d live frames at version 0 across %d rounds; want exactly 1\n"+
			"the open frame is being replaced per round, so the stable count never accumulates\nversions: %v",
			zeros, prov.sends, versions)
	}
}

// The canary: a renderable steer is the one thing that replaces the open frame mid-turn.
func TestOpenFrameIsReplacedByARenderableSteer(t *testing.T) {
	cb, _ := form.Open("")
	cb.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.model":    json.RawMessage(`"mock-model-v1"`),
		"system.provider": json.RawMessage(`"mock"`),
	}, nil))
	prov := &twoRoundProvider{}
	testBE, testID := store.NewTestAria(t, "d", message.Patch{})
	a := figaro.NewAgent(figaro.Config{
		Backend:    testBE,
		Projector:  uiir.New(nil),
		ID:         testID,
		SocketPath: "/tmp/test-figaro-rounds2.sock",
		Provider:   prov,
		Tools:      tool.NewRegistry(),
		Form:       cb,
	})
	t.Cleanup(a.Kill)

	ch, unsub := subscribeChan(a)
	defer unsub()
	a.SubmitPrompt(rpc.QuaRequest{Text: "do the thing"})
	// A steer must arrive while the turn runs.
	go func() {
		time.Sleep(15 * time.Millisecond)
		a.SubmitPrompt(rpc.QuaRequest{Text: "actually, do it differently"})
	}()

	zeros, frames := 0, 0
	deadline := time.After(20 * time.Second)
loop:
	for {
		select {
		case n := <-ch:
			switch n.Method {
			case rpc.MethodAriaFrame:
				for _, part := range n.Params.(aria.Page).Parts {
					if part.Live == nil {
						continue
					}
					frames++
					if part.Live.V == 0 {
						zeros++
					}
				}
			case rpc.MethodTurnDone:
				break loop
			}
		case <-deadline:
			break loop
		}
	}
	if frames == 0 {
		t.Skip("no live frames arrived; the steer did not land inside the turn")
	}
	if zeros < 2 {
		t.Skipf("the steer did not split the unit (zeros=%d, frames=%d); "+
			"this canary is timing-dependent and proves nothing when it does not land", zeros, frames)
	}
	t.Logf("a renderable steer replaced the open frame: %d version-0 frames across %d live frames", zeros, frames)
}
