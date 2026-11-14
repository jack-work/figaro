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
	for name, endpoint := range boxes {
		fmt.Fprintf(&b, "[sandbox.boxes.%s]\nexec = %q\nendpoint = %q\noutfits = [\"*\"]\nproviders = [\"*\"]\n", name, filepath.Join(dir, name+"-exec.sock"), endpoint)
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

type heldEndpoint struct {
	c      *jkrpc.Client
	lapsed chan string
}

func holdEndpoint(t *testing.T, path string) *heldEndpoint {
	t.Helper()
	nc, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	h := &heldEndpoint{lapsed: make(chan string, 4)}
	h.c = jkrpc.NewClient(jkrpc.NewConn(nc), func(method string, params json.RawMessage) {
		if method == rpc.MethodEndpointLapsed {
			var l rpc.EndpointLapsed
			_ = json.Unmarshal(params, &l)
			h.lapsed <- l.Reason
		}
	})
	t.Cleanup(func() { h.c.Close() })
	return h
}

func (h *heldEndpoint) list() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out rpc.ListResponse
	return h.c.Call(ctx, rpc.MethodList, rpc.ListRequest{}, &out)
}

func TestAHandoverThatCannotListenRestoresTheEndpointItClosed(t *testing.T) {
	dir := t.TempDir()
	endpoint := filepath.Join(dir, "slot.sock")
	first := loadBoxes(t, dir, map[string]string{"slot1": endpoint})
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
	if err := a.OpenBoxEndpoints(ctx, first); err != nil {
		t.Fatal(err)
	}
	held := holdEndpoint(t, endpoint)
	if err := held.list(); err != nil {
		t.Fatalf("canary: slot1's endpoint never served: %v", err)
	}

	refused := errors.New("injected listen failure")
	failures := 1
	listenBoxEndpoint = func(path string, mode os.FileMode) (net.Listener, error) {
		if path == endpoint && failures > 0 {
			failures--
			return nil, refused
		}
		return ListenUnix(path, mode)
	}
	t.Cleanup(func() { listenBoxEndpoint = ListenUnix })

	err := a.ReloadBoxEndpoints(loadBoxes(t, dir, map[string]string{"slot2": endpoint}))
	if !errors.Is(err, refused) || !strings.Contains(err.Error(), "slot1.endpoint") {
		t.Fatalf("the reload was not refused naming what it restored: %v", err)
	}
	select {
	case r := <-held.lapsed:
		if !strings.Contains(r, "box slot1's endpoint left the config") {
			t.Fatalf("lapse reason %q", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the connection held on the closed endpoint got no lapse")
	}
	again := holdEndpoint(t, endpoint)
	if err := again.list(); err != nil {
		t.Fatalf("the closed endpoint did not answer again after the refused reload: %v", err)
	}
	a.boxEndpointsMu.Lock()
	o, ok := a.boxEndpoints.open["slot1.endpoint"]
	_, handed := a.boxEndpoints.open["slot2.endpoint"]
	a.boxEndpointsMu.Unlock()
	if !ok || o.spec.box != "slot1" || handed {
		t.Fatalf("after the refused reload the path is held by %+v, slot2 holds it: %v", o, handed)
	}
}
