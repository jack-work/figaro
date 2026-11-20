package figaro_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/aria"
	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/uiir"
)

func newLogAgent(t *testing.T) *figaro.Agent {
	t.Helper()
	birth := form.Build(form.Snapshot{}, map[string]json.RawMessage{form.KindKey: json.RawMessage(`"log"`)}, nil)
	be, id := store.NewTestAria(t, "d", birth)
	cb, _ := form.Open("")
	cb.Apply(birth)
	a := figaro.NewAgent(figaro.Config{Projector: uiir.New(nil), ID: id, Backend: be, Form: cb})
	t.Cleanup(a.Kill)
	return a
}

func entriesOf(a *figaro.Agent) []message.Message {
	var out []message.Message
	for _, m := range a.Context() {
		if len(m.Content) > 0 {
			out = append(out, m)
		}
	}
	return out
}

func waitEntries(t *testing.T, a *figaro.Agent, n int) []message.Message {
	t.Helper()
	require.Eventually(t, func() bool { return len(entriesOf(a)) >= n }, 10*time.Second, 5*time.Millisecond)
	return entriesOf(a)
}

func TestAriaLogSendAppendsOneEntryAndNoModelTurn(t *testing.T) {
	a := newLogAgent(t)
	ch, unsub := subscribeChan(a)
	defer unsub()

	before := time.Now().UnixMilli()
	require.NoError(t, a.SubmitPromptFrom(context.Background(), rpc.QuaRequest{Text: "first entry"}, "rhapsody"))
	var reason string
	timeout := time.After(5 * time.Second)
	for reason == "" {
		select {
		case n := <-ch:
			if n.Method == rpc.MethodTurnDone {
				reason = n.Params.(rpc.DoneEntry).Reason
			}
		case <-timeout:
			t.Fatal("no turn.done for a log append")
		}
	}
	require.Equal(t, figaro.ReasonLogged, reason)
	require.NoError(t, a.SubmitPromptFrom(context.Background(), rpc.QuaRequest{Text: "second entry"}, "macaroni"))

	entries := waitEntries(t, a, 2)
	require.Len(t, entries, 2)
	for _, m := range a.Context() {
		require.NotEqual(t, message.RoleOutput, m.Role, "a log holds no model output")
	}
	require.Equal(t, "first entry", entries[0].Content[0].Text)
	require.Equal(t, "rhapsody", entries[0].Content[0].Sender)
	require.Equal(t, "second entry", entries[1].Content[0].Text)
	require.Equal(t, "macaroni", entries[1].Content[0].Sender)
	require.GreaterOrEqual(t, entries[0].Timestamp, before)
	require.LessOrEqual(t, entries[0].Timestamp, entries[1].Timestamp)
	require.Less(t, entries[0].TurnID, entries[1].TurnID)

	raw, err := a.Handle(context.Background(), rpc.MethodRead, json.RawMessage(`{}`))
	require.NoError(t, err)
	page := raw.(aria.Page)
	require.Len(t, page.Parts, 2)
	require.Equal(t, "first entry", page.Parts[0].Inquiry)
	require.Equal(t, "second entry", page.Parts[1].Inquiry)
	for _, p := range page.Parts {
		require.Empty(t, p.Nodes, "a log turn has no agent nodes")
	}
}

func TestAriaLogConcurrentSendsEachLandAsTheirOwnEntryInSenderOrder(t *testing.T) {
	a := newLogAgent(t)
	const senders, each = 8, 25
	var wg sync.WaitGroup
	for s := 0; s < senders; s++ {
		wg.Add(1)
		go func(s int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				err := a.SubmitPromptFrom(context.Background(), rpc.QuaRequest{Text: fmt.Sprintf("%d/%d", s, i)}, fmt.Sprintf("s%d", s))
				if err != nil {
					t.Error(err)
					return
				}
			}
		}(s)
	}
	wg.Wait()

	entries := waitEntries(t, a, senders*each)
	require.Len(t, entries, senders*each, "no send was folded into another")
	next := make([]int, senders)
	var lastTurn uint64
	for _, m := range entries {
		require.Len(t, m.Content, 1)
		var s, i int
		_, err := fmt.Sscanf(m.Content[0].Text, "%d/%d", &s, &i)
		require.NoError(t, err)
		require.Equal(t, fmt.Sprintf("s%d", s), m.Content[0].Sender)
		require.Equal(t, next[s], i, "sender %d's entries keep their order", s)
		next[s]++
		require.Greater(t, m.TurnID, lastTurn, "one entry is one turn")
		lastTurn = m.TurnID
	}
}

func TestAriaLogRedeliveryDropsSendsAlreadyAppended(t *testing.T) {
	birth := form.Build(form.Snapshot{}, map[string]json.RawMessage{form.KindKey: json.RawMessage(`"log"`)}, nil)
	be, id := store.NewTestAria(t, "d", birth)
	dir := filepath.Join(t.TempDir(), "spool")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	open := func() *figaro.Agent {
		cb, _ := form.Open("")
		cb.Apply(birth)
		a := figaro.NewAgent(figaro.Config{Projector: uiir.New(nil), ID: id, Backend: be, Form: cb, Spool: figaro.NewSpool(dir)})
		a.Redeliver()
		return a
	}
	a := open()
	for _, text := range []string{"a", "b", "c"} {
		require.NoError(t, a.SubmitPromptFrom(context.Background(), rpc.QuaRequest{Text: text}, "s"))
	}
	landed := waitEntries(t, a, 3)
	a.Kill()

	type queued struct {
		Text string   `json:"text"`
		At   int64    `json:"at"`
		Keys []string `json:"keys"`
	}
	var lagging []queued
	for _, m := range landed {
		require.Len(t, m.Accepted, 1, "every entry carries the key it was accepted under")
		lagging = append(lagging, queued{Text: m.Content[0].Text, At: m.Timestamp - 1, Keys: m.Accepted})
	}
	lagging = append(lagging, queued{Text: "d", At: time.Now().UnixMilli(), Keys: []string{"never-appended"}})
	raw, err := json.Marshal(map[string]any{"queued": lagging})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, id+".json"), raw, 0o600))

	b := open()
	defer b.Kill()
	entries := waitEntries(t, b, 4)
	time.Sleep(100 * time.Millisecond)
	entries = entriesOf(b)
	var texts []string
	for _, m := range entries {
		texts = append(texts, m.Content[0].Text)
	}
	require.Equal(t, []string{"a", "b", "c", "d"}, texts, "a send the spool still held after it was appended is not appended twice")
}

func TestAriaLogSendReturnsOnceTheEntryIsDurable(t *testing.T) {
	a := newLogAgent(t)
	for i := 0; i < 20; i++ {
		require.NoError(t, a.SubmitPromptFrom(context.Background(), rpc.QuaRequest{Text: fmt.Sprintf("e%d", i)}, "s"))
		require.Len(t, entriesOf(a), i+1, "the ack follows the append")
	}
}

func TestAriaLogKeepsAnEntryByteForByte(t *testing.T) {
	a := newLogAgent(t)
	texts := []string{"trailing newlines\n\n", "  spaced  \n", "a ref @nosuchkey! stays", "crlf\r\nand\ttab\n", strings.Repeat("big ", 300000)}
	for _, text := range texts {
		require.NoError(t, a.SubmitPromptFrom(context.Background(), rpc.QuaRequest{Text: text, Exact: true}, "s"))
	}
	entries := waitEntries(t, a, len(texts))
	for i, text := range texts {
		require.Equal(t, text, entries[i].Content[0].Text, "entry %d is the bytes that were sent", i)
	}
}

func TestAnExactPromptToAFigaroLosesItsTrailingNewlines(t *testing.T) {
	a := newTestAgent(t, "ok")
	defer a.Kill()
	require.NoError(t, a.SubmitPromptFrom(context.Background(), rpc.QuaRequest{Text: "question\n\n", Exact: true}, ""))
	require.Eventually(t, func() bool {
		for _, m := range a.Context() {
			if m.Role == message.RoleInput && len(m.Content) > 0 {
				return m.Content[0].Text == "question"
			}
		}
		return false
	}, 5*time.Second, 5*time.Millisecond)
}

func TestASendToALogReturnsItsEntrysTurnAndLT(t *testing.T) {
	a := newLogAgent(t)
	var prev figaro.LogAppend
	for i := 0; i < 3; i++ {
		raw, err := a.Handle(context.Background(), rpc.MethodQua, json.RawMessage(fmt.Sprintf(`{"text":"e%d"}`, i)))
		require.NoError(t, err)
		resp := raw.(rpc.QuaResponse)
		require.Greater(t, resp.Turn, prev.Turn)
		require.Greater(t, resp.LT, prev.LT)
		prev = figaro.LogAppend{Turn: resp.Turn, LT: resp.LT}
	}
	last := entriesOf(a)[2]
	require.Equal(t, prev.Turn, last.TurnID)
	var lt uint64
	for _, e := range a.Context() {
		if e.TurnID == last.TurnID && len(e.Content) > 0 {
			lt = e.LogicalTime
		}
	}
	require.Equal(t, prev.LT, lt, "the LT a send answers is the entry's")
}

func TestALogEntryIsNeverRewrittenAsAPrompt(t *testing.T) {
	a := newLogAgent(t)
	require.NoError(t, a.SubmitPromptFrom(context.Background(), rpc.QuaRequest{Text: "seed", Form: &rpc.FormInput{Patch: ptrPatch(form.Build(form.Snapshot{}, map[string]json.RawMessage{"mantra": json.RawMessage(`"the ledger"`)}, nil))}}, "s"))
	texts := []string{"the title is @mantra! here", "an unknown @nosuchkey! ref", "<1>! a leading quote"}
	for _, text := range texts {
		require.NoError(t, a.SubmitPromptFrom(context.Background(), rpc.QuaRequest{Text: text}, "s"), "a log refuses no entry for what a prompt rewrite would refuse")
	}
	entries := waitEntries(t, a, len(texts)+1)
	for i, text := range texts {
		require.Equal(t, text, entries[i+1].Content[0].Text, "entry %d lands byte-identical", i)
	}
}

func TestAnEmptyEntryLandsOnALogOnlyWhenSentExact(t *testing.T) {
	a := newLogAgent(t)
	raw, err := a.Handle(context.Background(), rpc.MethodQua, json.RawMessage(`{"text":"","exact":true}`))
	require.NoError(t, err)
	resp := raw.(rpc.QuaResponse)
	require.NotZero(t, resp.Turn, "an empty entry is a turn of its own")
	require.NoError(t, a.SubmitPromptFrom(context.Background(), rpc.QuaRequest{Text: "after", Exact: true}, "s"))
	var turns []uint64
	for _, m := range a.Context() {
		if message.IsEntry(m) {
			turns = append(turns, m.TurnID)
			if m.TurnID == resp.Turn {
				require.Equal(t, "", m.Content[0].Text)
			}
		}
	}
	require.Equal(t, []uint64{resp.Turn, resp.Turn + 1}, turns)

	b := newTestAgent(t, "ok")
	defer b.Kill()
	require.Error(t, b.SubmitPromptFrom(context.Background(), rpc.QuaRequest{Text: "\n\n", Exact: true}, ""), "a figaro still refuses an empty stdin")
}
