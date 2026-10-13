package angelus

import (
	"context"
	"encoding/json"
	"github.com/jack-work/figaro/api/form"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
)

// An import replays rather than grafts: every identity is minted here, so nothing can collide.
func importFixture(t *testing.T) (*handlers, store.Backend) {
	t.Helper()
	backend, err := store.NewXwalBackend(t.TempDir(), 0)
	require.NoError(t, err)
	t.Cleanup(func() { backend.Close() })
	return &handlers{angelus: &Angelus{Backend: backend}}, backend
}

func importReq(t *testing.T, h *handlers, req rpc.ImportRequest) rpc.ImportResponse {
	t.Helper()
	params, err := json.Marshal(req)
	require.NoError(t, err)
	out, err := h.importAria(context.Background(), params)
	require.NoError(t, err)
	return out.(rpc.ImportResponse)
}

func say(role message.Role, text string) message.Message {
	return message.Message{Role: role, Content: []message.Content{{Type: message.ContentProse, Text: text}}}
}

func TestImportLandsAsAWholeConversation(t *testing.T) {
	h, backend := importFixture(t)
	msgs := []message.Message{
		say(message.RoleInput, "say alpha"),
		say(message.RoleOutput, "alpha"),
		say(message.RoleInput, "say beta"),
		say(message.RoleOutput, "beta"),
	}
	resp := importReq(t, h, rpc.ImportRequest{
		Outfit:   "opus5-ant",
		WasID:    "23a5a06d",
		Mantra:   "a portable aria",
		Messages: msgs,
		Form: form.Build(form.Snapshot{}, map[string]json.RawMessage{
			"mantra":             json.RawMessage(`"a portable aria"`),
			"system.outfit_name": json.RawMessage(`"opus5-ant"`),
		}, nil),
	})

	require.NotEmpty(t, resp.FigaroID)
	require.NotEqual(t, "23a5a06d", resp.FigaroID, "a trunk id is unique per store; the old one is provenance, not a request")
	require.Equal(t, "23a5a06d", resp.WasID)
	require.Equal(t, len(msgs), resp.Messages)

	log, err := backend.OpenFigIR(resp.FigaroID)
	require.NoError(t, err)
	var got []string
	for _, e := range log.Read() {
		for _, c := range e.Payload.Content {
			if c.Text != "" {
				got = append(got, c.Text)
			}
		}
	}
	require.Equal(t, []string{"say alpha", "alpha", "say beta", "beta"}, got,
		"the conversation must arrive whole and in order")

	// The board carries this store's id, re-stamped as a fork does.
	board, err := backend.FormState(resp.FigaroID)
	require.NoError(t, err)
	raw, ok := board.Get("aria_id")
	require.True(t, ok, "an imported aria must know its own id")
	var stamped string
	require.NoError(t, json.Unmarshal(raw, &stamped))
	require.Equal(t, resp.FigaroID, stamped)

	meta, err := backend.Meta(resp.FigaroID)
	require.NoError(t, err)
	require.NotNil(t, meta)
	require.Equal(t, len(msgs), meta.MessageCount)
	require.Equal(t, "opus5-ant", meta.OutfitName)
	require.Equal(t, "a portable aria", meta.Mantra)
}

// CreateOutfit is content-addressed, so an identical outfit is reused.
func TestImportTwiceSharesTheOutfitAndNothingElse(t *testing.T) {
	h, backend := importFixture(t)
	req := rpc.ImportRequest{Outfit: "opus5-ant", Messages: []message.Message{say(message.RoleInput, "hello")}}

	first := importReq(t, h, req)
	second := importReq(t, h, req)
	require.NotEqual(t, first.FigaroID, second.FigaroID, "each import is its own conversation")

	stumps := map[string]int{}
	for _, n := range backend.Nodes() {
		if n.Kind == "outfit" {
			stumps[n.ID]++
		}
	}
	require.Len(t, stumps, 1, "an identical outfit is reused, not duplicated: %v", stumps)
}

func TestImportLeavesExistingAriasAlone(t *testing.T) {
	h, backend := importFixture(t)
	outfit, err := backend.CreateOutfit("resident", message.Patch{})
	require.NoError(t, err)
	resident, err := backend.CreateConversation(outfit)
	require.NoError(t, err)
	rlog, err := backend.OpenFigIR(resident)
	require.NoError(t, err)
	_, err = rlog.Append(store.Entry[message.Message]{Payload: say(message.RoleOutput, "I was here first")})
	require.NoError(t, err)
	before := len(rlog.Read())

	imported := importReq(t, h, rpc.ImportRequest{
		Outfit: "opus5-ant", Messages: []message.Message{say(message.RoleInput, "newcomer")},
	})
	require.NotEqual(t, resident, imported.FigaroID)

	after, err := backend.OpenFigIR(resident)
	require.NoError(t, err)
	require.Len(t, after.Read(), before, "the resident aria must be untouched")
	var texts []string
	for _, e := range after.Read() {
		for _, c := range e.Payload.Content {
			texts = append(texts, c.Text)
		}
	}
	require.Contains(t, texts, "I was here first")
}

func TestImportRefusesWithoutAOutfit(t *testing.T) {
	h, _ := importFixture(t)
	params, err := json.Marshal(rpc.ImportRequest{Messages: []message.Message{say(message.RoleInput, "x")}})
	require.NoError(t, err)
	_, err = h.importAria(context.Background(), params)
	require.ErrorContains(t, err, "no outfit named")
}
