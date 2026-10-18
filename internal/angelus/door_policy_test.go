package angelus_test

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

const doorPolicy = `
[roles.owner]
grants = [{ verbs = ["*"], on = ["*"] }]

[roles.guest]
grants = [
  { verbs = ["read", "listen", "send", "set"], on = ["owner:self"] },
  { verbs = ["create"], on = ["*"] },
  { verbs = ["use"], on = ["sandbox:local"] },
]

[[bind]]
role = "owner"
principals = ["user:SELF"]

[[bind]]
role = "guest"
principals = ["user:guest"]
`

func (r grantRig) call(ctx context.Context, method string, req any) (any, error) {
	raw, _ := json.Marshal(req)
	return r.h[method](ctx, raw)
}

func withAria(t *testing.T, id string, req any) json.RawMessage {
	t.Helper()
	raw, _ := json.Marshal(req)
	out, err := rpc.WithAria(raw, id)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestTheDoorDecidesEveryCallOnTheCallersRoles(t *testing.T) {
	r := newRigWith(t, doorPolicy)
	owner := as("user", strconv.Itoa(os.Getuid()))
	guest := as("user", "guest")

	mine, err := r.create(guest, "mock")
	if err != nil {
		t.Fatalf("guest create: %v", err)
	}
	theirs, err := r.create(owner, "mock")
	if err != nil {
		t.Fatal(err)
	}

	out, err := r.call(guest, rpc.MethodList, rpc.ListRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range out.(rpc.ListResponse).Figaros {
		if row.ID == theirs.FigaroID {
			t.Fatal("list showed the guest an aria it cannot read")
		}
	}

	read := func(ctx context.Context, id string) error {
		_, err := r.h[rpc.MethodContext](ctx, withAria(t, id, struct{}{}))
		return err
	}
	if err := read(guest, mine.FigaroID); err != nil {
		t.Fatalf("guest reading its own aria: %v", err)
	}
	if err := read(guest, theirs.FigaroID); err == nil {
		t.Fatal("guest read the owner's aria")
	}
	if _, err := r.h[rpc.MethodFollow](guest, withAria(t, theirs.FigaroID, struct{}{})); err == nil {
		t.Fatal("guest followed the owner's aria")
	}
	if _, err := r.call(guest, rpc.MethodKill, rpc.KillRequest{FigaroID: mine.FigaroID}); err == nil {
		t.Fatal("guest killed without a kill grant")
	}
	if _, err := r.call(guest, rpc.MethodGC, struct{}{}); err == nil {
		t.Fatal("guest ran an admin method")
	}
	if _, err := r.call(guest, rpc.MethodHello, rpc.HelloRequest{Version: rpc.ProtocolVersion}); err != nil {
		t.Fatalf("hello: %v", err)
	}

	set := func(ctx context.Context, id, key, val string) error {
		v, _ := json.Marshal(val)
		_, err := r.h[rpc.MethodSet](ctx, withAria(t, id, rpc.SetRequest{Patch: form.Build(form.Snapshot{}, map[string]json.RawMessage{key: v}, nil)}))
		return err
	}
	if err := set(guest, mine.FigaroID, "mantra", "mine"); err != nil {
		t.Fatalf("guest setting its own mantra: %v", err)
	}
	if err := set(guest, mine.FigaroID, "system.sandbox", "bwrap"); err == nil {
		t.Fatal("guest moved its aria to a sandbox it holds no use on")
	}
	if got := r.sandboxOf(t, mine.FigaroID); got != "" {
		t.Fatalf("a refused set landed: sandbox %q", got)
	}
	if err := set(owner, theirs.FigaroID, "system.sandbox", "bwrap"); err != nil {
		t.Fatalf("the owner: %v", err)
	}
}

func TestARecursiveKillNeedsKillOnEveryAriaItTakes(t *testing.T) {
	r := newRigWith(t, doorPolicy+`
[roles.reaper]
grants = [{ verbs = ["kill", "fork", "read"], on = ["owner:self"] }]

[[bind]]
role = "reaper"
principals = ["user:guest"]
`)
	owner := as("user", strconv.Itoa(os.Getuid()))
	guest := as("user", "guest")
	root, err := r.create(guest, "mock")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.call(owner, rpc.MethodFork, rpc.ForkRequest{FigaroID: root.FigaroID}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.call(guest, rpc.MethodKill, rpc.KillRequest{FigaroID: root.FigaroID, Recursive: true}); err == nil {
		t.Fatal("kill -r took a branch the owner forked")
	}
	if n := len(r.backend.ConversationIDs()); n != 2 {
		t.Fatalf("a refused kill -r removed something: %d left", n)
	}
	if _, err := r.call(owner, rpc.MethodKill, rpc.KillRequest{FigaroID: root.FigaroID, Recursive: true}); err != nil {
		t.Fatalf("the owner: %v", err)
	}
}

// A bounded page counts the rows the caller may read: arias it cannot read
// neither fill a page nor appear on one.
func TestABoundedListPagesOnlyWhatTheCallerMayRead(t *testing.T) {
	r := newRigWith(t, doorPolicy)
	owner := as("user", strconv.Itoa(os.Getuid()))
	guest := as("user", "guest")

	mine := map[string]bool{}
	for range 2 {
		c, err := r.create(guest, "mock")
		if err != nil {
			t.Fatal(err)
		}
		mine[c.FigaroID] = true
	}
	for range 3 {
		if _, err := r.create(owner, "mock"); err != nil {
			t.Fatal(err)
		}
	}

	seen := map[string]bool{}
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 5 {
			t.Fatal("paging never ended")
		}
		out, err := r.call(guest, rpc.MethodList, rpc.ListRequest{Limit: 1, Before: cursor})
		if err != nil {
			t.Fatal(err)
		}
		resp := out.(rpc.ListResponse)
		if resp.NextBefore != "" && len(resp.Figaros) != 1 {
			t.Fatalf("page %d holds %d rows before the end", pages, len(resp.Figaros))
		}
		for _, row := range resp.Figaros {
			if !mine[row.ID] {
				t.Fatalf("the guest was shown %s", row.ID)
			}
			seen[row.ID] = true
		}
		if resp.NextBefore == "" {
			break
		}
		cursor = resp.NextBefore
	}
	if len(seen) != len(mine) {
		t.Fatalf("the guest saw %d of its %d arias", len(seen), len(mine))
	}
}
