package store

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/turns"
)

// prompt is a message that OPENS a turn: input role, real text, not steering.
func prompt(text string) message.Message {
	return message.Message{Role: message.RoleInput, Content: []message.Content{message.TextContent(text)}}
}

func reply(text string) message.Message {
	return message.Message{Role: message.RoleOutput, Content: []message.Content{message.TextContent(text)}}
}

// No record reaches the log without a turn id, whichever of the five append
// paths makes it.
func TestDoorStampsEveryAppendWhoeverMakesIt(t *testing.T) {
	log := GuardIR(NewMemLog[message.Message]())

	// A caller that stamps nothing at all: the shape study notes, topology
	// writes and repair records all used.
	for _, m := range []message.Message{
		prompt("first question"),
		reply("first answer"),
		prompt("second question"),
		reply("second answer"),
	} {
		_, err := log.Append(Entry[message.Message]{Payload: m})
		require.NoError(t, err)
	}

	got := log.Read()
	require.Len(t, got, 4)
	for i, e := range got {
		require.NotZero(t, e.Payload.TurnID, "record %d reached the log unstamped", i)
	}
	require.Equal(t, []uint64{1, 1, 2, 2}, []uint64{
		got[0].Payload.TurnID, got[1].Payload.TurnID,
		got[2].Payload.TurnID, got[3].Payload.TurnID,
	}, "a reply belongs to the turn its prompt opened")
}

// The door's derivation must agree with turns.StampIDs, the rule every reader
// applies. If they disagree, a stored log and a derived one describe different
// histories.
func TestDoorDerivationAgreesWithStampIDs(t *testing.T) {
	convo := []message.Message{
		prompt("one"),
		reply("a"),
		{Role: message.RoleOutput, Content: []message.Content{{
			Type: message.ContentToolInvoke, ToolCallID: "call_1", ToolName: "read"}}},
		{Role: message.RoleInput, Content: []message.Content{
			message.ToolResultContent("call_1", "read", "contents", false)}},
		reply("b"),
		prompt("two"),
		reply("c"),
		prompt("three"),
	}

	log := GuardIR(NewMemLog[message.Message]())
	for _, m := range convo {
		_, err := log.Append(Entry[message.Message]{Payload: m})
		require.NoError(t, err)
	}
	stored := log.Read()

	// What the reader would have derived, from the same messages, unstamped.
	fresh := make([]message.Message, len(convo))
	copy(fresh, convo)
	for i := range fresh {
		fresh[i].TurnID = 0
	}
	turns.StampIDs(fresh)

	require.Len(t, stored, len(fresh))
	for i := range fresh {
		require.Equal(t, fresh[i].TurnID, stored[i].Payload.TurnID,
			"record %d: door stored %d, StampIDs derives %d",
			i, stored[i].Payload.TurnID, fresh[i].TurnID)
	}
}

// A caller that knows its turn keeps it.
func TestDoorRespectsACallerSuppliedTurn(t *testing.T) {
	log := GuardIR(NewMemLog[message.Message]())
	_, err := log.Append(Entry[message.Message]{Payload: prompt("one")})
	require.NoError(t, err)

	m := reply("late arrival")
	m.TurnID = 42
	_, err = log.Append(Entry[message.Message]{Payload: m})
	require.NoError(t, err)

	got := log.Read()
	require.Equal(t, uint64(42), got[1].Payload.TurnID)
}

// A record before the first prompt belongs to no turn and says so in its bytes.
func TestPreTurnRecordsCarryAnExplicitZero(t *testing.T) {
	log := GuardIR(NewMemLog[message.Message]())
	_, err := log.Append(Entry[message.Message]{Payload: message.Message{Role: "genesis"}})
	require.NoError(t, err)

	got := log.Read()
	require.Len(t, got, 1)
	require.Zero(t, got[0].Payload.TurnID)

	raw, err := json.Marshal(got[0].Payload)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"turn_id":0`, "the zero must be on the wire")
}
