package daemon

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/jkauthz"
	"github.com/jack-work/jkrpc"
)

// The daemon tells a host-door caller from one in the box by the socket it
// came through: a box that requires a claim still answers its host door.
func TestARequiredBoxStillAnswersItsHostDoor(t *testing.T) {
	dir := t.TempDir()
	door := filepath.Join(dir, "chat", "door", "door.sock")
	host := filepath.Join(dir, "chat", "host-door", "host.sock")
	for _, d := range []string{filepath.Dir(door), filepath.Dir(host)} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, required := range []bool{false, true} {
		t.Run(fmt.Sprintf("required=%v", required), func(t *testing.T) {
			claim := ""
			if required {
				claim = "figaro_claim = \"required\"\n"
			}
			body := fmt.Sprintf("[sandbox.boxes.chat]\nexec = %q\ndoor = %q\nhost_door = %q\noutfits = [\"*\"]\nproviders = [\"*\"]\n%s",
				filepath.Join(dir, "exec.sock"), door, host, claim)
			if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			loaded, err := config.Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			backend := store.NewTestBackend(t)
			a := angelus.New(angelus.Config{RuntimeDir: t.TempDir(), Backend: backend})
			a.SocketPath = filepath.Join(dir, "angelus.sock")
			a.Settings = loaded
			a.Policies = authz.NewPolicies(jkauthz.Principal{Kind: jkauthz.KindUser, ID: "1000"}, angelus.StoreFacts(backend))
			a.Policies.SetBoxes(loaded.DooredBoxes())
			a.Policies.SetBoxesRequiringClaim(loaded.BoxesRequiringClaim())
			if err := a.Policies.Reload(""); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			a.Handlers = angelus.NewHandlers(angelus.ServerConfig{Angelus: a, Config: loaded, Ctx: ctx}).Map
			if err := a.OpenBoxDoors(ctx, loaded); err != nil {
				t.Fatal(err)
			}
			list := func(path string) error {
				nc, err := net.Dial("unix", path)
				if err != nil {
					t.Fatal(err)
				}
				c := jkrpc.NewClient(jkrpc.NewConn(nc), nil)
				defer c.Close()
				var out rpc.ListResponse
				return c.Call(context.Background(), rpc.MethodList, rpc.ListRequest{}, &out)
			}
			if err := list(host); err != nil {
				t.Fatalf("an unclaimed call through the host door: %v", err)
			}
			err = list(door)
			if required && (err == nil || !strings.Contains(err.Error(), "figaro_claim")) {
				t.Fatalf("an unclaimed call through the in-box door of a required box: %v", err)
			}
			if !required && err != nil {
				t.Fatalf("an unclaimed in-box call to a box that requires nothing: %v", err)
			}
			cancel()
		})
	}
}
