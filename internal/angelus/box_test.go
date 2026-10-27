package angelus_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	api "github.com/jack-work/figaro/api/sandbox"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/sandbox/box"
	"github.com/jack-work/jkrpc"
)

type servedBox struct {
	sock, ws string
}

func serveBox(t *testing.T) servedBox { return serveBoxDoor(t, false) }

func serveBoxDoor(t *testing.T, door bool) servedBox {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "abox")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	ws := filepath.Join(dir, "ws")
	if err := os.Mkdir(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "exec.sock")
	srv, err := box.NewServer(ws)
	if err != nil {
		t.Fatal(err)
	}
	if door {
		if err := srv.CallBackThrough(filepath.Join(dir, "door.sock")); err != nil {
			t.Fatal(err)
		}
	}
	ln, err := box.Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { srv.Serve(ctx, ln); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return servedBox{sock: sock, ws: ws}
}

type bashProvider struct {
	command string
	turns   chan string
}

func (*bashProvider) Name() string        { return "mock" }
func (*bashProvider) Fingerprint() string { return "bash/v1" }
func (*bashProvider) SetModel(string)     {}
func (*bashProvider) Models(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}

func (p *bashProvider) Send(_ context.Context, in provider.SendInput, bus provider.Bus) error {
	last := ""
	for _, e := range in.FigLog.Read() {
		for _, c := range e.Payload.Content {
			if c.Type == message.ContentToolResult {
				last += c.Text
			}
		}
	}
	var msg message.Message
	if last == "" {
		call := message.Content{Type: message.ContentToolInvoke, ToolCallID: "c1", ToolName: "bash",
			Arguments: map[string]interface{}{"command": p.command}}
		bus.PushToolInvokeStart(call.ToolCallID, call.ToolName)
		bus.PushToolReady(call)
		msg = message.Message{Role: message.RoleOutput, Content: []message.Content{call}, StopReason: message.StopToolInvoke}
	} else {
		msg = message.Message{Role: message.RoleOutput, Content: []message.Content{message.TextContent("done")}, StopReason: message.StopEnd}
		defer func() { p.turns <- last }()
	}
	bus.PushMessageEnd(string(msg.StopReason))
	bus.PushFigaro(msg)
	return nil
}

func boxRig(t *testing.T, sb servedBox, command string) (grantRig, *bashProvider) {
	t.Helper()
	p := &bashProvider{command: command, turns: make(chan string, 4)}
	r := newRigProviding(t, grantPolicy, fmt.Sprintf("[sandbox.boxes.judge]\nexec = %q\n", sb.sock),
		func(string, provider.Knobs) (provider.Provider, error) { return p, nil })
	return r, p
}

func asOwner() context.Context { return as("user", strconv.Itoa(os.Getuid())) }

func TestAnAriaInABoxRunsBashThroughTheServedBox(t *testing.T) {
	sb := serveBox(t)
	r, p := boxRig(t, sb, `echo "$FIGARO_ARIA $(pwd)" > made-here; cat made-here`)
	resp, err := r.create(asOwner(), "judged")
	if err != nil {
		t.Fatal(err)
	}
	snap, err := r.backend.FormState(resp.FigaroID)
	if err != nil {
		t.Fatal(err)
	}
	if v := snap.Lookup("system.cwd"); v != nil {
		t.Fatalf("system.cwd %q: a host path means nothing in a box", *v)
	}
	if _, err := r.h[rpc.MethodQua](asOwner(), withAria(t, resp.FigaroID, rpc.QuaRequest{Text: "go"})); err != nil {
		t.Fatal(err)
	}
	select {
	case out := <-p.turns:
		want := resp.FigaroID + " " + sb.ws
		if !strings.Contains(out, want) {
			t.Fatalf("tool result %q, want %q", out, want)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the turn did not finish")
	}
	data, err := os.ReadFile(filepath.Join(sb.ws, "made-here"))
	if err != nil || !strings.HasPrefix(string(data), resp.FigaroID+" ") {
		t.Fatalf("the command did not run in the box's workspace: %q %v", data, err)
	}
}

func TestKillingABoxedAriaKillsItsSessionsInTheBox(t *testing.T) {
	sb := serveBox(t)
	r, _ := boxRig(t, sb, "true")
	resp, err := r.create(asOwner(), "judged")
	if err != nil {
		t.Fatal(err)
	}
	b, err := box.Provider{Name: "judge", Socket: sb.sock, Scope: resp.FigaroID}.Open(context.Background(), api.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	sess, err := b.Sessions().Start(api.ExecRequest{Command: "sleep 300"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.call(asOwner(), rpc.MethodKill, rpc.KillRequest{FigaroID: resp.FigaroID}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sess.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the aria's session in the box outlived the kill")
	}
}

func TestABoxThatIsDownFailsTheBirthAndLeavesNothing(t *testing.T) {
	r, _ := boxRig(t, servedBox{sock: filepath.Join(t.TempDir(), "nobody")}, "true")
	_, err := r.create(asOwner(), "judged")
	if err == nil || !strings.Contains(err.Error(), "box judge") {
		t.Fatalf("err %v", err)
	}
	if n := len(r.backend.ConversationIDs()); n != 0 {
		t.Fatalf("a failed birth left %d aria(s) behind", n)
	}
}

type doorRig struct {
	grantRig
	sb   servedBox
	door string
	p    *bashProvider
}

func boxDoorRig(t *testing.T, command string) doorRig {
	t.Helper()
	return boxDoorRigWith(t, defaultPolicy, command)
}

func boxDoorRigWith(t *testing.T, policy, command string) doorRig {
	t.Helper()
	sb := serveBoxDoor(t, true)
	door := filepath.Join(filepath.Dir(sb.sock), "door.sock")
	p := &bashProvider{command: command, turns: make(chan string, 4)}
	r := newRigProviding(t, policy, fmt.Sprintf("[sandbox.boxes.judge]\nexec = %q\ndoor = %q\n", sb.sock, door),
		func(string, provider.Knobs) (provider.Provider, error) { return p, nil })
	ln, err := angelus.ListenUnix(door, 0o660)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.a.Serve(ctx, ln, authz.DoorBox, authz.BoxAdmit("judge")); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return doorRig{grantRig: r, sb: sb, door: door, p: p}
}

func (d doorRig) dial(t *testing.T) *jkrpc.Client {
	t.Helper()
	nc, err := net.Dial("unix", d.door)
	if err != nil {
		t.Fatal(err)
	}
	c := jkrpc.NewClient(jkrpc.NewConn(nc), nil)
	t.Cleanup(func() { c.Close() })
	return c
}

func boxOnAria(t *testing.T, c *jkrpc.Client, method, id string, req any, out any) error {
	t.Helper()
	return c.Call(context.Background(), method, withAria(t, id, req), out)
}

func setKey(t *testing.T, c *jkrpc.Client, id, key, val string) error {
	t.Helper()
	v, _ := json.Marshal(val)
	return boxOnAria(t, c, rpc.MethodSet, id, rpc.SetRequest{Patch: form.Build(form.Snapshot{}, map[string]json.RawMessage{key: v}, nil)}, nil)
}

func TestTheBoxDoorReachesOnlyAriasInItsBox(t *testing.T) {
	d := boxDoorRig(t, `echo "$FIGARO_TROUPE"`)
	inBox, err := d.create(asOwner(), "judged")
	if err != nil {
		t.Fatal(err)
	}
	outside, err := d.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := d.h[rpc.MethodQua](asOwner(), withAria(t, inBox.FigaroID, rpc.QuaRequest{Text: "go"})); err != nil {
		t.Fatal(err)
	}
	select {
	case out := <-d.p.turns:
		if !strings.Contains(out, "unix://"+d.door) {
			t.Fatalf("bash in the box sees FIGARO_TROUPE %q, want the door", out)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the turn did not finish")
	}

	c := d.dial(t)
	var hello rpc.HelloResponse
	if err := c.Call(context.Background(), rpc.MethodHello, rpc.HelloRequest{Version: rpc.ProtocolVersion}, &hello); err != nil {
		t.Fatal(err)
	}
	if got := authz.PrincipalText(hello.Principal); got != "sandbox:judge@local:box" {
		t.Fatalf("the door stamped %q", got)
	}

	if err := boxOnAria(t, c, rpc.MethodContext, inBox.FigaroID, struct{}{}, nil); err != nil {
		t.Fatalf("reading an aria in its own box: %v", err)
	}
	if err := setKey(t, c, inBox.FigaroID, "mantra", "boxed"); err != nil {
		t.Fatalf("setting an aria in its own box: %v", err)
	}
	if err := boxOnAria(t, c, rpc.MethodContext, outside.FigaroID, struct{}{}, nil); err == nil {
		t.Fatal("the box read an aria outside it")
	}
	if err := setKey(t, c, outside.FigaroID, "mantra", "reached"); err == nil {
		t.Fatal("the box set an aria outside it")
	}
	if err := c.Call(context.Background(), rpc.MethodKill, rpc.KillRequest{FigaroID: outside.FigaroID}, nil); err == nil {
		t.Fatal("the box killed an aria outside it")
	}
	if err := boxOnAria(t, c, rpc.MethodQua, outside.FigaroID, rpc.QuaRequest{Text: "hi"}, nil); err == nil {
		t.Fatal("the box prompted an aria outside it")
	}

	if err := setKey(t, c, inBox.FigaroID, "system.sandbox", "local"); err == nil {
		t.Fatal("the box moved its aria to sandbox:local")
	}
	if got := d.sandboxOf(t, inBox.FigaroID); got != "judge" {
		t.Fatalf("a refused move landed: sandbox %q", got)
	}

	var made rpc.CreateResponse
	if err := c.Call(context.Background(), rpc.MethodCreate, rpc.CreateRequest{Outfits: []string{"mock"}}, &made); err != nil {
		t.Fatalf("the box creating an aria: %v", err)
	}
	if got := d.sandboxOf(t, made.FigaroID); got != "judge" {
		t.Fatalf("an aria the box minted runs in %q, want judge", got)
	}
	if m, _ := d.backend.Meta(made.FigaroID); m == nil || m.MintedBy != "sandbox:judge@local:box" {
		t.Fatalf("minted_by %+v", m)
	}
	var forked rpc.ForkResponse
	if err := c.Call(context.Background(), rpc.MethodFork, rpc.ForkRequest{FigaroID: made.FigaroID}, &forked); err != nil {
		t.Fatalf("the box forking its own aria: %v", err)
	}
	if err := c.Call(context.Background(), rpc.MethodFork, rpc.ForkRequest{FigaroID: outside.FigaroID}, nil); err == nil {
		t.Fatal("the box forked an aria outside it")
	}
	ownersForm, err := d.call(asOwner(), rpc.MethodFormCreate, rpc.FormCreateRequest{Patch: mantraPatch("the owner's")})
	if err != nil {
		t.Fatal(err)
	}
	err = c.Call(context.Background(), rpc.MethodFormCreate, rpc.FormCreateRequest{Parent: ownersForm.(rpc.FormCreateResponse).FormID, Patch: mantraPatch("copy")}, nil)
	if err == nil || !strings.Contains(err.Error(), "reads it") {
		t.Fatalf("the box copied a form it cannot read: %v", err)
	}
	if err := c.Call(context.Background(), rpc.MethodGC, struct{}{}, nil); err == nil {
		t.Fatal("the box ran an admin method")
	}

	var list rpc.ListResponse
	if err := c.Call(context.Background(), rpc.MethodList, rpc.ListRequest{}, &list); err != nil {
		t.Fatal(err)
	}
	for _, row := range list.Figaros {
		if row.ID == outside.FigaroID {
			t.Fatal("list showed the box an aria outside it")
		}
	}
}

func mantraPatch(v string) *form.Patch {
	raw, _ := json.Marshal(v)
	p := form.Build(form.Snapshot{}, map[string]json.RawMessage{"mantra": raw}, nil)
	return &p
}

func TestABoxThePolicyDoesNotBindIsRefusedAtTheDoor(t *testing.T) {
	d := boxDoorRigWith(t, grantPolicy, "true")
	c := d.dial(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Call(ctx, rpc.MethodHello, rpc.HelloRequest{Version: rpc.ProtocolVersion}, nil); err == nil {
		t.Fatal("a box no policy binds was seated")
	}
}
