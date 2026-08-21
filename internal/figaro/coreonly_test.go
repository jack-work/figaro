package figaro_test

import (
	"encoding/json"
	"github.com/jack-work/figaro/api/message"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/store"
)

// A figaro with NO Projector is the shippable core: it runs turns and persists
// fig IR, and renders nothing. This is the claim behind the Projector
// interface, so it is asserted directly rather than inferred from the fact that
// the code compiles.
//
// If this test fails, a build without the UI IR conversion is no longer viable
// and the boundary in projector_boundary_test.go has become decorative.
func TestCoreOnlyAgentRunsTurnsAndPersistsFigIR(t *testing.T) {
	cb, _ := form.Open("")
	cb.Apply(form.Patch{Set: map[string]json.RawMessage{
		"system.model":      json.RawMessage(`"mock-model-v1"`),
		"system.provider":   json.RawMessage(`"mock"`),
		"system.max_tokens": json.RawMessage(`1024`),
	}})

	testBE, testID := store.NewTestAria(t, "d", message.Patch{})
	a := figaro.NewAgent(figaro.Config{
		Backend: testBE,
		// Projector deliberately omitted: this IS the test.
		ID:         testID,
		SocketPath: filepath.Join(t.TempDir(), "figaro.sock"),
		Provider:   &mockProvider{response: "pong"},
		Form:       cb,
	})
	t.Cleanup(a.Kill)

	a.SubmitPrompt(rpc.QuaRequest{Text: "ping"})

	// The engine must reach a durable fig IR record without any projection.
	deadline := time.Now().Add(5 * time.Second)
	var msgs int
	for time.Now().Before(deadline) {
		if msgs = len(conversationOnly(a.Context())); msgs >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.GreaterOrEqual(t, msgs, 2,
		"a core-only agent must still run the turn and persist fig IR")

	// Turn ids are fig IR, not projection: they must be stamped anyway.
	var stamped bool
	for _, m := range a.Context() {
		if message.IsCeremonial(m) {
			continue // ceremony carries no turn
		}
		if m.TurnID != 0 {
			stamped = true
		}
	}
	require.True(t, stamped, "turn ids are fig IR and must be stamped without a projector")

	// And the UI IR surface is simply empty: not broken, not panicking.
	require.NotPanics(t, func() {
		p := a.Read(aria.Anchor{}, 1<<20)
		require.Empty(t, p.Parts, "no projector means no rendered turns")
	}, "reads must degrade to empty, never panic, when there is no projection")
}
