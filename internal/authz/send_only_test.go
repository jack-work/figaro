package authz

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/jkauthz"
	"github.com/jack-work/jkrpc"
)

const sendOnlyPolicy = `
[roles.owner]
grants = ["*:*"]

[roles.sender]
grants = ["send:aria:aaaa0002"]

[roles.reader]
grants = ["read:aria:aaaa0002"]

[[bind]]
role = "owner"
principals = ["user:1000"]

[[bind]]
role = "sender"
principals = ["sandbox:sender"]

[[bind]]
role = "reader"
principals = ["sandbox:reader"]

[roles.stranger]
grants = ["read:aria:bbbb0009", "send:aria:bbbb0009"]

[[bind]]
role = "stranger"
principals = ["sandbox:stranger"]
`

var (
	sender   = jkauthz.Principal{Kind: jkauthz.KindSandbox, ID: "sender"}
	reader   = jkauthz.Principal{Kind: jkauthz.KindSandbox, ID: "reader"}
	stranger = jkauthz.Principal{Kind: jkauthz.KindSandbox, ID: "stranger"}
)

func sendOnlyGuard(t *testing.T) map[string]jkrpc.HandlerFunc {
	t.Helper()
	ok := func(context.Context, json.RawMessage) (any, error) { return "ok", nil }
	return Guard(map[string]jkrpc.HandlerFunc{
		rpc.MethodAttach: ok,
		rpc.MethodQua:    ok,
		rpc.MethodRead:   ok,
	}, AriaHeader{}, Enforce(loaded(t, sendOnlyPolicy)))
}

func as(p jkauthz.Principal) context.Context {
	return WithSubject(context.Background(), subject(p))
}

// #68: send attaches before it sends, so a send grant must reach attach or
// it is unusable.
func TestASendGrantAloneAttachesAndSends(t *testing.T) {
	h := sendOnlyGuard(t)
	for _, m := range []string{rpc.MethodAttach, rpc.MethodQua} {
		if err := call(t, h, as(sender), m, "aaaa0002"); err != nil {
			t.Fatalf("send-only %s: %v", m, err)
		}
	}
	if err := call(t, h, as(sender), rpc.MethodRead, "aaaa0002"); err == nil {
		t.Fatal("a send grant read the transcript")
	}
}

func TestAttachWithNeitherReadNorSendIsNotFound(t *testing.T) {
	h := sendOnlyGuard(t)
	hidden := call(t, h, as(stranger), rpc.MethodAttach, "aaaa0002")
	absent := call(t, h, as(stranger), rpc.MethodAttach, "ffff0009")
	if hidden == nil || absent == nil {
		t.Fatalf("attach with no grant answered %v and %v", hidden, absent)
	}
	if strings.ReplaceAll(absent.Error(), "ffff0009", "aaaa0002") != hidden.Error() {
		t.Fatalf("an aria with no grant tells itself from a missing one:\n  hidden: %v\n  absent: %v", hidden, absent)
	}
	if err := call(t, h, as(sender), rpc.MethodAttach, "aaaa0003"); err == nil || err.Error() != NotFound(rpc.MethodAttach, "aaaa0003").Error() {
		t.Fatalf("a send grant on one aria reached another: %v", err)
	}
}

func TestAReadGrantAloneAttachesButCannotSend(t *testing.T) {
	h := sendOnlyGuard(t)
	if err := call(t, h, as(reader), rpc.MethodAttach, "aaaa0002"); err != nil {
		t.Fatalf("read-only attach: %v", err)
	}
	err := call(t, h, as(reader), rpc.MethodQua, "aaaa0002")
	if err == nil {
		t.Fatal("a read grant sent")
	}
	if err.Error() == NotFound(rpc.MethodQua, "aaaa0002").Error() {
		t.Fatalf("a reader was told the aria it reads does not exist: %v", err)
	}
}
