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
// user's. A prompt from another figaro names it (handle and id) in the text
// the provider sends, the same bytes on every render so the cached prefix
// holds; the owner's prompts are sent as they always were.
func TestAnotherFigarosPromptNamesItsSender(t *testing.T) {
	first := renderedInput(t, message.SenderText(forkID, "review my branch"))
	require.Equal(t, first, renderedInput(t, message.SenderText(forkID, "review my branch")), "two renders differ")
	for _, want := range []string{handle.Words(forkID, handle.MinWords), forkID, "review my branch"} {
		require.True(t, bytes.Contains(first, []byte(want)), "the rendered prompt lacks %q: %s", want, first)
	}
	require.Less(t, strings.Index(string(first), forkID), strings.Index(string(first), "review my branch"), "the sender comes after the text")

	plain := renderedInput(t, message.TextContent("review my branch"))
	for _, owner := range []string{"", "Gluck", "user"} {
		require.Equal(t, string(plain), string(renderedInput(t, message.SenderText(owner, "review my branch"))), "owner prompt from %q changed", owner)
	}
}
