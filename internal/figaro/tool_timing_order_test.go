package figaro_test

// THE GUARD THAT LIVES HERE, ASSERTED HERE.
//
// internal/compose memoizes the settled prefix of the open region and keys the
// memo on MESSAGE CONTENT only. A tool node's timings come from a map the key
// cannot see, so a timing stamped AFTER its result message became durable
// would be invisible: the memo would keep serving FinishedAt:0 for the rest of
// the turn. compose asserts that blindness as a fact
// (TestPrefixIsBlindToATimingStampedAfterDurability); what makes it harmless
// is an ordering that lives HERE and knows nothing about the memo -- toolEnd
// stamps finishToolTiming, and the tool_result tic is assembled and appended
// afterwards, in the same goroutine.
//
// Nothing failed the day that ordering could have moved. Now something does.

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
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
	"github.com/jack-work/figaro/internal/uiir"
)

// streamingTool writes a few chunks and then returns, so the node exists,
// streams, and finishes -- the shape whose timing the memo cannot see.
type streamingTool struct{}

func (streamingTool) Name() string        { return "bash" }
func (streamingTool) Description() string { return "test tool" }
func (streamingTool) Parameters() any     { return map[string]any{} }

func (streamingTool) Execute(_ context.Context, _ map[string]any, onOutput tool.OnOutput) ([]message.Content, error) {
	for i := 0; i < 3; i++ {
		if onOutput != nil {
			onOutput([]byte("chunk\n"))
		}
		time.Sleep(2 * time.Millisecond)
	}
	return []message.Content{{Type: message.ContentProse, Text: "tool done"}}, nil
}

// TestToolTimingIsStampedNoLaterThanItsDurableResult reads the WIRE, not the
// agent: the frame that turns the tool node's status to "ok" is the frame in
// which its result became durable. finished_at must have been set by then --
// in that same delta or an earlier one.
func TestToolTimingIsStampedNoLaterThanItsDurableResult(t *testing.T) {
	cb, _ := form.Open("")
	cb.Apply(form.Patch{Set: map[string]json.RawMessage{
		"system.model":    json.RawMessage(`"mock-model-v1"`),
		"system.provider": json.RawMessage(`"mock"`),
	}})
	reg := tool.NewRegistry()
	if err := reg.Register(streamingTool{}); err != nil {
		t.Fatal(err)
	}
	prov := &twoRoundProvider{}
	testBE, testID := store.NewTestAria(t, "d", message.Patch{})
	a := figaro.NewAgent(figaro.Config{
		Backend:    testBE,
		Projector:  uiir.New(nil),
		ID:         testID,
		SocketPath: "/tmp/test-figaro-timing-order.sock",
		Provider:   prov,
		Tools:      reg,
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

	firstOK, firstFinished, seen := scanTimingOrder(frames)

	// VACUITY GUARD. A run in which the tool never reported at all would
	// satisfy every assertion below by having nothing to check.
	if seen == 0 || firstOK < 0 {
		t.Fatalf("no tool node ever reached status ok across %d frames (%d status deltas); this measurement would be vacuous", len(frames), seen)
	}
	if firstFinished < 0 {
		t.Fatalf("the tool finished but finished_at was never set on the wire: the timing never reached a frame at all")
	}
	if firstFinished > firstOK {
		t.Errorf("finished_at was stamped AFTER the result became durable: status ok at frame %d, finished_at at frame %d\n"+
			"internal/compose memoizes a message once its result is present and cannot see `timings`,\n"+
			"so a timing stamped after that point is frozen on screen for the rest of the turn.\n"+
			"The guard that moved is the toolEnd ordering in turn.go (finishToolTiming before the tic is assembled).",
			firstOK, firstFinished)
	}
}

// scanTimingOrder reports the first frame that set a tool node's status to
// "ok", the first that set finished_at, and how many status deltas were seen
// at all (the vacuity meter).
func scanTimingOrder(frames []aria.Page) (firstOK, firstFinished, seen int) {
	firstOK, firstFinished = -1, -1
	for i, f := range frames {
		for _, part := range f.Parts {
			if part.Live == nil {
				continue
			}
			for _, d := range part.Live.Nodes {
				if d.Set == nil {
					continue
				}
				if v, ok := d.Set["finished_at"]; ok && v != nil && firstFinished < 0 {
					firstFinished = i
				}
				if v, ok := d.Set["status"]; ok {
					seen++
					if s, _ := v.(string); s == "ok" && firstOK < 0 {
						firstOK = i
					}
				}
			}
		}
	}
	return firstOK, firstFinished, seen
}

// THE CANARY FOR THE ORDERING ARM.
//
// Mutating turn.go to stamp the timing late does NOT exercise
// `firstFinished > firstOK`: a stamp delayed even 8 ms lands after the whole
// mock turn is over, so the "never arrived" arm fires instead. Proven by
// running it. So the ordering comparison is canaried HERE, against hand-built
// frames, or it would be an assertion no observation has ever reached.
func TestTimingOrderScanCatchesAnInversion(t *testing.T) {
	frame := func(set map[string]any) aria.Page {
		return aria.Page{Parts: []aria.TurnPart{{
			Turn: aria.Turn{Live: &aria.Live{Nodes: []aria.NodeDelta{{ID: 0, Set: set}}}},
		}}}
	}
	inverted := []aria.Page{
		frame(map[string]any{"status": "running"}),
		frame(map[string]any{"status": "ok"}),            // result durable here
		frame(map[string]any{"finished_at": int64(300)}), // timing lands AFTER
	}
	ok, fin, seen := scanTimingOrder(inverted)
	if seen == 0 {
		t.Fatal("the scan saw no status deltas in a fixture built entirely of them")
	}
	if !(fin > ok) {
		t.Fatalf("the scan did not detect an inversion it was handed: status ok at %d, finished_at at %d", ok, fin)
	}
	ordered := []aria.Page{
		frame(map[string]any{"finished_at": int64(300)}),
		frame(map[string]any{"status": "ok"}),
	}
	if ok, fin, _ := scanTimingOrder(ordered); fin > ok {
		t.Fatalf("the scan reported an inversion on correctly ordered frames: status ok at %d, finished_at at %d", ok, fin)
	}
}
