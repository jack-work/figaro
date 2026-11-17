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

// Gluck, 11-17: a prompt composed behind a running turn opened the queue with
// the keyboard, so the queue most readers ever see still took j/k from the turn
// they were following. It opens like any queue that appears on its own, and a
// send still revokes an earlier dismissal.
func TestAComposerSendThatQueuesOpensTheQueueWithoutTheKeyboard(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*transcript)
	}{
		{"dismissed", func(tr *transcript) { tr.queueDismissed = true }},
		{"receded", func(tr *transcript) { tr.showQueuedAuto(true) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := composerRig(t, func(context.Context, string) (*rpc.QuaResponse, error) {
				return &rpc.QuaResponse{OK: true, Active: true}, nil
			})
			in.mu.Lock()
			in.lt.tr.queued = []queuedItem{{id: 1, text: "already queued"}}
			tc.setup(in.lt.tr)
			in.mu.Unlock()
			in.keys(">and one more\x13")
			in.barSays(t, "the send lands", "sent")
			in.mu.Lock()
			defer in.mu.Unlock()
			tr := in.lt.tr
			if !tr.showing(pitQueue) {
				t.Fatalf("the queue is not showing after a send that queued (open %q)", tr.openPit())
			}
			if tr.focused != focusTranscript {
				t.Fatalf("a send that queued left focus %v, want the transcript", tr.focused)
			}
			if tr.queueDismissed {
				t.Fatal("the send did not revoke the dismissal")
			}
		})
	}
}
