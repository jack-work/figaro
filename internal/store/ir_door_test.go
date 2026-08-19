package store

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/message"
)

func doorAria(t *testing.T) (Log[message.Message], *XwalBackend, string) {
	t.Helper()
	be, err := NewXwalBackend(t.TempDir(), 0)
	require.NoError(t, err)
	t.Cleanup(func() { be.Close() })
	outfit, err := be.CreateOutfit("door", message.Patch{})
	require.NoError(t, err)
	id, err := be.CreateConversation(outfit)
	require.NoError(t, err)
	log, err := be.Open(id)
	require.NoError(t, err)
	return log, be, id
}

func invoke(id, name string) message.Message {
	return message.Message{Role: message.RoleOutput, Content: []message.Content{
		{Type: message.ContentToolInvoke, ToolCallID: id, ToolName: name},
	}}
}

func result(id, name, text string) message.Message {
	return message.Message{Role: message.RoleInput, Content: []message.Content{
		message.ToolResultContent(id, name, text, false),
	}}
}

func prose(text string) message.Message {
	return message.Message{Role: message.RoleInput, Content: []message.Content{message.TextContent(text)}}
}

// openInvokes reports invoke ids with no later result: the property every
// provider needs and the one the door exists to keep.
func openInvokes(rows []Entry[message.Message]) []string {
	answered := map[string]bool{}
	var order []string
	seen := map[string]bool{}
	for _, e := range rows {
		for _, c := range e.Payload.Content {
			switch c.Type {
			case message.ContentToolInvoke:
				if !seen[c.ToolCallID] {
					order = append(order, c.ToolCallID)
					seen[c.ToolCallID] = true
				}
			case message.ContentToolResult:
				answered[c.ToolCallID] = true
			}
		}
	}
	var open []string
	for _, id := range order {
		if !answered[id] {
			open = append(open, id)
		}
	}
	return open
}

// A message arriving while a call is open closes the call first, so the
// history never carries an invoke without its result.
func TestDoorClosesAnOpenCallWhenAMessageArrives(t *testing.T) {
	log, _, _ := doorAria(t)
	_, err := log.Append(Entry[message.Message]{Payload: invoke("t1", "bash")})
	require.NoError(t, err)
	_, err = log.Append(Entry[message.Message]{Payload: prose("never mind, do something else")})
	require.NoError(t, err)

	rows := log.Read()
	require.Empty(t, openInvokes(rows), "an invoke was left open")
	var closed bool
	for _, e := range rows {
		for _, c := range e.Payload.Content {
			if c.Type == message.ContentToolResult && c.ToolCallID == "t1" {
				require.True(t, c.IsError, "the closing result must be an error")
				closed = true
			}
		}
	}
	require.True(t, closed, "no closing result was written")
}

// A partial result set is COMPLETED IN THE SAME MESSAGE: a provider wants the
// results immediately after the call, so a second record would be too late.
func TestDoorCompletesAPartialResultSetInPlace(t *testing.T) {
	log, _, _ := doorAria(t)
	two := message.Message{Role: message.RoleOutput, Content: []message.Content{
		{Type: message.ContentToolInvoke, ToolCallID: "a", ToolName: "read"},
		{Type: message.ContentToolInvoke, ToolCallID: "b", ToolName: "write"},
	}}
	_, err := log.Append(Entry[message.Message]{Payload: two})
	require.NoError(t, err)
	_, err = log.Append(Entry[message.Message]{Payload: result("a", "read", "ok")})
	require.NoError(t, err)

	rows := log.Read()
	require.Empty(t, openInvokes(rows))
	last := rows[len(rows)-1].Payload
	ids := map[string]bool{}
	for _, c := range last.Content {
		if c.Type == message.ContentToolResult {
			ids[c.ToolCallID] = true
		}
	}
	require.True(t, ids["a"] && ids["b"], "both results must ride in ONE message, got %v", ids)
}

// A result for a call that is no longer open must not fail the write and must
// not reach the wire unmatched: it becomes a system note the model can read.
func TestDoorTurnsALateResultIntoASystemNote(t *testing.T) {
	log, _, _ := doorAria(t)
	_, err := log.Append(Entry[message.Message]{Payload: invoke("t1", "bash")})
	require.NoError(t, err)
	_, err = log.Append(Entry[message.Message]{Payload: prose("stop")})
	require.NoError(t, err)

	// The tool finally answers, long after the door closed it.
	_, err = log.Append(Entry[message.Message]{Payload: result("t1", "bash", "the output nobody waited for")})
	require.NoError(t, err, "a late result must not fail the write")

	rows := log.Read()
	var notes int
	for _, e := range rows {
		if e.Payload.Role == message.RoleSystem {
			for _, c := range e.Payload.Content {
				if c.Type == message.ContentProse {
					require.Contains(t, c.Text, "the output nobody waited for", "the note must carry the output")
					notes++
				}
			}
		}
	}
	require.Equal(t, 1, notes, "want exactly one system note for the late result")

	// And no second result block for t1 reached the history.
	var t1results int
	for _, e := range rows {
		for _, c := range e.Payload.Content {
			if c.Type == message.ContentToolResult && c.ToolCallID == "t1" {
				t1results++
			}
		}
	}
	require.Equal(t, 1, t1results, "the late block must not be written as a tool result")
}

// The ordinary path is untouched: a result that answers an open call is written
// as it stands, with nothing added.
func TestDoorLeavesAWellFormedExchangeAlone(t *testing.T) {
	log, _, _ := doorAria(t)
	base := len(log.Read()) // genesis and the birth record
	_, err := log.Append(Entry[message.Message]{Payload: invoke("t1", "bash")})
	require.NoError(t, err)
	_, err = log.Append(Entry[message.Message]{Payload: result("t1", "bash", "ok")})
	require.NoError(t, err)

	rows := log.Read()
	require.Len(t, rows, base+2, "the door wrote a record of its own into a well-formed exchange")
	last := rows[len(rows)-1].Payload
	require.Len(t, last.Content, 1)
	require.False(t, last.Content[0].IsError)
}

// A FORK TAKEN MID-TOOL-CALL heals on its own next write. The child inherits an
// invoke with no result; the door closes it before the child's first message,
// which is the case that reaches a provider as a hard error otherwise.
func TestDoorHealsAForkTakenMidToolCall(t *testing.T) {
	log, be, id := doorAria(t)
	_, err := log.Append(Entry[message.Message]{Payload: prose("hello")})
	require.NoError(t, err)
	_, err = log.Append(Entry[message.Message]{Payload: invoke("t1", "bash")})
	require.NoError(t, err)

	_, alt, err := be.Fork(id)
	require.NoError(t, err)
	childLog, err := be.Open(alt)
	require.NoError(t, err)

	require.NotEmpty(t, openInvokes(childLog.Read()), "fixture: the child must inherit an open call")

	_, err = childLog.Append(Entry[message.Message]{Payload: prose("carry on")})
	require.NoError(t, err)
	require.Empty(t, openInvokes(childLog.Read()), "the fork's inherited call was never closed")
}

// Ceremonial messages do not close a call: they are structure, not a turn, and
// closing on one would end a live tool call because a fork marker was written.
func TestDoorIgnoresCeremonialMessages(t *testing.T) {
	log, _, _ := doorAria(t)
	_, err := log.Append(Entry[message.Message]{Payload: invoke("t1", "bash")})
	require.NoError(t, err)
	_, err = log.Append(Entry[message.Message]{Payload: message.Message{Role: message.RoleGenesis}})
	require.NoError(t, err)
	require.NotEmpty(t, openInvokes(log.Read()), "a ceremonial record closed a live tool call")

	_, err = log.Append(Entry[message.Message]{Payload: result("t1", "bash", "ok")})
	require.NoError(t, err)
	require.Empty(t, openInvokes(log.Read()))
}
