package chat

import (
	"testing"

	"github.com/jack-work/figaro/api/aria"
	"github.com/jack-work/figaro/api/livedoc"
)

func opening(turn uint64, prompt, sender, name string) aria.TurnPart {
	p := aria.TurnPart{Turn: aria.Turn{ID: turn, Inquiry: prompt}}
	if sender != "" {
		p.InquirySegments = []aria.InquirySegment{{Sender: sender, Name: name, Text: prompt}}
	}
	return p
}

func foldOf(parts ...aria.TurnPart) *turnFold {
	f := newTurnFold(parts[0].ID)
	for _, p := range parts {
		f.apply(p)
	}
	return f
}

func TestAHistoryTurnNamesItsSender(t *testing.T) {
	p := opening(4, "ping", "k1", "king")
	p.Nodes = []livedoc.Node{{Type: livedoc.NodeProse, Role: livedoc.RoleOutput, Markdown: "pong"}}
	p.Sealed = true
	turn := foldOf(p).turn()
	if turn.Sender == nil || turn.Sender.ID != "k1" || turn.Sender.Name != "king" {
		t.Fatalf("sender %+v", turn.Sender)
	}
	if got := foldOf(opening(5, "hi", "", "")).turn().Sender; got != nil {
		t.Fatalf("unattributed turn has sender %+v", got)
	}
}

func TestFollowContinuesAfterAFinishedHistoryTurnAtTheNextOne(t *testing.T) {
	done := sealedTurn(4, "pong")
	if got := foldOf(done).turn().Next; got != (Cursor{Turn: 5}) {
		t.Fatalf("next %+v", got)
	}
}

func TestFollowContinuesAMidTurnHistoryTurnWithoutAnnouncingItAgain(t *testing.T) {
	open := opening(4, "ping", "", "")
	open.Nodes = []livedoc.Node{{Type: livedoc.NodeProse, Role: livedoc.RoleOutput, Markdown: "po"}}
	open.Live = &aria.Live{From: 0, V: 0}
	turn := foldOf(open).turn()
	if turn.Next != (Cursor{Turn: 4, Text: 2, Begun: true}) {
		t.Fatalf("next %+v", turn.Next)
	}
	if turn.Cursor.Begun {
		t.Fatalf("cursor %+v", turn.Cursor)
	}
}
