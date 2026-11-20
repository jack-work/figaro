package angelus_test

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"text/template"
	"time"

	"github.com/jack-work/figaro/api/aria"
	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/provider"
)

func logRig(t *testing.T, policy string, factory func(string, provider.Knobs) (provider.Provider, error)) grantRig {
	t.Helper()
	if factory == nil {
		factory = func(string, provider.Knobs) (provider.Provider, error) { return &mockProviderForIntegration{}, nil }
	}
	return newRigProviding(t, policy, "", factory, func(c *angelus.ServerConfig) { c.FormTemplates = template.New("t") })
}

func logBirth() *form.Patch {
	p := form.Build(form.Snapshot{}, map[string]json.RawMessage{form.KindKey: json.RawMessage(`"log"`)}, nil)
	return &p
}

func mintLog(t *testing.T, r grantRig, ctx context.Context) string {
	t.Helper()
	out, err := r.call(ctx, rpc.MethodFormBind, rpc.FormBindRequest{Patch: logBirth()})
	if err != nil {
		t.Fatalf("form.bind of a log: %v", err)
	}
	return out.(rpc.FormBindResponse).FigaroID
}

func appendTo(t *testing.T, r grantRig, ctx context.Context, id, text string) error {
	t.Helper()
	_, err := r.h[rpc.MethodQua](ctx, withAria(t, id, rpc.QuaRequest{Text: text}))
	return err
}

func readLog(t *testing.T, r grantRig, ctx context.Context, id string, req rpc.ReadRequest) (aria.Page, error) {
	t.Helper()
	out, err := r.h[rpc.MethodRead](ctx, withAria(t, id, req))
	if err != nil {
		return aria.Page{}, err
	}
	return out.(aria.Page), nil
}

func waitLogParts(t *testing.T, r grantRig, id string, n int) aria.Page {
	t.Helper()
	var page aria.Page
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		p, err := readLog(t, r, asOwner(), id, rpc.ReadRequest{})
		if err != nil {
			t.Fatal(err)
		}
		if page = p; len(p.Parts) >= n {
			return p
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("log %s holds %d entries, want %d", id, len(page.Parts), n)
	return page
}

func entriesInIR(t *testing.T, r grantRig, id string) []message.Message {
	t.Helper()
	out, err := r.call(asOwner(), rpc.MethodIR, rpc.IRRequest{FigaroID: id})
	if err != nil {
		t.Fatal(err)
	}
	var msgs []message.Message
	for _, e := range out.(rpc.IRResponse).Entries {
		var m message.Message
		if err := json.Unmarshal(e.Payload, &m); err != nil {
			t.Fatal(err)
		}
		if len(m.Content) > 0 {
			msgs = append(msgs, m)
		}
	}
	return msgs
}

func TestAnAriaLogIsBoundWithoutAProviderAndEachSendIsAnEntry(t *testing.T) {
	var built atomic.Int32
	r := logRig(t, defaultPolicy, func(string, provider.Knobs) (provider.Provider, error) {
		built.Add(1)
		return &mockProviderForIntegration{}, nil
	})
	id := mintLog(t, r, asOwner())
	for _, text := range []string{"first", "second", "third"} {
		if err := appendTo(t, r, asOwner(), id, text); err != nil {
			t.Fatalf("append %q: %v", text, err)
		}
	}
	page := waitLogParts(t, r, id, 3)
	for i, want := range []string{"first", "second", "third"} {
		if got := page.Parts[i].Inquiry; got != want {
			t.Fatalf("entry %d = %q, want %q", i, got, want)
		}
		if len(page.Parts[i].Nodes) != 0 {
			t.Fatalf("entry %d carries agent nodes %v: no model answered a log", i, page.Parts[i].Nodes)
		}
	}
	for _, m := range entriesInIR(t, r, id) {
		if m.Role == message.RoleOutput {
			t.Fatalf("a log holds model output: %+v", m)
		}
		if m.Timestamp == 0 || m.Content[0].Principal == "" {
			t.Fatalf("an entry carries its time and its sender: %+v", m)
		}
	}
	if n := built.Load(); n != 0 {
		t.Fatalf("waking a log built %d providers", n)
	}
	snap, err := r.backend.FormState(id)
	if err != nil {
		t.Fatal(err)
	}
	if !form.IsLog(snap) || snap.Lookup("system.provider") != nil {
		t.Fatalf("a log's form holds its kind and no provider: %v", snap)
	}
}

func TestAnAriaLogRefusesWhatOnlyAModelDoes(t *testing.T) {
	r := logRig(t, defaultPolicy, nil)
	id := mintLog(t, r, asOwner())
	if err := appendTo(t, r, asOwner(), id, "an entry"); err != nil {
		t.Fatal(err)
	}
	waitLogParts(t, r, id, 1)

	set := func(id, key string, val *string) error {
		var p form.Patch
		if val == nil {
			p = form.Build(form.Snapshot{}, nil, []string{key})
		} else {
			raw, _ := json.Marshal(*val)
			p = form.Build(form.Snapshot{}, map[string]json.RawMessage{key: raw}, nil)
		}
		_, err := r.h[rpc.MethodSet](asOwner(), withAria(t, id, rpc.SetRequest{Patch: p}))
		return err
	}
	str := func(s string) *string { return &s }
	refused := func(what string, err error, says string) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), says) {
			t.Fatalf("%s = %v, want a refusal saying %q", what, err, says)
		}
	}

	_, err := r.h[rpc.MethodInterrupt](asOwner(), withAria(t, id, struct{}{}))
	refused("interrupt on a log", err, "no turn to interrupt")
	refused("set system.model on a log", set(id, "system.model", str("m")), "no model")
	refused("set system.provider on a log", set(id, "system.provider", str("mock")), "no model")
	refused("unset system.kind on a log", set(id, form.KindKey, nil), "fixed at birth")
	refused("an empty send to a log", appendTo(t, r, asOwner(), id, ""), "needs text")
	if err := set(id, "mantra", str("the ledger")); err != nil {
		t.Fatalf("a log's own keys stay writable: %v", err)
	}

	model, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	refused("set system.kind on a figaro", set(model.FigaroID, form.KindKey, str("log")), "fixed at birth")
	_, err = r.call(asOwner(), rpc.MethodCreate, rpc.CreateRequest{Outfits: []string{"mock"}, Patch: logBirth(), Cwd: r.cwd})
	refused("figaro.create of a log", err, "has no model")
}

const logGrantsPolicy = `
[roles.owner]
permissions = ["*:*"]

[roles.appender]
permissions = ["send:aria:LOG"]

[roles.reader]
permissions = ["read:aria:LOG"]

[[bind]]
role = "owner"
principals = ["user:SELF"]

[[bind]]
role = "appender"
principals = ["user:clerk"]

[[bind]]
role = "reader"
principals = ["user:scholar"]
`

func TestAriaLogAppendAndReadFollowTheSendAndReadGrants(t *testing.T) {
	r := logRig(t, guardPolicy, nil)
	id := mintLog(t, r, asOwner())
	text := strings.ReplaceAll(strings.ReplaceAll(logGrantsPolicy, "SELF", strconv.Itoa(os.Getuid())), "LOG", id)
	if err := os.WriteFile(r.policy, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.a.Policies.Reload(r.policy); err != nil {
		t.Fatal(err)
	}
	clerk, scholar := as("user", "clerk"), as("user", "scholar")

	if err := appendTo(t, r, clerk, id, "by the clerk"); err != nil {
		t.Fatalf("send grant, append refused: %v", err)
	}
	if err := appendTo(t, r, scholar, id, "by the scholar"); err == nil {
		t.Fatal("a reader without send appended")
	}
	waitLogParts(t, r, id, 1)
	page, err := readLog(t, r, scholar, id, rpc.ReadRequest{})
	if err != nil {
		t.Fatalf("read grant, read refused: %v", err)
	}
	if len(page.Parts) != 1 || page.Parts[0].Inquiry != "by the clerk" {
		t.Fatalf("scholar read %+v, want the clerk's one entry", page.Parts)
	}
	if _, err := readLog(t, r, clerk, id, rpc.ReadRequest{}); err == nil {
		t.Fatal("an appender without read read the log")
	}
	msgs := entriesInIR(t, r, id)
	if len(msgs) != 1 || msgs[0].Content[0].Principal != "user:clerk" {
		t.Fatalf("the entry names its sender: %+v", msgs)
	}
}

func TestAnAriaLogReadsByRangeEitherWay(t *testing.T) {
	r := logRig(t, defaultPolicy, nil)
	id := mintLog(t, r, asOwner())
	for i := 1; i <= 5; i++ {
		if err := appendTo(t, r, asOwner(), id, "entry "+strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
	}
	whole := waitLogParts(t, r, id, 5)
	lo, hi := whole.Parts[1].ID, whole.Parts[3].ID
	inquiries := func(p aria.Page) []string {
		var out []string
		for _, part := range p.Parts {
			out = append(out, part.Inquiry)
		}
		return out
	}
	fwd, err := readLog(t, r, asOwner(), id, rpc.ReadRequest{At: aria.Anchor{Turn: lo}, Until: aria.Anchor{Turn: hi}})
	if err != nil {
		t.Fatal(err)
	}
	back, err := readLog(t, r, asOwner(), id, rpc.ReadRequest{At: aria.Anchor{Turn: hi}, Floor: aria.Anchor{Turn: lo}, Backward: true})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"entry 2", "entry 3"}
	if got := inquiries(fwd); strings.Join(got, ",") != strings.Join(want, ",") || !fwd.More.After {
		t.Fatalf("forward [%d,%d) = %v more=%+v, want %v and more after", lo, hi, got, fwd.More, want)
	}
	if got := inquiries(back); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("backward [%d,%d) = %v, want %v", lo, hi, got, want)
	}
}
