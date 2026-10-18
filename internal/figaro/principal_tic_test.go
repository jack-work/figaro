package figaro_test

import (
	"context"
	"testing"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
)

// The sender label is what a caller asserted; the principal is what the
// door proved. A transcript keeps both, so it says who spoke as well as who
// claimed to.
func TestAUserTicCarriesTheStampedPrincipalBesideTheLabel(t *testing.T) {
	a, _ := newQuoteAgent(t)
	ch, unsub := subscribeChan(a)
	defer unsub()

	ctx := authz.WithPrincipal(context.Background(), rpc.Principal{Kind: "sandbox", ID: "dev-3"})
	if err := a.SubmitPromptFrom(ctx, rpc.QuaRequest{Text: "hello"}, "aria 1234abcd"); err != nil {
		t.Fatal(err)
	}
	waitTurnDone(t, ch)

	var found *message.Content
	for _, m := range a.Context() {
		if m.Role != message.RoleInput {
			continue
		}
		for i := range m.Content {
			if m.Content[i].Text == "hello" {
				found = &m.Content[i]
			}
		}
	}
	if found == nil {
		t.Fatal("the prompt is not in the log")
	}
	if found.Principal != "sandbox:dev-3" || found.Sender != "aria 1234abcd" {
		t.Fatalf("principal %q sender %q", found.Principal, found.Sender)
	}
}
