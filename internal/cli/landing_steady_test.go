package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
)

func TestASignInAnswerNeverMovesALandingRow(t *testing.T) {
	ft := ldrender.NewFakeTerminal(120, 34)
	client := aria.NewClient()
	client.Apply(aria.Page{}, aria.Notify)
	tr := newTranscript(ft, 120, 34, ldrender.NodeText{}, client, "aria1234", time.Unix(0, 0))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, signedIn := range []bool{false, true} {
		in := &interactiveInput{mu: new(renderLock), lt: &livelogTurn{tr: tr}, life: ctx, login: &loginSession{backend: loginBackend{
			list: func() ([]loginProvider, string) {
				return []loginProvider{{name: "gateway", state: "not signed in", in: signedIn}}, ""
			},
		}}}
		screen := func() string {
			in.mu.Lock()
			defer in.mu.Unlock()
			tr.render()
			return strings.Join(ft.Screen(), "\n")
		}
		in.mu.Lock()
		tr.enter()
		in.mu.Unlock()
		first := screen()
		if !strings.Contains(first, ":login") {
			t.Fatalf("the first frame has no :login row:\n%s", first)
		}
		in.relistLogin()
		if now := screen(); now != first {
			t.Fatalf("signed in %v: the answer moved a row:\nfirst:\n%s\nafter:\n%s", signedIn, first, now)
		}
	}
}
