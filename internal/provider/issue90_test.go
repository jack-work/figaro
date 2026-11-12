package provider

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/figaro/wire"
	"github.com/jack-work/figaro/internal/hierarchy/hierarchytest"
	"github.com/jack-work/figaro/internal/store"
)

type switchFixture struct {
	t   *testing.T
	dir string
	b   *store.XwalBackend
}

func (f *switchFixture) open() {
	f.t.Helper()
	b, err := store.NewXwalBackend(f.dir, 0)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := wire.Install(b.Store(), f.dir, wire.Capabilities{Trunks: true}); err != nil {
		f.t.Fatal(err)
	}
	f.b = b
}

func (f *switchFixture) restart() {
	f.t.Helper()
	if err := f.b.Close(); err != nil {
		f.t.Fatal(err)
	}
	f.open()
}

func (f *switchFixture) say(id string, texts ...string) {
	f.t.Helper()
	ir, err := f.b.OpenFigIR(id)
	if err != nil {
		f.t.Fatal(err)
	}
	for _, text := range texts {
		if _, err := ir.Append(store.Entry[message.Message]{Payload: message.Message{
			Role: message.RoleInput, Content: []message.Content{message.TextContent(text)},
		}}); err != nil {
			f.t.Fatal(err)
		}
	}
}

func (f *switchFixture) turnOn(id, providerName string) error {
	f.t.Helper()
	ir, err := f.b.OpenFigIR(id)
	if err != nil {
		f.t.Fatal(err)
	}
	rows, err := f.b.OpenTranslator(id, providerName)
	if err != nil {
		f.t.Fatal(err)
	}
	_, err = CatchUp(CatchUpConfig{
		Log: ir, Translator: rows, Provider: providerName, Fingerprint: providerName + "/v0",
		Encode: func(m message.Message, _ form.Snapshot) ([]json.RawMessage, error) {
			body, merr := json.Marshal(map[string]any{"lt": m.LogicalTime})
			return []json.RawMessage{body}, merr
		},
	})
	return err
}

func (f *switchFixture) rowLTs(id, providerName string) []uint64 {
	f.t.Helper()
	rows, err := f.b.OpenTranslator(id, providerName)
	if err != nil {
		f.t.Fatal(err)
	}
	var out []uint64
	for _, r := range rows.Read() {
		out = append(out, r.FigaroLT)
	}
	return out
}

func (f *switchFixture) recordLTs(id string) []uint64 {
	f.t.Helper()
	ir, err := f.b.OpenFigIR(id)
	if err != nil {
		f.t.Fatal(err)
	}
	var out []uint64
	for _, e := range ir.Read() {
		if e.Payload.Role != message.RoleGenesis {
			out = append(out, e.LT)
		}
	}
	return out
}

// rowAtTail is what translate-on-append leaves: a row for the newest record
// and nothing for the ones before it.
func (f *switchFixture) rowAtTail(id, providerName string) {
	f.t.Helper()
	ir, _ := f.b.OpenFigIR(id)
	rows, _ := f.b.OpenTranslator(id, providerName)
	tail, _ := ir.PeekTail()
	hash, _ := store.FigaroHash(tail.Payload)
	if _, err := rows.Append(store.Entry[[]json.RawMessage]{FigaroLT: tail.LT, Payload: []json.RawMessage{json.RawMessage(`{}`)}, Fingerprint: providerName + "/v0", FigaroHash: hash}); err != nil {
		f.t.Fatal(err)
	}
}

// succession is a first holder on provider y, two successors forked off its
// tail that stayed on y, and a builder forked from the last one at the
// holder's first record and seated under it: the builder's history parent
// holds y rows the builder never inherited.
func succession(t *testing.T) (f *switchFixture, builder string) {
	t.Helper()
	f = &switchFixture{t: t, dir: t.TempDir()}
	f.open()
	t.Cleanup(func() { f.b.Close() })
	outfit, err := f.b.CreateOutfit("d", form.Build(form.Snapshot{}, map[string]json.RawMessage{"system.model": json.RawMessage(`"m"`)}, nil))
	if err != nil {
		t.Fatal(err)
	}
	holder, err := f.b.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	f.say(holder, "h1", "h2", "h3", "h4")
	lead := holder
	for i := 0; i < 2; i++ {
		if err := f.turnOn(lead, "y"); err != nil {
			t.Fatal(err)
		}
		if _, lead, err = f.b.Fork(lead); err != nil {
			t.Fatal(err)
		}
		f.say(lead, "s1", "s2")
	}
	if err := f.turnOn(lead, "y"); err != nil {
		t.Fatal(err)
	}
	if _, builder, err = f.b.ForkAt(lead, f.recordLTs(holder)[1]); err != nil {
		t.Fatal(err)
	}
	if err := hierarchytest.Of(f.b).Move(builder, lead); err != nil {
		t.Fatal(err)
	}
	if links := f.b.LineageLinks(builder); links[len(links)-2].Node != lead {
		t.Fatalf("setup: the builder's history parent is %s, want the lead %s", links[len(links)-2].Node, lead)
	}
	return f, builder
}

func (f *switchFixture) requireARowPerRecord(id, providerName string) {
	f.t.Helper()
	rows, records := f.rowLTs(id, providerName), f.recordLTs(id)
	if fmt.Sprint(rows) != fmt.Sprint(records) {
		f.t.Fatalf("rows translate records %v, the log holds %v", rows, records)
	}
}

// Issue 90, second sighting: a figaro with turns on one provider is switched
// to the provider its history parent uses, the daemon restarts, and the next
// turn dies with "main-LT must be non-decreasing: got 164, last 233". The
// restart is what makes catch-up check its rows for a hole.
func TestIssue90ASwitchToTheHistoryParentsProviderSurvivesARestart(t *testing.T) {
	f, builder := succession(t)
	f.say(builder, "b1", "b2", "b3", "b4")
	if err := f.turnOn(builder, "x"); err != nil {
		t.Fatal(err)
	}
	f.say(builder, "switched")
	f.rowAtTail(builder, "y")
	for restart := 1; restart <= 2; restart++ {
		f.restart()
		f.say(builder, fmt.Sprintf("after restart %d", restart))
		if err := f.turnOn(builder, "y"); err != nil {
			t.Fatalf("turn after restart %d: %v", restart, err)
		}
		f.requireARowPerRecord(builder, "y")
	}
}

// Issue 90, the fork sighting (El Buscador: "got 3, last 5"): the first turn
// of a fork seated under a relative on the same provider.
func TestIssue90AForksFirstTurnOnItsHistoryParentsProvider(t *testing.T) {
	f, builder := succession(t)
	f.say(builder, "b1", "b2")
	f.rowAtTail(builder, "y")
	f.say(builder, "first prompt")
	if err := f.turnOn(builder, "y"); err != nil {
		t.Fatalf("first turn: %v", err)
	}
	f.requireARowPerRecord(builder, "y")
	f.say(builder, "second prompt")
	if err := f.turnOn(builder, "y"); err != nil {
		t.Fatalf("second turn: %v", err)
	}
	f.requireARowPerRecord(builder, "y")
}
