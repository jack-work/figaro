package figaro

import (
	"encoding/json"
	"fmt"
	"github.com/jack-work/figaro/api/form"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/turns"
)

// One activation: the log is cold, reopened from disk, so the read decodes.
func BenchmarkSeedTurnID(b *testing.B) {
	for _, n := range []int{100, 1_000, 5_000} {
		lg := coldTurnLog(b, n)
		a := &Agent{figLog: lg}

		b.Run("walk/"+strconv.Itoa(n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if got := turns.StampIDs(unwrapMessages(lg.Read())); got == 0 {
					b.Fatal("no turns")
				}
			}
		})
		b.Run("peek/"+strconv.Itoa(n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if got := a.seedTurnID(); got == 0 {
					b.Fatal("no turns")
				}
			}
		})
	}
}

func coldTurnLog(tb testing.TB, n int) store.Log[message.Message] {
	tb.Helper()
	root := tb.TempDir()
	be, err := store.NewXwalBackend(root, 0)
	require.NoError(tb, err)

	outfit, err := be.CreateOutfit("l", turnSetPatch(map[string]string{"system.model": "m"}))
	require.NoError(tb, err)
	aria, _, err := be.ForkWith(outfit, 0, turnSetPatch(map[string]string{"system.cwd": "/tmp"}))
	require.NoError(tb, err)
	lg, err := be.OpenFigIR(aria)
	require.NoError(tb, err)

	body := make([]byte, 0, 512)
	for len(body) < 512 {
		body = append(body, "some conversational payload "...)
	}
	for i := 0; i < n; i++ {
		role := message.RoleInput
		if i%2 == 1 {
			role = message.RoleOutput
		}
		_, err := lg.Append(store.Entry[message.Message]{Payload: message.Message{
			Role:    role,
			Content: []message.Content{message.TextContent(fmt.Sprintf("%d %s", i, body))},
		}})
		require.NoError(tb, err)
	}
	require.NoError(tb, be.Close())

	cold, err := store.NewXwalBackend(root, 0)
	require.NoError(tb, err)
	tb.Cleanup(func() { cold.Close() })
	out, err := cold.OpenFigIR(aria)
	require.NoError(tb, err)
	return out
}

func turnSetPatch(kv map[string]string) message.Patch {
	set := make(map[string]json.RawMessage, len(kv))
	for k, v := range kv {
		raw, _ := json.Marshal(v)
		set[k] = raw
	}
	return form.Build(form.Snapshot{}, set, nil)
}
