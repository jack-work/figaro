package cli

import (
	"io"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
)

func studyKeysRig(t *testing.T) (*transcript, *[]int) {
	t.Helper()
	tr := newTranscript(io.Discard, 100, 30, ldrender.NodeText{}, aria.NewClient(), "aria1234", time.Unix(0, 0))
	tr.enter()
	var asked []int
	tr.openStudies = func(tab int) { asked = append(asked, tab) }
	return tr, &asked
}

func withFormPit(tr *transcript) {
	v := &formView{src: reapedShaped(), open: map[string]bool{}}
	tr.showLivePit("form show", v, false)
	tr.pit.lines(100, 20)
}

func TestCapitalROpensTheStudiesPitOnTheTabShownLast(t *testing.T) {
	tr, asked := studyKeysRig(t)
	tr.key('R')
	tr.pitFailed()
	withFormPit(tr)
	tr.key('R')
	if len(*asked) != 2 || (*asked)[0] != -1 || (*asked)[1] != -1 {
		t.Fatalf("asked %v from the transcript and from a pit", *asked)
	}
}

func TestCapitalRInTheStudiesPitClosesIt(t *testing.T) {
	tr, asked := studyKeysRig(t)
	v := &formView{src: reapedShaped(), open: map[string]bool{}}
	tr.showLivePit(studiesPitName, v, false)
	tr.pit.lines(100, 20)
	tr.key('R')
	if tr.showing(pitID(studiesPitName)) || len(*asked) != 0 {
		t.Fatalf("showing %v asked %v", tr.showing(pitID(studiesPitName)), *asked)
	}
}

func TestRThenADigitOpensThatTabFromTheTranscriptAndFromAPit(t *testing.T) {
	tr, asked := studyKeysRig(t)
	for i, d := range []byte("1234567890") {
		tr.key('r')
		tr.key(d)
		tr.pitFailed()
		if want := i; len(*asked) != i+1 || (*asked)[i] != want {
			t.Fatalf("r%c: asked %v", d, *asked)
		}
	}
	*asked = nil
	withFormPit(tr)
	tr.key('r')
	tr.key('2')
	tr.pitFailed()
	tr.key('r')
	tr.key('0')
	if len(*asked) != 2 || (*asked)[0] != 1 || (*asked)[1] != 9 {
		t.Fatalf("from a pit: asked %v", *asked)
	}
}

func TestRNotFollowedByADigitOpensNothing(t *testing.T) {
	tr, asked := studyKeysRig(t)
	tr.key('r')
	tr.key('x')
	tr.key('r')
	tr.key('j')
	if len(*asked) != 0 || tr.pending != "" {
		t.Fatalf("asked %v pending %q", *asked, tr.pending)
	}
}

func TestInAPitWhoseViewBindsRThatActionWinsOverTheStudiesPrefix(t *testing.T) {
	tr, asked := studyKeysRig(t)
	v := &formView{src: reapedShaped(), open: map[string]bool{}}
	v.setView(actingView)
	var ran []string
	v.act = func(a rpc.ViewAction) { ran = append(ran, a.Method) }
	tr.showLivePit("reaped", v, false)
	tr.pit.lines(100, 20)
	tr.key('r')
	if len(ran) != 1 || ran[0] != rpc.MethodReapedRestore || tr.pending != "" {
		t.Fatalf("restore ran %v, pending %q", ran, tr.pending)
	}
	tr.key('1')
	if len(*asked) != 0 {
		t.Fatalf("the digit after a restore opened a study: %v", *asked)
	}
}
