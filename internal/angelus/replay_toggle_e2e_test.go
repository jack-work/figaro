package angelus_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/sdk"
)

func sinceLastAnswer(t *testing.T, body string) string {
	t.Helper()
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &req))
	var out []string
	for _, m := range req.Messages {
		if m.Role == "assistant" {
			out = nil
			continue
		}
		b, _ := json.Marshal(m.Content)
		out = append(out, string(b))
	}
	return strings.Join(out, "\n")
}

func replayPatch(t *testing.T, of string, turn uint64) rpc.FormPatch {
	t.Helper()
	return rpc.FormPatch(form.Replay{Of: of, Turn: turn}.Patch(form.Snapshot{}))
}

func unsetReplay(t *testing.T, c *sdk.Aria, board form.Snapshot) {
	t.Helper()
	_, err := c.Set(t.Context(), rpc.FormPatch(form.Build(board, nil, []string{form.ReplayKey})), 0)
	require.NoError(t, err)
}

func replayMarks(t *testing.T, acli *sdk.Angelus, id string) []*message.Replay {
	t.Helper()
	var out []*message.Replay
	for _, m := range inquiriesOf(t, acli, t.Context(), id) {
		out = append(out, m.Replay)
	}
	return out
}

// Defends #114: system.replay is read at every turn. The owner switches a
// figaro into replay, out of it and back in; each turn runs under the rules
// it found, and leaving replay shows the model its own id and its fork.
func TestReplaySwitchesOnOffAndOnAcrossTurns(t *testing.T) {
	tap := &wireTap{}
	a, acli := gatewayDaemon(t, tap)
	parent := gatewayAria(t, a, acli)

	resp, err := acli.Replay(t.Context(), parent, 2, nil, nil)
	require.NoError(t, err)
	branch := resp.Alternative
	on := sinceLastAnswer(t, awaitWire(t, a, tap, branch, 1))
	require.Contains(t, on, "second question")
	require.Contains(t, on, "2PM EST")
	require.NotContains(t, on, branch)
	require.NotContains(t, on, replayForkNotice)

	c, err := sdk.DialAria(ariaAddr(a, branch), nil)
	require.NoError(t, err)
	defer c.Close()
	_, _, err = c.Qua(t.Context(), "are you there?", nil)
	require.ErrorContains(t, err, "takes no messages")

	unsetReplay(t, c, boardOf(t, acli, t.Context(), branch))
	askAt(t, a, branch, "fourth question", "Tuesday, November 17, 2026, 10AM EST")
	off := sinceLastAnswer(t, awaitWire(t, a, tap, branch, 2))
	require.Contains(t, off, "fourth question")
	require.Contains(t, off, "Tuesday, November 17")
	require.Contains(t, off, branch, "out of replay the model reads its own id")
	require.Contains(t, off, replayForkNotice, "out of replay the model learns it is a branch")

	_, err = c.Set(t.Context(), replayPatch(t, parent, 2), 0)
	require.NoError(t, err)
	again := sinceLastAnswer(t, awaitWire(t, a, tap, branch, 3))
	require.Contains(t, again, "second question")
	require.Contains(t, again, "2PM EST")
	require.Contains(t, again, parent)
	require.NotContains(t, again, branch)
	require.NotContains(t, again, "Tuesday")

	marks := replayMarks(t, acli, branch)
	require.Len(t, marks, 4, "turn 1 shared, then replay, the fourth question, replay")
	require.Nil(t, marks[0])
	require.NotNil(t, marks[1])
	require.Nil(t, marks[2])
	require.NotNil(t, marks[3])
	require.Equal(t, parent, marks[3].Of)
	require.Equal(t, uint64(2), marks[3].Turn)
}

func TestASetOfAnUnrecordedTurnIsRefused(t *testing.T) {
	a, acli, ctx, parent := replayFixture(t)
	c, err := sdk.DialAria(ariaAddr(a, parent), nil)
	require.NoError(t, err)
	defer c.Close()
	_, err = c.Set(ctx, replayPatch(t, parent, 9), 0)
	require.ErrorContains(t, err, "no turn 9")
	_, ok := form.ReadReplay(boardOf(t, acli, ctx, parent))
	require.False(t, ok)
}
