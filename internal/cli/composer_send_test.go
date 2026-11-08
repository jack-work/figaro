package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
)

func composerRig(t *testing.T, qua func(context.Context, string) (*rpc.QuaResponse, error)) *interactiveInput {
	t.Helper()
	client := aria.NewClient()
	client.Apply(aria.Page{}, aria.Notify)
	tr := newTranscript(ldrender.NewFakeTerminal(100, 30), 100, 30, ldrender.NodeText{}, client, "aria1234", time.Unix(0, 0))
	in := &interactiveInput{mu: new(renderLock), lt: &livelogTurn{tr: tr}, draftQua: qua}
	tr.sendComposer = in.sendComposer
	in.mu.Lock()
	tr.enter()
	in.mu.Unlock()
	return in
}

func (in *interactiveInput) barSays(t *testing.T, why, want string) {
	t.Helper()
	in.eventually(t, why, func(tr *transcript, _ *loginSession) bool {
		return strings.Contains(tr.status.noticeText(), want)
	})
}

func TestAStalledComposerSendSaysItIsSendingThenHowItEnded(t *testing.T) {
	release := make(chan error)
	in := composerRig(t, func(ctx context.Context, text string) (*rpc.QuaResponse, error) {
		if text != "hello there" {
			t.Errorf("sent %q", text)
		}
		return nil, <-release
	})
	in.keys(">hello there\x13")
	in.barSays(t, "a stalled send shows it is under way", "…sending")
	in.mu.Lock()
	if in.lt.tr.box != boxNone {
		t.Errorf("the composer is still open (box %d)", in.lt.tr.box)
	}
	in.mu.Unlock()
	release <- context.DeadlineExceeded
	in.barSays(t, "the stall ends in its error", "send: context deadline exceeded")
}

func TestAQuickComposerSendShowsOnlySent(t *testing.T) {
	in := composerRig(t, func(context.Context, string) (*rpc.QuaResponse, error) {
		return &rpc.QuaResponse{OK: true}, nil
	})
	in.keys(">hi\x13")
	in.barSays(t, "a quick send says sent", "sent")
	time.Sleep(2 * progressDelay)
	in.mu.Lock()
	defer in.mu.Unlock()
	if got := in.lt.tr.status.noticeText(); got != "sent" {
		t.Fatalf("bar %q after a quick send, want sent", got)
	}
}
