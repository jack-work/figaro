package angelus_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/sdk"
)

func threeTurnAria(t *testing.T, a *angelus.Angelus, acli *sdk.Angelus, ctx context.Context) string {
	t.Helper()
	created, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	done := make(chan struct{}, 1)
	fcli, err := sdk.DialAria(ariaAddr(a, created.FigaroID), func(method string, _ json.RawMessage) {
		if method == "turn.done" {
			select {
			case done <- struct{}{}:
			default:
			}
		}
	})
	require.NoError(t, err)
	defer fcli.Close()
	for i := 1; i <= 3; i++ {
		_, _, err := fcli.Qua(ctx, fmt.Sprintf("prompt %d", i), nil)
		require.NoError(t, err)
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("turn %d never finished", i)
		}
		waitIdle(t, a, created.FigaroID)
	}
	return created.FigaroID
}

func promptsOf(t *testing.T, acli *sdk.Angelus, ctx context.Context, id string) []string {
	t.Helper()
	resp, err := acli.IRAll(ctx, id, 0)
	require.NoError(t, err)
	var out []string
	for _, e := range resp.Entries {
		var m message.Message
		if json.Unmarshal(e.Payload, &m) != nil || m.Role != message.RoleInput {
			continue
		}
		for _, c := range m.Content {
			if c.Type == message.ContentProse {
				out = append(out, c.Text)
			}
		}
	}
	return out
}

// gen4 take 29: `figaro succeed @figaro --turn 4` on a three-turn aria failed
// with "has no turn 4 (it has turns 1..3)". A fork at turn N replaces turn N;
// one past the last replaces nothing, so it is a head fork and keeps every
// turn, the same as asking for the head.
func TestAForkOnePastTheLastTurnIsAHeadFork(t *testing.T) {
	a, acli, ctx := daemonFixture(t)
	parent := threeTurnAria(t, a, acli, ctx)
	all := []string{"prompt 1", "prompt 2", "prompt 3"}
	require.Equal(t, all, promptsOf(t, acli, ctx, parent))

	head, err := acli.Fork(ctx, parent, 0, 0, nil, nil)
	require.NoError(t, err)
	require.Equal(t, all, promptsOf(t, acli, ctx, head.Alternative), "a head fork keeps every turn")

	past, err := acli.Fork(ctx, parent, 4, 0, nil, nil)
	require.NoError(t, err, "turn 4 on a three-turn aria is its head")
	require.Equal(t, all, promptsOf(t, acli, ctx, past.Alternative), "turn 4 keeps every turn, as the head does")

	interior, err := acli.Fork(ctx, parent, 3, 0, nil, nil)
	require.NoError(t, err)
	require.Equal(t, all[:2], promptsOf(t, acli, ctx, interior.Alternative), "turn 3 still replaces turn 3")

	_, err = acli.Fork(ctx, parent, 5, 0, nil, nil)
	require.ErrorContains(t, err, "has no turn 5")
}
