package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/sdk/aria"
)

func capacitySession(t *testing.T) *interactiveInput {
	t.Helper()
	var out bytes.Buffer
	in := &interactiveInput{mu: new(renderLock), bound: &formMirror{}}
	in.lt = newLivelogTurn(&out, 120, 20, &renderSettings{}, "abcd1234", time.Now(),
		newSessionStatus("abcd1234", time.Now()), nil, dimRule)
	in.models = &modelsFeed{onChange: in.recomputeLimit}
	in.lt.status.update(aria.Metrics{ContextTokens: 12_400, ContextExact: true})
	return in
}

func boundFormPatch(base form.Snapshot, kv map[string]string) form.Patch {
	set := map[string]json.RawMessage{}
	for k, v := range kv {
		set[k] = json.RawMessage(v)
	}
	return form.Build(base, set, nil)
}

func catalogPatch(t *testing.T, base form.Snapshot, provider string, entries map[string]int) form.Patch {
	t.Helper()
	p := models.Provider{IR: models.IRVersion}
	for id, w := range entries {
		p.Place(id, models.Entry{Context: models.Context{Default: w}})
	}
	raw, err := json.Marshal(p)
	require.NoError(t, err)
	return form.Build(base, map[string]json.RawMessage{provider: raw}, nil)
}

func barText(in *interactiveInput) string {
	in.mu.Lock()
	defer in.mu.Unlock()
	return strings.Join(in.lt.status.viewOf(pitNothing, false, time.Now()).render(120), "\n")
}

// The bar shows the capacity the daemon published on the bound form, the
// same number figaro status prints. The pager's own catalog copy never
// decides it: #210, where a stale copy said "capacity unknown".
func TestBarShowsTheDaemonsPublishedCapacity(t *testing.T) {
	in := capacitySession(t)
	cat, _, _ := in.models.mirror.state()
	in.models.mirror.reset(cat.Apply(catalogPatch(t, cat, "anthropic", map[string]int{"claude-opus-5": 500_000})), 0, 1)

	bound, _, _ := in.bound.state()
	in.applyBoundFormDelta(0, rpc.FormDelta{Schema: rpc.FormDeltaSchema, Version: 1,
		Patch: boundFormPatch(bound, map[string]string{"system.provider": `"anthropic"`, "system.model": `"claude-opus-5"`})})
	require.Contains(t, barText(in), "12.4k")
	require.NotContains(t, barText(in), "%", "nothing published yet: tokens only, whatever the local catalog says")

	bound, _, _ = in.bound.state()
	in.applyBoundFormDelta(0, rpc.FormDelta{Schema: rpc.FormDeltaSchema, Version: 2,
		Patch: boundFormPatch(bound, map[string]string{form.ContextLimitKey: `1000000`})})
	require.Contains(t, barText(in), "12.4k/1.0m 1.2%")

	bound, _, _ = in.bound.state()
	in.applyBoundFormDelta(0, rpc.FormDelta{Schema: rpc.FormDeltaSchema, Version: 3,
		Patch: form.Build(bound, nil, []string{form.ContextLimitKey})})
	require.NotContains(t, barText(in), "%", "a withdrawn capacity clears the percentage")
}

// A parked subject keeps its bound form: hopping back paints the percentage from
// what is held, before any read.
func TestAParkedSubjectKeepsItsBoundForm(t *testing.T) {
	in := capacitySession(t)
	in.mu.Lock()
	in.figaroID = "first"
	in.mu.Unlock()
	bound, _, _ := in.bound.state()
	in.applyBoundFormDelta(0, rpc.FormDelta{Schema: rpc.FormDeltaSchema, Version: 1, Patch: boundFormPatch(bound, map[string]string{
		"system.provider": `"anthropic"`, "system.model": `"claude-opus-5"`, form.ContextLimitKey: `1000000`})})
	held := in.subjectBoundForm()

	in.lt.client.Apply(aria.Page{Parts: partsFor(1, 2, "FIRST")}, aria.Notify)
	_, _, ok := in.claimSubject(0, "second", "first", nil, 0)
	require.True(t, ok)
	require.NotSame(t, held, in.subjectBoundForm(), "a new subject starts from an empty bound form")

	_, _, ok = in.claimSubject(0, "first", "second", nil, 0)
	require.True(t, ok)
	require.Same(t, held, in.subjectBoundForm(), "the parked bound form comes back with its subject")
	in.lt.status.update(aria.Metrics{ContextTokens: 12_400, ContextExact: true})
	in.recomputeLimit()
	require.Contains(t, barText(in), "1.2%")
}

func TestCatchUpSkipsWhatADeltaAlreadyApplied(t *testing.T) {
	var m formMirror
	p1 := boundFormPatch(form.Snapshot{}, map[string]string{"a": `1`})
	s1 := form.Snapshot{}.Apply(p1)
	p2 := boundFormPatch(s1, map[string]string{"b": `2`})
	s2 := s1.Apply(p2)
	p3 := boundFormPatch(s2, map[string]string{"a": `3`})

	m.reset(s1, 0, 1)
	require.Equal(t, formApplied, m.apply(rpc.FormDelta{Schema: rpc.FormDeltaSchema, Version: 2, Patch: p2}))
	m.catchUp(&rpc.FormResponse{Delta: true, Version: 3, Patches: []rpc.FormVersionedPatch{
		{Version: 2, Patch: p2}, {Version: 3, Patch: p3}}})
	snap, v, _ := m.state()
	require.Equal(t, uint64(3), v)
	raw, _ := snap.Get("a")
	require.JSONEq(t, `3`, string(raw))

	m.catchUp(&rpc.FormResponse{Version: 9, Snapshot: form.FromMap(map[string]json.RawMessage{"z": json.RawMessage(`1`)})})
	snap, v, _ = m.state()
	require.Equal(t, uint64(9), v)
	require.False(t, snap.Has("a"), "a full answer replaces the mirror")
}

// A replay has only its tape. A tape recorded with no window (older than the
// header field, or recorded before the catalog answered) draws tokens alone:
// today's @models would put a window the recording never saw under it, and
// 0.58.0's 136k against a 200k tape is exactly that drift.
func TestAReplayWithNoRecordedWindowDrawsNoPercentage(t *testing.T) {
	tapeOnly.Store(true)
	t.Cleanup(func() { tapeOnly.Store(false) })
	in := capacitySession(t)
	cat, _, _ := in.models.mirror.state()
	delta, err := json.Marshal(rpc.FormDelta{Schema: rpc.FormDeltaSchema, AriaID: models.FormID, Version: 1,
		Patch: catalogPatch(t, cat, "copilot", map[string]int{"claude-haiku-4.5": 136_000})})
	require.NoError(t, err)
	in.models.handle(rpc.MethodFormDelta, delta)

	bound, _, _ := in.bound.state()
	in.applyBoundFormDelta(0, rpc.FormDelta{Schema: rpc.FormDeltaSchema, Version: 1,
		Patch: boundFormPatch(bound, map[string]string{"system.provider": `"copilot"`, "system.model": `"claude-haiku-4.5"`, form.ContextLimitKey: `136000`})})
	in.recomputeLimit()
	require.Contains(t, barText(in), "12.4k")
	require.NotContains(t, barText(in), "%", "neither the live catalog nor today's published capacity reaches a replay's bar")

	in.recordedWindow = 200_000
	in.recomputeLimit()
	require.Contains(t, barText(in), "12.4k/200.0k 6.2%")
}
