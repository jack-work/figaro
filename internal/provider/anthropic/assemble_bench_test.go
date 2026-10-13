package anthropic

import (
	"encoding/json"
	"fmt"
	"io"
	"runtime"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

// Read B/op against conversation length: the slice column tracks it, the
// streamed column must not.
func BenchmarkAssembleBody(b *testing.B) {
	for _, n := range []int{100, 1_000, 10_000} {
		rows := benchRowLog(b, n)
		snap := form.FromMap(map[string]json.RawMessage{
			"system.credo": json.RawMessage(`"you are figaro"`),
		})
		a := &Anthropic{Model: "claude-opus-5", ReminderRenderer: "tag"}

		b.Run("slice/"+strconv.Itoa(n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				perMessage, lts := provider.CollectRows(provider.TranslationRows(rows, 0))
				req, err := a.projectMessagesWithLTs(oneRowEach(perMessage), lts, snap, nil, 4096, false, "claude-opus-5")
				if err != nil {
					b.Fatal(err)
				}
				if err := bodyFunc(req)(io.Discard); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run("streamed/"+strconv.Itoa(n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				req, seq, err := a.projectRequest(provider.TranslationRows(rows, 0), snap, nil, 4096, false, "claude-opus-5")
				if err != nil {
					b.Fatal(err)
				}
				if err := bodyFuncSeq(req, seq)(io.Discard); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// Samples the live heap mid-write: the streamed assembler must hold a row,
// not the conversation.
func TestStreamedAssemblyDoesNotRetainTheConversation(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates a conversation")
	}
	const n = 3_000
	rows := benchRowLog(t, n)
	snap := form.FromMap(map[string]json.RawMessage{
		"system.credo": json.RawMessage(`"you are figaro"`),
	})
	a := &Anthropic{Model: "claude-opus-5", ReminderRenderer: "tag"}

	sliceLive := func() uint64 {
		perMessage, lts := provider.CollectRows(provider.TranslationRows(rows, 0))
		req, err := a.projectMessagesWithLTs(oneRowEach(perMessage), lts, snap, nil, 4096, false, "claude-opus-5")
		require.NoError(t, err)
		w := &liveAtEnd{}
		require.NoError(t, bodyFunc(req)(w))
		runtime.KeepAlive(req)
		return w.live
	}()

	streamedLive := func() uint64 {
		req, seq, err := a.projectRequest(provider.TranslationRows(rows, 0), snap, nil, 4096, false, "claude-opus-5")
		require.NoError(t, err)
		w := &liveAtEnd{}
		require.NoError(t, bodyFuncSeq(req, seq)(w))
		runtime.KeepAlive(req)
		return w.live
	}()

	// No baseline: subtracting a pre-run floor is flaky once the fixture's
	// garbage is collected. The two runs differ by the conversation alone.
	body := bodyBytes(t, a, rows, snap)
	t.Logf("live heap while writing: slice=%d KiB streamed=%d KiB (body=%d KiB)",
		sliceLive>>10, streamedLive>>10, body>>10)
	require.Greater(t, sliceLive, streamedLive,
		"the slice assembler must hold more than the streamed one")
	require.GreaterOrEqual(t, sliceLive-streamedLive, body/2,
		"the gap between them should be most of a body: the conversation the slice path holds")
}

// liveAtEnd samples the live heap on the body's last write.
type liveAtEnd struct {
	n    int
	live uint64
}

func (w *liveAtEnd) Write(p []byte) (int, error) {
	w.n += len(p)
	if w.n > 0 {
		var ms runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&ms)
		w.live = ms.HeapAlloc
	}
	return len(p), nil
}

func bodyBytes(t *testing.T, a *Anthropic, rows store.Log[[]json.RawMessage], snap form.Snapshot) uint64 {
	t.Helper()
	req, seq, err := a.projectRequest(provider.TranslationRows(rows, 0), snap, nil, 4096, false, "claude-opus-5")
	require.NoError(t, err)
	c := &counter{}
	require.NoError(t, bodyFuncSeq(req, seq)(c))
	return uint64(c.n)
}

type counter struct{ n int }

func (c *counter) Write(p []byte) (int, error) { c.n += len(p); return len(p), nil }

func oneRowEach(rows []json.RawMessage) [][]json.RawMessage {
	out := make([][]json.RawMessage, len(rows))
	for i, r := range rows {
		out[i] = []json.RawMessage{r}
	}
	return out
}

func setPatch(kv map[string]string) message.Patch {
	set := make(map[string]json.RawMessage, len(kv))
	for k, v := range kv {
		raw, _ := json.Marshal(v)
		set[k] = raw
	}
	return form.Build(form.Snapshot{}, set, nil)
}

// benchRowLog reopens the backend before returning the log: an append seeds
// the tree cache, and a resident log decodes nothing.
func benchRowLog(tb testing.TB, n int) store.Log[[]json.RawMessage] {
	tb.Helper()
	root := tb.TempDir()
	be, err := store.NewXwalBackend(root, 0)
	require.NoError(tb, err)
	outfit, err := be.CreateOutfit("l", setPatch(map[string]string{"system.model": "m"}))
	require.NoError(tb, err)
	aria, _, err := be.ForkWith(outfit, 0, setPatch(map[string]string{"system.cwd": "/tmp"}))
	require.NoError(tb, err)
	rows, err := be.OpenTranslator(aria, "anthropic")
	require.NoError(tb, err)

	for i := 0; i < n; i++ {
		var row json.RawMessage
		switch i % 3 {
		case 0:
			row = json.RawMessage(`{"role":"user","content":[{"type":"text","text":"` + padding(i) + `"}]}`)
		case 1:
			row = json.RawMessage(`{"role":"assistant","content":[{"type":"text","text":"thinking about it"},` +
				`{"type":"tool_use","id":"call_` + strconv.Itoa(i) + `","name":"read","input":{"path":"/tmp/x"}}]}`)
		default:
			row = json.RawMessage(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_` +
				strconv.Itoa(i-1) + `","content":"` + padding(i) + `"}]}`)
		}
		_, err := rows.Append(store.Entry[[]json.RawMessage]{
			FigaroLT: uint64(i + 1),
			Payload:  []json.RawMessage{row},
		})
		require.NoError(tb, err)
	}
	require.NoError(tb, be.Close())

	cold, err := store.NewXwalBackend(root, 0)
	require.NoError(tb, err)
	tb.Cleanup(func() { cold.Close() })
	coldRows, err := cold.OpenTranslator(aria, "anthropic")
	require.NoError(tb, err)
	return coldRows
}

func padding(i int) string {
	s := make([]byte, 0, 512)
	for len(s) < 512 {
		s = append(s, fmt.Sprintf("message %d payload ", i)...)
	}
	return string(s[:512])
}
