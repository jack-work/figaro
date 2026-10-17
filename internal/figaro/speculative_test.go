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

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
	"github.com/jack-work/figaro/internal/uiir"
)

// staggeredProvider emits PushToolReady at readyAt and PushFigaro at finishAt,
// as the real provider makes tool_use dispatchable before the message ends.
type staggeredProvider struct {
	tools     []specTool    // ordered by emission
	streamEnd time.Duration // when to PushFigaro after Send starts
	calls     atomic.Int32  // counts Send invocations

	// streamEnded is set just before the final message is pushed; a tool that
	// observes it false was dispatched speculatively. A happens-before, not a duration.
	streamEnded atomic.Bool

	modelMu sync.Mutex
	models  []string // system.model seen at each Send, in call order
}

type specTool struct {
	id      string
	name    string
	args    map[string]interface{}
	readyAt time.Duration // delay from Send-start to PushToolReady
}

func (p *staggeredProvider) Name() string        { return "staggered" }
func (p *staggeredProvider) Fingerprint() string { return "staggered/v0" }
func (p *staggeredProvider) SetModel(string)     {}
func (p *staggeredProvider) Models(ctx context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}

func (p *staggeredProvider) Send(ctx context.Context, in provider.SendInput, bus provider.Bus) error {
	p.modelMu.Lock()
	if v := in.Snapshot.Lookup("system.model"); v != nil {
		p.models = append(p.models, *v)
	} else {
		p.models = append(p.models, "")
	}
	p.modelMu.Unlock()
	if p.calls.Add(1) > 1 {
		msg := message.Message{
			Role:       message.RoleOutput,
			Content:    []message.Content{message.TextContent("done")},
			StopReason: message.StopEnd,
		}
		bus.PushMessageEnd(string(msg.StopReason))
		bus.PushFigaro(msg)
		return nil
	}
	start := time.Now()
	// Send must not return until every staggered push has happened, or the
	// deferred close(bus.toolsReady) races a late PushToolReady.
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
	wait := p.streamEnd - time.Since(start)
	if wait > 0 {
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			pushWG.Wait()
			return ctx.Err()
		}
	}
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
		Role:       message.RoleOutput,
		Content:    calls,
		StopReason: message.StopToolInvoke,
	}
	p.streamEnded.Store(true)
	bus.PushMessageEnd(string(msg.StopReason))
	bus.PushFigaro(msg)
	return nil
}

// recordingTool records its start time and sleeps dur.
type recordingTool struct {
	name        string
	dur         time.Duration
	zero        time.Time
	starts      sync.Map // map[toolCallID]time.Duration
	streamEnded *atomic.Bool
	late        sync.Map // map[toolCallID]bool -- true if the stream had ended
}

func (rt *recordingTool) Name() string        { return rt.name }
func (rt *recordingTool) Description() string { return "test tool" }
func (rt *recordingTool) Parameters() any     { return map[string]any{} }

func (rt *recordingTool) Execute(ctx context.Context, args map[string]any, _ tool.OnOutput) ([]message.Content, error) {
	id, _ := args["id"].(string)
	if rt.streamEnded != nil {
		rt.late.Store(id, rt.streamEnded.Load())
	}
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

// startedAfterStreamEnd is what sequential dispatch looks like from inside the tool.
func (rt *recordingTool) startedAfterStreamEnd(id string) (bool, bool) {
	v, ok := rt.late.Load(id)
	if !ok {
		return false, false
	}
	return v.(bool), true
}

// Each tool's Execute begins when its PushToolReady arrives, not when the stream ends.
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
	rec.streamEnded = &prov.streamEnded

	cb, _ := form.Open("")
	cb.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.model":    json.RawMessage(`"mock"`),
		"system.provider": json.RawMessage(`"staggered"`),
	}, nil))
	testBE, testID := store.NewTestAria(t, "d", message.Patch{})
	a := figaro.NewAgent(figaro.Config{
		Backend:   testBE,
		Projector: uiir.New(nil),
		ID:        testID,
		Provider:  prov,
		Tools:     reg,
		Form:      cb,
	})
	defer a.Kill()

	ch, _ := subscribeChan(a)
	submitPrompt(a, "go")

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

	// Order, not duration: a `start < 350ms` proxy went red under a loaded suite
	// while the ordering held.
	for id, want := range map[string]time.Duration{
		"tc_1": 50 * time.Millisecond,
		"tc_2": 100 * time.Millisecond,
		"tc_3": 150 * time.Millisecond,
	} {
		late, ok := rec.startedAfterStreamEnd(id)
		require.True(t, ok, "tool %s never recorded a start", id)
		assert.False(t, late,
			"%s began only after the final assistant message: that is sequential dispatch, not speculative", id)

		// And not before it was ready, which load cannot cause.
		got, _ := rec.startTimeOf(id)
		assert.GreaterOrEqual(t, got, want-20*time.Millisecond,
			"%s started at %v; expected at or after readyAt %v",
			id, got, want)
	}
}

// tool_results match tool_call order even when tools finish out of order.
func TestSpeculativeDispatch_ResultOrdering(t *testing.T) {
	zero := time.Now()
	fastTool := &recordingTool{name: "fast", dur: 10 * time.Millisecond, zero: zero}
	slowTool := &recordingTool{name: "slow", dur: 80 * time.Millisecond, zero: zero}
	reg := tool.NewRegistry()
	require.NoError(t, reg.Register(fastTool))
	require.NoError(t, reg.Register(slowTool))

	prov := &staggeredProvider{
		tools: []specTool{
			{id: "tc_fast", name: "fast", args: map[string]interface{}{"id": "tc_fast"}, readyAt: 60 * time.Millisecond},
			{id: "tc_slow", name: "slow", args: map[string]interface{}{"id": "tc_slow"}, readyAt: 10 * time.Millisecond},
		},
		streamEnd: 80 * time.Millisecond,
	}

	cb, _ := form.Open("")
	cb.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.model":    json.RawMessage(`"mock"`),
		"system.provider": json.RawMessage(`"staggered"`),
	}, nil))
	testBE, testID := store.NewTestAria(t, "d", message.Patch{})
	a := figaro.NewAgent(figaro.Config{
		Backend:   testBE,
		Projector: uiir.New(nil),
		ID:        testID,
		Provider:  prov,
		Tools:     reg,
		Form:      cb,
	})
	defer a.Kill()

	ch, _ := subscribeChan(a)
	submitPrompt(a, "go")
	waitTurnDone(t, ch)

	toolResult := findToolResult(a.Context())
	require.NotNil(t, toolResult, "expected a tool_result message in the IR")
	var ids []string
	for _, c := range toolResult.Content {
		if c.Type == message.ContentToolResult {
			ids = append(ids, c.ToolCallID)
		}
	}
	assert.Equal(t, []string{"tc_fast", "tc_slow"}, ids,
		"tool_result blocks must follow tool_call order")
}

func waitTurnDone(t *testing.T, ch <-chan rpc.Notification) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case n := <-ch:
			if n.Method == rpc.MethodTurnDone {
				return
			}
		case <-timeout:
			t.Fatal("timeout waiting for turn.done")
		}
	}
}

func findToolResult(msgs []message.Message) *message.Message {
	for i := len(msgs) - 1; i >= 0; i-- {
		if hasToolResultBlocks(msgs[i]) {
			return &msgs[i]
		}
	}
	return nil
}

func hasToolResultBlocks(m message.Message) bool {
	for _, c := range m.Content {
		if c.Type == message.ContentToolResult {
			return true
		}
	}
	return false
}

// A tool-calling turn lands user, assistant (tool_invoke), tool_result, assistant.
func TestToolTurn_IRStructure(t *testing.T) {
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

	cb, _ := form.Open("")
	cb.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.model":    json.RawMessage(`"mock"`),
		"system.provider": json.RawMessage(`"staggered"`),
	}, nil))
	testBE, testID := store.NewTestAria(t, "d", message.Patch{})
	a := figaro.NewAgent(figaro.Config{
		Backend:   testBE,
		Projector: uiir.New(nil),
		ID:        testID,
		Provider:  prov,
		Tools:     reg,
		Form:      cb,
	})
	defer a.Kill()

	ch, _ := subscribeChan(a)
	submitPrompt(a, "go")
	waitTurnDone(t, ch)

	msgs := conversationOnly(a.Context())
	roles := make([]message.Role, len(msgs))
	for i, m := range msgs {
		roles[i] = m.Role
	}
	require.Equal(t, []message.Role{
		message.RoleInput,
		message.RoleOutput,
		message.RoleInput,
		message.RoleOutput,
	}, roles, "tool turn must produce this message sequence")

	assistant := msgs[1]
	assert.Equal(t, message.StopToolInvoke, assistant.StopReason)
	assert.Len(t, assistantToolInvokeIDs(assistant), 2)

	toolResult := msgs[2]
	assert.True(t, hasToolResultBlocks(toolResult))
	assert.Len(t, toolResult.Content, 2)
}

func assistantToolInvokeIDs(m message.Message) []string {
	var ids []string
	for _, c := range m.Content {
		if c.Type == message.ContentToolInvoke {
			ids = append(ids, c.ToolCallID)
		}
	}
	return ids
}

// conversationOnly drops genesis and the birth patch.
func conversationOnly(msgs []message.Message) []message.Message {
	var out []message.Message
	for _, m := range msgs {
		if !message.IsCeremonial(m) {
			out = append(out, m)
		}
	}
	return out
}
