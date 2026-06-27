package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/message"
)

// --- MemLog[T] tests (ephemeral) ---

func TestMemLog_Standalone(t *testing.T) {
	s := NewMemLog[message.Message]()

	assert.Empty(t, s.Read())

	entry, err := s.Append(Entry[message.Message]{
		Payload: message.Message{Role: message.RoleUser, Content: []message.Content{message.TextContent("hello")}},
	})
	require.NoError(t, err)
	assert.Equal(t, uint64(1), entry.LT)

	require.Len(t, s.Read(), 1)
	require.NoError(t, s.Clear())
	assert.Empty(t, s.Read())
}
