package figaro

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/turns"
)

// The peek and the walk must agree on every log, or a resumed aria mints ids
// that collide with history it cannot see. The walk is the oracle.
func TestSeedTurnIDPeeksToTheSameAnswerTheWalkFinds(t *testing.T) {
	for _, n := range []int{0, 1, 2, 7, 50, 200} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			log := store.GuardIR(store.NewMemLog[message.Message]())
			for i := 0; i < n; i++ {
				var m message.Message
				switch i % 3 {
				case 0:
					m = message.Message{Role: message.RoleInput,
						Content: []message.Content{message.TextContent(fmt.Sprintf("q%d", i))}}
				case 1:
					m = message.Message{Role: message.RoleOutput,
						Content: []message.Content{message.TextContent(fmt.Sprintf("a%d", i))}}
				default:
					// Input role, but does not open a turn.
					m = message.Message{Role: message.RoleInput, Steering: true,
						Content: []message.Content{message.TextContent("steer")}}
				}
				_, err := log.Append(store.Entry[message.Message]{Payload: m})
				require.NoError(t, err)
			}

			a := &Agent{figLog: log}
			peeked := a.seedTurnID()
			walked := turns.StampIDs(unwrapMessages(log.Read()))
			require.Equal(t, walked, peeked,
				"peek says %d, the walk says %d", peeked, walked)
		})
	}
}

// A fork inherits its parent's numbering rather than restarting at 1.
func TestSeedTurnIDInheritsAcrossAFork(t *testing.T) {
	parent := store.GuardIR(store.NewMemLog[message.Message]())
	for i := 0; i < 6; i++ {
		m := message.Message{Role: message.RoleInput,
			Content: []message.Content{message.TextContent(fmt.Sprintf("q%d", i))}}
		_, err := parent.Append(store.Entry[message.Message]{Payload: m})
		require.NoError(t, err)
	}
	require.Equal(t, uint64(6), (&Agent{figLog: parent}).seedTurnID())

	// A child seeded from the same records continues, rather than restarting.
	child := store.GuardIR(store.NewMemLog[message.Message]())
	for _, e := range parent.Read() {
		_, err := child.Append(store.Entry[message.Message]{Payload: e.Payload})
		require.NoError(t, err)
	}
	require.Equal(t, uint64(6), (&Agent{figLog: child}).seedTurnID(),
		"the child restarted its numbering instead of continuing its parent's")
}
