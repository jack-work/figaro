package figaro

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
)

func TestReplayEventKeepsWhoAskedWhat(t *testing.T) {
	fromFigaro := message.SenderText("figaro lukewarm-utmost (947bf)", "lane done")
	fromFigaro.Principal = "aria:947bf"
	fromDuke := message.SenderText("Gluck", "and check the tests")
	img := message.ImageContent("image/png", "AAAA")
	inquiry := message.Message{Role: message.RoleInput, Content: []message.Content{img, fromFigaro, fromDuke}}

	evt := replayEvent(inquiry)
	if !evt.replay {
		t.Fatal("not marked as a replay")
	}
	if got := senderRuns(evt.segments); !reflect.DeepEqual(got, []message.Content{fromFigaro, fromDuke}) {
		t.Fatalf("senders lost: %+v", got)
	}
	if !reflect.DeepEqual(evt.images, []message.Content{img}) {
		t.Fatalf("images lost: %+v", evt.images)
	}
	if evt.text != "lane done\n\nand check the tests" {
		t.Fatalf("text = %q", evt.text)
	}
}

func TestTheOnAppendViewOfAReplayIsMasked(t *testing.T) {
	be, err := store.NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()
	parent, _, err := be.ForkWith("", 0, form.Build(form.Snapshot{}, map[string]json.RawMessage{"aria_id": json.RawMessage(`"seen"`)}, nil))
	if err != nil {
		t.Fatal(err)
	}
	rec := form.Replay{Of: parent, Turn: 1, LT: 2, AriaID: "seen"}
	birth := form.Merge(form.Build(form.Snapshot{}, map[string]json.RawMessage{form.ForkedFromKey: json.RawMessage(`"` + parent + `"`)}, nil), rec.Patch())
	branch, _, err := be.ForkWith(parent, 0, birth, store.Stamped(func(id string) form.Patch {
		return form.Build(form.Snapshot{}, map[string]json.RawMessage{"aria_id": json.RawMessage(`"` + id + `"`)}, nil)
	}))
	if err != nil {
		t.Fatal(err)
	}
	head, err := be.FormVersion(branch)
	if err != nil {
		t.Fatal(err)
	}
	view := BoundFormAccessorFor(be, branch)
	ps := view.PatchesBetween(0, head)
	if got := form.Fold(form.Snapshot{}, ps); *got.Lookup("aria_id") != "seen" {
		t.Fatalf("the provider reads aria_id %q", *got.Lookup("aria_id"))
	}
	for _, p := range ps {
		if _, forked := p.Entry(form.ForkedFromKey); forked {
			t.Fatal("the provider reads the fork mark")
		}
	}
	snap, err := be.FormState(branch)
	if err != nil {
		t.Fatal(err)
	}
	if *snap.Lookup("aria_id") != branch {
		t.Fatal("the store lost the branch's own id")
	}
}
