package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/transport"
)

// ariaBackend constructs the angelus's aria storage backend. Today
// this is always a FileBackend at ~/.local/state/figaro/arias; once
// a DB backend lands, this is the single switch point.
func ariaBackend() (store.Backend, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("user home: %w", err)
	}
	dir := filepath.Join(home, ".local", "state", "figaro", "arias")
	return store.NewFileBackend(dir)
}

func angelusRuntimeDir() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "figaro")
	}
	return filepath.Join(os.TempDir(), "figaro")
}

func angelusSocketPath() string {
	return filepath.Join(angelusRuntimeDir(), "angelus.sock")
}

// ensureAngelus starts the angelus if it's not running.
func ensureAngelus() {
	sockPath := angelusSocketPath()
	ep := transport.UnixEndpoint(sockPath)
	if cli, err := angelus.DialClient(ep); err == nil {
		cli.Close()
		return
	}

	exe, err := os.Executable()
	if err != nil {
		die("find executable: %s", err)
	}

	cmd := exec.Command(exe, "--angelus")
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = detachAttr()
	if err := cmd.Start(); err != nil {
		die("start angelus: %s", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cli, err := angelus.DialClient(ep); err == nil {
			cli.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	die("angelus did not start within 5 seconds")
}

func mustConnectAngelus(loaded *config.Loaded) *angelus.Client {
	_ = loaded
	ensureHush()
	ensureAngelus()
	ep := transport.UnixEndpoint(angelusSocketPath())
	cli, err := angelus.DialClient(ep)
	if err != nil {
		die("connect angelus: %s", err)
	}
	return cli
}

func mustCreateAndBind(ctx context.Context, acli *angelus.Client, loaded *config.Loaded, ppid int) (string, transport.Endpoint) {
	_ = loaded
	createResp, err := acli.Create(ctx, "", nil)
	if err != nil {
		die("create figaro: %s", err)
	}

	if err := acli.Bind(ctx, ppid, createResp.FigaroID); err != nil {
		die("bind: %s", err)
	}

	ep := transport.Endpoint{
		Scheme:  createResp.Endpoint.Scheme,
		Address: createResp.Endpoint.Address,
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, serr := os.Stat(ep.Address); serr == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	return createResp.FigaroID, ep
}
