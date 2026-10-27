package angelus_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	api "github.com/jack-work/figaro/api/sandbox"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/sandbox/box"
)

type servedBox struct {
	sock, ws string
}

func serveBox(t *testing.T) servedBox {
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
