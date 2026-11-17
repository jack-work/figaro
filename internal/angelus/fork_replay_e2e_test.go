package angelus_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/sdk"
)

func askAt(t *testing.T, a *angelus.Angelus, id, text, datetime string) {
	t.Helper()
	done := make(chan struct{}, 1)
	fcli, err := sdk.DialAria(ariaAddr(a, id), func(method string, _ json.RawMessage) {
		if method == "turn.done" {
			select {
			case done <- struct{}{}:
			default:
			}
		}
	})
	require.NoError(t, err)
	defer fcli.Close()
	dt, _ := json.Marshal(datetime)
	_, _, err = fcli.Qua(context.Background(), text, &rpc.FormInput{Context: map[string]json.RawMessage{"datetime": dt}})
	require.NoError(t, err)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("%q never finished", text)
	}
	waitIdle(t, a, id)
}

func inquiriesOf(t *testing.T, acli *sdk.Angelus, ctx context.Context, id string) []message.Message {
	t.Helper()
	resp, err := acli.IRAll(ctx, id, 0)
	require.NoError(t, err)
	var out []message.Message
	for _, e := range resp.Entries {
		var m message.Message
		if json.Unmarshal(e.Payload, &m) == nil && m.Role == message.RoleInput && len(m.Content) > 0 && m.Content[0].Type == message.ContentProse {
			out = append(out, m)
		}
	}
	return out
}

func answeredTurns(t *testing.T, acli *sdk.Angelus, ctx context.Context, id string) int {
	t.Helper()
	resp, err := acli.IRAll(ctx, id, 0)
	require.NoError(t, err)
	n := 0
	for _, e := range resp.Entries {
		var m message.Message
		if json.Unmarshal(e.Payload, &m) == nil && m.Role == message.RoleOutput {
			n++
		}
	}
	return n
}

func boardOf(t *testing.T, acli *sdk.Angelus, ctx context.Context, id string) form.Snapshot {
	t.Helper()
	resp, err := acli.Form(ctx, id)
	require.NoError(t, err)
	return resp.Snapshot
}

func replayFixture(t *testing.T) (*angelus.Angelus, *sdk.Angelus, context.Context, string) {
	t.Helper()
	a, acli, ctx := daemonFixture(t)
	created, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	askAt(t, a, created.FigaroID, "first question", "Thursday, November 12, 2026, 9AM EST")
	askAt(t, a, created.FigaroID, "second question", "Thursday, November 12, 2026, 2PM EST")
	askAt(t, a, created.FigaroID, "third question", "Monday, November 16, 2026, 8PM EST")
	return a, acli, ctx, created.FigaroID
}

func TestReplayResendsTheRecordedInquiryOnABranchCutBeforeIt(t *testing.T) {
	a, acli, ctx, parent := replayFixture(t)
	effort := rpc.FormPatch(form.Build(form.Snapshot{}, map[string]json.RawMessage{"system.thinking_effort": json.RawMessage(`"high"`)}, nil))

	resp, err := acli.Replay(ctx, parent, 2, nil, &effort)
	require.NoError(t, err)
	branch := resp.Alternative
	require.NotEqual(t, parent, branch)

	require.Eventually(t, func() bool { return answeredTurns(t, acli, ctx, branch) == 2 }, 10*time.Second, 20*time.Millisecond)
	waitIdle(t, a, branch)

	original := inquiriesOf(t, acli, ctx, parent)
	replayed := inquiriesOf(t, acli, ctx, branch)
	require.Len(t, replayed, 2, "turn 1 is shared, turn 2 is replayed, turn 3 is cut away")
	require.Equal(t, original[1].Content, replayed[1].Content, "the replayed inquiry carries the recorded words and senders")

	board := boardOf(t, acli, ctx, branch)
	require.Equal(t, "Thursday, November 12, 2026, 2PM EST", *board.Lookup("datetime"), "the inquiry's datetime rides with it")
	require.Equal(t, "high", *board.Lookup("system.thinking_effort"), "the caller's dress is kept")
	rec, ok := form.ReadReplay(board)
	require.True(t, ok)
	require.Equal(t, parent, rec.Of)
	require.Equal(t, uint64(2), rec.Turn)
}

func TestReplayRefusesATurnThatHasNotHappened(t *testing.T) {
	_, acli, ctx, parent := replayFixture(t)
	_, err := acli.Replay(ctx, parent, 4, nil, nil)
	require.ErrorContains(t, err, "has not been asked yet")
	_, err = acli.Replay(ctx, parent, 0, nil, nil)
	require.ErrorContains(t, err, "<id>:<turn>")
}
