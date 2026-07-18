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
	p := &Provider{reminder: "tag", snapCache: map[string]*snapCacheEntry{}}
	for _, role := range []message.Role{message.RoleUser, message.RoleAssistant} {
		_, err := log.Append(store.Entry[message.Message]{Payload: message.Message{
			Role: role, Content: []message.Content{message.TextContent(string(role))},
		}})
		require.NoError(t, err)
	}

	first, _ := p.catchUp("aria", log, cache, nil)
	prefix := append([]byte(nil), first[0][0]...)
	_, err := log.Append(store.Entry[message.Message]{Payload: message.Message{
		Role: message.RoleUser, Content: []message.Content{message.TextContent("next")},
	}})
	require.NoError(t, err)
	second, _ := p.catchUp("aria", log, cache, nil)

	require.Len(t, second, 3)
	assert.Equal(t, prefix, []byte(second[0][0]))
}

func TestCatchUpReplaysCachedPrefixSnapshot(t *testing.T) {
	tmpl := template.Must(template.New("chalkboard").New("mode").Parse(`{{.OldString}}=>{{.NewString}}`))
	log := store.NewMemLog[message.Message]()
	cache := store.NewMemLog[[]json.RawMessage]()
	p := &Provider{reminder: "tag", Templates: tmpl, snapCache: map[string]*snapCacheEntry{}}
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

	perMessage, _ := p.catchUp("aria", log, cache, nil)
	require.Len(t, perMessage, 2)
	var second struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	require.NoError(t, json.Unmarshal(perMessage[1][0], &second))
	require.Len(t, second.Content, 2)
	assert.Contains(t, second.Content[1].Text, "old=>new")
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
