package angelus_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/figaro/wire"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/sdk"
)

// What one request to the model looked like: the conversation the provider
// was handed, and whether its translated rows could be brought up to date.
type witnessedSend struct {
	aria     string
	said     []string
	lastRole message.Role
	rowLTs   []uint64
	catchUp  error
}

type witness struct {
	mu      sync.Mutex
	backend *store.XwalBackend
	sends   []witnessedSend
}

func (w *witness) of(aria string) []witnessedSend {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []witnessedSend
	for _, s := range w.sends {
		if s.aria == aria {
			out = append(out, s)
		}
	}
	return out
}

// witnessProvider keeps translated rows the way a real provider does, so a
// fork whose rows cannot be written fails its turn here too.
type witnessProvider struct{ w *witness }

func (p *witnessProvider) Name() string        { return "mock" }
func (p *witnessProvider) Fingerprint() string { return "mock/v0" }
func (p *witnessProvider) SetModel(string)     {}
func (p *witnessProvider) Models(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}

func (p *witnessProvider) Send(_ context.Context, in provider.SendInput, bus provider.Bus) error {
	seen := witnessedSend{aria: in.AriaID}
	rows, err := p.w.backend.OpenTranslator(in.AriaID, "mock")
	if err == nil {
		_, err = provider.CatchUp(provider.CatchUpConfig{
			Log: in.FigLog, Translator: rows, Form: in.Form, Studies: in.Studies,
			Fingerprint: p.Fingerprint(), Provider: p.Name(),
			Encode: func(m message.Message, _ form.Snapshot) ([]json.RawMessage, error) {
				body, merr := json.Marshal(map[string]any{"role": m.Role, "lt": m.LogicalTime})
				return []json.RawMessage{body}, merr
			},
		})
	}
	seen.catchUp = err
	if rows != nil {
		for _, r := range rows.Read() {
			seen.rowLTs = append(seen.rowLTs, r.FigaroLT)
		}
	}
	for _, e := range in.FigLog.Read() {
		text := ""
		for _, c := range e.Payload.Content {
			if c.Type == message.ContentProse {
				text += c.Text
			}
		}
		if text == "" {
			continue
		}
		seen.said = append(seen.said, text)
		seen.lastRole = e.Payload.Role
	}
	p.w.mu.Lock()
	p.w.sends = append(p.w.sends, seen)
	p.w.mu.Unlock()
	if err != nil {
		return err
	}
	msg := message.Message{
		Role:       message.RoleOutput,
		Content:    []message.Content{message.TextContent("answer to " + seen.said[len(seen.said)-1])},
		StopReason: message.StopEnd,
	}
	bus.PushDelta(msg.Content[0])
	bus.PushMessageEnd(string(msg.StopReason))
	bus.PushFigaro(msg)
	return nil
}

func witnessedDaemon(t *testing.T) (*angelus.Angelus, *sdk.Angelus, context.Context, *witness) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(dir+"/outfits", 0700))
	require.NoError(t, os.WriteFile(dir+"/outfits/mock.toml", []byte("\n[system]\nprovider = \"mock\"\nmodel = \"mock-model\"\n"), 0600))
	require.NoError(t, os.WriteFile(dir+"/config.toml", []byte("default_outfit = \"mock\"\n"), 0600))
	w := &witness{}
	a, acli, ctx, _ := daemonWithProvider(t, dir,
		func(_ *angelus.Angelus, b *store.XwalBackend) {
			w.backend = b
			// The fork handler seats a builder through the hierarchy; without
			// it the builder stays under the first holder and nothing is tested.
			require.NoError(t, wire.Install(b.Store(), t.TempDir(), wire.Capabilities{Trunks: true}))
		},
		func() provider.Provider { return &witnessProvider{w: w} })
	return a, acli, ctx, w
}

func ask(t *testing.T, a *angelus.Angelus, ctx context.Context, id, prompt string) {
	t.Helper()
	done := make(chan struct{}, 1)
	fcli, err := sdk.DialAria(ariaAddr(a, id), func(method string, _ json.RawMessage) {
		if method == "turn.done" {
			select {
			case done <- struct{}{}:
			default:
			}
		}
	})
	require.NoError(t, err)
	defer fcli.Close()
	_, _, err = fcli.Qua(ctx, prompt, nil)
	require.NoError(t, err)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("%q to %s never finished", prompt, id)
	}
	waitIdle(t, a, id)
}

type irRow struct {
	lt   uint64
	role message.Role
	text string
}

func spokenIn(t *testing.T, acli *sdk.Angelus, ctx context.Context, id string) []irRow {
	t.Helper()
	resp, err := acli.IRAll(ctx, id, 0)
	require.NoError(t, err)
	var out []irRow
	for _, e := range resp.Entries {
		var m message.Message
		require.NoError(t, json.Unmarshal(e.Payload, &m))
		text := ""
		for _, c := range m.Content {
			if c.Type == message.ContentProse {
				text += c.Text
			}
		}
		if text != "" {
			out = append(out, irRow{lt: e.LT, role: m.Role, text: text})
		}
	}
	return out
}

func textsIn(rows []irRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.text
	}
	return out
}

// The succession line a long-lived role leaves behind: a first holder whose
// turn 1 every successor still opens with, and a lead two head forks later
// with turns of its own.
type successionLine struct {
	a     *angelus.Angelus
	acli  *sdk.Angelus
	ctx   context.Context
	w     *witness
	first string
	lead  string
	// LT of turn 1's question, the cut `<lead>:1.0` resolves to.
	question uint64
}

func successionOf(t *testing.T) successionLine {
	t.Helper()
	a, acli, ctx, w := witnessedDaemon(t)
	s := successionLine{a: a, acli: acli, ctx: ctx, w: w}
	created, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	s.first = created.FigaroID
	for i := 1; i <= 3; i++ {
		ask(t, a, ctx, s.first, fmt.Sprintf("holder prompt %d", i))
	}
	cur := s.first
	for i := 1; i <= 2; i++ {
		next, err := acli.Fork(ctx, cur, 0, 0, nil, nil)
		require.NoError(t, err)
		cur = next.Alternative
		ask(t, a, ctx, cur, fmt.Sprintf("successor %d prompt", i))
	}
	s.lead = cur
	rows := spokenIn(t, acli, ctx, s.lead)
	require.Equal(t, "holder prompt 1", rows[0].text)
	s.question = rows[0].lt
	return s
}

func (s successionLine) builder(t *testing.T, keepQuestion bool) string {
	t.Helper()
	var turn, lt uint64 = 1, 0
	if keepQuestion {
		turn, lt = 0, s.question
	}
	forked, err := s.acli.Fork(s.ctx, s.lead, turn, lt, nil, nil)
	require.NoError(t, err)
	n, ok := s.w.backend.Node(forked.Alternative)
	require.True(t, ok)
	require.Equal(t, s.lead, n.Parent, "the builder does not answer to its lead")
	require.Equal(t, s.first, n.ForkPoint, "the builder's history was not cut from the figaro that holds the cut")
	return forked.Alternative
}

// A listing carries the fork point only where it differs from the parent, so
// status can name where a seated builder's history was cut.
func TestAListingNamesASeatedBuildersForkPoint(t *testing.T) {
	s := successionOf(t)
	id := s.builder(t, true)
	list, err := s.acli.List(s.ctx)
	require.NoError(t, err)
	rows := map[string]rpc.FigaroInfoResponse{}
	for _, f := range list.Figaros {
		rows[f.ID] = f
	}
	require.Equal(t, s.lead, rows[id].Parent)
	require.Equal(t, s.first, rows[id].ForkPoint)
	require.NotEmpty(t, rows[s.lead].Parent)
	require.Empty(t, rows[s.lead].ForkPoint, "the lead answers to the figaro it was cut from: one edge, not two")
}

var builderCuts = []struct {
	name         string
	keepQuestion bool
	inherits     []string
}{
	{"at the first question (:1.0)", true, []string{"holder prompt 1"}},
	{"before the first turn (:1)", false, []string{}},
}

// Fork point 1: the builder holds nothing of the turn it was cut at.
func TestABuilderForkedAtItsLeadsFirstTurnCarriesNoneOfThatTurn(t *testing.T) {
	s := successionOf(t)
	for _, cut := range builderCuts {
		id := s.builder(t, cut.keepQuestion)
		require.Equal(t, cut.inherits, textsIn(spokenIn(t, s.acli, s.ctx, id)), cut.name)
		page, err := s.acli.Read(s.ctx, rpc.ReadRequest{FigaroID: id})
		require.NoError(t, err)
		var questions []string
		for _, part := range page.Parts {
			questions = append(questions, part.Inquiry)
			require.Empty(t, part.Nodes, "%s: the builder shows an answer to %q", cut.name, part.Inquiry)
		}
		require.ElementsMatch(t, cut.inherits, questions, "%s: the turns the builder shows", cut.name)
	}
}

// Fork point 2, and the three-send case seen on Opus: the first send was
// refused (the conversation handed over ended on an old assistant message),
// the second was swallowed, the third landed. Every send must reach the
// model as the last thing said, after everything inherited.
func TestEverySendToAFreshBuilderReachesTheModelLast(t *testing.T) {
	s := successionOf(t)
	for _, cut := range builderCuts {
		id := s.builder(t, cut.keepQuestion)
		want := append([]string(nil), cut.inherits...)
		var inheritedTail uint64
		if rows := spokenIn(t, s.acli, s.ctx, id); len(rows) > 0 {
			inheritedTail = rows[len(rows)-1].lt
		}
		for n := 1; n <= 3; n++ {
			prompt := fmt.Sprintf("builder prompt %d", n)
			ask(t, s.a, s.ctx, id, prompt)
			sends := s.w.of(id)
			require.Len(t, sends, n, "%s: send %d did not reach the model once", cut.name, n)
			got := sends[n-1]
			want = append(want, prompt)
			require.Equal(t, want, got.said, "%s: what the model was handed on send %d", cut.name, n)
			require.Equal(t, message.RoleInput, got.lastRole, "%s: send %d ends on the model's own words", cut.name, n)
			want = append(want, "answer to "+prompt)
		}
		rows := spokenIn(t, s.acli, s.ctx, id)
		require.Equal(t, want, textsIn(rows), cut.name)
		for i := 1; i < len(rows); i++ {
			require.Greater(t, rows[i].lt, rows[i-1].lt, "%s: LTs out of order at %q", cut.name, rows[i].text)
		}
		require.Greater(t, rows[len(cut.inherits)].lt, inheritedTail, "%s: the first send landed inside what was inherited", cut.name)
	}
}

// Fork point 3: El Buscador's first turn died writing a translated row at an
// LT below the channel's tail. A builder's rows continue from its own records.
func TestAFreshBuildersTranslatedRowsFollowItsOwnRecords(t *testing.T) {
	s := successionOf(t)
	for _, cut := range builderCuts {
		id := s.builder(t, cut.keepQuestion)
		for n := 1; n <= 3; n++ {
			ask(t, s.a, s.ctx, id, fmt.Sprintf("builder prompt %d", n))
		}
		sends := s.w.of(id)
		require.Len(t, sends, 3, cut.name)
		for n, send := range sends {
			require.NoError(t, send.catchUp, "%s: send %d could not write its rows", cut.name, n+1)
			for i := 1; i < len(send.rowLTs); i++ {
				require.Greater(t, send.rowLTs[i], send.rowLTs[i-1], "%s: send %d rows out of order: %v", cut.name, n+1, send.rowLTs)
			}
		}
		rows := spokenIn(t, s.acli, s.ctx, id)
		prompt := rows[len(rows)-2]
		require.Equal(t, "builder prompt 3", prompt.text)
		last := sends[2].rowLTs
		require.Equal(t, prompt.lt, last[len(last)-1], "%s: the newest row is not the newest record's", cut.name)
		own := 0
		for _, lt := range last {
			if lt > prompt.lt-5 {
				own++
			}
		}
		require.Equal(t, 5, own, "%s: three prompts and two answers of its own, one row each: %v", cut.name, last)
	}
}
