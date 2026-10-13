package angelus

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/uiir"
)

// The floor is honoured in the walk both sources run, so the two pages are the same bytes.
func TestFloorReadsTheSameFromTheAgentAndFromTheStore(t *testing.T) {
	backend, id := benchStore(t, 24) // twelve turns
	floor := aria.Anchor{Turn: 9}

	fromStore, err := NewAriaReader(backend, uiir.New(nil)).Page(id, aria.Anchor{}, floor, 1<<16, true)
	require.NoError(t, err)

	agent := figaro.NewAgent(figaro.Config{
		Projector:  uiir.New(nil),
		ID:         id,
		Backend:    backend,
		SocketPath: filepath.Join(t.TempDir(), "figaro.sock"),
	})
	t.Cleanup(agent.Kill)
	fromAgent := agent.ReadBefore(aria.Anchor{}, floor, 1<<16)

	require.NotEmpty(t, fromStore.Parts)
	require.Equal(t, uint64(9), fromStore.Parts[0].ID, "the page must begin at the floor")
	require.True(t, fromStore.More.Before, "eight turns sit below the floor")
	for _, part := range fromStore.Parts {
		require.GreaterOrEqual(t, part.ID, uint64(9), "a part below the floor")
	}

	// Metrics are per source, so the comparison is the page itself.
	require.Equal(t, mustJSON(t, fromStore.Parts), mustJSON(t, fromAgent.Parts))
	require.Equal(t, fromStore.More, fromAgent.More)
	require.Equal(t, fromStore.Prev, fromAgent.Prev)
	require.Equal(t, fromStore.Next, fromAgent.Next)

	// And the floor is what made the page short, on both sides.
	openPage, err := NewAriaReader(backend, uiir.New(nil)).Page(id, aria.Anchor{}, aria.Anchor{}, 1<<16, true)
	require.NoError(t, err)
	require.Greater(t, len(openPage.Parts), len(fromStore.Parts))
	require.Greater(t, len(mustJSON(t, openPage.Parts)), len(mustJSON(t, fromStore.Parts)))
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}
