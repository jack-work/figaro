package angelus_test

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/provider"
)

// El Inspector XII on e04be645: the sender id on a prompt is attribution,
// never verified, and a box principal is local. A box that names an owner
// figaro as the sender of a prompt that fails must not get that figaro woken
// and prompted; the notice is checked as a send by the prompt's own
// principal.

type alwaysRateLimited struct{ mockProviderForIntegration }

func (*alwaysRateLimited) Send(context.Context, provider.SendInput, provider.Bus) error {
	return &provider.RateLimited{Provider: "anthropic", Window: "7-day"}
}

type doneSink chan struct{}

func (d doneSink) Notify(method string, _ any) error {
	if method == rpc.MethodTurnDone {
		select {
		case d <- struct{}{}:
		default:
		}
	}
	return nil
}

func failAs(t *testing.T, r grantRig, failing, namedSender string, caller rpc.Principal) {
	t.Helper()
	agent, ok := r.a.Registry.Get(failing).(*figaro.Agent)
	if !ok {
		t.Fatal("fixture: the failing aria is not live")
	}
	done := make(doneSink, 1)
	defer agent.Subscribe(done)()
	ctx := authz.WithPrincipal(context.Background(), caller)
	if err := agent.SubmitPromptFrom(ctx, rpc.QuaRequest{Text: "fail for me"}, namedSender); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the failing turn never ended")
	}
}

func noticed(r grantRig, id string) bool {
	ir, err := r.backend.OpenFigIR(id)
	if err != nil {
		return false
	}
	page, _ := ir.ReadPage(0, 1<<62, 64)
	for _, e := range page {
		m := e.Payload
		if m.Role != message.RoleInput {
			continue
		}
		for _, c := range m.Content {
			if strings.Contains(c.Text, "failed (rate_limited)") {
				return true
			}
		}
	}
	return false
}

func noticeRig(t *testing.T) (grantRig, string, string) {
	t.Helper()
	r := newRigProviding(t, grantPolicy, "", func(string, provider.Knobs) (provider.Provider, error) {
		return &alwaysRateLimited{}, nil
	}, func(c *angelus.ServerConfig) { c.FormTemplates = template.New("t") })
	owner := as("user", strconv.Itoa(os.Getuid()))
	target, err := r.create(owner, "mock")
	if err != nil {
		t.Fatal(err)
	}
	failing, err := r.create(owner, "mock")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.a.Registry.Hibernate(target.FigaroID); err != nil {
		t.Fatal(err)
	}
	if r.a.Registry.Get(target.FigaroID) != nil {
		t.Fatal("fixture: the named sender is still awake")
	}
	return r, failing.FigaroID, target.FigaroID
}

func TestABoxCannotHaveAFailedTurnPromptAnOwnerFigaro(t *testing.T) {
	r, failing, target := noticeRig(t)
	failAs(t, r, failing, target, rpc.Principal{Kind: rpc.PrincipalSandbox, ID: "dev-3"})
	time.Sleep(300 * time.Millisecond)
	if r.a.Registry.Get(target) != nil {
		t.Fatal("a box's failed turn woke the owner figaro it named as its sender")
	}
	if noticed(r, target) {
		t.Fatal("a box's failed turn prompted the owner figaro it named as its sender")
	}
}

func TestTheOwnersFailedTurnTellsTheFigaroThatSentIt(t *testing.T) {
	r, failing, target := noticeRig(t)
	failAs(t, r, failing, target, rpc.Principal{Kind: rpc.PrincipalUser, ID: strconv.Itoa(os.Getuid())})
	deadline := time.Now().Add(5 * time.Second)
	for !noticed(r, target) {
		if time.Now().After(deadline) {
			t.Fatal("the figaro that sent the failed turn's message was never told")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
