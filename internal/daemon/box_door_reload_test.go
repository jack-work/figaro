package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/jkauthz"
	"github.com/jack-work/jkrpc"
)

type reloadRig struct {
	t   *testing.T
	dir string
	a   *angelus.Angelus
}

type rigBox struct {
	name, door, host string
}

func (r *reloadRig) write(boxes ...rigBox) {
	r.t.Helper()
	var b strings.Builder
	for _, x := range boxes {
		fmt.Fprintf(&b, "[sandbox.boxes.%s]\nexec = %q\ndoor = %q\noutfits = [\"*\"]\nproviders = [\"*\"]\n", x.name, filepath.Join(r.dir, x.name+"-exec.sock"), x.door)
		if x.host != "" {
			fmt.Fprintf(&b, "host_door = %q\n", x.host)
		}
	}
	if err := os.WriteFile(filepath.Join(r.dir, "config.toml"), []byte(b.String()), 0o600); err != nil {
		r.t.Fatal(err)
	}
}

func (r *reloadRig) path(parts ...string) string {
	p := filepath.Join(append([]string{r.dir}, parts...)...)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		r.t.Fatal(err)
	}
	return p
}

func newReloadRig(t *testing.T, boxes ...rigBox) *reloadRig {
	t.Helper()
	r := &reloadRig{t: t, dir: t.TempDir()}
	r.write(boxes...)
	loaded, err := config.Load(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	backend := store.NewTestBackend(t)
	a := angelus.New(angelus.Config{RuntimeDir: t.TempDir(), Backend: backend})
	a.SocketPath = filepath.Join(r.dir, "angelus.sock")
	a.Settings = loaded
	a.Policies = authz.NewPolicies(jkauthz.Principal{Kind: jkauthz.KindUser, ID: "1000"}, angelus.StoreFacts(backend))
	a.Policies.SetBoxes(loaded.DooredBoxes())
	if err := a.Policies.Reload(""); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	a.Handlers = angelus.NewHandlers(angelus.ServerConfig{Angelus: a, Config: loaded, Ctx: ctx}).Map
	if err := a.OpenBoxDoors(ctx, loaded); err != nil {
		t.Fatal(err)
	}
	r.a = a
	return r
}

func (r *reloadRig) refresh() error {
	ctx := authz.WithPrincipal(authz.WithDoor(context.Background(), authz.DoorLocal), rpc.Principal{Kind: rpc.PrincipalUser, ID: "1000"})
	raw, _ := json.Marshal(rpc.ConfigureRequest{Refresh: true})
	_, err := r.a.Handlers[rpc.MethodConfigure](ctx, raw)
	return err
}

type boxCaller struct {
	c      *jkrpc.Client
	lapsed chan string
}

func dialBox(path string) (*boxCaller, error) {
	nc, err := net.Dial("unix", path)
	if err != nil {
		return nil, err
	}
	b := &boxCaller{lapsed: make(chan string, 4)}
	b.c = jkrpc.NewClient(jkrpc.NewConn(nc), func(method string, params json.RawMessage) {
		if method != rpc.MethodDoorLapsed {
			return
		}
		var l rpc.DoorLapsed
		_ = json.Unmarshal(params, &l)
		b.lapsed <- l.Reason
	})
	return b, nil
}

func (b *boxCaller) list() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out rpc.ListResponse
	return b.c.Call(ctx, rpc.MethodList, rpc.ListRequest{}, &out)
}

func (b *boxCaller) lapseReason(t *testing.T) string {
	t.Helper()
	select {
	case r := <-b.lapsed:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("the held connection got no door.lapsed")
		return ""
	}
}

func reaches(t *testing.T, path string) error {
	t.Helper()
	b, err := dialBox(path)
	if err != nil {
		return err
	}
	defer b.c.Close()
	return b.list()
}

func TestABoxAddedAtRuntimeOpensItsDoorOnRefresh(t *testing.T) {
	r := newReloadRig(t)
	door := r.path("chat", "door", "door.sock")
	if reaches(t, door) == nil {
		t.Fatal("canary: the door answers before the box exists")
	}
	r.write(rigBox{name: "chat", door: door})
	if err := r.refresh(); err != nil {
		t.Fatal(err)
	}
	if err := reaches(t, door); err != nil {
		t.Fatalf("a command in the added box does not reach the daemon: %v", err)
	}
}

func TestABoxRemovedAtRuntimeShutsItsDoorWithAReason(t *testing.T) {
	r := newReloadRig(t)
	door := r.path("chat", "door", "door.sock")
	r.write(rigBox{name: "chat", door: door})
	if err := r.refresh(); err != nil {
		t.Fatal(err)
	}
	held, err := dialBox(door)
	if err != nil {
		t.Fatal(err)
	}
	defer held.c.Close()
	if err := held.list(); err != nil {
		t.Fatalf("canary: the held connection never worked: %v", err)
	}
	r.write()
	if err := r.refresh(); err != nil {
		t.Fatal(err)
	}
	if got := held.lapseReason(t); !strings.Contains(got, "box chat's door left the config") {
		t.Fatalf("lapse reason %q", got)
	}
	if err := held.list(); err == nil {
		t.Fatal("the held connection still serves after its box left")
	}
	if err := reaches(t, door); err == nil {
		t.Fatal("a new call through the removed box's door still reaches the daemon")
	}
	if _, err := os.Stat(door); !os.IsNotExist(err) {
		t.Fatalf("the removed door's socket stays: %v", err)
	}
}

func TestABoxWrittenWithoutARefreshHasNoDoor(t *testing.T) {
	r := newReloadRig(t)
	door := r.path("chat", "door", "door.sock")
	r.write(rigBox{name: "chat", door: door})
	if err := reaches(t, door); err == nil {
		t.Fatal("a door opened with no refresh")
	}
	if err := r.refresh(); err != nil {
		t.Fatal(err)
	}
	if err := reaches(t, door); err != nil {
		t.Fatalf("canary: the written config never opens a door: %v", err)
	}
}

func TestABoxDoorMovedAtRuntimeReopensAtItsNewPath(t *testing.T) {
	r := newReloadRig(t)
	old := r.path("chat", "door", "door.sock")
	moved := r.path("chat", "door2", "door.sock")
	r.write(rigBox{name: "chat", door: old})
	if err := r.refresh(); err != nil {
		t.Fatal(err)
	}
	held, err := dialBox(old)
	if err != nil {
		t.Fatal(err)
	}
	defer held.c.Close()
	if err := held.list(); err != nil {
		t.Fatalf("canary: the old door never worked: %v", err)
	}
	r.write(rigBox{name: "chat", door: moved})
	if err := r.refresh(); err != nil {
		t.Fatal(err)
	}
	if got := held.lapseReason(t); !strings.Contains(got, "moved to "+moved) {
		t.Fatalf("lapse reason %q", got)
	}
	if err := reaches(t, moved); err != nil {
		t.Fatalf("the moved door does not serve: %v", err)
	}
	if err := reaches(t, old); err == nil {
		t.Fatal("the old path still serves after the move")
	}
}

func TestABoxRenamedOnTheSamePathServesTheNewBox(t *testing.T) {
	r := newReloadRig(t)
	door := r.path("slot", "door", "door.sock")
	r.write(rigBox{name: "slot1", door: door})
	if err := r.refresh(); err != nil {
		t.Fatal(err)
	}
	held, err := dialBox(door)
	if err != nil {
		t.Fatal(err)
	}
	defer held.c.Close()
	if err := held.list(); err != nil {
		t.Fatalf("canary: slot1's door never worked: %v", err)
	}
	r.write(rigBox{name: "slot2", door: door})
	if err := r.refresh(); err != nil {
		t.Fatal(err)
	}
	if got := held.lapseReason(t); !strings.Contains(got, "box slot1's door left the config") {
		t.Fatalf("a connection admitted as slot1 lapsed with %q", got)
	}
	if err := reaches(t, door); err != nil {
		t.Fatalf("slot2 does not answer on the path slot1 gave up: %v", err)
	}
}

func TestAReloadThatBreaksAHostDoorRuleKeepsTheOldDoors(t *testing.T) {
	r := newReloadRig(t)
	door := r.path("chat", "door", "door.sock")
	r.write(rigBox{name: "chat", door: door})
	if err := r.refresh(); err != nil {
		t.Fatal(err)
	}
	if err := reaches(t, door); err != nil {
		t.Fatalf("canary: the first door never worked: %v", err)
	}
	other := r.path("other", "door", "door.sock")
	inside := filepath.Join(filepath.Dir(door), "host.sock")
	r.write(rigBox{name: "chat", door: door}, rigBox{name: "other", door: other, host: inside})
	err := r.refresh()
	if err == nil || !strings.Contains(err.Error(), "a host door lives where no box does") {
		t.Fatalf("a host door inside a box's door directory was accepted on reload: %v", err)
	}
	if err := reaches(t, door); err != nil {
		t.Fatalf("the old door stopped serving after a refused reload: %v", err)
	}
	for _, p := range []string{other, inside} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("a refused reload left %s behind: %v", p, err)
		}
	}
}

func TestAReloadWhoseDoorCannotOpenNeverHalfApplies(t *testing.T) {
	r := newReloadRig(t)
	door := r.path("chat", "door", "door.sock")
	r.write(rigBox{name: "chat", door: door})
	if err := r.refresh(); err != nil {
		t.Fatal(err)
	}
	added := r.path("added", "door", "door.sock")
	missing := filepath.Join(r.dir, "nowhere", "door.sock")
	r.write(rigBox{name: "added", door: added}, rigBox{name: "chat", door: door}, rigBox{name: "zbroken", door: missing})
	err := r.refresh()
	if err == nil || !strings.Contains(err.Error(), "sandbox.boxes.zbroken.door") {
		t.Fatalf("a door that cannot listen was accepted: %v", err)
	}
	if _, err := os.Stat(added); !os.IsNotExist(err) {
		t.Fatalf("a refused reload half-applied: the added box's door opened: %v", err)
	}
	if err := reaches(t, door); err != nil {
		t.Fatalf("the old door stopped serving after a refused reload: %v", err)
	}
	r.write(rigBox{name: "added", door: added}, rigBox{name: "chat", door: door})
	if err := r.refresh(); err != nil {
		t.Fatal(err)
	}
	if err := reaches(t, added); err != nil {
		t.Fatalf("canary: the added box never opens without the broken one: %v", err)
	}
}

func TestAHostDoorAddedAtRuntimeOpens(t *testing.T) {
	r := newReloadRig(t)
	door := r.path("chat", "door", "door.sock")
	host := r.path("chat", "host", "host.sock")
	r.write(rigBox{name: "chat", door: door})
	if err := r.refresh(); err != nil {
		t.Fatal(err)
	}
	if reaches(t, host) == nil {
		t.Fatal("canary: the host door answers before it is configured")
	}
	r.write(rigBox{name: "chat", door: door, host: host})
	if err := r.refresh(); err != nil {
		t.Fatal(err)
	}
	if err := reaches(t, host); err != nil {
		t.Fatalf("a host door added at runtime does not serve: %v", err)
	}
	r.write(rigBox{name: "chat", door: door})
	if err := r.refresh(); err != nil {
		t.Fatal(err)
	}
	if err := reaches(t, host); err == nil {
		t.Fatal("a host door removed at runtime still serves")
	}
	if err := reaches(t, door); err != nil {
		t.Fatalf("dropping the host door took the box door with it: %v", err)
	}
}
