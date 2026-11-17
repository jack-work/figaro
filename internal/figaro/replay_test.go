package figaro

import (
	"reflect"
	"testing"

	"github.com/jack-work/figaro/api/message"
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
