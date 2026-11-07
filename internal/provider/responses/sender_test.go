package responses

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

func encodedInput(t *testing.T, c message.Content) []byte {
	t.Helper()
	items, err := encodeResponseMessage(message.Message{Role: message.RoleInput, Content: []message.Content{c}}, nil, form.Snapshot{}, nil)
	require.NoError(t, err)
	raw, err := json.Marshal(items)
	require.NoError(t, err)
	return raw
}

// A fork's `figaro send` reached its parent as a plain user prompt (see the
// anthropic twin). The Responses encoder names the sender the same way.
func TestAnotherFigarosPromptNamesItsSender(t *testing.T) {
	first := encodedInput(t, message.SenderText(forkID, "review my branch"))
	require.Equal(t, first, encodedInput(t, message.SenderText(forkID, "review my branch")), "two renders differ")
	for _, want := range []string{handle.Words(forkID, handle.MinWords), forkID, "review my branch"} {
		require.True(t, bytes.Contains(first, []byte(want)), "the rendered prompt lacks %q: %s", want, first)
	}
	require.Less(t, strings.Index(string(first), forkID), strings.Index(string(first), "review my branch"))

	plain := encodedInput(t, message.TextContent("review my branch"))
	for _, owner := range []string{"", "Gluck", "user"} {
		require.Equal(t, string(plain), string(encodedInput(t, message.SenderText(owner, "review my branch"))), "owner prompt from %q changed", owner)
	}
}
