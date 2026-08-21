package figaro_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/uiir"
	"github.com/stretchr/testify/require"
	"path/filepath"
)

// A LIVE aria's pages must carry the same form deltas a dormant read
// shows: the transcript telling two stories keyed on liveness is the bug
// this exists to keep dead ('saw it once, then never', 2026-08-13).
func TestLiveAgentPagesCarryFormDeltas(t *testing.T) {
	backend, err := store.NewXwalBackend(t.TempDir(), 0)
	require.NoError(t, err)
	t.Cleanup(func() { backend.Close() })

	src, _, err := backend.CreateForm("", message.Patch{Set: map[string]json.RawMessage{
		"brief": json.RawMessage(`"the studied thing"`)}})
	require.NoError(t, err)
	id, _, err := backend.ForkWith("", 0, message.Patch{Set: map[string]json.RawMessage{
		"aria_id": json.RawMessage(`"a1"`)}})
	require.NoError(t, err)

	studiesDecl, err := backend.StudyForm(id, src)
	studies := studiesDecl.Studies
	require.NoError(t, err)
	backend.SetObservedForms(id, studies)
	lib, err := backend.Libretto(src)
	require.NoError(t, err)

	// Two records with a studied-form change folded between their stamps.
	log, err := backend.OpenFigIR(id)
	require.NoError(t, err)
	_, err = log.Append(store.Entry[message.Message]{Payload: message.Message{
		Role: message.RoleInput, TurnID: 1,
		Content: []message.Content{message.TextContent("one")}}})
	require.NoError(t, err)
	v, err := backend.ApplyForm(src, message.Patch{Set: map[string]json.RawMessage{
		"phase": json.RawMessage(`"ga"`)}})
	require.NoError(t, err)
	deadline := time.Now().Add(5 * time.Second)
	for lib.At() < v {
		if time.Now().After(deadline) {
			t.Fatalf("fold never caught up (at %d want %d)", lib.At(), v)
		}
		time.Sleep(2 * time.Millisecond)
	}
	_, err = log.Append(store.Entry[message.Message]{Payload: message.Message{
		Role: message.RoleOutput, TurnID: 1,
		Content: []message.Content{message.TextContent("ONE.")}}})
	require.NoError(t, err)

	// A LIVE agent over that history, exactly as restore builds one.
	snap, err := backend.FormState(id)
	require.NoError(t, err)
	cb, err := form.Open("")
	require.NoError(t, err)
	cb.Apply(snap.AsPatch())
	a := figaro.NewAgent(figaro.Config{
		Projector:  uiir.New(nil),
		ID:         id,
		SocketPath: filepath.Join(t.TempDir(), "figaro.sock"),
		Provider:   nil,
		Backend:    backend,
		Form:       cb,
	})
	t.Cleanup(a.Kill)

	page := a.Read(aria.Anchor{}, 1<<20)
	found := false
	for _, part := range page.Parts {
		if _, ok := part.FormDeltas[src+".phase"]; ok {
			found = true
		}
		for _, n := range part.Nodes {
			if _, ok := n.FormDeltas[src+".phase"]; ok {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("the live agent's page carries no delta for %s.phase: %+v", src, page.Parts)
	}

	// And through the CLIENT fold, which is what `listen` renders from. The
	// fold rebuilds messages field by field; the turn-level deltas were
	// dropped there once, invisibly, while the wire carried them fine.
	cl := aria.NewClient()
	var folded []aria.Message
	cl.OnClosed = func(m aria.Message) { folded = append(folded, m) }
	cl.Apply(page)
	foldedHasDelta := false
	for _, m := range folded {
		if _, ok := m.FormDeltas[src+".phase"]; ok {
			foldedHasDelta = true
		}
		for _, n := range m.Nodes {
			if _, ok := n.FormDeltas[src+".phase"]; ok {
				foldedHasDelta = true
			}
		}
	}
	if !foldedHasDelta {
		t.Fatalf("the client fold dropped the turn-level delta: %+v", folded)
	}
}
