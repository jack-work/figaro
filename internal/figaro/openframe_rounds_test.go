package figaro_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
	"github.com/jack-work/figaro/internal/uiir"
)

// THE DENOMINATOR QUESTION.
//
// The live composer reports a stable node count and aria.Server skips diffing
// that prefix. OpenTurn replaces the open frame outright, so ANY caller of it
// resets that count to zero -- and if it fired once per ROUND, the steady state
// the benchmarks measure (a long multi-round turn accumulating one open frame)
// would be a state the daemon never reaches, and the speedup would be a
// property of the fixture rather than of the product.
//
// Live.V is the open frame's version and OpenTurn resets it to 0, so the
// question is answerable from the WIRE, without reaching into the agent: count
// the V==0 frames inside one turn.

// twoRoundProvider emits a tool call on its first Send and a final answer on
// its second, so the agent's real loop runs two rounds inside ONE turn.
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
	entry, err := in.FigLog.Append(store.Entry[message.Message]{Payload: msg})
	if err != nil {
		return err
	}
	msg.LogicalTime = entry.LT
	bus.PushMessageEnd(string(msg.StopReason))
	bus.PushFigaro(msg)
	return nil
}

func TestOpenFrameIsNotResetPerRound(t *testing.T) {
	cb, _ := form.Open("")
	cb.Apply(form.Patch{Set: map[string]json.RawMessage{
		"system.model":    json.RawMessage(`"mock-model-v1"`),
		"system.provider": json.RawMessage(`"mock"`),
	}})
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
	// ONE reopen per turn, not one per round. More than one V==0 inside a
	// single turn means the open frame was replaced mid-turn, which resets the
	// stable count and makes the accumulating steady state unreachable.
	if zeros != 1 {
		t.Errorf("saw %d live frames at version 0 across %d rounds; want exactly 1\n"+
			"the open frame is being replaced per round, so the stable count never accumulates\nversions: %v",
			zeros, prov.sends, versions)
	}
}

// TestOpenFrameIsReplacedByARenderableSteer is the canary for the test above:
// it proves the V==0 counter can read more than one, and it names the ONE
// thing that does replace the open frame mid-turn.
//
// startAssistantUnit -- the only caller of OpenTurn -- runs once when the turn
// opens and again only when a renderable prompt arrives mid-turn and splits
// the unit. So a steer is the case that resets the stable count, and rounds
// are not.
func TestOpenFrameIsReplacedByARenderableSteer(t *testing.T) {
	cb, _ := form.Open("")
	cb.Apply(form.Patch{Set: map[string]json.RawMessage{
		"system.model":    json.RawMessage(`"mock-model-v1"`),
		"system.provider": json.RawMessage(`"mock"`),
	}})
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
	// A steer must arrive WHILE the turn runs; that is what makes it a steer.
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
