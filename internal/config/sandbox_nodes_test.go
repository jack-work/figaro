package config

import (
	"strings"
	"testing"
)

const nodeBody = `[sandbox.nodes.spain]
agent = "unix:///run/sandbox-agent/agent.sock"
workload = ["job", "conversation"]
`

func TestASandboxNodeLoadsAndBindsLeasedSandboxes(t *testing.T) {
	l, err := loadWith(t, nodeBody)
	if err != nil {
		t.Fatal(err)
	}
	n, ok := l.SandboxNode("spain")
	if !ok || !n.Hosts("job") || !n.Hosts("conversation") {
		t.Fatalf("node %+v %v", n, ok)
	}
	if l.BoundSandbox("shell0") {
		t.Fatal("a sandbox is bound before any lease")
	}
	if err := l.BindSandbox("shell0", "/run/sandbox-agent/exec/shell0.sock"); err != nil {
		t.Fatal(err)
	}
	b, ok := l.Box("shell0")
	if !ok || b.Exec != "/run/sandbox-agent/exec/shell0.sock" || b.Door != "" || b.PurgeGrace != "" {
		t.Fatalf("bound sandbox reads as %+v %v", b, ok)
	}
	fresh, err := l.Reload()
	if err != nil {
		t.Fatal(err)
	}
	if !fresh.BoundSandbox("shell0") {
		t.Fatal("a reload forgot a bound sandbox")
	}
	fresh.UnbindSandbox("shell0")
	if _, ok := l.Box("shell0"); ok {
		t.Fatal("an unbound sandbox still reads as a box")
	}
}

func TestABoundSandboxCannotTakeABoxsName(t *testing.T) {
	l, err := loadWith(t, nodeBody+`
[sandbox.boxes.box1]
exec = "/run/sandboxes/box1/exec/exec.sock"
`)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"box1", "local", "self"} {
		if err := l.BindSandbox(name, "/x.sock"); err == nil {
			t.Errorf("%s: bound", name)
		}
	}
	plain, err := loadWith(t, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := plain.BindSandbox("shell0", "/x.sock"); err == nil {
		t.Fatal("a daemon with no node bound a sandbox")
	}
}

func TestASandboxNodeOutOfShapeDoesNotLoad(t *testing.T) {
	for name, body := range map[string]string{
		"tcp agent":      "[sandbox.nodes.spain]\nagent = \"tcp://spain:1\"\nworkload = [\"job\"]\n",
		"relative agent": "[sandbox.nodes.spain]\nagent = \"unix://run/a.sock\"\nworkload = [\"job\"]\n",
		"no workload":    "[sandbox.nodes.spain]\nagent = \"unix:///run/a.sock\"\n",
		"odd workload":   "[sandbox.nodes.spain]\nagent = \"unix:///run/a.sock\"\nworkload = [\"batch\"]\n",
		"twice":          "[sandbox.nodes.spain]\nagent = \"unix:///run/a.sock\"\nworkload = [\"job\", \"job\"]\n",
		"bad name":       "[sandbox.nodes.Spain]\nagent = \"unix:///run/a.sock\"\nworkload = [\"job\"]\n",
		"shared agent":   "[sandbox.nodes.a]\nagent = \"unix:///run/a.sock\"\nworkload = [\"job\"]\n[sandbox.nodes.b]\nagent = \"unix:///run/a.sock\"\nworkload = [\"job\"]\n",
	} {
		if _, err := loadWith(t, body); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
}

func TestAPoolDaemonHoldsNoSandboxNode(t *testing.T) {
	_, err := loadBody(t, poolBody+nodeBody, "rou2")
	if err == nil || !strings.Contains(err.Error(), "sandbox.nodes") {
		t.Fatalf("got %v", err)
	}
}
