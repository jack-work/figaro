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
				Type:       message.ContentToolInvoke,
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
			Type:       message.ContentToolInvoke,
			ToolCallID: t.id,
			ToolName:   t.name,
			Arguments:  t.args,
		}
	}
	msg := message.Message{
		Role:       message.RoleAssistant,
		Content:    calls,
		StopReason: message.StopToolInvoke,
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
			if n.Method == rpc.MethodTurnDone {
				sawDone = true
			}
		case <-timeout:
			t.Fatal("timeout waiting for turn.done")
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

	// The sealed tool_result message carries one tool_result block per
	// call, in canonical (call) order, even though the tools finished
	// out of order.
	var toolResult *message.Message
	timeout := time.After(5 * time.Second)
	var sawDone bool
	for !sawDone {
		select {
		case n := <-ch:
			switch n.Method {
			case rpc.MethodLogEntry:
				if p, ok := n.Params.(rpc.LogEntry); ok && hasToolResultBlocks(p.Message) {
					m := p.Message
					toolResult = &m
				}
			case rpc.MethodTurnDone:
				sawDone = true
			}
		case <-timeout:
			t.Fatal("timeout")
		}
	}
	require.NotNil(t, toolResult, "expected a sealed tool_result message")
	var ids []string
	for _, c := range toolResult.Content {
		if c.Type == message.ContentToolResult {
			ids = append(ids, c.ToolCallID)
		}
	}
	assert.Equal(t, []string{"tc_fast", "tc_slow"}, ids,
		"tool_result blocks must follow tool_call order")
}

// hasToolResultBlocks reports whether m carries any tool_result block.
func hasToolResultBlocks(m message.Message) bool {
	for _, c := range m.Content {
		if c.Type == message.ContentToolResult {
			return true
		}
	}
	return false
}

// TestToolTurn_FrameOrdering asserts the log.* frame shape of a
// tool-calling turn: the sealed entries arrive in index order —
// user prompt, assistant (tool_invoke), tool_result, assistant (final)
// — and the assistant's tool_invoke message seals before the
// tool_result message it triggers. turn.done fires last.
func TestToolTurn_FrameOrdering(t *testing.T) {
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

	// Collect sealed entries (log.entry) in order, plus the terminal
	// turn.done.
	var sealed []rpc.LogEntry
	var doneSeen bool
	timeout := time.After(5 * time.Second)
	for !doneSeen {
		select {
		case n := <-ch:
			switch n.Method {
			case rpc.MethodLogEntry:
				if p, ok := n.Params.(rpc.LogEntry); ok {
					sealed = append(sealed, p)
				}
			case rpc.MethodTurnDone:
				doneSeen = true
			}
		case <-timeout:
			t.Fatal("timeout")
		}
	}

	roles := make([]message.Role, len(sealed))
	for i, e := range sealed {
		roles[i] = e.Message.Role
	}
	require.Equal(t, []message.Role{
		message.RoleUser,      // prompt tic
		message.RoleAssistant, // tool_invoke
		message.RoleUser,      // tool_result
		message.RoleAssistant, // final reply
	}, roles, "tool-turn sealed entries must follow this index order")

	// Entries are sealed in strictly increasing index order.
	for i := 1; i < len(sealed); i++ {
		assert.Greater(t, sealed[i].Index, sealed[i-1].Index,
			"entries must seal in increasing index order")
	}

	// The assistant entry stops on tool_invoke and carries both calls.
	assistant := sealed[1].Message
	assert.Equal(t, message.StopToolInvoke, assistant.StopReason)
	assert.Len(t, assistantToolInvokeIDs(assistant), 2)

	// The tool_result entry pairs one result per call.
	toolResult := sealed[2].Message
	assert.True(t, hasToolResultBlocks(toolResult))
	assert.Len(t, toolResult.Content, 2)
}

// assistantToolInvokeIDs returns the tool_call_ids of an assistant
// message's tool_invoke blocks.
func assistantToolInvokeIDs(m message.Message) []string {
	var ids []string
	for _, c := range m.Content {
		if c.Type == message.ContentToolInvoke {
			ids = append(ids, c.ToolCallID)
		}
	}
	return ids
}
