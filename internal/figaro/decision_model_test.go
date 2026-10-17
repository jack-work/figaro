package figaro_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/uiir"
)

// A decision model answers typed questions and cannot hold a conversation, so
// an aria pointed at one would fail every turn. The set is refused at the
// door, with the verb that does work named in the refusal.
func TestSetRefusesADecisionModelAsSystemModel(t *testing.T) {
	cb, _ := form.Open("")
	be, id := store.NewTestAria(t, "decide-refuse", message.Patch{})
	a := figaro.NewAgent(figaro.Config{
		ID: id, Backend: be, Projector: uiir.New(nil), Form: cb,
	})
	defer a.Kill()

	for _, model := range []string{"jev-latest", "explabs/jev-latest", "jev-1.13.0"} {
		_, _, err := a.SetAwaiting(context.Background(), form.Build(form.Snapshot{}, map[string]json.RawMessage{
			"system.model": json.RawMessage(`"` + model + `"`),
		}, nil), 0, false)
		require.Error(t, err, model)
		require.True(t, strings.Contains(err.Error(), "figaro decide"), "the refusal does not name the verb: %v", err)
	}

	// The key that IS for decision models takes one.
	_, applied, err := a.SetAwaiting(context.Background(), form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.decide.model": json.RawMessage(`"jev-latest"`),
	}, nil), 0, false)
	require.NoError(t, err)
	_, ok := applied.Entry("system.decide.model")
	require.True(t, ok)

	// And an ordinary model is untouched by the guard.
	_, _, err = a.SetAwaiting(context.Background(), form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.model": json.RawMessage(`"claude-opus-5"`),
	}, nil), 0, false)
	require.NoError(t, err)
}
