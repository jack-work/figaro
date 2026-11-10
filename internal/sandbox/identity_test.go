package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	api "github.com/jack-work/figaro/api/sandbox"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/sandbox/box"
	"github.com/jack-work/figaro/internal/sandbox/local"
)

const figaroID = "f1d0c0ffee"

func boxConfig(t *testing.T) *config.Loaded {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "idbox")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	ws := filepath.Join(dir, "ws")
	if err := os.Mkdir(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	srv, err := box.NewServer(ws)
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "exec.sock")
	ln, err := box.Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { srv.Serve(ctx, ln); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	cfgDir := filepath.Join(dir, "config")
	if err := os.Mkdir(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), fmt.Appendf(nil, "[sandbox.boxes.idbox]\nexec = %q\n", sock), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(cfgDir)
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

func output(t *testing.T, sb api.Sandbox, command string) string {
	t.Helper()
	var mu sync.Mutex
	var out strings.Builder
	p, err := sb.Exec(context.Background(), api.ExecRequest{Command: command, OnChunk: func(b []byte) { mu.Lock(); out.Write(b); mu.Unlock() }})
	if err != nil {
		t.Fatal(err)
	}
	p.Wait()
	mu.Lock()
	defer mu.Unlock()
	return out.String()
}

// Every kind the daemon opens is told which figaro it serves: the process
// sees FIGARO_ARIA, and the provider files its sessions under the id, so
// killing that figaro's scope finds them.
func TestEveryProviderKindIsOpenedForItsFigaro(t *testing.T) {
	for _, kind := range []string{"local", "box"} {
		t.Run(kind, func(t *testing.T) {
			host := local.NewHost(local.DefaultTTL)
			var loaded *config.Loaded
			name := kind
			switch kind {
			case "box":
				loaded, name = boxConfig(t), "idbox"
			}
			sb, err := Open(context.Background(), host, loaded, name, figaroID, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer sb.Close()
			if got := output(t, sb, `printf %s "$FIGARO_ARIA"`); got != figaroID {
				t.Fatalf("FIGARO_ARIA %q, want %q", got, figaroID)
			}
			if _, err := sb.Sessions().Start(api.ExecRequest{Command: "sleep 300"}); err != nil {
				t.Fatal(err)
			}
			var killed int
			if kind == "box" {
				killed, err = KillBoxScope(context.Background(), loaded, name, figaroID)
			} else {
				killed = host.KillScope(figaroID)
			}
			if err != nil || killed != 1 {
				t.Fatalf("killing %s's scope found %d sessions (%v), want 1", figaroID, killed, err)
			}
		})
	}
}
