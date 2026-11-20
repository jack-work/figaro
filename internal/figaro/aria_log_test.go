package figaro_test

import (
	"context"
	"encoding/json"
	"fmt"
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
