package figaro_test

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
	"github.com/jack-work/figaro/internal/uiir"
)

// LEGALITY ACROSS A RELOAD.
//
// A turn cut short mid-tool-call is repaired in memory by the agent that was
// running it. That is not enough to call the conversation legal: the aria goes
// dormant, the daemon restarts, and the NEXT prompt is served by a completely
// new Agent reading the same bytes off disk. repairInterruptedTail runs at boot
// for exactly this reason, and a legality that only holds in a warm agent is
// not legality at all: it is a cache.
//
// So this test interrupts, then throws the agent and the backend away, reopens
// the store, and asks the fresh agent to answer. What it asserts is what the
// user experiences: the next message is answered, and the IR it was answered
// from has no dangling tool_use.
func TestInterruptLegality_SurvivesAReload(t *testing.T) {
	dir := t.TempDir()

	backend1, err := store.NewXwalBackend(dir, 0)
	require.NoError(t, err)

	outfit, err := backend1.CreateOutfit("d", message.Patch{Set: map[string]json.RawMessage{
		"system.model":    json.RawMessage(`"m"`),
		"system.provider": json.RawMessage(`"interrupt-test"`),
	}})
	require.NoError(t, err)
	conv, err := backend1.CreateConversation(outfit)
	require.NoError(t, err)

	toolStarted := make(chan struct{})
	registry := tool.NewRegistry()
	registry.MustRegister(&waitTool{started: toolStarted})

	a1 := figaro.NewAgent(figaro.Config{
		Projector:  uiir.New(nil),
		ID:         conv,
		SocketPath: filepath.Join(t.TempDir(), "a1.sock"),
		Provider:   &interruptProvider{mode: "tool", started: make(chan struct{})},
		Backend:    backend1,
		Tools:      registry,
	})

	sink := make(chan rpc.Notification, 64)
	unsub := a1.Subscribe(&reproNotifier{ch: sink})

	a1.SubmitPrompt(rpc.QuaRequest{Text: "call the tool"})
	select {
	case <-toolStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("the tool never started; nothing was in flight to interrupt")
	}

	a1.Interrupt()
	waitFor(t, sink, rpc.MethodTurnDone, 5*time.Second)
	unsub()

	// Everything warm is now thrown away: the agent, its in-memory turn state,
	// and the backend handle. What survives is bytes.
	a1.Kill()
	require.NoError(t, backend1.Close())

	backend2, err := store.NewXwalBackend(dir, 0)
	require.NoError(t, err)
	defer backend2.Close()

	a2 := figaro.NewAgent(figaro.Config{
		Projector:  uiir.New(nil),
		ID:         conv,
		SocketPath: filepath.Join(t.TempDir(), "a2.sock"),
		Provider:   &staticReplyProvider{reply: "answered after the reload"},
		Backend:    backend2,
	})
	defer a2.Kill()

	// The claim, as the user meets it: the aria answers the next thing said to
	// it. If the reloaded IR were illegal this is where it would die.
	sink2 := make(chan rpc.Notification, 64)
	unsub2 := a2.Subscribe(&reproNotifier{ch: sink2})
	defer unsub2()
	a2.SubmitPrompt(rpc.QuaRequest{Text: "still there?"})
	waitFor(t, sink2, rpc.MethodTurnDone, 5*time.Second)

	msgs := a2.Context()
	assertNoDanglingToolUse(t, msgs)

	var answered bool
	for _, m := range msgs {
		if m.Role != message.RoleOutput {
			continue
		}
		for _, c := range m.Content {
			if c.Type == message.ContentProse && c.Text == "answered after the reload" {
				answered = true
			}
		}
	}
	assert.True(t, answered, "the reloaded aria never answered the next prompt")
}

// assertNoDanglingToolUse is invariant L1/L2: every tool_invoke is answered by
// a tool_result with the same id in a LATER message, and the tail is not an
// assistant message with an unanswered call.
func assertNoDanglingToolUse(t *testing.T, msgs []message.Message) {
	t.Helper()
	uses := map[string]int{}
	results := map[string]int{}
	for i, m := range msgs {
		for _, c := range m.Content {
			switch c.Type {
			case message.ContentToolInvoke:
				uses[c.ToolCallID] = i
			case message.ContentToolResult:
				results[c.ToolCallID] = i
			}
		}
	}
	require.NotEmpty(t, uses, "the fixture must actually leave a tool_use in the IR")
	for id, at := range uses {
		answeredAt, ok := results[id]
		require.True(t, ok, "tool_use %q is dangling after the reload", id)
		require.Greater(t, answeredAt, at, "tool_result for %q must follow its tool_use", id)
	}
}
