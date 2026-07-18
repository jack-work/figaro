package anthropicsdk

import (
	"encoding/json"
	"testing"
	"text/template"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
)

func TestCatchUpPreservesPrefixBytes(t *testing.T) {
	log := store.NewMemLog[message.Message]()
	cache := store.NewMemLog[[]json.RawMessage]()
	p := &Provider{reminder: "tag"}
	for _, role := range []message.Role{message.RoleUser, message.RoleAssistant} {
		_, err := log.Append(store.Entry[message.Message]{Payload: message.Message{
			Role: role, Content: []message.Content{message.TextContent(string(role))},
		}})
		require.NoError(t, err)
	}

	first, err := p.catchUp(log, cache, nil)
	require.NoError(t, err)
	require.Len(t, first.Messages, 2)
	firstBlock := first.Messages[0].Content[0].OfText
	prefixEntry, ok := cache.Lookup(1)
	require.True(t, ok)
	prefix := append([]byte(nil), prefixEntry.Payload[0]...)
	_, err = log.Append(store.Entry[message.Message]{Payload: message.Message{
		Role: message.RoleUser, Content: []message.Content{message.TextContent("next")},
	}})
	require.NoError(t, err)
	second, err := p.catchUp(log, cache, nil)
	require.NoError(t, err)

	require.Len(t, second.Messages, 3)
	assert.Same(t, firstBlock, second.Messages[0].Content[0].OfText)
	prefixEntry, ok = cache.Lookup(1)
	require.True(t, ok)
	assert.Equal(t, prefix, []byte(prefixEntry.Payload[0]))
}

func TestCatchUpReplaysCachedPrefixSnapshot(t *testing.T) {
	tmpl := template.Must(template.New("chalkboard").New("mode").Parse(`{{.OldString}}=>{{.NewString}}`))
	log := store.NewMemLog[message.Message]()
	cache := store.NewMemLog[[]json.RawMessage]()
	p := &Provider{reminder: "tag", Templates: tmpl}
	oldPatch := message.Patch{Set: map[string]json.RawMessage{"mode": json.RawMessage(`"old"`)}}
	newPatch := message.Patch{Set: map[string]json.RawMessage{"mode": json.RawMessage(`"new"`)}}

	first, err := log.Append(store.Entry[message.Message]{Payload: message.Message{
		Role: message.RoleUser, Content: []message.Content{message.TextContent("first")}, Patches: []message.Patch{oldPatch},
	}})
	require.NoError(t, err)
	_, err = log.Append(store.Entry[message.Message]{Payload: message.Message{
		Role: message.RoleUser, Content: []message.Content{message.TextContent("second")}, Patches: []message.Patch{newPatch},
	}})
	require.NoError(t, err)
	encodedFirst, err := p.encode(first.Payload, chalkboard.Snapshot{})
	require.NoError(t, err)
	_, err = cache.Append(store.Entry[[]json.RawMessage]{
		FigaroLT: first.LT, Payload: encodedFirst, Fingerprint: p.Fingerprint(),
	})
	require.NoError(t, err)

	projected, err := p.catchUp(log, cache, nil)
	require.NoError(t, err)
	require.Len(t, projected.Messages, 2)
	require.Len(t, projected.Messages[1].Content, 2)
	require.NotNil(t, projected.Messages[1].Content[1].OfText)
	assert.Contains(t, projected.Messages[1].Content[1].OfText.Text, "old=>new")
}

func TestInvalidateIfStaleUsesTailFingerprint(t *testing.T) {
	cache := store.NewMemLog[[]json.RawMessage]()
	p := &Provider{reminder: "tag"}
	_, err := cache.Append(store.Entry[[]json.RawMessage]{
		FigaroLT: 1, Payload: []json.RawMessage{json.RawMessage(`{}`)}, Fingerprint: "stale",
	})
	require.NoError(t, err)

	p.invalidateIfStale(cache)
	assert.Empty(t, cache.Read())
}
