package anthropic

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/handle"
	"github.com/jack-work/figaro/api/message"
)

const forkID = "38f4563ac90d4206364a64c0a4f4c4fa"

func renderedInput(t *testing.T, c message.Content) []byte {
	t.Helper()
	a := &Anthropic{}
	snap := form.Snapshot{}
	nm, ok := a.renderMessage(message.Message{Role: message.RoleInput, Content: []message.Content{c}}, &snap)
	require.True(t, ok)
	raw, err := json.Marshal(nm)
	require.NoError(t, err)
	return raw
}

// A fork's `figaro send` reached its parent as a plain user prompt: the
// sender was recorded and never rendered, so the model answered it as the
// user's. A prompt from another figaro carries a sender reminder (handle and
// short id) in the same user message, ahead of its text, through the same
// <system-reminder> channel form state uses; the text itself is sent as
// typed, the bytes are the same on every render so the cached prefix holds,
// and the owner's prompts are sent as they always were.
func TestAnotherFigarosPromptCarriesASenderReminder(t *testing.T) {
	first := renderedInput(t, message.SenderText(forkID, "review my branch"))
	require.Equal(t, first, renderedInput(t, message.SenderText(forkID, "review my branch")), "two renders differ")
	tag := "<system-reminder name=\"sender\">\nfigaro " + handle.Words(forkID, handle.MinWords) + " (" + forkID[:5] + ")\n</system-reminder>"
	quoted, err := json.Marshal(tag)
	require.NoError(t, err)
	wire := string(quoted[1 : len(quoted)-1])
	require.True(t, bytes.Contains(first, []byte(wire)), "no sender reminder %s in %s", tag, first)
	require.Less(t, strings.Index(string(first), wire), strings.Index(string(first), "review my branch"), "the reminder comes after the text")
	require.False(t, bytes.Contains(first, []byte("[from figaro")), "the text still carries a leading line: %s", first)

	plain := renderedInput(t, message.TextContent("review my branch"))
	for _, owner := range []string{"", "Gluck", "user"} {
		require.Equal(t, string(plain), string(renderedInput(t, message.SenderText(owner, "review my branch"))), "owner prompt from %q changed", owner)
	}
}
