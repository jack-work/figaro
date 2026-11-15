package figaro

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/arialog"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/store"
)

func TestForkAfterUnansweredInquiryCarriesOpeningAndSealedDeltas(t *testing.T) {
	be, err := store.NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()
	parent, _, err := be.ForkWith("", 0, form.Build(form.Snapshot{}, map[string]json.RawMessage{"aria_id": json.RawMessage(`"parent"`)}, nil))
	if err != nil {
		t.Fatal(err)
	}
	plog, err := be.OpenFigIR(parent)
	if err != nil {
		t.Fatal(err)
	}
	ask, err := plog.Append(arialog.Entry[message.Message]{Payload: message.Message{Role: message.RoleInput, Content: []message.Content{message.TextContent("unanswered parent inquiry")}}})
	if err != nil {
		t.Fatal(err)
	}
	child, _, err := be.ForkWith(parent, 0, form.Build(form.Snapshot{}, map[string]json.RawMessage{form.ForkedFromKey: json.RawMessage(`"` + parent + `"`)}, nil))
	if err != nil {
		t.Fatal(err)
	}
	log, err := be.OpenFigIR(child)
	if err != nil {
		t.Fatal(err)
	}
	birth, _ := log.PeekTail()
	prev := aria.Turn{ID: 1, Inquiry: "unanswered parent inquiry", LTs: []uint64{ask.LT, birth.LT}, Sealed: true}
	srv := aria.NewServer()
	srv.Restore([]aria.Turn{prev})
	a := &Agent{id: child, backend: be, figLog: log, ariaSrv: srv}
	check := func(where string, deltas map[string]livedoc.FormDelta) {
		t.Helper()
		d, ok := deltas[child+"."+form.ForkedFromKey]
		if !ok || string(d.Value) != `"`+parent+`"` {
			t.Errorf("%s lost the fork birth marker: %+v", where, deltas)
		}
	}
	check("opening frame", a.openingFormDeltas())
	opened, err := log.Append(arialog.Entry[message.Message]{Payload: message.Message{Role: message.RoleInput, Content: []message.Content{message.TextContent("child inquiry")}}})
	if err != nil {
		t.Fatal(err)
	}
	ended, err := log.Append(arialog.Entry[message.Message]{Payload: message.Message{Role: message.RoleOutput, Content: []message.Content{message.TextContent("child answer")}}})
	if err != nil {
		t.Fatal(err)
	}
	srv.Restore([]aria.Turn{prev, {ID: 2, Inquiry: "child inquiry", LTs: []uint64{opened.LT, ended.LT}, Nodes: []livedoc.Node{{Src: []livedoc.Src{{LT: ended.LT}}}}}})
	a.stampSealDeltas()
	page := srv.Read(aria.Anchor{Turn: 2}, 1<<20)
	if len(page.Parts) != 1 {
		t.Fatalf("sealed fixture returned %d parts", len(page.Parts))
	}
	check("sealed frame", page.Parts[0].FormDeltas)
}
