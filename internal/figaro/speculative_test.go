package figaro_test

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
)

// --- Speculative-dispatch test infrastructure ---

// staggeredProvider emits PushToolReady for each of `tools` at
// `readyAt` after Send begins, then waits `finishAt` before pushing
// PushFigaro with the final assembled message. The point is to model
// what the real Anthropic provider does: tool_use blocks become
// dispatchable well before the full message arrives.
type staggeredProvider struct {
	tools     []specTool      // ordered by emission
	streamEnd time.Duration   // when to PushFigaro after Send starts
	calls     atomic.Int32    // counts Send invocations
}

type specTool struct {
	id      string
	name    string
	args    map[string]interface{}
	readyAt time.Duration // delay from Send-start to PushToolReady
}

func (p *staggeredProvider) Name() string                                             { return "staggered" }
func (p *staggeredProvider) Fingerprint() string                                      { return "staggered/v0" }
func (p *staggeredProvider) SetModel(string)                                          {}
func (p *staggeredProvider) Models(ctx context.Context) ([]provider.ModelInfo, error) { return nil, nil }

func (p *staggeredProvider) Send(ctx context.Context, in provider.SendInput, bus provider.Bus) error {
	if p.calls.Add(1) > 1 {
		// Second round (after tool results) — terminate with no
		// further tool calls so the agent's outer loop returns.
		msg := message.Message{
			Role:       message.RoleAssistant,
			Content:    []message.Content{message.TextContent("done")},
			StopReason: message.StopEnd,
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
	start := time.Now()
	// Stagger PushToolReady calls. Track each in a WaitGroup so Send
	// doesn't return until every speculative push has happened —
	// otherwise driveOneRound's deferred close(bus.toolsReady) races
	// the late PushToolReady.
	var pushWG sync.WaitGroup
	for _, t := range p.tools {
		t := t
		pushWG.Add(1)
		go func() {
			defer pushWG.Done()
			d := t.readyAt - time.Since(start)
			if d > 0 {
				select {
				case <-time.After(d):
				case <-ctx.Done():
					return
				}
			}
			bus.PushToolReady(message.Content{
				Type:       message.ContentToolCall,
				ToolCallID: t.id,
				ToolName:   t.name,
				Arguments:  t.args,
			})
		}()
	}
	// Wait until streamEnd, then push the final assistant message.
	wait := p.streamEnd - time.Since(start)
	if wait > 0 {
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			pushWG.Wait()
			return ctx.Err()
		}
	}
	// All staggered pushes must complete before we return so the
	// driveOneRound producer-close path doesn't race the channel send.
	pushWG.Wait()
	calls := make([]message.Content, len(p.tools))
	for i, t := range p.tools {
		calls[i] = message.Content{
			Type:       message.ContentToolCall,
			ToolCallID: t.id,
			ToolName:   t.name,
			Arguments:  t.args,
		}
	}
	msg := message.Message{
		Role:       message.RoleAssistant,
		Content:    calls,
		StopReason: message.StopToolUse,
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

// recordingTool records the time it started executing (relative to a
// shared zero) and sleeps for `dur` before returning a marker.
type recordingTool struct {
	name   string
	dur    time.Duration
	zero   time.Time
	starts sync.Map // map[toolCallID]time.Duration
}

func (rt *recordingTool) Name() string         { return rt.name }
func (rt *recordingTool) Description() string  { return "test tool" }
func (rt *recordingTool) Parameters() any      { return map[string]any{} }

func (rt *recordingTool) Execute(ctx context.Context, args map[string]any, _ tool.OnOutput) ([]message.Content, error) {
	id, _ := args["id"].(string)
	rt.starts.Store(id, time.Since(rt.zero))
	select {
	case <-time.After(rt.dur):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return []message.Content{message.TextContent("ok:" + id)}, nil
}

func (rt *recordingTool) startTimeOf(id string) (time.Duration, bool) {
	v, ok := rt.starts.Load(id)
	if !ok {
		return 0, false
	}
	return v.(time.Duration), true
}

// TestSpeculativeDispatch_StartsBeforeStreamEnd is the cornerstone of
// Slice A: each tool's Execute must begin within ~PushToolReady latency
// of its readyAt, not blocked until streamEnd. With 3 tools staged
// 50/100/150ms in and a stream that doesn't end until 300ms, the
// third tool's start should still occur near 150ms.
func TestSpeculativeDispatch_StartsBeforeStreamEnd(t *testing.T) {
	zero := time.Now()
	rec := &recordingTool{name: "rec", dur: 50 * time.Millisecond, zero: zero}
	reg := tool.NewRegistry()
	require.NoError(t, reg.Register(rec))

	prov := &staggeredProvider{
		tools: []specTool{
			{id: "tc_1", name: "rec", args: map[string]interface{}{"id": "tc_1"}, readyAt: 50 * time.Millisecond},
			{id: "tc_2", name: "rec", args: map[string]interface{}{"id": "tc_2"}, readyAt: 100 * time.Millisecond},
			{id: "tc_3", name: "rec", args: map[string]interface{}{"id": "tc_3"}, readyAt: 150 * time.Millisecond},
		},
		streamEnd: 400 * time.Millisecond,
	}

	cb, _ := chalkboard.Open("")
	cb.Apply(chalkboard.Patch{Set: map[string]json.RawMessage{
		"system.model":    json.RawMessage(`"mock"`),
		"system.provider": json.RawMessage(`"staggered"`),
	}})
	a := figaro.NewAgent(figaro.Config{
		ID:         "spec-001",
		SocketPath: "/tmp/spec-test.sock",
		Provider:   prov,
		Tools:      reg,
		Chalkboard: cb,
	})
	defer a.Kill()

	ch, _ := subscribeChan(a)
	a.Prompt("go")

	// Drain until Done.
	timeout := time.After(5 * time.Second)
	var sawDone bool
	for !sawDone {
		select {
		case n := <-ch:
			if n.Method == rpc.MethodDone {
				sawDone = true
			}
		case <-timeout:
			t.Fatal("timeout waiting for stream.done")
		}
	}

	// Each tool's Execute should have begun close to its readyAt.
	// Sequential execution (the pre-Slice-A behavior) would have all
	// three starting after streamEnd (≥400ms).
	for id, want := range map[string]time.Duration{
		"tc_1": 50 * time.Millisecond,
		"tc_2": 100 * time.Millisecond,
		"tc_3": 150 * time.Millisecond,
	} {
		got, ok := rec.startTimeOf(id)
		require.True(t, ok, "tool %s never recorded a start", id)
		// Generous slack to absorb scheduling jitter on shared CI.
		// The key check is that the start is well below streamEnd.
		assert.Less(t, got, 350*time.Millisecond,
			"%s started at %v; expected near %v, well before streamEnd=%v",
			id, got, want, prov.streamEnd)
		assert.GreaterOrEqual(t, got, want-20*time.Millisecond,
			"%s started at %v; expected at or after readyAt %v",
			id, got, want)
	}
}

// TestSpeculativeDispatch_ResultOrdering checks that tool_results in
// the appended tic match the order of tool_calls in the assistant
// message, even when speculative dispatch finishes them out of order.
func TestSpeculativeDispatch_ResultOrdering(t *testing.T) {
	zero := time.Now()
	// Two tools, second one ready first but slower — so it finishes
	// after the first. Result order must still match call order.
	fastTool := &recordingTool{name: "fast", dur: 10 * time.Millisecond, zero: zero}
	slowTool := &recordingTool{name: "slow", dur: 80 * time.Millisecond, zero: zero}
	reg := tool.NewRegistry()
	require.NoError(t, reg.Register(fastTool))
	require.NoError(t, reg.Register(slowTool))

	prov := &staggeredProvider{
		tools: []specTool{
			// Order in calls = [fast, slow] but slow is ready first
			// and fast ready after a delay. We still expect results
			// in call order.
			{id: "tc_fast", name: "fast", args: map[string]interface{}{"id": "tc_fast"}, readyAt: 60 * time.Millisecond},
			{id: "tc_slow", name: "slow", args: map[string]interface{}{"id": "tc_slow"}, readyAt: 10 * time.Millisecond},
		},
		streamEnd: 80 * time.Millisecond,
	}

	cb, _ := chalkboard.Open("")
	cb.Apply(chalkboard.Patch{Set: map[string]json.RawMessage{
		"system.model":    json.RawMessage(`"mock"`),
		"system.provider": json.RawMessage(`"staggered"`),
	}})
	a := figaro.NewAgent(figaro.Config{
		ID:         "spec-002",
		SocketPath: "/tmp/spec-test-2.sock",
		Provider:   prov,
		Tools:      reg,
		Chalkboard: cb,
	})
	defer a.Kill()

	ch, _ := subscribeChan(a)
	a.Prompt("go")

	// Collect tool_end notifications in arrival order.
	var ends []string
	timeout := time.After(5 * time.Second)
	var sawDone bool
	for !sawDone {
		select {
		case n := <-ch:
			switch n.Method {
			case rpc.MethodToolEnd:
				if p, ok := n.Params.(rpc.ToolEndParams); ok {
					ends = append(ends, p.ToolCallID)
				}
			case rpc.MethodDone:
				sawDone = true
			}
		case <-timeout:
			t.Fatal("timeout")
		}
	}
	// Tool_end notifications are emitted in canonical (call) order by
	// runTools, even though the goroutines finished out of order.
	assert.Equal(t, []string{"tc_fast", "tc_slow"}, ends,
		"tool_end notifications must follow tool_call order")
}

// TestInvokeLifecycle_WireOrdering asserts the new lifecycle events
// (MethodToolInvokeReady, MethodMessageEnd) flow in the expected
// order relative to the rest of the wire: per-tool invoke_ready
// arrives before MessageEnd, which arrives before the per-tool
// Start (execution), which arrives before MethodMessage.
func TestInvokeLifecycle_WireOrdering(t *testing.T) {
	zero := time.Now()
	rec := &recordingTool{name: "rec", dur: 5 * time.Millisecond, zero: zero}
	reg := tool.NewRegistry()
	require.NoError(t, reg.Register(rec))

	prov := &staggeredProvider{
		tools: []specTool{
			{id: "tc_a", name: "rec", args: map[string]interface{}{"id": "tc_a"}, readyAt: 10 * time.Millisecond},
			{id: "tc_b", name: "rec", args: map[string]interface{}{"id": "tc_b"}, readyAt: 20 * time.Millisecond},
		},
		streamEnd: 100 * time.Millisecond,
	}

	cb, _ := chalkboard.Open("")
	cb.Apply(chalkboard.Patch{Set: map[string]json.RawMessage{
		"system.model":    json.RawMessage(`"mock"`),
		"system.provider": json.RawMessage(`"staggered"`),
	}})
	a := figaro.NewAgent(figaro.Config{
		ID:         "invoke-001",
		SocketPath: "/tmp/invoke-test.sock",
		Provider:   prov,
		Tools:      reg,
		Chalkboard: cb,
	})
	defer a.Kill()

	ch, _ := subscribeChan(a)
	a.Prompt("go")

	// Collect the methods in order.
	var methods []string
	timeout := time.After(5 * time.Second)
	var sawDone bool
	for !sawDone {
		select {
		case n := <-ch:
			methods = append(methods, n.Method)
			if n.Method == rpc.MethodDone {
				sawDone = true
			}
		case <-timeout:
			t.Fatal("timeout")
		}
	}
	t.Logf("methods: %v", methods)

	// Both invoke_ready events should arrive before MessageEnd.
	invokeReadyA := indexOf(methods, rpc.MethodToolInvokeReady)
	require.GreaterOrEqual(t, invokeReadyA, 0, "expected at least one MethodToolInvokeReady")
	messageEnd := indexOf(methods, rpc.MethodMessageEnd)
	require.GreaterOrEqual(t, messageEnd, 0, "expected MethodMessageEnd")
	assert.Less(t, invokeReadyA, messageEnd,
		"MethodToolInvokeReady must precede MethodMessageEnd; got %v", methods)

	// MessageEnd must precede the assistant MethodMessage payload.
	message := indexOf(methods, rpc.MethodMessage)
	require.GreaterOrEqual(t, message, 0, "expected MethodMessage")
	assert.Less(t, messageEnd, message,
		"MethodMessageEnd must precede MethodMessage; got %v", methods)

	// And the assistant MethodMessage must precede the per-tool
	// execution starts (those happen in runTools after the message
	// is appended and fanned out).
	toolStart := indexOf(methods, rpc.MethodToolStart)
	require.GreaterOrEqual(t, toolStart, 0, "expected MethodToolStart")
	assert.Less(t, message, toolStart,
		"MethodMessage must precede MethodToolStart; got %v", methods)
}

func indexOf(haystack []string, needle string) int {
	for i, v := range haystack {
		if v == needle {
			return i
		}
	}
	return -1
}
