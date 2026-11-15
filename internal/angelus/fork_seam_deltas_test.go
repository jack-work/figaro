package angelus

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/arialog"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/uiir"
)

func TestForkBirthDeltasSurviveTurnBracketRecomposition(t *testing.T) {
	for _, answered := range []bool{false, true} {
		t.Run(map[bool]string{false: "unanswered parent", true: "answered parent"}[answered], func(t *testing.T) {
			be, err := store.NewXwalBackend(t.TempDir(), 0)
			if err != nil {
				t.Fatal(err)
			}
			defer be.Close()
			parent, _, err := be.ForkWith("", 0, form.Build(form.Snapshot{}, map[string]json.RawMessage{"aria_id": json.RawMessage(`"parent"`)}, nil))
			if err != nil {
				t.Fatal(err)
			}
			put := func(id string, role message.Role, text string) uint64 {
				log, err := be.OpenFigIR(id)
				if err != nil {
					t.Fatal(err)
				}
				e, err := log.Append(arialog.Entry[message.Message]{Payload: message.Message{Role: role, Content: []message.Content{message.TextContent(text)}}})
				if err != nil {
					t.Fatal(err)
				}
				return e.LT
			}
			put(parent, message.RoleInput, "parent question")
			if answered {
				put(parent, message.RoleOutput, "parent answer")
			}
			child, _, err := be.ForkWith(parent, 0, form.Build(form.Snapshot{}, map[string]json.RawMessage{form.ForkedFromKey: json.RawMessage(`"` + parent + `"`)}, nil))
			if err != nil {
				t.Fatal(err)
			}
			opened := put(child, message.RoleInput, "child question")
			ended := put(child, message.RoleOutput, "child answer")
			put(child, message.RoleInput, "next question")
			put(child, message.RoleOutput, "next answer")
			a := &Angelus{Backend: be, uiProj: uiir.New(nil)}
			whole := a.composeTurns(child, 1, ^uint64(0))
			want := whole[1].FormDeltas
			if _, ok := want[child+"."+form.ForkedFromKey]; !ok {
				t.Fatal("whole-history fixture has no birth marker")
			}
			got := a.composeTurns(child, opened, ended)
			if len(got) != 1 {
				t.Fatalf("turn bracket returned %d turns", len(got))
			}
			if !reflect.DeepEqual(got[0].FormDeltas, want) {
				t.Fatalf("cache miss lost the birth deltas: got %+v, want %+v", got[0].FormDeltas, want)
			}
			cache := aria.NewComposedCache(nil, a.composeTurns, a.uiLineage)
			srv := aria.NewServer()
			srv.BindCache(child, cache)
			srv.Restore(whole)
			srv.ReleaseCache()
			page := srv.Read(aria.Anchor{Turn: whole[1].ID}, 1)
			if len(page.Parts) != 1 || !reflect.DeepEqual(page.Parts[0].FormDeltas, want) {
				t.Fatalf("cold page lost the birth marker: %+v", page.Parts)
			}
		})
	}
}
