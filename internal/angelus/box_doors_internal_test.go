package angelus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/jkauthz"
	"github.com/jack-work/jkrpc"
)

func loadBoxes(t *testing.T, dir string, boxes map[string]string) *config.Loaded {
	t.Helper()
	var b strings.Builder
	for name, door := range boxes {
		fmt.Fprintf(&b, "[sandbox.boxes.%s]\nexec = %q\ndoor = %q\noutfits = [\"*\"]\nproviders = [\"*\"]\n", name, filepath.Join(dir, name+"-exec.sock"), door)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

type heldDoor struct {
	c      *jkrpc.Client
	lapsed chan string
}

func holdDoor(t *testing.T, path string) *heldDoor {
	t.Helper()
	nc, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	h := &heldDoor{lapsed: make(chan string, 4)}
	h.c = jkrpc.NewClient(jkrpc.NewConn(nc), func(method string, params json.RawMessage) {
		if method == rpc.MethodDoorLapsed {
			var l rpc.DoorLapsed
			_ = json.Unmarshal(params, &l)
			h.lapsed <- l.Reason
		}
	})
	t.Cleanup(func() { h.c.Close() })
	return h
}

func (h *heldDoor) list() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out rpc.ListResponse
	return h.c.Call(ctx, rpc.MethodList, rpc.ListRequest{}, &out)
}

func TestAHandoverThatCannotListenRestoresTheDoorItClosed(t *testing.T) {
	dir := t.TempDir()
	door := filepath.Join(dir, "slot.sock")
	first := loadBoxes(t, dir, map[string]string{"slot1": door})
	backend := store.NewTestBackend(t)
	a := New(Config{RuntimeDir: t.TempDir(), Backend: backend})
	a.SocketPath = filepath.Join(dir, "angelus.sock")
	a.Settings = first
	a.Policies = authz.NewPolicies(jkauthz.Principal{Kind: jkauthz.KindUser, ID: "1000"}, StoreFacts(backend))
	a.Policies.SetBoxes([]string{"slot1", "slot2"})
	if err := a.Policies.Reload(""); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	a.Handlers = NewHandlers(ServerConfig{Angelus: a, Config: first, Ctx: ctx}).Map
	if err := a.OpenBoxDoors(ctx, first); err != nil {
		t.Fatal(err)
	}
	held := holdDoor(t, door)
	if err := held.list(); err != nil {
		t.Fatalf("canary: slot1's door never served: %v", err)
	}

	refused := errors.New("injected listen failure")
	failures := 1
	listenBoxDoor = func(path string, mode os.FileMode) (net.Listener, error) {
		if path == door && failures > 0 {
			failures--
			return nil, refused
		}
		return ListenUnix(path, mode)
	}
	t.Cleanup(func() { listenBoxDoor = ListenUnix })

	err := a.ReloadBoxDoors(loadBoxes(t, dir, map[string]string{"slot2": door}))
	if !errors.Is(err, refused) || !strings.Contains(err.Error(), "slot1.door") {
		t.Fatalf("the reload was not refused naming what it restored: %v", err)
	}
	select {
	case r := <-held.lapsed:
		if !strings.Contains(r, "box slot1's door left the config") {
			t.Fatalf("lapse reason %q", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the connection held on the closed door got no lapse")
	}
	again := holdDoor(t, door)
	if err := again.list(); err != nil {
		t.Fatalf("the closed door did not answer again after the refused reload: %v", err)
	}
	a.boxDoorsMu.Lock()
	o, ok := a.boxDoors.open["slot1.door"]
	_, handed := a.boxDoors.open["slot2.door"]
	a.boxDoorsMu.Unlock()
	if !ok || o.spec.box != "slot1" || handed {
		t.Fatalf("after the refused reload the path is held by %+v, slot2 holds it: %v", o, handed)
	}
}
