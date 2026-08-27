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

// The number this change exists for.
//
// The source is A REAL LOG, because that is where the cost was: the slice
// column pays for the whole conversation decoded out of the store on every
// send, which is what the live daemon was caught doing -- 357MB of a 461MB
// heap in one in-flight request. The streamed column pays for one row.
//
// Read B/op against conversation length. The slice column tracks length.
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
				// What the send path did: the whole log, materialized, then
				// assembled.
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

// THE ASSERTION, NOT THE BENCHMARK. A benchmark reports; this one refuses.
//
// It writes the body of a long conversation and asks the runtime, WHILE THE
// WRITE IS STILL IN FLIGHT, how much is live. The slice assembler is holding
// the conversation at that moment by construction. The streamed one is holding
// a row, and the test fails if it ever starts holding more than a fraction of
// the whole -- which is the regression this change is guarding against.
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

	// The store's own cache holds what this daemon appended, which is neither
	// path's doing and is bounded by the translation budget. It is measured
	// once and subtracted, so what remains is what THE ASSEMBLY retains.
	base := liveHeap()

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

	body := bodyBytes(t, a, rows, snap)
	// SIGNED, AND CLAMPED. The floor is measured before the first write and
	// the heap can fall BELOW it once the fixture's own garbage is collected,
	// which underflowed an unsigned subtraction into 18 exabytes and made the
	// assertion pass for the wrong reason in one direction and fail in the
	// other.
	held := func(live uint64) uint64 {
		if live < base {
			return 0
		}
		return live - base
	}
	sliceHeld := held(sliceLive)
	streamedHeld := held(streamedLive)
	t.Logf("held while writing: slice=%d KiB streamed=%d KiB (body=%d KiB, base=%d KiB)",
		sliceHeld>>10, streamedHeld>>10, body>>10, base>>10)
	require.Less(t, streamedHeld, sliceHeld/2,
		"the streamed assembler must not hold the conversation the slice one held")
	require.Less(t, streamedHeld, body/2,
		"a streamed body must not retain a body's worth of anything")
}

// liveHeap is the live heap after a collection: the floor a measurement is
// taken against.
func liveHeap() uint64 {
	var ms runtime.MemStats
	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&ms)
	return ms.HeapAlloc
}

// liveAtEnd samples the live heap on the last write of the body, which is the
// moment the whole conversation would be reachable if anything were holding
// it.
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

// oneRowEach restates a flat row list as the per-record shape the deleted
// signature took.
func oneRowEach(rows []json.RawMessage) [][]json.RawMessage {
	out := make([][]json.RawMessage, len(rows))
	for i, r := range rows {
		out[i] = []json.RawMessage{r}
	}
	return out
}

// setPatch is the store's own test helper, restated: a form patch that sets
// string keys.
func setPatch(kv map[string]string) message.Patch {
	set := make(map[string]json.RawMessage, len(kv))
	for k, v := range kv {
		raw, _ := json.Marshal(v)
		set[k] = raw
	}
	return message.Patch{Set: set}
}

// benchRowLog is a translator channel in a real store, holding a plausible
// turn shape: a user message, an assistant message with a tool call, and its
// result.
//
// IT REOPENS THE BACKEND BEFORE HANDING THE LOG BACK, and that is the whole
// difference between measuring something and measuring nothing. An append
// SEEDS the tree cache, so a log read in the process that wrote it is served
// resident and never decodes a single record -- which is not the case that
// costs anything. The rows a real send pays for are the ones written before
// the daemon started: cold, and decoded from the frame every time.
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
