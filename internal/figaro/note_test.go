package figaro_test

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
	"github.com/jack-work/figaro/internal/uiir"
)

type noteRig struct {
	a    *figaro.Agent
	prov *formSpyProvider
	be   *store.XwalBackend
	id   string
}

func newNoteRig(t *testing.T) *noteRig {
	t.Helper()
	cb, err := form.Open(filepath.Join(t.TempDir(), "the form channel"))
	require.NoError(t, err)
	prov := &formSpyProvider{}
	be, id := store.NewTestAria(t, "d", message.Patch{})
	a := figaro.NewAgent(figaro.Config{
		Backend: be, Projector: uiir.New(nil), ID: id, Provider: prov, Tools: tool.NewRegistry(), Form: cb,
	})
	t.Cleanup(func() { a.Kill() })
	return &noteRig{a: a, prov: prov, be: be, id: id}
}

func (r *noteRig) version(t *testing.T) uint64 {
	t.Helper()
	v, err := r.be.FormVersion(r.id)
	require.NoError(t, err)
	return v
}

func (r *noteRig) shownSince(t *testing.T, after uint64) []string {
	t.Helper()
	ps, err := r.be.FormPatchesBetween(r.id, after, r.version(t))
	require.NoError(t, err)
	var patches []form.Patch
	for _, p := range ps {
		patches = append(patches, p.Patch)
	}
	return renderedBodies(t, patches...)
}

const noteSender = "figaro 0123456789abcdef0123456789abcdef"

func noteKeys(t *testing.T, snap form.Snapshot) []string {
	t.Helper()
	raw, ok := snap.Get(form.NotesRoot)
	if !ok {
		return nil
	}
	var tree map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &tree))
	var keys []string
	for id := range tree {
		keys = append(keys, id)
	}
	return keys
}

func renderedBodies(t *testing.T, patches ...form.Patch) []string {
	t.Helper()
	tmpls, err := form.LoadDefaultTemplates()
	require.NoError(t, err)
	var out []string
	snap := form.Snapshot{}
	for _, p := range patches {
		rendered, err := form.Render(p, snap, tmpls)
		require.NoError(t, err)
		for _, r := range rendered {
			out = append(out, r.Key+": "+r.Body)
		}
		snap = snap.Apply(p)
	}
	return out
}

func TestANoteStartsNoTurnAndRendersWithTheNextOne(t *testing.T) {
	r := newNoteRig(t)
	a, prov := r.a, r.prov
	runOneTurn(t, a, "boot", nil)
	sends := prov.sendCount()
	before := r.version(t)

	resp, err := a.Note(noteSender, rpc.NoteRequest{Text: "thumbs up on your last message"})
	require.NoError(t, err)
	assert.True(t, resp.OK)
	assert.True(t, strings.HasPrefix(resp.Key, "notes."), resp.Key)
	assert.Equal(t, sends, prov.sendCount(), "a note must not start a turn")

	bodies := r.shownSince(t, before)
	require.Len(t, bodies, 1)
	assert.Contains(t, bodies[0], resp.Key)
	assert.Contains(t, bodies[0], "from figaro ")
	assert.Contains(t, bodies[0], "thumbs up on your last message")
	assert.Regexp(t, `\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z from`, bodies[0])
}

func TestTheTurnThatShowedANoteClearsIt(t *testing.T) {
	r := newNoteRig(t)
	a := r.a
	runOneTurn(t, a, "boot", nil)
	_, err := a.Note(noteSender, rpc.NoteRequest{Text: "one"})
	require.NoError(t, err)
	require.Len(t, noteKeys(t, a.Snapshot()), 1)

	runOneTurn(t, a, "next", nil)
	assert.Empty(t, noteKeys(t, a.Snapshot()))
	cleared := r.version(t)
	runOneTurn(t, a, "again", nil)
	assert.Empty(t, r.shownSince(t, cleared), "a cleared note shows nothing again")
}

func TestOnlyTheNewestEightNotesStay(t *testing.T) {
	r := newNoteRig(t)
	a := r.a
	runOneTurn(t, a, "boot", nil)
	before := r.version(t)
	for i := range 11 {
		_, err := a.Note(noteSender, rpc.NoteRequest{Text: strings.Repeat("x", i+1)})
		require.NoError(t, err)
	}
	assert.Len(t, noteKeys(t, a.Snapshot()), rpc.NoteKeep)

	assert.Len(t, r.shownSince(t, before), 11, "each note shows once and a trimmed one shows nothing more")

	raw, _ := a.Snapshot().Get(form.NotesRoot)
	var tree map[string]string
	require.NoError(t, json.Unmarshal(raw, &tree))
	var kept []string
	for _, line := range tree {
		_, text, _ := strings.Cut(line, ": ")
		kept = append(kept, text)
	}
	sort.Strings(kept)
	var want []string
	for n := 4; n <= 11; n++ {
		want = append(want, strings.Repeat("x", n))
	}
	sort.Strings(want)
	assert.Equal(t, want, kept)
}

func TestAnIdenticalNoteFromTheSameSenderReplacesTheEarlierOne(t *testing.T) {
	a, _, _ := newAgentWithForm(t)
	runOneTurn(t, a, "boot", nil)
	first, err := a.Note(noteSender, rpc.NoteRequest{Text: "same"})
	require.NoError(t, err)
	second, err := a.Note(noteSender, rpc.NoteRequest{Text: "same"})
	require.NoError(t, err)
	assert.Equal(t, first.Key, second.Key)
	assert.Len(t, noteKeys(t, a.Snapshot()), 1)

	other, err := a.Note("figaro fedcba9876543210fedcba9876543210", rpc.NoteRequest{Text: "same"})
	require.NoError(t, err)
	assert.NotEqual(t, first.Key, other.Key, "another sender's note is its own")
	assert.Len(t, noteKeys(t, a.Snapshot()), 2)
}

func TestACallerCannotSetOrUnsetNotes(t *testing.T) {
	a, _, _ := newAgentWithForm(t)
	runOneTurn(t, a, "boot", nil)
	resp, err := a.Note(noteSender, rpc.NoteRequest{Text: "mine"})
	require.NoError(t, err)

	set := form.Build(form.Snapshot{}, map[string]json.RawMessage{"notes.forged": json.RawMessage(`"x"`)}, nil)
	_, _, err = a.Set(set, 0)
	require.Error(t, err)

	unset := form.Build(a.Snapshot(), nil, []string{resp.Key})
	_, _, err = a.Set(unset, 0)
	require.Error(t, err)
	assert.Len(t, noteKeys(t, a.Snapshot()), 1)
}

func TestAnEmptyOrOversizeNoteIsRefused(t *testing.T) {
	a, _, _ := newAgentWithForm(t)
	_, err := a.Note(noteSender, rpc.NoteRequest{Text: "  \n"})
	require.Error(t, err)
	_, err = a.Note(noteSender, rpc.NoteRequest{Text: strings.Repeat("y", rpc.NoteMaxBytes+1)})
	require.Error(t, err)
	_, err = a.Note(noteSender, rpc.NoteRequest{Text: strings.Repeat("y", rpc.NoteMaxBytes)})
	require.NoError(t, err)
}
