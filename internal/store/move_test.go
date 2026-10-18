package store

import (
	"encoding/json"
	"io"
	"reflect"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store/xwal"
)

func moveBackend(t *testing.T) *XwalBackend {
	t.Helper()
	be, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { be.Close() })
	return be
}

func shipOutbound(t *testing.T, out *Outbound, g *xwal.Graft) {
	t.Helper()
	for _, f := range out.Files {
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		if err := g.Write(f.Node, f.Channel, f.Name, 0, data); err != nil {
			t.Fatal(err)
		}
	}
}

type ariaView struct {
	IR          []message.Message
	Translation [][]json.RawMessage
	Form        string
	Meta        *AriaMeta
	Parent      string
}

func viewAria(t *testing.T, be *XwalBackend, id string) ariaView {
	t.Helper()
	ir, err := be.OpenFigIR(id)
	if err != nil {
		t.Fatalf("open %s: %v", id, err)
	}
	tr, err := be.OpenTranslator(id, "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	var v ariaView
	v.IR = readAll(ir)
	for _, e := range tr.ReadFrom(1, 0) {
		v.Translation = append(v.Translation, e.Payload)
	}
	snap, err := be.FormState(id)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	v.Form = string(raw)
	if v.Meta, err = be.Meta(id); err != nil {
		t.Fatal(err)
	}
	n, _ := be.Node(id)
	v.Parent = n.Parent
	return v
}

func talk(t *testing.T, be *XwalBackend, id string, turn uint64, text string) uint64 {
	t.Helper()
	ir, err := be.OpenFigIR(id)
	if err != nil {
		t.Fatal(err)
	}
	put(t, ir, ask(turn, text))
	lt := put(t, ir, answer(turn, text+"!"))
	tr, err := be.OpenTranslator(id, "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Append(Entry[[]json.RawMessage]{LT: lt, Payload: []json.RawMessage{json.RawMessage(`{"signed":"` + text + `"}`)}}); err != nil {
		t.Fatal(err)
	}
	return lt
}

// A whole fork component moves between backends under its ids: the IR, the
// translator cache, the board and the meta sidecar read back the same on the
// new home, the old home forgets it, and a neighbour under the same outfit
// is untouched.
func TestABackendMovesAComponentWhole(t *testing.T) {
	src, dst := moveBackend(t), moveBackend(t)
	outfit, err := src.CreateOutfit("o", form.Build(form.Snapshot{}, map[string]json.RawMessage{"credo": json.RawMessage(`"figaro"`)}, nil))
	if err != nil {
		t.Fatal(err)
	}
	a, err := src.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	neighbour, err := src.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	talk(t, src, neighbour, 1, "hello")
	at := talk(t, src, a, 1, "alpha")
	talk(t, src, a, 2, "beta")
	if _, err := src.ApplyForm(a, form.Build(form.Snapshot{}, map[string]json.RawMessage{"mantra": json.RawMessage(`"moving"`)}, nil)); err != nil {
		t.Fatal(err)
	}
	if err := src.UpdateMeta(a, func(m *AriaMeta) { m.Mantra = "moving"; m.TTLMS = 3600000; m.CreatedAtMS = 1 }); err != nil {
		t.Fatal(err)
	}
	b, _, err := src.ForkWith(a, at, birth("b"))
	if err != nil {
		t.Fatal(err)
	}
	talk(t, src, b, 2, "gamma")

	before := map[string]ariaView{a: viewAria(t, src, a), b: viewAria(t, src, b)}
	neighbourBefore := viewAria(t, src, neighbour)

	out, err := src.ExportComponent(b, "feedf00d")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if got := out.TrunkIDs(); !reflect.DeepEqual(got, []string{a, b}) {
		t.Fatalf("component %v, want [%s %s]", got, a, b)
	}
	digest, err := out.Digest()
	if err != nil {
		t.Fatal(err)
	}
	g, err := dst.BeginImport("feedf00d", out.Component)
	if err != nil {
		t.Fatal(err)
	}
	shipOutbound(t, out, g)
	if err := dst.CommitImport(g, digest, out.Metas); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{a, b} {
		got := viewAria(t, dst, id)
		want := before[id]
		if id == a {
			got.Parent, want.Parent = "", ""
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s on the new home:\n%+v\nwant\n%+v", id, got, want)
		}
	}
	if entries := dst.TTLEntries(); len(entries) != 1 || entries[0].ID != a {
		t.Fatalf("ttl on the new home: %+v", entries)
	}

	if err := src.RemoveMoved(out.Component, "feedf00d"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{a, b} {
		if _, ok := src.Node(id); ok {
			t.Fatalf("%s still listed on the old home", id)
		}
		if m, _ := src.Meta(id); m != nil {
			t.Fatalf("%s meta left on the old home", id)
		}
	}
	if got := viewAria(t, src, neighbour); !reflect.DeepEqual(got, neighbourBefore) {
		t.Fatal("the neighbour under the same outfit changed")
	}
	talk(t, dst, b, 3, "delta")
}
