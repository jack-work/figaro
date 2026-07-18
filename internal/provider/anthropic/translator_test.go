package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
)

func TestCatchUpPreservesPrefixBytes(t *testing.T) {
	log := store.NewMemLog[message.Message]()
	cache := store.NewMemLog[[]json.RawMessage]()
	a := &Anthropic{ReminderRenderer: "tag"}
	for _, role := range []message.Role{message.RoleUser, message.RoleAssistant} {
		_, err := log.Append(store.Entry[message.Message]{Payload: message.Message{
			Role: role, Content: []message.Content{message.TextContent(string(role))},
		}})
		require.NoError(t, err)
	}

	first, _ := a.catchUp("aria", log, cache, nil)
	prefix := append([]byte(nil), first[0][0]...)
	_, err := log.Append(store.Entry[message.Message]{Payload: message.Message{
		Role: message.RoleUser, Content: []message.Content{message.TextContent("next")},
	}})
	require.NoError(t, err)
	second, _ := a.catchUp("aria", log, cache, nil)

	require.Len(t, second, 3)
	assert.Equal(t, prefix, []byte(second[0][0]))
}

func TestInvalidateIfStaleUsesTailFingerprint(t *testing.T) {
	cache := store.NewMemLog[[]json.RawMessage]()
	a := &Anthropic{ReminderRenderer: "tag"}
	_, err := cache.Append(store.Entry[[]json.RawMessage]{
		FigaroLT: 1, Payload: []json.RawMessage{json.RawMessage(`{}`)}, Fingerprint: "stale",
	})
	require.NoError(t, err)

	a.invalidateIfStale(cache)
	assert.Empty(t, cache.Read())
}
