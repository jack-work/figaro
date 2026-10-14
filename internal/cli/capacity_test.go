package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/sdk/aria"
)

type askLog struct {
	mu   sync.Mutex
	asks []string
}

func (a *askLog) ask(provider, model string) {
	a.mu.Lock()
	a.asks = append(a.asks, provider+"/"+model)
	a.mu.Unlock()
}

func (a *askLog) list() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.asks...)
}

func capacitySession(t *testing.T) (*interactiveInput, *askLog) {
	t.Helper()
	var out bytes.Buffer
	in := &interactiveInput{mu: new(renderLock), board: &formMirror{}}
	in.lt = newLivelogTurn(&out, 120, 20, &renderSettings{}, "abcd1234", time.Now(),
		newSessionStatus("abcd1234", time.Now()), nil, dimRule)
	asks := &askLog{}
	in.models = &modelsFeed{ask: asks.ask, wanted: map[string]time.Time{}, onChange: in.recomputeLimit}
	in.lt.status.update(aria.Metrics{ContextTokens: 12_400, ContextExact: true})
	return in, asks
}

func boardPatch(base form.Snapshot, kv map[string]string) form.Patch {
	set := map[string]json.RawMessage{}
	for k, v := range kv {
		set[k] = json.RawMessage(v)
	}
	return form.Build(base, set, nil)
}

func catalogPatch(t *testing.T, base form.Snapshot, provider string, entries map[string]int) form.Patch {
	t.Helper()
	p := models.Provider{Models: map[string]models.Entry{}}
	for id, w := range entries {
		p.Models[id] = models.Entry{Context: models.Context{Default: w}}
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

// The bar's percentage is derived on the client from two mirrors: the
// subject's board names provider x model, the catalog names its capacity. A
// fork's dormant alternative has both, so it reads like its parent.
func TestBarDerivesCapacityFromBoardAndCatalog(t *testing.T) {
	in, asks := capacitySession(t)

	board, _, _ := in.board.state()
	in.applyBoardDelta(0, rpc.FormDelta{Schema: rpc.FormDeltaSchema, Version: 1,
		Patch: boardPatch(board, map[string]string{"system.provider": `"anthropic"`, "system.model": `"claude-opus-5"`})})
	require.Contains(t, barText(in), "12.4k")
	require.NotContains(t, barText(in), "%", "no catalog yet: tokens only")
	require.Eventually(t, func() bool { return len(asks.list()) == 1 }, time.Second, 2*time.Millisecond)
	require.Equal(t, []string{"anthropic/claude-opus-5"}, asks.list(), "a miss asks the daemon for exactly the board's pair")

	cat, _, _ := in.models.mirror.state()
	delta, err := json.Marshal(rpc.FormDelta{Schema: rpc.FormDeltaSchema, AriaID: models.FormID, Version: 1,
		Patch: catalogPatch(t, cat, "anthropic", map[string]int{"claude-opus-5": 1_000_000, "claude-sonnet-5": 500_000})})
	require.NoError(t, err)
	in.models.handle(rpc.MethodFormDelta, delta)
	require.Contains(t, barText(in), "12.4k/1.0m 1.2%")

	// A model switch is a board delta; the bar follows it with no round trip.
	board, _, _ = in.board.state()
	in.applyBoardDelta(0, rpc.FormDelta{Schema: rpc.FormDeltaSchema, Version: 2,
		Patch: boardPatch(board, map[string]string{"system.model": `"claude-sonnet-5"`})})
	require.Contains(t, barText(in), "12.4k/500.0k 2.5%")

	// The same model under another provider is not the same entry.
	board, _, _ = in.board.state()
	in.applyBoardDelta(0, rpc.FormDelta{Schema: rpc.FormDeltaSchema, Version: 3,
		Patch: boardPatch(board, map[string]string{"system.provider": `"copilot"`})})
	require.NotContains(t, barText(in), "%")
}

func TestABoardPinNeedsNoCatalogAndAsksForNothing(t *testing.T) {
	in, asks := capacitySession(t)
	board, _, _ := in.board.state()
	in.applyBoardDelta(0, rpc.FormDelta{Schema: rpc.FormDeltaSchema, Version: 1, Patch: boardPatch(board, map[string]string{
		"system.provider": `"anthropic"`, "system.model": `"unknown"`, "system.max_context_tokens": `200000`})})
	require.Contains(t, barText(in), "12.4k/200.0k 6.2%")
	time.Sleep(20 * time.Millisecond)
	require.Empty(t, asks.list())
}

// A miss is asked about once per window, not once per delta: the bar
// recomputes on every board change and must not flood the daemon.
func TestAMissIsAskedOncePerWindow(t *testing.T) {
	in, asks := capacitySession(t)
	board, _, _ := in.board.state()
	in.applyBoardDelta(0, rpc.FormDelta{Schema: rpc.FormDeltaSchema, Version: 1, Patch: boardPatch(board, map[string]string{
		"system.provider": `"anthropic"`, "system.model": `"nope"`})})
	for v := uint64(2); v < 20; v++ {
		board, _, _ = in.board.state()
		in.applyBoardDelta(0, rpc.FormDelta{Schema: rpc.FormDeltaSchema, Version: v,
			Patch: boardPatch(board, map[string]string{"mantra": `"m` + string(rune('a'+v)) + `"`})})
	}
	time.Sleep(20 * time.Millisecond)
	require.Len(t, asks.list(), 1)
}

// A parked subject keeps its board: hopping back paints the percentage from
// what is held, before any read.
func TestAParkedSubjectKeepsItsBoard(t *testing.T) {
	in, _ := capacitySession(t)
	cat, _, _ := in.models.mirror.state()
	in.models.mirror.reset(cat.Apply(catalogPatch(t, cat, "anthropic", map[string]int{"claude-opus-5": 1_000_000})), 1)

	in.mu.Lock()
	in.figaroID = "first"
	in.mu.Unlock()
	board, _, _ := in.board.state()
	in.applyBoardDelta(0, rpc.FormDelta{Schema: rpc.FormDeltaSchema, Version: 1, Patch: boardPatch(board, map[string]string{
		"system.provider": `"anthropic"`, "system.model": `"claude-opus-5"`})})
	held := in.subjectBoard()

	in.lt.client.Apply(aria.Page{Parts: partsFor(1, 2, "FIRST")}, aria.Notify)
	_, _, ok := in.claimSubject(0, "second", "first", nil, 0)
	require.True(t, ok)
	require.NotSame(t, held, in.subjectBoard(), "a new subject starts from an empty board")

	_, _, ok = in.claimSubject(0, "first", "second", nil, 0)
	require.True(t, ok)
	require.Same(t, held, in.subjectBoard(), "the parked board comes back with its subject")
	in.lt.status.update(aria.Metrics{ContextTokens: 12_400, ContextExact: true})
	in.recomputeLimit()
	require.Contains(t, barText(in), "1.2%")
}

func TestCatchUpSkipsWhatADeltaAlreadyApplied(t *testing.T) {
	var m formMirror
	p1 := boardPatch(form.Snapshot{}, map[string]string{"a": `1`})
	s1 := form.Snapshot{}.Apply(p1)
	p2 := boardPatch(s1, map[string]string{"b": `2`})
	s2 := s1.Apply(p2)
	p3 := boardPatch(s2, map[string]string{"a": `3`})

	m.reset(s1, 1)
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
