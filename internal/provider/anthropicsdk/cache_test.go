package anthropicsdk

import (
	"encoding/json"
	"testing"
	"text/template"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
)

func TestCatchUpPreservesPrefixBytes(t *testing.T) {
	log := store.NewMemLog[message.Message]()
	cache := store.NewMemLog[[]json.RawMessage]()
	p := &Provider{reminder: "tag"}
	for _, role := range []message.Role{message.RoleInput, message.RoleOutput} {
		_, err := log.Append(store.Entry[message.Message]{Payload: message.Message{
			Role: role, Content: []message.Content{message.TextContent(string(role))},
		}})
		require.NoError(t, err)
	}

	first, _, err := p.catchUp(log, cache, nil, nil)
	require.NoError(t, err)
	require.Len(t, first, 2)
	prefixEntry, ok := cache.Lookup(1)
	require.True(t, ok)
	prefix := append([]byte(nil), prefixEntry.Payload[0]...)
	_, err = log.Append(store.Entry[message.Message]{Payload: message.Message{
		Role: message.RoleInput, Content: []message.Content{message.TextContent("next")},
	}})
	require.NoError(t, err)
	second, _, err := p.catchUp(log, cache, nil, nil)
	require.NoError(t, err)

	require.Len(t, second, 3)
	// THE STORED BYTES ARE THE PROPERTY, NOT THE ADDRESS. The old assertion
	// here was assert.Same on a parsed block -- an address, which was true
	// only because a memo handed back the same object, and which this
	// campaign's own notes name as one of three claims no instrument could
	// check. What must hold is that the row on disk did not move.
	prefixEntry, ok = cache.Lookup(1)
	require.True(t, ok)
	assert.Equal(t, prefix, []byte(prefixEntry.Payload[0]))
}

func TestCatchUpReplaysCachedPrefixSnapshot(t *testing.T) {
	tmpl := template.Must(template.New("form").New("mode").Parse(`{{.OldString}}=>{{.NewString}}`))
	log := store.NewMemLog[message.Message]()
	cache := store.NewMemLog[[]json.RawMessage]()
	p := &Provider{reminder: "tag", Templates: tmpl}
	oldPatch := message.Patch{Set: map[string]json.RawMessage{"mode": json.RawMessage(`"old"`)}}
	newPatch := message.Patch{Set: map[string]json.RawMessage{"mode": json.RawMessage(`"new"`)}}

	first, err := log.Append(store.Entry[message.Message]{Payload: message.Message{
		Role: message.RoleInput, Content: []message.Content{message.TextContent("first")}, Patches: []message.Patch{oldPatch},
	}})
	require.NoError(t, err)
	_, err = log.Append(store.Entry[message.Message]{Payload: message.Message{
		Role: message.RoleInput, Content: []message.Content{message.TextContent("second")}, Patches: []message.Patch{newPatch},
	}})
	require.NoError(t, err)
	encodedFirst, err := p.encode(first.Payload, form.Snapshot{})
	require.NoError(t, err)
	_, err = cache.Append(store.Entry[[]json.RawMessage]{
		FigaroLT: first.LT, Payload: encodedFirst, Fingerprint: p.Fingerprint(),
	})
	require.NoError(t, err)

	messages, _, err := p.catchUp(log, cache, nil, nil)
	require.NoError(t, err)
	require.Len(t, messages, 2)
	require.Len(t, messages[1].Content, 2)
	require.NotNil(t, messages[1].Content[1].OfText)
	assert.Contains(t, messages[1].Content[1].OfText.Text, "old=>new")
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
